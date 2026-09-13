package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"time"
)

// ManagedHost est une machine enrôlée (§10.2).
//
// C'est LE formalisme du lot : ce qu'un opérateur déclare en mode A et ce qu'un
// enrôlement assisté produit en mode B convergent sur cette même structure.
// Les deux modes se distinguent par `Mode`, pas par ce qu'ils enregistrent.
//
// Ce n'est PAS une source d'inventaire Ansible : ce qui décide de ce qui
// s'exécute vient du commit d'audit (ADR-0009). Le dupliquer ici en ferait une
// seconde vérité.
type ManagedHost struct {
	// Name est le nom TEL QUE L'INVENTAIRE ANSIBLE LE NOMME. C'est par lui
	// qu'un event de run — dont l'`host` vient d'Ansible — se rapproche d'une
	// machine enrôlée.
	Name        string
	Environment string

	// Address est l'endroit où l'enrôlement se connecte : une adresse IP, ou un
	// nom que LE POD sait résoudre (ADR-0036).
	//
	// L'ADR-0030 n'acceptait qu'une adresse, pour que la machine jointe soit
	// exactement celle que l'opérateur désignait. Ce risque n'est pas effacé, il
	// est DÉPLACÉ : un nom est résolu par le résolveur du pod, dont la vue n'est
	// pas celle du poste de l'opérateur. Ce qui l'a emporté est qu'un inventaire
	// Ansible nomme couramment ses machines sans `ansible_host`, et qu'exiger une
	// adresse forçait à tenir une seconde vérité à la main.
	//
	// Ce qui protège reste l'empreinte épinglée, pas la forme de ce champ.
	Address string
	Port    int

	// Login est le compte que warden ouvre, pas l'utilisateur du produit.
	Login string

	// HostKey est l'empreinte ÉPINGLÉE, vide tant que la machine n'a jamais été
	// contactée. C'est la seule protection contre un intermédiaire.
	HostKey string

	Mode       EnrollmentMode
	EnrolledAt time.Time
	EnrolledBy int64
}

// EnrollmentMode dit comment la clé publique est arrivée sur la machine.
type EnrollmentMode string

const (
	// HostDeclared : l'opérateur a posé la clé lui-même, warden n'a
	// qu'enregistré. Le produit n'a jamais vu de mot de passe.
	HostDeclared EnrollmentMode = "declared"
	// HostAssisted : warden s'est connecté et a posé la clé. Le produit a donc
	// vu passer un mot de passe d'utilisateur — éphémère, mais vu.
	HostAssisted EnrollmentMode = "assisted"
)

// Valid dit si le mode fait partie de ceux que le schéma accepte.
func (m EnrollmentMode) Valid() bool {
	return m == HostDeclared || m == HostAssisted
}

// PortSSHParDefaut est le port pris quand le descripteur n'en donne pas.
const PortSSHParDefaut = 22

// nomDHote reprend la contrainte `managed_host_name_forme` de la migration
// 00008, et `compteSSH` la contrainte `managed_host_login_forme`.
//
// Deux expressions identiques dans deux couches, comme pour le nom de dépôt :
// chacune couvre un chemin d'entrée différent, et aucune ne peut compter sur
// l'autre.
var (
	nomDHote  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	compteSSH = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	cleDHote  = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp[0-9]+) `)

	// nomResoluble est la forme d'un nom d'hôte RFC 1123 : des labels
	// alphanumériques séparés par des points, un tiret accepté à l'intérieur d'un
	// label mais jamais à ses bords.
	//
	// Elle ne dit RIEN de la résolubilité — c'est le résolveur du pod qui
	// tranchera, et lui seul. Elle écarte ce qui ne peut pas être un nom : les
	// espaces, les guillemets, les sauts de ligne, un `_` (légal en DNS, refusé
	// par la plupart des piles TCP pour un nom d'hôte), une URL.
	nomResoluble = regexp.MustCompile(
		`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// longueurMaxAdresse borne un nom d'hôte. 253 est la limite du DNS ; la colonne
