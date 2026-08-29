package core

import (
	"errors"
	"time"
)

// ErrNotFound est renvoyée quand une entité nommée n'existe pas.
//
// Elle vit dans le cœur parce que les adaptateurs entrants doivent pouvoir la
// reconnaître — traduire « absent » en 404 est une décision de transport — sans
// importer l'adaptateur qui l'a produite.
var ErrNotFound = errors.New("introuvable")

// ErrArtifactGone : l'entité existe, son code non.
//
// Distincte d'ErrNotFound parce que la réponse diffère — 410 contre 404 — et
// parce que l'ADR-0013 fait de « code purgé » un cas de premier ordre de
// l'interface d'audit, à traiter dès maintenant et non le jour de la première
// purge. Un run dont l'artefact a disparu a bel et bien existé.
var ErrArtifactGone = errors.New("artefact git indisponible")

// Identity est ce qu'un fournisseur d'identité affirme d'un utilisateur qui
// arrive.
type Identity struct {
	Type UserType

	// Subject est l'identifiant IMMUABLE du fournisseur : le `sub` OIDC,
	// l'entryUUID ou l'objectGUID LDAP. Jamais le DN, qui change dès qu'on
	// déplace quelqu'un d'unité organisationnelle ; jamais l'email ni le login,
	// qui changent aussi. L'ADR-0013 ne purgeant rien, un run de 2026 doit
	// encore se résoudre à la bonne personne en 2032.
	Subject string

	// DisplayName et Email sont un INSTANTANÉ rafraîchi à chaque connexion. Le
	// run pointant l'id, l'audit affiche l'identité actuelle d'un acteur passé.
	DisplayName string
	Email       string
}

// User est un utilisateur tel que stocké.
type User struct {
	ID          int64
	Type        UserType
	Subject     string
	DisplayName string
	Email       string
	// Disabled est rendu à l'appelant plutôt qu'appliqué au stockage : refuser
	// ou non est une décision d'autorisation, elle appartient au cœur.
	Disabled bool

	// IsAdmin ouvre l'administration de la configuration des dépôts, et rien
	// d'autre. Il n'élargit PAS la lecture de l'audit : un administrateur ne voit
	// pas les runs des autres, parce qu'administrer la politique et lire le code
	// d'autrui sont deux capacités distinctes qu'il n'y a aucune raison de lier.
	IsAdmin bool
}

// Run est un run tel que lu.
type Run struct {
	ID     int64
	RunRef string
	UserID int64

	// RunDay est la clé de partition des events et des diffs du run.
	RunDay time.Time

	// Repo est le dépôt d'où vient le run. Le rôle `git` en déduit le bare repo
	// à ouvrir pour les diffs du §6.5 (ADR-0006).
	Repo string

	Environment string
	Inventory   string
	Playbook    string

	// Kind distingue un run de playbook d'un run d'enrôlement. Vide vaut
	// `playbook` : c'est ce que toutes les lignes antérieures sont.
	Kind RunKind

	// EnrollTarget n'est renseignée que pour un enrôlement.
	EnrollTarget *EnrollTarget

	AuditSHA    string
	BaseSHA     string
	MainlineRef string
	MainlineSHA string

	State            RunState
	TerminationCause TerminationCause

	ChangedCount    int
	SecretsUnmasked bool

	// StartedAt et EndedAt datent le run. EndedAt est nul tant qu'il court.
	//
	// Sur `Run` et pas seulement sur `RunListItem` : un lien profond rechargé
	// n'a QUE cet appel pour peindre son en-tête, et un détail de run sans date
	// ni durée n'est pas un détail de run.
	StartedAt time.Time
	EndedAt   *time.Time

	// ArtifactPrunedAt est non nul quand le code du run a été purgé (ADR-0013).
	// L'interface d'audit doit traiter ce cas en première classe plutôt que de
	// renvoyer un 404 le jour de la première purge : le run a bien existé, c'est
	// son arbre git qui n'est plus là.
	ArtifactPrunedAt *time.Time
}

// ArtifactPruned dit si le code du run a été purgé et si ses diffs git sont donc
// devenus incalculables.
func (r Run) ArtifactPruned() bool { return r.ArtifactPrunedAt != nil }

// RunFilter décrit une interrogation de la liste des runs.
// La pagination est par keyset : la table ne se purge jamais (ADR-0013), et un
// OFFSET y coûterait un parcours proportionnel au rang demandé.
type RunFilter struct {
	// Cursor est opaque pour l'appelant : il vient d'un RunListResult.
	Cursor string

	// Limit est la taille de page (50 par défaut, 500 au maximum).
	Limit int

	// Filtres, tous facultatifs et combinés par ET.
	Environment string
	Inventory   string
	UserID      int64
	State       string
	Since       time.Time
	Until       time.Time
	ChangedMin  int
}

// RunListItem est un run dans la liste d'audit (sous-ensemble de Run).
type RunListItem struct {
	ID          int64
	RunRef      string
	UserID      int64
	Repo        string
	Environment string
	Inventory   string
	Playbook    string
	State       string

	// TerminationCause qualifie un run `interrupted`, et est vide sur tout autre
	// état.
	//
	// Dans la LISTE et pas seulement dans le détail, parce que « interrompu »
	// sans la cause est presque inutile : un Ctrl-C est un signal fort, un
	// client_disconnect_timeout est accidentel. Une liste qui n'affiche que
	// l'état oblige à ouvrir chaque run pour savoir lequel mérite un regard,
	// c'est-à-dire à jeter l'information qui compte.
	TerminationCause TerminationCause

	StartedAt       time.Time
	EndedAt         *time.Time
	ChangedCount    int
	SecretsUnmasked bool
}

// RunListResult est une page de la liste des runs.
type RunListResult struct {
	Runs []RunListItem

	// NextCursor est vide sur la dernière page.
	NextCursor string
}
