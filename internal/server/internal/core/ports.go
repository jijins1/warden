package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jijins1/warden/internal/events"
)

// Les ports que les adaptateurs entrants consomment (ADR-0019).
//
// Ils sont déclarés ici, du côté qui les APPELLE, et non du côté qui les
// implémente : c'est ce qui permet à `inbound/api` de servir l'audit sans
// connaître PostgreSQL, et de se tester sans base. Un adaptateur sortant les
// satisfait sans jamais les nommer.
//
// Les interfaces restent étroites volontairement. Une interface large redonne à
// l'appelant l'accès à tout l'adaptateur, et la frontière ne vaut plus rien.

// RunReader lit les runs pour la consultation d'audit (§6.5).
type RunReader interface {
	// RunByRef résout l'identifiant public d'un run. Renvoie une erreur
	// enveloppant ErrNotFound quand il n'existe pas.
	RunByRef(ctx context.Context, runRef string) (Run, error)

	// ListRuns rend une page de runs. Un curseur illisible est une erreur, pas
	// un retour silencieux à la première page.
	ListRuns(ctx context.Context, filter RunFilter) (RunListResult, error)

	// RunsByHost rend les runs ayant touché un hôte, borné côté adaptateur.
	RunsByHost(ctx context.Context, host string, since, until time.Time) ([]RunListItem, error)

	// PlaybooksByHost rend les playbooks DISTINCTS joués sur un hôte, un par
	// (dépôt, playbook), chacun portant son run le plus récent — le même §6.5
	// que RunsByHost, dédupliqué par playbook plutôt que par run.
	PlaybooksByHost(ctx context.Context, host string, since, until time.Time) ([]PlaybookSummary, error)

	// PreviousRun rend le run qui précède celui-ci sur le même TRIPLET
	// (dépôt, environnement, inventaire) — le « qu'est-ce qui a changé depuis
	// que ça marchait » du §6.5.
	//
	// Le dépôt fait partie de la clé, et pas seulement pour l'index : deux runs
	// d'inventaires homonymes dans des dépôts différents n'ont aucun ancêtre
	// commun, et les differ ne donnerait pas un mauvais résultat mais un échec.
	//
	// Erreur enveloppant ErrNotFound quand ce run est le premier.
	//
	// L'appelant DOIT vérifier la visibilité du run rendu. Le run précédent
	// appartient souvent à quelqu'un d'autre, et son arbre est son code : le
	// servir sans contrôle divulguerait la source d'un tiers à qui n'a pas le
	// droit de voir ce run.
	PreviousRun(ctx context.Context, run Run) (Run, error)
}

// UserDirectory résout une identité déjà provisionnée.
//
// Il n'expose délibérément AUCUN provisionnement : créer un utilisateur
// appartient au flux de connexion, qui a vérifié l'identité. Un port de lecture
// qui saurait créer laisserait un appelant non authentifié faire grossir la
// table des utilisateurs.
type UserDirectory interface {
	UserBySubject(ctx context.Context, typ UserType, subject string) (User, error)
}

// EventDiffReader lit les payloads --diff, séparés des events par un contrôle
// d'accès distinct (ADR-0008, §6.5).
type EventDiffReader interface {
	// DiffForRun rend le diff d'un event, ou nil quand l'event n'en porte pas.
	// L'absence de diff n'est pas une erreur.
	DiffForRun(ctx context.Context, run Run, seq int64) (json.RawMessage, error)
}

// EventStream est un flux d'events en cours de lecture.
type EventStream interface {
	// Events se ferme quand le flux se termine.
	Events() <-chan events.Event

	// Err rend la cause de la fermeture, nil si elle est normale.
	Err() error
}

// EventFollower suit les events d'un run à partir d'un offset.
//
// L'offset est fourni par le client : le serveur ne garde pas trace de ce qu'un
// client déconnecté avait déjà reçu.
type EventFollower interface {
	Follow(ctx context.Context, runID int64, fromOffset int64) (EventStream, error)
}

// RepoDiffer rend les diffs git d'audit du §6.5.
//
// Le port ne prend aucun chemin de dépôt : le rôle `api` n'a jamais accès au
// filesystem git (ADR-0006), il interroge le rôle `git` par `outbound/gitclient`.
// Une implémentation qui lirait un dépôt local trahirait cette frontière.
type RepoDiffer interface {
	// DiffMainline compare la branche de référence au commit audité :
	// mainline_sha -> audit_sha.
	//
	// Le SHA de la branche est celui FIGÉ dans le run, pas la position de la
	// branche aujourd'hui : sans quoi le diff d'un run de mars changerait à
	// chaque consultation (ADR-0021).
	DiffMainline(ctx context.Context, run Run) (string, error)

	// DiffPrevious compare le commit audité du run PRÉCÉDENT à celui de ce run :
	// precedent.AuditSHA -> run.AuditSHA.
	//
	// Et NON base_sha -> audit_sha, qui répondait à une autre question — « qu'a
	// modifié l'utilisateur en local », déjà couverte par DiffMainline. Le §6.5
	// demande « qu'est-ce qui a changé depuis que ça marchait », et l'ADR-0004
	// nomme cette comparaison la justification même du choix de git.
	//
	// Le run précédent est résolu et autorisé par l'appelant : le rôle `git` ne
	// connaît pas le modèle de run, et la politique d'accès ne vit pas chez lui.
	DiffPrevious(ctx context.Context, run, precedent Run) (string, error)

	// DiffAgainst compare le commit audité du run à un commit arbitraire DU
	// MÊME dépôt : audit_sha -> to.
	//
	// Elle existe pour vs=current — « qu'est-ce qui a changé depuis ce run,
	// playbook actuel compris » — où `to` est le sommet courant de la branche
	// protégée dans le miroir, résolu par l'appelant via RefResolver. Contrairement
	// à DiffMainline, qui compare au SHA FIGÉ dans le run (ADR-0021), ce diff
	// change à chaque consultation : c'est précisément ce qu'on lui demande.
	DiffAgainst(ctx context.Context, run Run, to string) (string, error)
}

// RepoFile est un fichier de l'arbre d'un commit.
type RepoFile struct {
	Path string
	Size int64
}

// RepoTree est la liste des fichiers d'un commit.
type RepoTree struct {
	Files []RepoFile

	// Truncated dit que la liste a été coupée par le rôle `git`. Une liste coupée
	// présentée comme complète ferait conclure à l'absence des fichiers restants.
	Truncated bool
}

// RepoBrowser donne à lire le code d'un run, au-delà de son diff (ADR-0055).
//
// Le commit lu est TOUJOURS l'audit_sha du run : le code qui a tourné. Le port
// ne prend pas de SHA libre, pour que l'appelant ne puisse pas s'en servir à
// lire un commit qu'aucun run autorisé ne désigne.
//
// Comme RepoDiffer, il ne prend aucun chemin de dépôt local (ADR-0006).
type RepoBrowser interface {
	// Tree liste les fichiers de l'arbre du commit audité.
	Tree(ctx context.Context, run Run) (RepoTree, error)

	// File lit un fichier de cet arbre. ErrPathMissing si le chemin n'y est
	// pas, ErrFileTooLarge s'il dépasse la borne servie, ErrArtifactGone si le
	// commit lui-même n'est plus là.
	File(ctx context.Context, run Run, path string) ([]byte, error)
}