// `managed_host.address` en accepte autant.
const longueurMaxAdresse = 253

// ValiderAdresse accepte une adresse IP ou un nom d'hôte (ADR-0036).
//
// L'ordre compte : on tente l'ADRESSE d'abord. Une IP est une désignation sans
// intermédiaire, et les refus qu'on lui applique — `0.0.0.0`, le multicast —
// n'ont aucun sens pour un nom. Ce qui n'est pas une IP est alors jugé comme un
// nom, et rien de plus n'est vérifié ici : savoir si `db-prod-02` existe est une
// question que seul le résolveur DU POD peut trancher, au moment de la connexion.
//
// Conséquence à dire à l'utilisateur, et pas seulement ici : le pod ne lit pas
// de `ssh_config`. Un alias qui n'existe que dans le `~/.ssh/config` de
// l'opérateur passera cette validation et échouera à la connexion.
func ValiderAdresse(adresse string) error {
	if adresse == "" {
		return errors.New("adresse absente — c'est là que l'enrôlement se connecte")
	}
	if len(adresse) > longueurMaxAdresse {
		return fmt.Errorf("adresse de %d caractères, %d au plus", len(adresse), longueurMaxAdresse)
	}

	if ip, err := netip.ParseAddr(adresse); err == nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			// `0.0.0.0` désigne « toutes les interfaces » côté écoute et rien du
			// tout côté connexion ; une adresse multicast ne désigne pas une
			// machine. Les accepter enrôlerait une ligne qui ne joint jamais rien.
			return fmt.Errorf("%s ne désigne pas une machine", ip)
		}
		return nil
	}

	if !nomResoluble.MatchString(adresse) {
		return fmt.Errorf("%q n'est ni une adresse IP ni un nom d'hôte", adresse)
	}
	return nil
}

// Validate applique les invariants du descripteur.
//
// Au bord plutôt qu'au fond : une violation découverte par PostgreSQL ne dit pas
// QUELLE machine d'un fichier de déclaration est en cause, alors qu'ici on peut
// la nommer.
func (h ManagedHost) Validate() error {
	if !nomDHote.MatchString(h.Name) {
		return fmt.Errorf("hôte %q : nom refusé", h.Name)
	}
	if err := ValidateEnvironment(h.Environment); err != nil {
		return fmt.Errorf("hôte %s : %w", h.Name, err)
	}
	if err := ValiderAdresse(h.Address); err != nil {
		return fmt.Errorf("hôte %s : %w", h.Name, err)
	}
	if h.Port < 1 || h.Port > 65535 {
		return fmt.Errorf("hôte %s : port %d hors bornes", h.Name, h.Port)
	}
	if !compteSSH.MatchString(h.Login) {
		return fmt.Errorf("hôte %s : compte %q refusé", h.Name, h.Login)
	}
	if h.HostKey != "" && !cleDHote.MatchString(h.HostKey) {
		return fmt.Errorf("hôte %s : empreinte %q n'est pas une clé d'hôte SSH", h.Name, h.HostKey)
	}
	if !h.Mode.Valid() {
		return fmt.Errorf("hôte %s : mode d'enrôlement %q inconnu", h.Name, h.Mode)
	}
	return nil
}

// ErrNoHost signale une machine absente de l'inventaire.
var ErrNoHost = errors.New("machine non enrôlée")

// ErrHostKeyMismatch signale une empreinte qui a changé.
//
// Elle existe dans le cœur parce que les deux extrémités doivent la nommer de
// la même façon : c'est le cas MITM, et le confondre avec une panne réseau
// serait la pire confusion possible du produit.
var ErrHostKeyMismatch = errors.New("l'empreinte de la machine a changé")

