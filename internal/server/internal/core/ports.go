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
	// DiffMainline compare l'état de la branche de référence à l'état audité.
	DiffMainline(ctx context.Context, run Run) (string, error)

	// DiffPrevious compare l'état de base du run à l'état audité.
	DiffPrevious(ctx context.Context, run Run) (string, error)
}
