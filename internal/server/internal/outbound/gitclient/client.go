package gitclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// Config configure le client vers le rôle `git`.
type Config struct {
	// BaseURL est l'adresse de l'API INTERNE, distincte du smart HTTP.
	BaseURL string

	// Token est partagé avec le rôle `git`.
	Token string

	// Timeout borne un appel de bout en bout. Le rôle `git` est en instance
	// unique (ADR-0006) : son indisponibilité toucherait tous les réplicas `api`
	// à la fois, et sans borne elle les immobiliserait tous.
	Timeout time.Duration
}

// Client interroge le rôle `git`. Il satisfait core.RepoDiffer.
type Client struct {
	base    *url.URL
	token   string
	timeout time.Duration
	httpc   *http.Client
}

// New construit le client et refuse une configuration inutilisable.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("jeton de l'API interne absent")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("adresse du rôle git: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("adresse du rôle git incomplète: %q", cfg.BaseURL)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}

	return &Client{
		base:    base,
		token:   cfg.Token,
		timeout: cfg.Timeout,
		httpc: &http.Client{
			// Pas de http.Client.Timeout : il coupe pendant la lecture du corps
			// et produit alors un diff tronqué indiscernable d'un diff complet.
			// La durée totale est bornée par le contexte, et les phases de la
			// connexion par le transport.
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConnsPerHost:   4,
				ForceAttemptHTTP2:     true,
			},
		},
	}, nil
}

// DiffMainline compare la branche de référence figée au commit audité.
func (c *Client) DiffMainline(ctx context.Context, run core.Run) (string, error) {
	return c.diff(ctx, run.Repo, run.MainlineSHA, run.AuditSHA)
}

// DiffPrevious compare le commit audité du run précédent à celui de ce run.
//
// Les deux runs viennent de l'appelant, qui les a résolus ET autorisés : c'est
// lui qui détient la politique d'accès, et le run précédent appartient souvent à
// quelqu'un d'autre.
func (c *Client) DiffPrevious(ctx context.Context, run, precedent core.Run) (string, error) {
	if precedent.Repo != run.Repo {
		// Deux dépôts n'ont aucun ancêtre commun. Le rôle `git` échouerait, mais
		// l'incohérence se nomme mieux ici.
		return "", fmt.Errorf("run %s et son précédent %s ne sont pas du même dépôt: %w",
			run.RunRef, precedent.RunRef, core.ErrArtifactGone)
	}
	return c.diff(ctx, run.Repo, precedent.AuditSHA, run.AuditSHA)
}