// HostCatalog tient l'inventaire des machines enrôlées.
type HostCatalog interface {
	// SaveHost écrit ou remplace une machine, et retient qui l'a enrôlée.
	SaveHost(ctx context.Context, h ManagedHost) error

	// HostByName rend une machine, ou une erreur enveloppant ErrNoHost.
	HostByName(ctx context.Context, environment, name string) (ManagedHost, error)

	// ListHosts rend les machines d'un environnement, ou TOUTES quand
	// l'environnement est vide. Triées par (environnement, nom).
	ListHosts(ctx context.Context, environment string) ([]ManagedHost, error)

	// ForgetHost retire une machine de l'inventaire.
	//
	// Il ne retire RIEN de la machine elle-même : la clé publique reste dans
	// son `authorized_keys`. C'est une limite du produit, pas de cette
	// méthode, et l'appelant doit le dire à l'utilisateur.
	ForgetHost(ctx context.Context, environment, name string) error
}

// --- l'enrôlement et l'ad-hoc comme genres de run ---

// RunKind distingue ce qu'un run fait.
//
// Les trois sont des runs PARCE QUE les trois sont des actions auditées sur la
// production : l'un exécute le playbook d'un commit, l'autre pose une clé
// d'accès sur une machine, le troisième exécute un module Ansible isolé sans
// playbook. Tout le reste — le pod, le canal, les events, la clôture — leur est
// commun, et c'est ce qui a décidé de ne pas créer une table parallèle.
type RunKind string

const (
	RunPlaybook   RunKind = "playbook"
	RunEnrollment RunKind = "enrollment"
	// RunAdhoc exécute un module Ansible contre un motif de machines, sans
	// playbook — l'équivalent de la commande `ansible` (par opposition à
	// `ansible-playbook`). Plus proche de RunPlaybook que de RunEnrollment : il
	// a toujours besoin du dépôt audité et de l'inventaire, seul « quoi
	// exécuter » diffère.
	RunAdhoc RunKind = "adhoc"
)

// Valid dit si le genre fait partie de ceux que le schéma accepte.
func (k RunKind) Valid() bool {
	return k == RunPlaybook || k == RunEnrollment || k == RunAdhoc
}

// EnrollTarget est la machine qu'un run d'enrôlement doit joindre.
//
// Elle ne porte PAS le mot de passe : celui-ci vient d'un humain, ne sert
// qu'une fois, et n'a rien à faire dans un journal répliqué, sauvegardé et
// jamais purgé (ADR-0013). Il transite par un magasin éphémère, lu à la
// livraison de l'ordre et détruit ensuite.
type EnrollTarget struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Login   string `json:"login"`
}

// Validate applique les invariants de la cible.
func (t EnrollTarget) Validate() error {
	// La cible réutilise la validation d'une machine enrôlée, parce que c'est
	// bien ce qu'elle deviendra : la faire diverger produirait un enrôlement qui
	// réussit et une déclaration que la base refuse.
	h := ManagedHost{
		Name: t.Host, Environment: "x", Login: t.Login,
		Address: t.Address, Port: t.Port, Mode: HostAssisted,
	}
	return h.Validate()
}

// SecretStash range une valeur sensible le temps qu'elle serve.
//
// Le mot de passe d'un enrôlement est créé par `POST /admin/enrollments` et lu
// à la LIVRAISON de l'ordre, quelques secondes plus tard. Entre les deux il faut
// bien qu'il soit quelque part, et ce quelque part ne peut pas être PostgreSQL :
// l'ADR-0018 pose qu'aucun credential n'y est persisté, WAL compris.
//
// `Take` et non `Get` : la lecture CONSOMME. Une valeur qui ne sert qu'une fois
// et qu'on peut relire est une valeur qui traîne.
type SecretStash interface {
	Stash(ctx context.Context, cle string, valeur []byte) error
	Take(ctx context.Context, cle string) ([]byte, error)
}

// ErrNoStash signale une valeur absente ou déjà consommée.
var ErrNoStash = errors.New("valeur éphémère absente ou déjà consommée")
