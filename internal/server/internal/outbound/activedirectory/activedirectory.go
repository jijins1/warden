// Package activedirectory vérifie un mot de passe auprès d'un Active Directory
// on-prem, et lit les groupes de l'utilisateur (ADR-0053).
//
// Le déroulé d'une connexion, et pourquoi chaque étape est séparée :
//
//  1. le COMPTE DE SERVICE cherche le DN de l'utilisateur par son
//     `sAMAccountName`. L'utilisateur ne tape pas son DN, et un bind direct par
//     `DOMAINE\login` ne rendrait pas l'objectGUID ;
//  2. le bind de l'UTILISATEUR, sur une connexion À PART, vérifie le mot de
//     passe. Aucune opération ne s'exécute jamais avec son identité sur une
//     connexion qui servira ensuite à autre chose ;
//  3. le compte de service lit les groupes, IMBRIQUÉS compris, en une requête
//     que l'AD résout lui-même (LDAP_MATCHING_RULE_IN_CHAIN).
package activedirectory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Config décrit l'annuaire à joindre.
type Config struct {
	// URL est `ldaps://dc.exemple.local:636`, ou `ldap://…:389` avec StartTLS.
	URL      string
	StartTLS bool

	// BaseDN borne la recherche des utilisateurs et des groupes.
	BaseDN string

	// BindDN et BindPassword sont ceux du compte de service, en LECTURE.
	BindDN       string
	BindPassword string

	// CAFile est l'autorité qui a signé le certificat des contrôleurs de
	// domaine, souvent une PKI interne. Vide : les autorités du système.
	CAFile string

	// Timeout borne chaque connexion et chaque opération. Défaut : 5 s.
	Timeout time.Duration
}

// Annuaire implémente core.Annuaire.
type Annuaire struct {
	cfg Config
	tls *tls.Config
}

// timeoutParDefaut est assez long pour un contrôleur de domaine distant, et
// assez court pour que la file de quatre connexions de l'ADR-0032 ne reste pas
// bloquée derrière un AD qui ne répond plus.
const timeoutParDefaut = 5 * time.Second

// New valide la configuration et échoue À FROID.
//
// Une URL `ldap://` sans StartTLS est REFUSÉE : le mot de passe de l'utilisateur
// traverserait le réseau en clair, à chaque connexion. Mieux vaut un
// control plane qui ne démarre pas qu'un control plane qui le fait.
func New(cfg Config) (*Annuaire, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("annuaire : URL %q illisible", cfg.URL)
	}
	switch {
	case u.Scheme == "ldaps":
	case u.Scheme == "ldap" && cfg.StartTLS:
	case u.Scheme == "ldap":
		return nil, fmt.Errorf("annuaire : %q sans StartTLS ferait passer les mots de passe en clair", cfg.URL)
	default:
		return nil, fmt.Errorf("annuaire : schéma %q inconnu, `ldaps` ou `ldap` avec StartTLS attendu", u.Scheme)
	}
	if cfg.BaseDN == "" || cfg.BindDN == "" || cfg.BindPassword == "" {
		return nil, errors.New("annuaire : base DN, DN et mot de passe du compte de service sont requis")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = timeoutParDefaut
	}

	tc := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile) // #nosec G304 -- chemin de configuration du déploiement
		if err != nil {
			return nil, fmt.Errorf("annuaire : autorité %s : %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("annuaire : %s ne contient aucun certificat PEM", cfg.CAFile)
		}
		tc.RootCAs = pool
	}
	return &Annuaire{cfg: cfg, tls: tc}, nil
}

// Authentifier vérifie le mot de passe et rend l'identité et les groupes.
func (a *Annuaire) Authentifier(ctx context.Context, login string, motDePasse []byte) (core.IdentiteAnnuaire, error) {
	login = strings.TrimSpace(login)

	// AVANT TOUT BIND. Un bind simple avec un DN et un mot de passe VIDE est un
	// bind non authentifié, que l'AD accepte (RFC 4513 §5.1.2) : sans ce
	// garde-fou, n'importe quel login connu ouvrirait une session. go-ldap le
	// refuse aussi par défaut ; ce contrôle-ci ne dépend pas d'une bibliothèque.
	if login == "" || len(motDePasse) == 0 {
		return core.IdentiteAnnuaire{}, core.ErrIdentifiantsRefuses
	}

	svc, err := a.connecter(ctx)
	if err != nil {
		return core.IdentiteAnnuaire{}, err
	}
	defer func() { _ = svc.Close() }()
	if err := svc.Bind(a.cfg.BindDN, a.cfg.BindPassword); err != nil {
		return core.IdentiteAnnuaire{}, fmt.Errorf("bind du compte de service : %w", err)
	}

	entree, err := a.chercherLUtilisateur(svc, login)
	if err != nil {
		return core.IdentiteAnnuaire{}, err
	}

	if err := a.verifierLeMotDePasse(ctx, entree.DN, motDePasse); err != nil {
		return core.IdentiteAnnuaire{}, err
	}

	guid, err := FormaterGUID(entree.GetRawAttributeValue("objectGUID"))
	if err != nil {
		return core.IdentiteAnnuaire{}, fmt.Errorf("compte %q : %w", login, err)
	}

	groupes, err := a.lireLesGroupes(svc, entree.DN)
	if err != nil {
		return core.IdentiteAnnuaire{}, err
	}

	nom := entree.GetAttributeValue("displayName")
	if nom == "" {
		nom = entree.GetAttributeValue("sAMAccountName")
	}
	return core.IdentiteAnnuaire{
		Subject:     guid,
		DisplayName: nom,
		Email:       entree.GetAttributeValue("mail"),
		Groupes:     groupes,
	}, nil
}

