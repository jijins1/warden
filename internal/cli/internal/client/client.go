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

// ErrIntrouvable : le serveur a répondu 404 sur la surface d'ADMINISTRATION.
//
// Le message est délibérément ambigu parce que la réalité l'est : cette surface
// répond 404 à qui n'est pas administrateur, précisément pour ne pas confirmer
// son existence à un utilisateur ordinaire. Affirmer « cette clé n'existe pas »
// serait donc faux une fois sur deux, et enverrait l'utilisateur chercher au
// mauvais endroit.
var ErrIntrouvable = errors.New("introuvable, ou hors de votre portée")

// Config configure le client.
type Config struct {
	// BaseURL est l'adresse de l'API. C'est la SEULE valeur que la CLI a le
	// droit de tenir du fichier de projet : on ne peut pas demander au serveur où
	// est le serveur.
	BaseURL string

	// Token est le jeton de session ouvert par `warden login`.
	//
	// C'est le chemin d'authentification RÉEL : son porteur a prouvé qu'il
	// connaissait un mot de passe. Quand il est posé, `Subject` n'est pas envoyé —
	// le serveur essaie le porteur d'abord, et l'affirmation ne servirait à rien
	// sinon à traîner une identité non vérifiée dans les en-têtes.
	Token string

	// Subject porte l'identité en mode de développement.
	//
	// Le serveur ne l'accepte que derrière un opt-in explicite (voir
	// AuthnMiddleware). C'est délibéré de sa part : un produit d'audit qui
	// accepterait « je suis alice » en silence serait pire qu'un produit qui
	// refuse de répondre. Ce champ disparaîtra avec l'arrivée d'OIDC.
	Subject string

	Timeout time.Duration
}