func (c *Client) diff(ctx context.Context, repo, from, to string) (string, error) {
	if repo == "" {
		// Run antérieur à la migration 00004 : inutile d'aller le demander.
		return "", fmt.Errorf("dépôt inconnu: %w", core.ErrArtifactGone)
	}

	ctx, annuler := context.WithTimeout(ctx, c.timeout)
	defer annuler()

	corps, err := json.Marshal(gitwire.DiffRequest{Repo: repo, From: from, To: to})
	if err != nil {
		return "", fmt.Errorf("encodage de la requête: %w", err)
	}

	cible := c.base.JoinPath(gitwire.DiffPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cible.String(), bytes.NewReader(corps))
	if err != nil {
		return "", fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("appel du rôle git: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", c.erreur(resp)
	}

	// La borne est celle du contrat. Elle vaut même si le service la respecte :
	// c'est la mémoire de CE processus qu'elle protège.
	diff, err := io.ReadAll(io.LimitReader(resp.Body, gitwire.MaxDiffBytes))
	if err != nil {
		return "", fmt.Errorf("lecture du diff: %w", err)
	}
	return string(diff), nil
}

// erreur traduit une réponse d'échec.
//
// Le classement se fait sur le CODE du corps, pas sur le statut : « dépôt
// inconnu » et « objet absent » partagent le 404 et se distinguent pourtant pour
// l'audit. Le statut ne sert que de repli quand le corps est illisible.
func (c *Client) erreur(resp *http.Response) error {
	brut, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var e gitwire.Error
	if err := json.Unmarshal(brut, &e); err != nil || e.Code == "" {
		e.Code = gitwire.CodeFromStatus(resp.StatusCode)
		e.Message = "réponse illisible du rôle git"
	}

	switch e.Code {
	case gitwire.CodeUnknownRepo, gitwire.CodeUnknownObject:
		// Les deux se présentent à l'audit comme « le code n'est plus là ».
		return fmt.Errorf("%s: %w", e.Message, core.ErrArtifactGone)

	case gitwire.CodeBadRequest:
		// Les deux extrémités sont du même produit : c'est notre défaut.
		return fmt.Errorf("requête refusée par le rôle git: %s", e.Message)

	default:
		if resp.StatusCode == http.StatusUnauthorized {
			// Jamais le jeton dans le message, même tronqué.
			return errors.New("rôle git: authentification refusée (jeton mal configuré ?)")
		}
		return fmt.Errorf("rôle git: %s", e.Message)
	}
}

// RefreshMirror demande au rôle `git` de rafraîchir le miroir de la branche
// protégée, et rend sa position.
//
// L'ordre part d'ici parce que la politique vit ici : quel dépôt, quelle URL de
// forge, quelle branche protégée sont de la configuration serveur (décision
// actée n°7). Le rôle `git` ne les connaît pas et n'a pas à les connaître — il
// détient le volume et le credential, pas la politique.
//
// Le SHA rendu est ce que le run FIGE dans `mainline_sha`. Sans ce gel, le diff
// « run contre principale » du §6.5 se recalculerait contre la branche
// d'aujourd'hui et changerait à chaque consultation (ADR-0021).
func (c *Client) RefreshMirror(ctx context.Context, repo, remote, ref string) (string, error) {
	if repo == "" || remote == "" || ref == "" {
		return "", fmt.Errorf("rafraîchissement du miroir : dépôt, forge et branche sont obligatoires")
	}

	ctx, annuler := context.WithTimeout(ctx, c.timeout)
	defer annuler()

	corps, err := json.Marshal(gitwire.MirrorRequest{Repo: repo, Remote: remote, Ref: ref})
	if err != nil {
		return "", fmt.Errorf("encodage de la requête: %w", err)
	}

	cible := c.base.JoinPath(gitwire.MirrorPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cible.String(), bytes.NewReader(corps))
	if err != nil {
		return "", fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("appel du rôle git: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", c.erreur(resp)
	}

	var out gitwire.MirrorResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("lecture de la réponse: %w", err)
	}
	if out.SHA == "" {
		// Un miroir rafraîchi sans position est une réponse qu'on ne peut pas
		// utiliser : elle deviendrait un `mainline_sha` vide, donc un ancrage
		// absent pour le contrôle de fraîcheur ET un diff incalculable.
		return "", fmt.Errorf("le rôle git n'a rendu aucune position de branche")
	}
	return out.SHA, nil
}

// ResolveRef rend la position d'une ref dans un dépôt.
//
// L'appelant compose la ref à partir de ce qu'il détient — l'identité
// authentifiée et l'identifiant du run — et non de ce que le client a envoyé.
// C'est ce qui empêche un utilisateur de faire enregistrer comme sien le commit
// d'audit d'un autre, et de le lire ensuite par le diff de son propre run.
func (c *Client) ResolveRef(ctx context.Context, repo, ref string) (string, error) {
	if repo == "" || ref == "" {
		return "", fmt.Errorf("résolution de ref : dépôt et ref sont obligatoires")
	}

	ctx, annuler := context.WithTimeout(ctx, c.timeout)
	defer annuler()

	corps, err := json.Marshal(gitwire.ResolveRequest{Repo: repo, Ref: ref})
	if err != nil {
		return "", fmt.Errorf("encodage de la requête: %w", err)
	}

	cible := c.base.JoinPath(gitwire.ResolvePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cible.String(), bytes.NewReader(corps))
	if err != nil {
		return "", fmt.Errorf("construction de la requête: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("appel du rôle git: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", c.erreur(resp)
	}

	var out gitwire.ResolveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("lecture de la réponse: %w", err)
	}
	if out.SHA == "" {
		return "", fmt.Errorf("le rôle git n'a rendu aucune position")
	}
	return out.SHA, nil
}
