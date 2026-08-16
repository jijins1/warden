// Étage 1 de l'ADR-0010 : le contrat seul, aucune E/S.
//
// Le paquet est le point de rendez-vous de deux domaines de confiance qui ne
// partagent aucun autre code (ADR-0011). Ce que ces tests protègent n'est donc
// pas une fonction, c'est un accord : ce qui part d'un pod hostile doit arriver
// intact, et ce qui ne respecte pas l'accord doit être nommé.

package ingest_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	wire "github.com/jijins1/warden/internal/wire/ingest"
)

// evt construit un event minimal valide de seq donné.
func evt(seq int64) events.Event {
	return events.Event{
		Seq:        seq,
		OccurredAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		Host:       "db-prod-02",
		State:      events.StateChanged,
		Payload:    json.RawMessage(`{"changed":true}`),
	}
}

func TestAllerRetourDUnLot(t *testing.T) {
	t.Parallel()

	e := evt(7)
	e.Diff = json.RawMessage(`[{"before":"","after":"bonjour\n"}]`)

	envoye := wire.Batch{
		Events: []events.Event{evt(6), e},
		Final: &wire.Final{
			Status:          wire.StatusInterrupted,
			Cause:           wire.CauseUserInterrupt,
			SecretsUnmasked: "jeu de secrets indisponible",
		},
	}

	brut, err := json.Marshal(envoye)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}

	var recu wire.Batch
	if err := json.Unmarshal(brut, &recu); err != nil {
		t.Fatalf("désérialisation : %v", err)
	}

	if len(recu.Events) != 2 {
		t.Fatalf("events reçus = %d, attendu 2", len(recu.Events))
	}
	if recu.Events[1].Seq != 7 || recu.Events[1].Host != "db-prod-02" {
		t.Errorf("enveloppe altérée : %+v", recu.Events[1])
	}
	// Le diff traverse le fil intact : il part dans une table séparée à
	// contrôle d'accès distinct (ADR-0008), et le perdre ici le ferait
	// silencieusement retomber dans la table ordinaire.
	if string(recu.Events[1].Diff) != string(e.Diff) {
		t.Errorf("diff = %s, attendu %s", recu.Events[1].Diff, e.Diff)
	}
	if recu.Final == nil {
		t.Fatal("trame terminale perdue")
	}
	if recu.Final.Status != wire.StatusInterrupted ||
		recu.Final.Cause != wire.CauseUserInterrupt ||
		recu.Final.SecretsUnmasked != "jeu de secrets indisponible" {
		t.Errorf("trame terminale altérée : %+v", *recu.Final)
	}
}

func TestUnLotSansFinalNAPasDeChampFinal(t *testing.T) {
	t.Parallel()

	// `omitempty` sur un pointeur : un lot ordinaire ne doit pas porter un
	// `"final":null` que le serveur devrait apprendre à distinguer d'une
	// trame terminale vide.
	brut, err := json.Marshal(wire.Batch{Events: []events.Event{evt(1)}})
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}
	if strings.Contains(string(brut), "final") {
		t.Errorf("un lot sans trame terminale porte quand même le champ : %s", brut)
	}
}

func TestUneTrameTerminaleVoyageSansEvent(t *testing.T) {
	t.Parallel()

	// Le cas réel : un run qui n'a plus rien à dire mais doit être clos.
	b := wire.Batch{Final: &wire.Final{Status: wire.StatusSucceeded}}
	if err := b.Validate(); err != nil {
		t.Fatalf("une trame terminale seule doit être acceptée : %v", err)
	}
}

func TestUnLotVideEstRefuse(t *testing.T) {
	t.Parallel()

	if err := (wire.Batch{}).Validate(); err == nil {
		t.Fatal("un lot qui ne demande rien doit être refusé")
	} else if !errors.Is(err, wire.ErrProtocol) {
		t.Errorf("erreur = %v, attendu ErrProtocol", err)
	}
}