// connecter ouvre une connexion chiffrée, refermée si le contexte est annulé.
func (a *Annuaire) connecter(ctx context.Context) (*ldap.Conn, error) {
	conn, err := ldap.DialURL(a.cfg.URL,
		ldap.DialWithDialer(&net.Dialer{Timeout: a.cfg.Timeout}),
		ldap.DialWithTLSConfig(a.tls))
	if err != nil {
		return nil, fmt.Errorf("connexion à %s : %w", a.cfg.URL, err)
	}
	conn.SetTimeout(a.cfg.Timeout)

	// go-ldap ne prend pas de contexte : la connexion est fermée à l'annulation,
	// ce qui fait échouer l'opération en cours. Sans cela, un appelant parti
	// garderait une place de la file jusqu'au timeout. Une double fermeture est
	// sans effet, donc le rappel n'a pas à être retiré après le Close de
	// l'appelant.
	context.AfterFunc(ctx, func() { _ = conn.Close() })

	if a.cfg.StartTLS {
		if err := conn.StartTLS(a.tls); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("StartTLS vers %s : %w", a.cfg.URL, err)
		}
	}
	return conn, nil
}

// chercherLUtilisateur rend l'entrée du compte, ou ErrIdentifiantsRefuses.
//
// Aucune entrée et PLUSIEURS entrées rendent le même refus : la seconde ne
// devrait pas arriver dans un AD (le `sAMAccountName` est unique dans le
// domaine), mais la deviner serait choisir au hasard l'identité de quelqu'un.
func (a *Annuaire) chercherLUtilisateur(svc *ldap.Conn, login string) (*ldap.Entry, error) {
	filtre := fmt.Sprintf("(&(objectCategory=person)(objectClass=user)(sAMAccountName=%s))",
		ldap.EscapeFilter(login))
	req := ldap.NewSearchRequest(a.cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(a.cfg.Timeout/time.Second), false, filtre,
		[]string{"objectGUID", "displayName", "mail", "sAMAccountName"}, nil)

	res, err := svc.Search(req)
	switch {
	case ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded):
		return nil, core.ErrIdentifiantsRefuses
	case err != nil:
		return nil, fmt.Errorf("recherche du compte : %w", err)
	case len(res.Entries) != 1:
		return nil, core.ErrIdentifiantsRefuses
	}
	return res.Entries[0], nil
}

// verifierLeMotDePasse fait un bind de l'utilisateur sur une connexion à part.
//
// Seul le code 49 (`invalidCredentials`) est un REFUS : l'AD le rend pour un
// mauvais mot de passe comme pour un compte désactivé, verrouillé ou expiré. Tout
// autre échec est une panne, et l'appelant doit pouvoir les distinguer.
func (a *Annuaire) verifierLeMotDePasse(ctx context.Context, dn string, motDePasse []byte) error {
	conn, err := a.connecter(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	err = conn.Bind(dn, string(motDePasse))
	switch {
	case err == nil:
		return nil
	case ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials):
		return core.ErrIdentifiantsRefuses
	default:
		return fmt.Errorf("bind de l'utilisateur : %w", err)
	}
}

// lireLesGroupes rend les groupes du compte, imbriqués compris.
//
// LDAP_MATCHING_RULE_IN_CHAIN fait parcourir la chaîne d'appartenance par l'AD
// lui-même. `memberOf` ne rendrait que les groupes directs, et un droit donné à
// `GG-Warden-Prod` ne toucherait pas les membres de `GG-Equipe-Infra` qui y est
// imbriqué — alors que c'est ainsi que l'AD est organisé presque partout.
func (a *Annuaire) lireLesGroupes(svc *ldap.Conn, dn string) ([]string, error) {
	filtre := fmt.Sprintf("(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))",
		ldap.EscapeFilter(dn))
	req := ldap.NewSearchRequest(a.cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, int(a.cfg.Timeout/time.Second), false, filtre, []string{"sAMAccountName"}, nil)

	res, err := svc.SearchWithPaging(req, 500)
	if err != nil {
		return nil, fmt.Errorf("lecture des groupes : %w", err)
	}
	out := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		if nom := e.GetAttributeValue("sAMAccountName"); nom != "" {
			out = append(out, nom)
		}
	}
	return out, nil
}

// FormaterGUID rend l'objectGUID sous sa forme canonique.
//
// L'AD le stocke en 16 octets dont les TROIS premiers champs sont en
// petit-boutiste — l'ordre d'un GUID Microsoft. Les lire dans l'ordre des octets
// donnerait un identifiant stable, mais différent de celui que montrent les
// outils AD (`Get-ADUser`), et le rapprochement à la main deviendrait
// impossible.
func FormaterGUID(b []byte) (string, error) {
	if len(b) != 16 {
		return "", fmt.Errorf("objectGUID de %d octets, 16 attendus", len(b))
	}
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x",
		binary.LittleEndian.Uint32(b[0:4]),
		binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]),
		b[8:10], b[10:16]), nil
}