// Client appelle l'API du control plane.
type Client struct {
	base    *url.URL
	token   string
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
		token:   cfg.Token,
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

	// PushURL est l'adresse du rôle `git` telle que le SERVEUR l'annonce.
	//
	// Même raisonnement que RefPrefix, dont le commentaire ci-dessus dit qu'il
	// vient du serveur « précisément pour qu'il vienne du serveur » : dire avec
	// quel jeton et sous quel préfixe on écrit sans dire où laisserait la moitié
	// de la phrase au client.
	//
	// Vide sur un déploiement qui ne l'a pas configurée : l'appelant se rabat
	// alors sur sa propre configuration.
	PushURL string `json:"push_url,omitempty"`
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
	Playbook    string          `json:"playbook,omitempty"`
	BaseSHA     string          `json:"base_sha"`
	Flags       json.RawMessage `json:"flags,omitempty"`

	// Kind distingue un run ad-hoc d'un run de playbook. Vide vaut `playbook`.
	Kind string `json:"kind,omitempty"`

	// Module, ModuleArgs et HostPattern ne valent que pour `Kind == "adhoc"`,
	// mutuellement exclusifs avec Playbook.
	Module      string `json:"module,omitempty"`
	ModuleArgs  string `json:"module_args,omitempty"`
	HostPattern string `json:"host_pattern,omitempty"`

	// BecomePassword ne survit pas à la requête : le serveur le range dans une
	// consigne éphémère, et la livraison de l'ordre au pod le consomme.
	BecomePassword string `json:"become_password,omitempty"`

	// ConnectionPassword suit le même chemin (ADR-0048).
	ConnectionPassword string `json:"connection_password,omitempty"`

	// ForwardEnv porte les VALEURS lues dans le shell de l'appelant, et elles
	// seules (§5.4).
	//
	// Les NOMS ne sont PAS autoritaires ici : le serveur les relit dans le
	// commit et n'en retient que l'intersection (ADR-0009). Envoyer une valeur
	// pour un nom que le commit ne déclare pas ne la fait pas passer — c'est
	// exactement ce qui rend l'audit fidèle sans rien devoir à la discipline de
	// cette CLI.
	ForwardEnv map[string]string `json:"forward_env,omitempty"`
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

	// Le chemin et la QUERY sont séparés avant d'être joints : `JoinPath`
	// échappe le `?`, ce qui transformait `/admin/hosts?environment=prod` en un
	// segment de chemin littéral — donc en une route qui n'existe pas. Le
	// symptôme était un 404 sur un filtre parfaitement valide.
	cheminSeul, requete, _ := strings.Cut(chemin, "?")
	u := c.base.JoinPath(cheminSeul)
	u.RawQuery = requete

	req, err := http.NewRequestWithContext(ctx, methode, u.String(), body)
	if err != nil {
		return fmt.Errorf("construction de la requête : %w", err)
	}
	if corps != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Le jeton l'emporte, et l'affirmation n'est PAS envoyée avec lui : un
	// serveur en mode de développement retomberait sinon sur l'en-tête si le
	// jeton venait à expirer, et l'appelant continuerait de travailler sans
	// savoir que sa session est morte.
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.subject != "":
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
		// Deux 404 de natures différentes, et les confondre égarerait. Sur
		// `/admin`, il ne dit RIEN de plus que « pas pour vous, ou pas là ».
		if strings.HasPrefix(cheminSeul, "/admin/") {
			return fmt.Errorf("%w", ErrIntrouvable)
		}
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

// --- administration : clés d'accès et machines enrôlées ---

// Key est ce qu'on peut dire d'une clé sans la révéler.
//
// La partie PRIVÉE n'y figure pas : le serveur ne la rend à personne. Ce que la
// CLI affiche est la partie publique, qui est faite pour être copiée dans un
// `authorized_keys`, et l'empreinte, qui est faite pour être comparée.
type Key struct {
	Environment string `json:"environment"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Origin      string `json:"origin"`
	CreatedAt   string `json:"created_at"`
	RotatedAt   string `json:"rotated_at,omitempty"`
}

// PutKey pose la clé d'un environnement.
//
// Les deux modes sont exclusifs : soit `private` porte une clé existante, soit
// `generate` demande au serveur de la frapper. La partie privée ne revient
// jamais.
type PutKey struct {
	PrivateKey string `json:"private_key,omitempty"`
	Generate   bool   `json:"generate,omitempty"`
}

// SetKey pose ou remplace la clé d'un environnement.
//
// Remplacer EST la rotation, et c'est le seul mécanisme de révocation dont le
// produit dispose (ADR-0020).
func (c *Client) SetKey(ctx context.Context, environment string, p PutKey) (Key, error) {
	var out Key
	err := c.appeler(ctx, http.MethodPut, "/admin/keys/"+url.PathEscape(environment), p, &out)
	return out, err
}

// GetKey rend le descripteur d'une clé.
func (c *Client) GetKey(ctx context.Context, environment string) (Key, error) {
	var out Key
	err := c.appeler(ctx, http.MethodGet, "/admin/keys/"+url.PathEscape(environment), nil, &out)
	return out, err
}

// ListKeys rend les descripteurs de toutes les clés.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var out []Key
	err := c.appeler(ctx, http.MethodGet, "/admin/keys", nil, &out)
	return out, err
}

// VaultKey est ce qu'on peut dire d'une clé de vault sans la révéler
// (ADR-0045).
//
// Aucun champ n'y porte la valeur en clair, et aucun n'y portera jamais :
// contrairement à une clé SSH, un mot de passe symétrique n'a pas de partie
// publique à afficher.
type VaultKey struct {
	Environment string `json:"environment"`
	Fingerprint string `json:"fingerprint"`
	Origin      string `json:"origin"`
	CreatedAt   string `json:"created_at"`
	RotatedAt   string `json:"rotated_at,omitempty"`
}

// PutVaultKey pose la clé de vault d'un environnement.
//
// Les deux modes sont exclusifs, même contrainte que PutKey : soit
// `plaintext` porte un mot de passe existant (migration d'un dépôt déjà
// chiffré), soit `generate` demande à warden de le tirer.
type PutVaultKey struct {
	Plaintext string `json:"plaintext,omitempty"`
	Generate  bool   `json:"generate,omitempty"`
}

// SetVaultKey pose ou remplace la clé de vault d'un environnement.
func (c *Client) SetVaultKey(ctx context.Context, environment string, p PutVaultKey) (VaultKey, error) {
	var out VaultKey
	err := c.appeler(ctx, http.MethodPost, "/environments/"+url.PathEscape(environment)+"/vault", p, &out)
	return out, err
}

// GetVaultKey rend le descripteur de la clé de vault d'un environnement.
func (c *Client) GetVaultKey(ctx context.Context, environment string) (VaultKey, error) {
	var out VaultKey
	err := c.appeler(ctx, http.MethodGet, "/environments/"+url.PathEscape(environment)+"/vault", nil, &out)
	return out, err
}

// EncryptVault chiffre une valeur avec la clé de vault d'un environnement.
//
// La valeur en clair transite une seule fois, dans cette requête ; ce qui
// revient est le bloc `!vault |` chiffré, jamais la clé.
func (c *Client) EncryptVault(ctx context.Context, environment, varName, plaintext string) (string, error) {
	var out struct {
		Block string `json:"block"`
	}
	body := struct {
		Plaintext string `json:"plaintext"`
		VarName   string `json:"var_name"`
	}{Plaintext: plaintext, VarName: varName}
	err := c.appeler(ctx, http.MethodPost,
		"/environments/"+url.PathEscape(environment)+"/vault/encrypt", body, &out)
	return out.Block, err
}

// Host est le formalisme d'une machine enrôlée, tel qu'il circule.
//
// L'adresse est obligatoire : c'est là que l'enrôlement se connecte, et un nom
// d'hôte n'en tient pas lieu — dépendre d'une résolution faite dans le pod
// ferait enrôler une machine que l'opérateur croyait désigner.
type Host struct {
	Name        string `json:"host"        yaml:"host"`
	Environment string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Address     string `json:"address"     yaml:"address"`
	Port        int    `json:"port,omitempty" yaml:"port,omitempty"`
	Login       string `json:"login"       yaml:"login"`
	HostKey     string `json:"host_key,omitempty" yaml:"host_key,omitempty"`

	// Renseignés par le serveur, jamais lus d'un fichier de déclaration.
	Mode       string `json:"mode,omitempty"        yaml:"-"`
	EnrolledAt string `json:"enrolled_at,omitempty" yaml:"-"`
}

// Inventory est le document que `warden host declare` envoie.
type Inventory struct {
	Hosts []Host `json:"hosts" yaml:"hosts"`
}

// DeclareHosts enregistre des machines déclarées par un opérateur.
//
// Une seule requête pour tout le document : une flotte se déclare d'un fichier,
// et N requêtes laisseraient un inventaire à moitié appliqué sur la première
// erreur.
func (c *Client) DeclareHosts(ctx context.Context, environment string, inv Inventory) ([]Host, error) {
	return c.declarer(ctx, environment, "", inv)
}

// DeclareEnrolledHost déclare la machine qu'un run d'enrôlement vient de
// joindre.
//
// Le `run_ref` n'est pas décoratif : c'est lui qui permet au serveur de
// CONSTATER que warden s'est bien connecté, et donc d'enregistrer le mode
// `assisted`. Sans lui, la machine serait notée `declared` — ce qui reviendrait
// à dire que le produit n'a jamais vu de mot de passe, alors qu'il en a vu un.
func (c *Client) DeclareEnrolledHost(ctx context.Context, environment, runRef string, h Host) ([]Host, error) {
	return c.declarer(ctx, environment, runRef, Inventory{Hosts: []Host{h}})
}

func (c *Client) declarer(ctx context.Context, environment, runRef string, inv Inventory) ([]Host, error) {
	q := url.Values{}
	if environment != "" {
		q.Set("environment", environment)
	}
	if runRef != "" {
		q.Set("enrollment", runRef)
	}
	chemin := "/admin/hosts"
	if len(q) > 0 {
		chemin += "?" + q.Encode()
	}
	var out []Host
	err := c.appeler(ctx, http.MethodPut, chemin, inv, &out)
	return out, err
}

// ListHosts rend l'inventaire, filtré par environnement quand il est donné.
func (c *Client) ListHosts(ctx context.Context, environment string) ([]Host, error) {
	chemin := "/admin/hosts"
	if environment != "" {
		chemin += "?environment=" + url.QueryEscape(environment)
	}
	var out []Host
	err := c.appeler(ctx, http.MethodGet, chemin, nil, &out)
	return out, err
}

// ForgetHost retire une machine de l'inventaire.
//
// Elle ne retire RIEN de la machine : sa clé publique reste dans son
// `authorized_keys`. L'appelant doit le dire à l'utilisateur.
func (c *Client) ForgetHost(ctx context.Context, environment, name string) error {
	return c.appeler(ctx, http.MethodDelete,
		"/admin/hosts/"+url.PathEscape(environment)+"/"+url.PathEscape(name), nil, nil)
}

// Repo est un dépôt tel que le serveur le connaît.
//
// Comme `Host`, c'est un type de FIL et non le type du cœur, dupliqué à dessein
// (ADR-0011) : le jour où l'un des deux gagne un champ que l'autre ne doit pas
// porter, la divergence se déclare ici.
type Repo struct {
	Name        string `json:"name"`
	RemoteURL   string `json:"remote_url"`
	MainlineRef string `json:"mainline_ref"`
}

// ListRepos rend les dépôts connus du serveur.
func (c *Client) ListRepos(ctx context.Context) ([]Repo, error) {
	var out []Repo
	err := c.appeler(ctx, http.MethodGet, "/admin/repos", nil, &out)
	return out, err
}

// PutRepo déclare un dépôt, ou remplace sa configuration.
//
// Le nom voyage DEUX FOIS — dans le chemin et dans le corps — parce que c'est ce
// que le serveur attend, et qu'il refuse la requête si les deux se contredisent.
// C'est sa façon de rendre impossible de configurer un dépôt en croyant en viser
// un autre.
func (c *Client) PutRepo(ctx context.Context, r Repo) error {
	if r.Name == "" {
		return errors.New("le nom du dépôt est requis")
	}
	return c.appeler(ctx, http.MethodPut, "/admin/repos/"+url.PathEscape(r.Name), r, nil)
}

// NewEnrollment est ce que `warden enroll` déclare.
//
// Le mot de passe ne survit PAS à cette requête : le serveur le range dans une
// consigne éphémère, et la livraison de l'ordre au pod le consomme.
type NewEnrollment struct {
	RunRef      string `json:"run_ref"`
	Environment string `json:"environment"`

	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Login   string `json:"login"`

	Password string `json:"password"`
}

// EnrollmentCreated est ce que le serveur a décidé.
type EnrollmentCreated struct {
	RunRef string `json:"run_ref"`
	Pod    string `json:"pod,omitempty"`
}

// CreateEnrollment déclare et démarre un run d'enrôlement.
func (c *Client) CreateEnrollment(ctx context.Context, n NewEnrollment) (EnrollmentCreated, error) {
	var out EnrollmentCreated
	err := c.appeler(ctx, http.MethodPost, "/admin/enrollments", n, &out)
	return out, err
}

// --- l'authentification ---

// Ouverture est ce qu'une connexion réussie rend.
type Ouverture struct {
	Token     string `json:"token"`
	Subject   string `json:"subject"`
	Name      string `json:"name,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// Login ouvre une session.
//
// Le mot de passe voyage dans un CORPS et jamais dans l'URL : les chemins
// finissent dans les logs d'accès des reverse-proxys, ce que les corps ne font
// pas.
//
// Le type du paramètre est `[]byte` et non `string` : il vient de `prompt.Secret`
// et le garder en octets évite une copie immuable de plus dans le tas, que
// `Wipe` ne pourrait pas effacer.
func (c *Client) Login(ctx context.Context, subject string, motDePasse []byte) (Ouverture, error) {
	var out Ouverture
	corps := struct {
		Subject  string `json:"subject"`
		Password string `json:"password"`
	}{subject, string(motDePasse)}

	err := c.appeler(ctx, http.MethodPost, "/auth/login", corps, &out)
	return out, err
}

// Logout révoque la session côté serveur.
func (c *Client) Logout(ctx context.Context) error {
	return c.appeler(ctx, http.MethodPost, "/auth/logout", nil, nil)
}

// Identite est ce que le serveur croit de l'appelant.
type Identite struct {
	Subject   string `json:"subject"`
	Name      string `json:"name,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Whoami demande au SERVEUR qui il croit que nous sommes.
//
// Au serveur et non au fichier local : le fichier dit ce qu'on a rangé, le
// serveur dit ce qui est encore valable. C'est toute la différence quand une
// session a été révoquée ailleurs.
func (c *Client) Whoami(ctx context.Context) (Identite, error) {
	var out Identite
	err := c.appeler(ctx, http.MethodGet, "/auth/whoami", nil, &out)
	return out, err
}

// VersionServeur demande sa version au serveur (ADR-0049).
//
// Anonyme côté serveur : elle répond avant toute session. Un serveur antérieur
// à l'ADR-0049 répond 404, rendu ici comme une version VIDE et non comme une
// erreur — il n'annonce rien, et ne rien annoncer ne bloque rien.
func (c *Client) VersionServeur(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := c.appeler(ctx, http.MethodGet, "/version", nil, &out)
	if errors.Is(err, ErrRepoNonConfigure) {
		return "", nil
	}
	return out.Version, err
}

// ChangerMotDePasse remplace le mot de passe de l'appelant.
func (c *Client) ChangerMotDePasse(ctx context.Context, ancien, nouveau []byte) error {
	corps := struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}{string(ancien), string(nouveau)}

	return c.appeler(ctx, http.MethodPost, "/auth/password", corps, nil)
}

// --- l'administration des comptes ---

// Compte est un compte local tel que l'administration le voit.
type Compte struct {
	Subject     string `json:"subject"`
	Name        string `json:"name,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	HasPassword bool   `json:"has_password"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`

	// Password n'est rempli qu'à la CRÉATION et à la réinitialisation. Le serveur
	// le tire et ne le redonnera jamais.
	Password string `json:"password,omitempty"`
}

// ListerComptes rend les comptes locaux.
func (c *Client) ListerComptes(ctx context.Context) ([]Compte, error) {
	var out []Compte
	err := c.appeler(ctx, http.MethodGet, "/admin/users", nil, &out)
	return out, err
}

// CreerCompte crée un compte et rend le mot de passe tiré par le serveur.
func (c *Client) CreerCompte(ctx context.Context, subject, nom string, admin bool) (Compte, error) {
	q := url.Values{}
	if nom != "" {
		q.Set("name", nom)
	}
	if admin {
		q.Set("admin", "true")
	}

	chemin := "/admin/users/" + url.PathEscape(subject)
	if len(q) > 0 {
		chemin += "?" + q.Encode()
	}

	var out Compte
	err := c.appeler(ctx, http.MethodPut, chemin, nil, &out)
	return out, err
}

// ReinitialiserMotDePasse tire un nouveau mot de passe pour un compte.
func (c *Client) ReinitialiserMotDePasse(ctx context.Context, subject string) (Compte, error) {
	var out Compte
	err := c.appeler(ctx, http.MethodPost,
		"/admin/users/"+url.PathEscape(subject)+"/password", nil, &out)
	return out, err
}

// BasculerCompte désactive ou réactive un compte.
func (c *Client) BasculerCompte(ctx context.Context, subject string, desactive bool) (Compte, error) {
	action := "enable"
	if desactive {
		action = "disable"
	}

	var out Compte
	err := c.appeler(ctx, http.MethodPost,
		"/admin/users/"+url.PathEscape(subject)+"/"+action, nil, &out)
	return out, err
}

// --- les droits par environnement (§10.2) ---

// Droit est un droit d'accès à un environnement.
type Droit struct {
	Subject     string `json:"subject"`
	Environment string `json:"environment"`
	GrantedAt   string `json:"granted_at"`
	GrantedBy   string `json:"granted_by,omitempty"`

	// RevokedAt vide = droit vivant. Les révoqués sont rendus : c'est une vue
	// d'audit, pas une liste de ce qui est permis.
	RevokedAt string `json:"revoked_at,omitempty"`
}

// ListerDroits rend les droits, tous ou ceux d'un compte.
func (c *Client) ListerDroits(ctx context.Context, subject string) ([]Droit, error) {
	chemin := "/admin/grants"
	if subject != "" {
		chemin += "?subject=" + url.QueryEscape(subject)
	}

	var out []Droit
	err := c.appeler(ctx, http.MethodGet, chemin, nil, &out)
	return out, err
}
