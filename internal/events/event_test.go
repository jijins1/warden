// Étage 1 de l'ADR-0010 : ni Docker, ni cluster, ni base. Si ce fichier se met
// à en avoir besoin, c'est qu'un test a glissé d'étage.
package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStateValid(t *testing.T) {
	valides := []State{StateOK, StateChanged, StateFailed, StateSkipped, StateUnreachable, StateUnknown}
	for _, s := range valides {
		if !s.Valid() {
			t.Errorf("%q devrait être un statut valide", s)
		}
	}
	// Le schéma refuse tout le reste : la liste Go et la contrainte CHECK
	// doivent dire la même chose, sinon l'erreur remonte de PostgreSQL sans
	// nommer l'event en cause.
	for _, s := range []State{"", "ok ", "OK", "rescued", "ignored"} {
		if s.Valid() {
			t.Errorf("%q ne devrait pas être un statut valide", s)
		}
	}
}

func TestValidate(t *testing.T) {
	base := func() Event {
		return Event{
			Seq:        3,
			OccurredAt: time.Now().UTC(),
			Host:       "db-01",
			State:      StateChanged,
			Payload:    json.RawMessage(`{"changed":true}`),
		}
	}

	cas := []struct {
		nom     string
		modifie func(*Event)
		valide  bool
	}{
		{"event complet", func(*Event) {}, true},
		{"seq zéro", func(e *Event) { e.Seq = 0 }, true},

		// Un event de niveau playbook n'a ni hôte ni tâche : l'enveloppe ne
		// contient que ce qui existe pour TOUT event (ADR-0021).
		{"sans hôte ni tâche", func(e *Event) { e.Host = ""; e.TaskNumber = 0; e.TaskName = "" }, true},

		// La soupape : un type d'event que le traducteur ne connaît pas
		// devient `unknown`, il ne devient pas une erreur. Le traducteur est
		// dans le domaine hostile et doit être incapable de faire échouer un
		// run.
		{"statut inconnu, payload intact", func(e *Event) {
			e.State = StateUnknown
			e.Payload = json.RawMessage(`{"event":"v2_quelque_chose_de_neuf"}`)
		}, true},

		{"diff présent", func(e *Event) { e.Diff = json.RawMessage(`{"before":"a","after":"b"}`) }, true},

		{"seq négatif", func(e *Event) { e.Seq = -1 }, false},
		{"statut hors liste", func(e *Event) { e.State = "rescued" }, false},
		{"statut vide", func(e *Event) { e.State = "" }, false},
		{"horodatage absent", func(e *Event) { e.OccurredAt = time.Time{} }, false},
		{"payload absent", func(e *Event) { e.Payload = nil }, false},
		{"payload vide", func(e *Event) { e.Payload = json.RawMessage(``) }, false},
		{"payload non JSON", func(e *Event) { e.Payload = json.RawMessage(`{pas du json`) }, false},
		{"diff non JSON", func(e *Event) { e.Diff = json.RawMessage(`{`) }, false},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			e := base()
			c.modifie(&e)

			err := e.Validate()
			if c.valide && err != nil {
				t.Fatalf("refusé alors qu'il devrait passer: %v", err)
			}
			if !c.valide {
				if err == nil {
					t.Fatal("accepté alors qu'il devrait être refusé")
				}
				if !errors.Is(err, ErrInvalidEvent) {
					t.Errorf("erreur = %v, devrait envelopper ErrInvalidEvent", err)
				}
			}
		})
	}
}

// Le seq est le point d'ancrage de l'idempotence (ADR-0008) : un message
// d'erreur qui ne le nomme pas oblige à retrouver l'event à la main dans un lot
// de plusieurs centaines.
func TestValidateNommeLeSeq(t *testing.T) {
	e := Event{Seq: 417, OccurredAt: time.Now(), State: "n'importe quoi", Payload: json.RawMessage(`{}`)}
	err := e.Validate()
	if err == nil {
		t.Fatal("attendu une erreur")
	}
	if got := err.Error(); !contains(got, "417") {
		t.Errorf("le message ne nomme pas le seq: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
