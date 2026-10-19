package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// cleValeur est le champ KV v2 qui porte la valeur, encodée en base64 : les
// clés privées et les mots de passe ne sont pas forcément de l'UTF-8 valide, et
// JSON ne transporte que des chaînes.
const cleValeur = "value"

// delaiRequete borne chaque appel. Vault injoignable ne doit pas suspendre une
// livraison d'ordre au-delà de ce que le runner attend.
const delaiRequete = 10 * time.Second

// Config décrit l'accès à Vault.
type Config struct {
	// Addr est l'adresse de Vault, par exemple `https://vault:8200`.
	Addr string

	// AppRoleMount est le chemin du montage AppRole, `approle` par défaut.
	AppRoleMount string

	// RoleID n'est pas un secret : il nomme le rôle, comme un identifiant.
	RoleID string

	// SecretIDFile est le chemin du fichier portant le `secret_id`, seul secret
	// à fournir au déploiement. Il est RELU à chaque authentification : faire
	// tourner le `secret_id` revient à remplacer le fichier, sans redémarrage.
	SecretIDFile string

	// CACertFile, facultatif, est l'autorité qui signe le certificat de Vault.
	CACertFile string
}

// Client parle à Vault au nom du serveur.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu           sync.Mutex
	jeton        string
	expiration   time.Time // zéro : pas d'expiration
	renouveler   time.Time // zéro : jamais
	renouvelable bool
}

// New construit le client et échoue à froid. Il ne contacte pas Vault :
// l'authentification a lieu au premier appel, et un Vault indisponible au
// démarrage ne doit pas empêcher le reste de l'API de servir.
func New(cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("vault : adresse absente")
	}
	if _, err := url.Parse(cfg.Addr); err != nil {
		return nil, fmt.Errorf("vault : adresse illisible : %w", err)
	}
	if cfg.RoleID == "" {
		return nil, errors.New("vault : role_id absent")
	}
	if cfg.SecretIDFile == "" {
		return nil, errors.New("vault : fichier du secret_id absent")
	}
	if cfg.AppRoleMount == "" {
		cfg.AppRoleMount = "approle"
	}
	cfg.Addr = strings.TrimRight(cfg.Addr, "/")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CACertFile != "" {
		pem, err := os.ReadFile(cfg.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("vault : lecture de l'autorité : %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("vault : l'autorité ne contient aucun certificat PEM")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &Client{
		cfg:  cfg,
		http: &http.Client{Transport: transport, Timeout: delaiRequete},
		now:  time.Now,
	}, nil
}

// Lire rend la valeur rangée sous `<montage>/data/<chemin>`. `trouve` est faux
// si la clé n'existe pas — ce n'est pas une erreur.
func (c *Client) Lire(ctx context.Context, montage, chemin string) (valeur []byte, trouve bool, err error) {
	donnees, trouve, err := c.LireChamps(ctx, montage, chemin)
	if err != nil || !trouve {
		return nil, trouve, err
	}
	brut, ok := donnees[cleValeur].(string)
	if !ok {
		return nil, false, fmt.Errorf("vault %s/%s : champ %q absent", montage, chemin, cleValeur)
	}
	valeur, err = base64.StdEncoding.DecodeString(brut)
	if err != nil {
		return nil, false, fmt.Errorf("vault %s/%s : champ %q illisible", montage, chemin, cleValeur)
	}
	return valeur, true, nil
}

// Ecrire range une valeur sous `<montage>/data/<chemin>`, en remplaçant ce qui
// s'y trouvait.
func (c *Client) Ecrire(ctx context.Context, montage, chemin string, valeur []byte) error {
	return c.EcrireChamps(ctx, montage, chemin, map[string]string{
		cleValeur: base64.StdEncoding.EncodeToString(valeur),
	})
}

// LireChamps rend les champs bruts d'une clé KV v2.
func (c *Client) LireChamps(ctx context.Context, montage, chemin string) (map[string]any, bool, error) {
	var corps struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	statut, err := c.appeler(ctx, http.MethodGet, montage, "data", chemin, nil, &corps)
	if err != nil {
		return nil, false, err
	}
	// 404 couvre la clé absente ET la dernière version effacée : les deux
	// veulent dire « rien à lire ».
	if statut == http.StatusNotFound || corps.Data.Data == nil {
		return nil, false, nil
	}
	return corps.Data.Data, true, nil
}

// EcrireChamps range des champs bruts sous une clé KV v2.
func (c *Client) EcrireChamps(ctx context.Context, montage, chemin string, champs map[string]string) error {
	statut, err := c.appeler(ctx, http.MethodPost, montage, "data", chemin,
		map[string]any{"data": champs}, nil)
	if err != nil {
		return err
	}
	if statut == http.StatusNotFound {
		return fmt.Errorf("vault %s/%s : montage introuvable", montage, chemin)
	}
	return nil
}

// Detruire supprime une clé, toutes versions comprises. Idempotent : une clé
// absente n'est pas une erreur.
func (c *Client) Detruire(ctx context.Context, montage, chemin string) error {
	_, err := c.appeler(ctx, http.MethodDelete, montage, "metadata", chemin, nil, nil)
	return err
}

// appeler fait un appel KV v2 authentifié. Il rend le statut pour 2xx et 404,
// une erreur pour le reste. Un 403 provoque UNE réauthentification puis un
// nouvel essai : le jeton a pu être révoqué entre deux renouvellements.
func (c *Client) appeler(ctx context.Context, methode, montage, espace, chemin string,
	entree, sortie any,
) (int, error) {
	if err := cheminSur(montage); err != nil {
		return 0, err
	}
	if err := cheminSur(chemin); err != nil {
		return 0, err
	}
	adresse := fmt.Sprintf("%s/v1/%s/%s/%s", c.cfg.Addr, montage, espace, chemin)

	for essai := 0; ; essai++ {
		jeton, err := c.jetonValide(ctx)
		if err != nil {
			return 0, err
		}
		statut, err := c.envoyer(ctx, methode, adresse, jeton, entree, sortie)
		if err != nil {
			return 0, fmt.Errorf("vault %s/%s : %w", montage, chemin, err)
		}
		if statut == http.StatusForbidden && essai == 0 {
			c.oublierJeton(jeton)
			continue
		}
		switch {
		case statut >= 200 && statut < 300, statut == http.StatusNotFound:
			return statut, nil
		default:
			return statut, fmt.Errorf("vault %s/%s : statut %d", montage, chemin, statut)
		}
	}
}

// cheminSur refuse ce qui sortirait du montage ou de la clé visés.
func cheminSur(s string) error {
	if s == "" {
		return errors.New("vault : chemin vide")
	}
	for _, segment := range strings.Split(s, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("vault : chemin %q refusé", s)
		}
	}
	if strings.ContainsAny(s, "?#%\\ ") {
		return fmt.Errorf("vault : chemin %q refusé", s)
	}
	return nil
}

