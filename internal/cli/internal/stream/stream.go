package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jijins1/warden/internal/wire/ws"
)

// Reprises borne les reconnexions après une coupure.
//
// Le §9.1 exige qu'« un run de dix minutes survive à trois coupures réseau
// client ». Cinq laisse de la marge sans transformer un serveur définitivement
// mort en attente interminable.
const (
	Reprises        = 5
	AttenteInitiale = 500 * time.Millisecond
)

// Fin décrit comment un run s'est terminé.
type Fin struct {
	Status string
	Reason string
}

// Client suit le flux d'events d'un run.
//
// Il ne rend PAS un canal d'events : l'appelant fournit une fonction, appelée
// dans l'ordre de lecture. Ce que la CLI en fait — afficher, chercher une
// empreinte, compter des `changed` — la regarde, et un canal l'obligerait à
// gérer sa fermeture en plus du reste.
type Client struct {
	base    *url.URL
	token   string
	subject string
	dialer  *websocket.Dialer
}

// New construit le client.
//
// Le jeton de session ET le sujet, parce que le flux traverse le MÊME middleware
// que l'API : ce qui authentifie l'un authentifie l'autre. Les oublier ici
// donnerait un `warden submit` qui pousse, lance le run, puis échoue à en suivre
// la sortie — le pire des trois résultats.
func New(baseURL, token, subject string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("adresse du serveur invalide : %w", err)
	}
	// `ws` et `wss` selon `http` et `https` : le schéma du WebSocket suit celui
	// de l'API, et forcer l'un des deux ferait échouer un déploiement sur deux.
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("schéma %q inattendu pour le flux", u.Scheme)
	}

	return &Client{
		base:    u,
		token:   token,
		subject: subject,
		dialer: &websocket.Dialer{
			HandshakeTimeout: 15 * time.Second,
		},
	}, nil
}

// Suivre lit le flux d'un run jusqu'à sa fin.
//
// Le RÉASSEMBLAGE après coupure est la raison d'être de cette fonction : le
// §9.1 exige qu'un run survive à trois coupures réseau côté client sans perte
// d'event. La reprise se fait sur l'offset du dernier event REÇU, et le serveur
// rejoue depuis là — c'est ce que `from_offset` achète, et c'est pourquoi
// l'offset est assigné par le runner (ADR-0008).
//
// Un event déjà vu n'est PAS réémis vers l'appelant : après une reprise, le
// serveur peut renvoyer le dernier event connu, et l'afficher deux fois ferait
// douter de ce qu'on lit.
func (c *Client) Suivre(ctx context.Context, runRef string, voir func(ws.EventFrame)) (Fin, error) {
	offset := int64(-1) // -1 : rien reçu, on demande depuis le début
	attente := AttenteInitiale

	for essai := 1; essai <= Reprises; essai++ {
		fin, dernier, err := c.lireUneFois(ctx, runRef, offset, voir)
		if dernier > offset {
			offset = dernier
		}
		if err == nil {
			return fin, nil
		}
		if ctx.Err() != nil {
			return Fin{}, ctx.Err()
		}
		if essai == Reprises {
			return Fin{}, fmt.Errorf("flux du run %s interrompu : %w", runRef, err)
		}

		select {
		case <-ctx.Done():
			return Fin{}, ctx.Err()
		case <-time.After(attente):
		}
		attente *= 2
	}
	return Fin{}, fmt.Errorf("flux du run %s : trop de coupures", runRef)
}

// lireUneFois ouvre une connexion et lit jusqu'à la fin ou la rupture.
//
// Rend le dernier offset REÇU même en cas d'erreur : c'est lui qui permet à la
// reprise de ne pas repartir de zéro.
func (c *Client) lireUneFois(ctx context.Context, runRef string, depuis int64, voir func(ws.EventFrame)) (Fin, int64, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/runs/" + url.PathEscape(runRef) + "/stream"
	if depuis >= 0 {
		// `from_offset` est EXCLUSIF côté serveur au sens où il rejoue à partir
		// de cet offset ; on demande donc le suivant du dernier reçu.
		u.RawQuery = "from_offset=" + strconv.FormatInt(depuis+1, 10)
	}

	// Même priorité que le client HTTP : le jeton l'emporte, et l'affirmation
	// n'est pas envoyée avec lui.
	entetes := http.Header{}
	switch {
	case c.token != "":
		entetes.Set("Authorization", "Bearer "+c.token)
	case c.subject != "":
		entetes.Set("X-User-Subject", c.subject)
	}

	conn, resp, err := c.dialer.DialContext(ctx, u.String(), entetes)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return Fin{}, depuis, fmt.Errorf("le serveur a refusé le flux (%d)", resp.StatusCode)
		}
		return Fin{}, depuis, fmt.Errorf("ouverture du flux : %w", err)
	}
	defer func() { _ = conn.Close() }()

	// La fermeture du contexte doit couper la lecture : sans ça, un Ctrl-C
	// pendant un run de dix minutes attendrait la fin du run.
	fini := make(chan struct{})
	defer close(fini)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-fini:
		}
	}()

	dernier := depuis
	for {
		var m ws.Message
		if err := conn.ReadJSON(&m); err != nil {
			if ctx.Err() != nil {
				return Fin{}, dernier, ctx.Err()
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				// Fermeture propre SANS trame de fin : le run continue peut-être
				// ailleurs. C'est une coupure, donc une reprise.
				return Fin{}, dernier, errors.New("flux fermé par le serveur avant la fin du run")
			}
			return Fin{}, dernier, err
		}

		switch m.Type {
		case ws.TypeEvent:
			var e ws.EventFrame
			if err := json.Unmarshal(m.Body, &e); err != nil {
				// Une trame illisible n'arrête pas le suivi : le reste du flux
				// vaut mieux que rien, et l'audit fait foi de toute façon.
				continue
			}
			if e.Seq <= dernier {
				// Déjà vue. Après une reprise, le serveur peut renvoyer le
				// dernier event connu ; l'afficher deux fois ferait douter.
				continue
			}
			dernier = e.Seq
			voir(e)

		case ws.TypeEnd:
			var f ws.EndFrame
			if err := json.Unmarshal(m.Body, &f); err != nil {
				return Fin{}, dernier, fmt.Errorf("trame de fin illisible : %w", err)
			}
			return Fin{Status: f.Status, Reason: f.Reason}, dernier, nil

		case ws.TypeError:
			var f ws.ErrorFrame
			_ = json.Unmarshal(m.Body, &f)
			return Fin{}, dernier, fmt.Errorf("le serveur a interrompu le flux : %s", f.Error)

		case ws.TypeKeepAlive:
			// Rien à faire : la trame existe pour tenir la connexion à travers
			// un proxy.
		}
	}
}
