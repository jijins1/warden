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

	// Address est l'endroit où l'enrôlement se connecte. Une adresse et pas un
	// nom : dépendre d'une résolution faite dans le pod ferait enrôler une
	// machine que l'opérateur croyait désigner.
	Address netip.Addr
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
)

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
	if !h.Address.IsValid() {
		return fmt.Errorf("hôte %s : adresse absente ou illisible — c'est là que "+
			"l'enrôlement se connecte, et un nom d'hôte n'en tient pas lieu", h.Name)
	}
	if h.Address.IsUnspecified() || h.Address.IsMulticast() {
		// `0.0.0.0` désigne « toutes les interfaces » côté écoute et rien du
		// tout côté connexion ; une adresse multicast ne désigne pas une
		// machine. Les accepter enrôlerait une ligne qui ne joint jamais rien.
		return fmt.Errorf("hôte %s : %s ne désigne pas une machine", h.Name, h.Address)
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