// envoyer fait l'aller-retour HTTP. Le corps d'une réponse en erreur n'est
// pas recopié : il n'apprend rien d'utile à une trace et pourrait citer la
// requête.
func (c *Client) envoyer(ctx context.Context, methode, adresse, jeton string, entree, sortie any) (int, error) {
	var corps io.Reader
	if entree != nil {
		brut, err := json.Marshal(entree)
		if err != nil {
			return 0, err
		}
		corps = bytes.NewReader(brut)
	}
	req, err := http.NewRequestWithContext(ctx, methode, adresse, corps)
	if err != nil {
		return 0, err
	}
	if jeton != "" {
		req.Header.Set("X-Vault-Token", jeton)
	}
	req.Header.Set("X-Vault-Request", "true")
	if entree != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rep, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rep.Body.Close() }()

	if rep.StatusCode >= 200 && rep.StatusCode < 300 && sortie != nil && rep.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(rep.Body, 1<<20)).Decode(sortie); err != nil {
			return 0, fmt.Errorf("réponse illisible : %w", err)
		}
	}
	return rep.StatusCode, nil
}

// reponseAuth est la forme commune de `auth/<approle>/login` et de
// `auth/token/renew-self`.
type reponseAuth struct {
	Auth struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int    `json:"lease_duration"`
		Renewable     bool   `json:"renewable"`
	} `json:"auth"`
}

// jetonValide rend un jeton utilisable : le jeton courant, renouvelé s'il
// approche de son expiration, ou un nouveau obtenu par AppRole quand il n'est
// plus renouvelable.
func (c *Client) jetonValide(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	maintenant := c.now()
	if c.jeton != "" && (c.renouveler.IsZero() || maintenant.Before(c.renouveler)) {
		return c.jeton, nil
	}
	if c.jeton != "" && c.renouvelable && maintenant.Before(c.expiration) {
		var rep reponseAuth
		statut, err := c.envoyer(ctx, http.MethodPost, c.cfg.Addr+"/v1/auth/token/renew-self",
			c.jeton, map[string]any{}, &rep)
		if err == nil && statut == http.StatusOK && rep.Auth.ClientToken != "" {
			c.retenir(rep, maintenant)
			return c.jeton, nil
		}
		// Un renouvellement refusé (TTL maximal atteint, jeton révoqué) se
		// rattrape par une nouvelle authentification, juste en dessous.
	}
	if err := c.authentifier(ctx, maintenant); err != nil {
		return "", err
	}
	return c.jeton, nil
}

// authentifier obtient un jeton par AppRole. Le `secret_id` est relu à chaque
// fois, pour que sa rotation ne demande aucun redémarrage.
func (c *Client) authentifier(ctx context.Context, maintenant time.Time) error {
	brut, err := os.ReadFile(c.cfg.SecretIDFile)
	if err != nil {
		return fmt.Errorf("vault : lecture du secret_id : %w", err)
	}
	secretID := strings.TrimSpace(string(brut))
	if secretID == "" {
		return errors.New("vault : le fichier du secret_id est vide")
	}

	var rep reponseAuth
	statut, err := c.envoyer(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/auth/%s/login", c.cfg.Addr, c.cfg.AppRoleMount), "",
		map[string]string{"role_id": c.cfg.RoleID, "secret_id": secretID}, &rep)
	if err != nil {
		return fmt.Errorf("vault : authentification AppRole : %w", err)
	}
	if statut != http.StatusOK || rep.Auth.ClientToken == "" {
		return fmt.Errorf("vault : authentification AppRole refusée (statut %d)", statut)
	}
	c.retenir(rep, maintenant)
	return nil
}

// retenir garde un jeton et planifie son renouvellement aux deux tiers de sa
// durée, pour qu'un Vault lent ne le laisse pas expirer en route.
func (c *Client) retenir(rep reponseAuth, maintenant time.Time) {
	c.jeton = rep.Auth.ClientToken
	c.renouvelable = rep.Auth.Renewable
	if rep.Auth.LeaseDuration <= 0 {
		c.expiration, c.renouveler = time.Time{}, time.Time{}
		return
	}
	duree := time.Duration(rep.Auth.LeaseDuration) * time.Second
	c.expiration = maintenant.Add(duree)
	c.renouveler = maintenant.Add(duree * 2 / 3)
}

// oublierJeton invalide le jeton s'il est toujours celui en cours : deux
// appels concurrents refusés ne doivent pas jeter un jeton déjà remplacé.
func (c *Client) oublierJeton(jeton string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jeton == jeton {
		c.jeton = ""
		c.renouvelable = false
	}
}
