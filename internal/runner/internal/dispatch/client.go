// Package dispatch reçoit l'ordre de run depuis le control plane (ADR-0018).
//
// Le sens DESCENDANT du dialogue, et le seul canal qui transporte des
// credentials. Le sens montant — les events — passe par l'ingestion, sur un
// autre port et un autre contrat, et cette séparation est délibérée.
//
// Ce paquet vit dans le domaine hostile : son code est lisible par un
// adversaire, ce qui ne change rien à sa correction mais explique ce qu'il ne
// contient pas — aucune connaissance du modèle de run, aucun identifiant, rien
// qu'un jeton et ce qu'on lui répond.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// ErrCanceled signale que le control plane a demandé l'arrêt (§5.6).
var ErrCanceled = errors.New("annulation demandée par le control plane")

// Client tient le canal de dispatch d'un pod.
type Client struct {
	url   string
	token string
	httpc *http.Client
}

// New construit le client.
//
// Le jeton est passé et non lu ici : ce paquet ne sait pas d'où il vient, et
// c'est ce qui rend le montage — un fichier de Secret — remplaçable sans le
// toucher.
func New(url, token string) (*Client, error) {
	if url == "" {
		return nil, errors.New("adresse du canal de dispatch absente")
	}
	if token == "" {
		return nil, errors.New("jeton d'amorçage absent")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("adresse du canal de dispatch invalide")
	}

	return &Client{
		url:   url,
		token: token,
		httpc: &http.Client{
			// PAS de Timeout global : le canal est un flux qui dure toute la vie
			// du pod, et un délai global le couperait au milieu d'un run. Les
			// phases de la connexion sont bornées par le transport.
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}, nil
}

// Mission est ce qu'un pod reçoit : soit un run, soit un enrôlement.
//
// Un type de retour plutôt que deux méthodes, parce que le pod ne sait pas
// LEQUEL il va recevoir avant de l'avoir reçu — il ouvre un canal et écoute.
// Exactement UN des deux champs est non nul.
type Mission struct {
	Order  *wire.Order
	Enroll *wire.Enroll
}

// Await ouvre le canal et attend l'ordre de ce pod.
//
// Le canal ne se ROUVRE PAS (ADR-0018) : si la connexion tombe, le pod est perdu
// pour le control plane, qui le remplacera. Réessayer ici donnerait l'illusion
// d'une résilience que le protocole n'a pas — et deux pods se croyant tous deux
// titulaires du même run seraient bien pires qu'un pod mort.
//
// Elle rend ErrCanceled si l'annulation arrive avant l'ordre, ce qui est un cas
// réel : un Ctrl-C pendant le démarrage du pod.
func (c *Client) Await(ctx context.Context) (Mission, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Mission{}, fmt.Errorf("construction de la requête : %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return Mission{}, fmt.Errorf("ouverture du canal de dispatch : %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Le serveur ne dit rien de plus, délibérément : un porteur de jeton ne
		// doit pas apprendre si un pod a existé ni s'il a été libéré. Le message
		// d'ici ne peut donc pas être plus précis, et prétendre le contraire
		// serait trompeur.
		return Mission{}, fmt.Errorf("canal de dispatch refusé (%d)", resp.StatusCode)
	}

	dec := wire.NewDecoder(resp.Body)
	for {
		msg, err := dec.Next()
		if err != nil {
			return Mission{}, fmt.Errorf("lecture du canal de dispatch : %w", err)
		}

		switch msg.Kind {
		case wire.KindOrder:
			if msg.Order == nil {
				return Mission{}, errors.New("ordre vide reçu")
			}
			return Mission{Order: msg.Order}, nil

		case wire.KindEnroll:
			if msg.Enroll == nil {
				return Mission{}, errors.New("enrôlement vide reçu")
			}
			return Mission{Enroll: msg.Enroll}, nil

		case wire.KindCancel:
			return Mission{}, ErrCanceled

		default:
			// Maintien de connexion, ou type ajouté après ce binaire. Le format
			// est extensible exprès (ADR-0018) : ignorer ce qu'on ne connaît pas
			// est ce qui permet d'ajouter un message sans casser les runners
			// déjà déployés.
		}
	}
}