func TestLePlafondDuNombreDEventsEstApplique(t *testing.T) {
	t.Parallel()

	evs := make([]events.Event, wire.MaxEventsPerBatch+1)
	for i := range evs {
		evs[i] = evt(int64(i))
	}
	err := wire.Batch{Events: evs}.Validate()
	if err == nil {
		t.Fatal("un lot au-delà du plafond doit être refusé")
	}
	if !strings.Contains(err.Error(), "plafond") {
		t.Errorf("erreur = %v, elle devrait nommer le plafond", err)
	}
}

// Le plafond du lot doit rester sous celui d'une instruction d'insertion, sinon
// `store` découpe et le lot cesse d'être UN déclenchement du trigger qui tient
// le compteur `changed`. Le lien entre les deux constantes n'est visible nulle
// part ailleurs.
func TestLePlafondResteSousCeluiDeLInsertion(t *testing.T) {
	t.Parallel()

	const plafondInsertion = 500 // store.maxEventsPerStatement
	if wire.MaxEventsPerBatch > plafondInsertion {
		t.Errorf("MaxEventsPerBatch = %d, au-delà du plafond d'insertion %d",
			wire.MaxEventsPerBatch, plafondInsertion)
	}
}

func TestLesSeqDoiventEtreStrictementCroissants(t *testing.T) {
	t.Parallel()

	cas := map[string][]int64{
		"répétition": {4, 4},
		"recul":      {4, 3},
	}
	for nom, seqs := range cas {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			evs := make([]events.Event, len(seqs))
			for i, s := range seqs {
				evs[i] = evt(s)
			}
			err := wire.Batch{Events: evs}.Validate()
			if err == nil {
				t.Fatal("l'insertion étant idempotente sur (run_id, seq), un seq " +
					"répété ou reculé perdrait un event en silence")
			}
			if !strings.Contains(err.Error(), "seq") {
				t.Errorf("erreur = %v, elle devrait nommer les seq", err)
			}
		})
	}
}

func TestUnEventInvalideEstRefuseEtNommeSonSeq(t *testing.T) {
	t.Parallel()

	mauvais := evt(42)
	mauvais.State = "presque"

	err := wire.Batch{Events: []events.Event{evt(41), mauvais}}.Validate()
	if err == nil {
		t.Fatal("un event au statut inconnu doit être refusé")
	}
	if !errors.Is(err, events.ErrInvalidEvent) {
		t.Errorf("erreur = %v, elle devrait envelopper ErrInvalidEvent", err)
	}
	// Sans le seq, l'émetteur ne peut que tout réémettre — et échouer pareil.
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("erreur = %v, elle devrait nommer le seq fautif", err)
	}
}

func TestUneCauseEstExigeeSurUneInterruptionEtRefuseeSinon(t *testing.T) {
	t.Parallel()

	cas := []struct {
		nom    string
		final  wire.Final
		accept bool
	}{
		{"interruption avec cause", wire.Final{Status: wire.StatusInterrupted, Cause: wire.CauseUserInterrupt}, true},
		{"interruption sans cause", wire.Final{Status: wire.StatusInterrupted}, false},
		{"succès avec cause", wire.Final{Status: wire.StatusSucceeded, Cause: wire.CauseUnknown}, false},
		{"succès nu", wire.Final{Status: wire.StatusSucceeded}, true},
		{"échec nu", wire.Final{Status: wire.StatusFailed}, true},
		{"statut inconnu", wire.Final{Status: "presque"}, false},
		{"statut absent", wire.Final{}, false},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			t.Parallel()
			err := c.final.Validate()
			if c.accept && err != nil {
				t.Fatalf("refusé à tort : %v", err)
			}
			if !c.accept && err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// Le runner ne peut pas rapporter ce qui le tue. Les causes d'infrastructure
// n'ont donc pas leur place dans un contrat où il est l'émetteur : les y
// admettre laisserait croire qu'un pod OOM-killed a pu le dire lui-même.
func TestLesCausesDInfrastructureNeSontPasDansCeContrat(t *testing.T) {
	t.Parallel()

	for _, c := range []wire.Cause{"pod_oom_killed", "pod_evicted", "node_lost", "admin_killed"} {
		if c.Valid() {
			t.Errorf("%q est acceptée alors que le runner ne peut pas la constater", c)
		}
	}
}
