package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// State est le statut d'un event, colonne de l'enveloppe normalisée
// (ADR-0021).
type State string

// Les statuts possibles. StateUnknown n'est pas une lacune de conception :
// c'est la soupape qui rend acceptable qu'un traducteur soit en retard sur une
// version d'ansible-core déployée par un projet (ADR-0014). L'event est alors
// stocké tel quel, payload d'origine intact — jamais ignoré, jamais perdu.
const (
	StateOK          State = "ok"
	StateChanged     State = "changed"
	StateFailed      State = "failed"
	StateSkipped     State = "skipped"
	StateUnreachable State = "unreachable"
	StateUnknown     State = "unknown"
)

// Valid dit si le statut fait partie de ceux que le schéma accepte.
func (s State) Valid() bool {
	switch s {
	case StateOK, StateChanged, StateFailed, StateSkipped, StateUnreachable, StateUnknown:
		return true
	}
	return false
}

// Event est l'enveloppe normalisée d'un event de run (ADR-0021).
//
// Le critère de partage entre l'enveloppe et le payload : l'enveloppe contient
// ce qui existe pour TOUT event, quel que soit le module et quelle que soit la
// version d'ansible-core. C'est ce critère, et non cette liste de champs, qui
// tranchera les cas futurs.
//
// Application immédiate : Stdout et Stderr ne sont PAS des champs. Ils
// n'existent que pour command, shell, raw et script ; un template n'a pas de
// stdout, il a un diff. Ils vivent dans Payload.
type Event struct {
	// Seq est assigné par le runner, monotone par run. C'est ce qui rend
	// l'insertion idempotente et l'ingestion parallélisable sans contrainte
	// d'ordre (ADR-0008).
	Seq        int64     `json:"seq"`
	OccurredAt time.Time `json:"occurred_at"`

	// Host est vide pour un event de niveau playbook. TaskNumber vaut 0 quand
	// la notion ne s'applique pas ; les deux deviennent NULL en base.
	Host       string `json:"host,omitempty"`
	TaskNumber int    `json:"task_number,omitempty"`
	TaskName   string `json:"task_name,omitempty"`
	Role       string `json:"role,omitempty"`

	State State `json:"state"`

	// Payload est le résultat de module, opaque. La redaction de l'ADR-0017
	// s'applique à TOUT le payload, pas aux seuls diffs : un
	// `shell: mysql -p{{ password }}` met le secret dans la line de commande,
	// donc dans un event ordinaire.
	Payload json.RawMessage `json:"payload"`

	// Diff est nil quand --diff est inactif ou que le module n'en produit pas.
	// Il est stocké dans une table SÉPARÉE à contrôle d'accès distinct
	// (ADR-0008) : il contient les secrets rendus (§6.5).
	Diff json.RawMessage `json:"diff,omitempty"`
}

// ErrInvalidEvent est la classe d'erreur des events refusés à la validation.
var ErrInvalidEvent = errors.New("event invalide")

// Validate applique les invariants que le schéma impose de toute façon, mais
// au bord du système plutôt qu'au fond : un lot rejeté par PostgreSQL ne dit
// pas QUEL event est en cause, alors qu'ici on peut le nommer.
//
// Cette fonction ne juge PAS du contenu du payload. Un event qu'on ne comprend
// pas devient StateUnknown, il ne devient pas une erreur — le traducteur est
// dans le domaine hostile et doit être incapable de faire échouer un run
// (ADR-0021).
func (e Event) Validate() error {
	if e.Seq < 0 {
		return fmt.Errorf("%w: seq négatif (%d)", ErrInvalidEvent, e.Seq)
	}
	if !e.State.Valid() {
		return fmt.Errorf("%w (seq %d): statut %q inconnu", ErrInvalidEvent, e.Seq, e.State)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w (seq %d): horodatage absent", ErrInvalidEvent, e.Seq)
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("%w (seq %d): payload absent", ErrInvalidEvent, e.Seq)
	}
	if !json.Valid(e.Payload) {
		return fmt.Errorf("%w (seq %d): payload n'est pas du JSON", ErrInvalidEvent, e.Seq)
	}
	if e.Diff != nil && !json.Valid(e.Diff) {
		return fmt.Errorf("%w (seq %d): diff n'est pas du JSON", ErrInvalidEvent, e.Seq)
	}
	return nil
}
