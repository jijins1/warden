package core

import (
	"context"
	"encoding/json"
	"regexp"
	"time"
)

// NewRun porte ce qu'on connaît d'un run au moment de l'insérer, c'est-à-dire
// AVANT que le pod existe.
//
// On y trouve ce que git ne conserve PAS (ADR-0021) : le contenu à un SHA est
// figé par git et n'a pas à être recopié ici, mais la position d'une branche à
// une date ne l'est pas — les reflogs expirent et sont locaux au miroir.
type NewRun struct {
	// RunRef est un ULID, sûr comme nom de ref git (ADR-0007) et dans une URL.
	// C'est l'identifiant qui sort du produit ; l'id bigint reste interne.
	RunRef string
	UserID int64

	// Repo est le dépôt d'où vient le run. Le rôle `git` en déduit le bare repo
	// à ouvrir : l'ADR-0006 veut tous les runs d'un dépôt dans le même, et les
	// diffs du §6.5 en dépendent.
	Repo string

	Environment string
	Inventory   string
	Playbook    string

	AuditSHA string // le commit d'audit poussé par la CLI (§5.2)
	BaseSHA  string // son parent (ADR-0003)

	// MainlineRef et MainlineSHA figent le point de comparaison. Sans eux, le
	// diff « run contre branche principale » du §6.5 se calcule contre la
	// branche telle qu'elle est AUJOURD'HUI, et change donc à chaque
	// consultation. Le nom de la ref accompagne le SHA parce que la branche de
	// référence est une configuration par environnement (§3.2) : un SHA seul
	// cesse d'être interprétable si elle change.
	MainlineRef string
	MainlineSHA string

	// Flags tels qu'exécutés (§5.5). Il n'y a pas de colonne « demandé » en
	// face : la politique serveur étant mise de côté, demandé et effectif
	// coïncident (ADR-0021).
	Flags json.RawMessage

	RunnerImage       string // ADR-0014
	RunnerImageDigest string
}

// ulid borne l'identifiant public d'un run : Crockford base32 sur 26 caractères,
// sans I, L, O ni U — exclus pour qu'un identifiant recopié à la main ne puisse
// pas être confondu avec 1 et 0.
//
// Il est tiré par la CLI et non par le serveur, ce qui supprime un aller-retour
// (ADR-0028). Il est donc de l'entrée non fiable, et sa forme est vérifiée avant
// tout usage : il compose un nom de ref git et une URL.
var ulid = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)

// ValidRunRef vérifie la forme d'un identifiant public de run.
func ValidRunRef(ref string) bool { return ulid.MatchString(ref) }

// RunCreator crée la ligne de run.
//
// Séparé de RunReader : lire un run et en créer un sont deux capacités, et les
// réunir dans un port unique donnerait le droit d'écrire à tout ce qui a besoin
// de lire.
type RunCreator interface {
	CreateRun(ctx context.Context, n NewRun) (int64, error)
}

// RefResolver rend la position d'une ref dans un dépôt.
//
// Il existe pour une raison de sécurité et non de commodité. `audit_sha` ne peut
// pas venir du client : le diff d'audit du §6.5 compare `mainline_sha` à
// `audit_sha` sans se demander à qui ce dernier appartient, donc un utilisateur
// qui déclarerait celui d'un autre lirait le code de cet autre à travers le diff
// de SON PROPRE run — auquel la politique d'accès lui donne légitimement droit.
//
// Le serveur compose donc la ref lui-même, à partir de l'identité authentifiée et
// de l'identifiant du run, et demande où elle pointe.
type RefResolver interface {
	ResolveRef(ctx context.Context, repo, ref string) (string, error)
}

// AuditRefFor compose la ref d'audit d'un run.
//
// Les deux composants viennent du SERVEUR : l'identifiant numérique de
// l'utilisateur, qui sort de l'authentification, et l'identifiant du run, dont la
// forme est vérifiée juste avant. Aucun des deux n'est cru sur parole, ce qui est
// exactement ce qui rend la résolution sûre.
//
// L'identifiant numérique plutôt que le nom affiché : celui-ci n'est ni stable ni
// unique, et il change quand l'annuaire change. Une ref d'audit vit indéfiniment
// (ADR-0013) ; elle ne peut pas dépendre d'une valeur qui bouge.
func AuditRefFor(userID int64, runRef string) string {
	return "refs/audit/" + itoa(userID) + "/" + runRef
}

// AuditPrefixFor compose le préfixe qu'un jeton de push ouvre à un utilisateur.
//
// La barre finale est ce qui borne le namespace : sans elle, le préfixe de
// l'utilisateur 1 couvrirait aussi celui de l'utilisateur 12.
func AuditPrefixFor(userID int64) string {
	return "refs/audit/" + itoa(userID) + "/"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// PushGrantIssuer frappe le jeton qui autorise un push d'audit.
//
// Un port, parce que la frappe suppose un secret partagé et un format de fil, et
// que le cœur ne connaît ni l'un ni l'autre (ADR-0019). L'implémentation vit du
// côté sortant, seul autorisé à voir le contrat `gitwire`.
//
// La portée passée ici est DÉRIVÉE de l'identité authentifiée par l'appelant. Une
// implémentation n'a pas à la vérifier — mais elle n'a pas non plus le droit de
// l'élargir.
type PushGrantIssuer interface {
	IssuePushGrant(ctx context.Context, repo, refPrefix string, expires time.Time) (string, error)
}
