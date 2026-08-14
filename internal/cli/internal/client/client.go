// Package client parle à l'API du control plane depuis la machine de
// l'utilisateur.
//
// Les types de fil sont DUPLIQUÉS et non partagés avec le serveur. L'ADR-0011
// l'impose : la CLI et le control plane sont deux domaines de confiance, et le
// binaire de la CLI est lisible par un adversaire. Le coût est une paire de
// structures à tenir en accord ; le bénéfice est qu'aucun type du serveur
// n'entre dans un binaire distribué.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrRepoNonConfigure : le dépôt n'a pas de configuration serveur.
//
// Distingué parce que la suite à donner est distincte et humaine : ce n'est ni
// une panne ni une faute de l'utilisateur, c'est une action d'administrateur en
// attente. Un message générique enverrait quelqu'un chercher un bug.
var ErrRepoNonConfigure = errors.New("ce dépôt n'a pas encore de configuration côté serveur")

// ErrAuthentification : le serveur n'a pas reconnu l'appelant.
var ErrAuthentification = errors.New("authentification refusée par le serveur")

// Config configure le client.
type Config struct {
	// BaseURL est l'adresse de l'API. C'est la SEULE valeur que la CLI a le
	// droit de tenir du fichier de projet : on ne peut pas demander au serveur où
	// est le serveur.
	BaseURL string

	// Subject porte l'identité en mode de développement.
	//
	// Le serveur ne l'accepte que derrière un opt-in explicite, et refuse toute
	// requête sinon (voir AuthnMiddleware). C'est délibéré de sa part : un produit
	// d'audit qui accepterait « je suis alice » en silence serait pire qu'un
	// produit qui refuse de répondre. Ce champ disparaîtra avec l'arrivée d'OIDC.
	Subject string

	Timeout time.Duration
}

// Client appelle l'API du control plane.
type Client struct {
	base    *url.URL
	subject string
	httpc   *http.Client
}

// New construit le client et échoue à froid.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("adresse du serveur absente")
	}
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("adresse du serveur illisible : %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("adresse du serveur : seuls http et https sont acceptés")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	return &Client{
		base:    base,
		subject: cfg.Subject,
		httpc:   &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Grant est ce que le serveur autorise à pousser.
type Grant struct {
	// Token voyage ensuite dans un en-tête HTTP vers le rôle `git`, jamais dans
	// une URL ni dans argv.
	Token string `json:"token"`

	// RefPrefix borne ce que ce jeton ouvre. La CLI y appose son identifiant de
	// run ; elle ne le compose pas elle-même, précisément pour qu'il vienne du
	// serveur (ADR-0028).
	RefPrefix string `json:"ref_prefix"`

	ExpiresAt int64 `json:"expires_at"`
}

// IssuePushGrant obtient le jeton qui autorise le push d'audit.
//
// Appelé une fois puis réutilisé pour plusieurs runs : c'est ce que la portée par
// préfixe achète (ADR-0028), et c'est ce qui ramène le cas courant à deux appels.
func (c *Client) IssuePushGrant(ctx context.Context, repo string) (Grant, error) {
	var out Grant
	err := c.appeler(ctx, http.MethodPost, "/repos/"+url.PathEscape(repo)+"/push-grant", nil, &out)
	if err != nil {
		return Grant{}, err
	}
	if out.Token == "" || out.RefPrefix == "" {
		return Grant{}, errors.New("le serveur a rendu un jeton inutilisable")
	}
	return out, nil
}

// NewRun est ce que la CLI déclare après avoir poussé.
//
// Pas de champ pour le commit d'audit : le serveur le résout depuis la ref qu'il
// compose lui-même, et refuse le champ s'il apparaît. C'est ce qui empêche un
// client de faire enregistrer comme sien le commit d'un autre.
type NewRun struct {
	RunRef      string          `json:"run_ref"`
	Repo        string          `json:"repo"`
	Environment string          `json:"environment"`
	Inventory   string          `json:"inventory"`
	Playbook    string          `json:"playbook"`
	BaseSHA     string          `json:"base_sha"`
	Flags       json.RawMessage `json:"flags,omitempty"`
}

// RunCreated est ce que le serveur a décidé.
type RunCreated struct {
	RunRef      string `json:"run_ref"`
	AuditSHA    string `json:"audit_sha"`
	MainlineRef string `json:"mainline_ref"`
	MainlineSHA string `json:"mainline_sha"`
}

// CreateRun enregistre le run une fois son commit d'audit poussé.
func (c *Client) CreateRun(ctx context.Context, n NewRun) (RunCreated, error) {
	var out RunCreated
	if err := c.appeler(ctx, http.MethodPost, "/runs", n, &out); err != nil {
		return RunCreated{}, err
	}
	return out, nil
}

// tailleMaxReponse borne ce qu'on lit d'une réponse. Les corps de cette API font
// quelques centaines d'octets ; le reste est une erreur ou un serveur qui n'est
// pas celui qu'on croit.
const tailleMaxReponse = 64 << 10

func (c *Client) appeler(ctx context.Context, methode, chemin string, corps, sortie any) error {
	var body io.Reader
	if corps != nil {
		brut, err := json.Marshal(corps)
		if err != nil {
			return fmt.Errorf("encodage de la requête : %w", err)
		}
		body = bytes.NewReader(brut)
	}

	req, err := http.NewRequestWithContext(ctx, methode, c.base.JoinPath(chemin).String(), body)
	if err != nil {
		return fmt.Errorf("construction de la requête : %w", err)
	}
	if corps != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.subject != "" {
		req.Header.Set("X-User-Subject", c.subject)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		// L'adresse du serveur figure dans le message : ici elle AIDE. C'est le
		// symétrique exact de l'URL de forge côté serveur, que le rôle `git` tait
		// — là-bas elle décrit l'infrastructure à un appelant qui n'a pas à la
		// connaître, ici elle décrit la configuration de celui qui lit.
		return fmt.Errorf("le serveur %s est injoignable : %w", c.base.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	brut, _ := io.ReadAll(io.LimitReader(resp.Body, tailleMaxReponse))

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusNotImplemented:
		return fmt.Errorf("%w (%s)", ErrAuthentification, strings.TrimSpace(string(brut)))
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w : %s", ErrRepoNonConfigure, strings.TrimSpace(string(brut)))
	case resp.StatusCode >= 400:
		// Le corps est repris tel quel : ces messages sont rédigés côté serveur
		// pour être lus par un humain (§5.7), et les reformuler ici les
		// appauvrirait.
		return fmt.Errorf("le serveur a refusé (%d) : %s", resp.StatusCode, strings.TrimSpace(string(brut)))
	}

	if sortie != nil {
		if err := json.Unmarshal(brut, sortie); err != nil {
			return fmt.Errorf("réponse du serveur illisible : %w", err)
		}
	}
	return nil
}
