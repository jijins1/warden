// Étage 1 de l'ADR-0010 : le traducteur contre des fixtures, sans Python.
//
// `testdata/*.jsonl` est la sortie RÉELLE d'`ansible-runner worker` dans
// l'image du runner, capturée une fois et figée. C'est ce qui permet de tester
// le mapping de l'ADR-0021 sans ansible-core, et de voir tout de suite ce qui
// change le jour où la version d'`ansible-runner` bouge.

package exec_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/exec"
)

// traduire rejoue une fixture et rend les events produits, plus le statut final.
func traduire(t *testing.T, fixture string) ([]events.Event, string) {
	t.Helper()

	f, err := os.Open("testdata/" + fixture)
	if err != nil {
		t.Fatalf("ouverture de la fixture : %v", err)
	}
	defer func() { _ = f.Close() }()

	tr := exec.NewTraducteur()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var evs []events.Event
	var statut string
	for sc.Scan() {
		ligne := sc.Bytes()
		if exec.FinDeFlux(ligne) {
			break
		}
		if s := exec.Status(ligne); s != "" {
			statut = s
			continue
		}
		if e, ok := tr.Traduire(ligne); ok {
			evs = append(evs, e)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("lecture de la fixture : %v", err)
	}
	return evs, statut
}

// parEtat compte les events par statut.
func parEtat(evs []events.Event) map[events.State]int {
	n := map[events.State]int{}
	for _, e := range evs {
		n[e.State]++
	}
	return n
}

// LE test du mapping. Les cinq états de l'ADR-0021 apparaissent, chacun sur le
// bon event du flux mesuré.
func TestLesCinqEtatsSontReconnus(t *testing.T) {
	evs, statut := traduire(t, "flux-complet.jsonl")

	if statut != string(exec.StatutEchoue) {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutEchoue)
	}

	n := parEtat(evs)
	attendus := map[events.State]int{
		events.StateOK:          1, // la tâche `inchangee`
		events.StateChanged:     1, // la tâche `changee`
		events.StateSkipped:     1,
		events.StateFailed:      1,
		events.StateUnreachable: 1,
	}
	for etat, veut := range attendus {
		if n[etat] != veut {
			t.Errorf("events %q = %d, attendu %d", etat, n[etat], veut)
		}
	}
	// Tout le reste — playbook_on_start, les `verbose`, les `runner_on_start`,
	// playbook_on_stats — passe par la soupape et n'est jamais perdu.
	if n[events.StateUnknown] == 0 {
		t.Error("aucun event `unknown` : la soupape de l'ADR-0021 ne s'applique pas")
	}
}

// MESURÉ, et c'est le piège du mapping : une commande qui ÉCHOUE rapporte
// `res.changed = true`. Lire le drapeau avant le type d'event compterait des
// échecs comme des modifications, dans la colonne qui est la métrique d'audit
// principale (§6.5).
func TestUnEchecNEstJamaisCompteCommeChanged(t *testing.T) {
	evs, _ := traduire(t, "flux-complet.jsonl")

	var vu bool
	for _, e := range evs {
		if e.TaskName != "echouee" {
			continue
		}
		if e.State == events.StateChanged {
			t.Fatalf("la tâche en échec est comptée `changed` (seq %d)", e.Seq)
		}
		if e.State == events.StateFailed {
			vu = true
		}
	}
	if !vu {
		t.Fatal("la tâche en échec n'a produit aucun event `failed`")
	}
}

// Le diff part dans une table à contrôle d'accès distinct parce qu'il contient
// les secrets rendus (§6.5). Le laisser AUSSI dans le payload rendrait cette
// séparation décorative.
func TestLeDiffEstSortiDuPayload(t *testing.T) {
	evs, _ := traduire(t, "flux-complet.jsonl")

	var avecDiff int
	for _, e := range evs {
		if e.Diff == nil {
			continue
		}
		avecDiff++
		if !json.Valid(e.Diff) {
			t.Errorf("seq %d : diff invalide : %s", e.Seq, e.Diff)
		}
		// Le contenu du fichier écrit par la tâche `changee`. S'il est encore
		// dans le payload, il atterrira dans la table des events ordinaires.
		if strings.Contains(string(e.Payload), `"after": "bonjour`) ||
			strings.Contains(string(e.Payload), `"after":"bonjour`) {
			t.Errorf("seq %d : le diff est resté dans le payload : %s", e.Seq, e.Payload)
		}
	}
	// Deux events portent le diff : `runner_on_file_diff` et le `runner_on_ok`
	// qui le suit. Ne traiter que le second laisserait le premier déposer le
	// même contenu dans la table ordinaire.
	if avecDiff != 2 {
		t.Errorf("events avec diff = %d, attendu 2", avecDiff)
	}
}

// La trame de contrôle `status: starting` contient `env`, c'est-à-dire tout
// l'environnement du processus — donc, le jour où `forward_env` existera, les
// valeurs mêmes que l'ADR-0017 masque. Elle ne doit jamais devenir un payload.
func TestLesTramesDeControleNeDeviennentPasDesEvents(t *testing.T) {
	evs, _ := traduire(t, "flux-complet.jsonl")

	for _, e := range evs {
		if strings.Contains(string(e.Payload), `"ANSIBLE_STDOUT_CALLBACK"`) {
			t.Fatalf("seq %d : l'environnement du processus est passé dans un payload", e.Seq)
		}
	}
}

// Chaque event doit franchir la validation du schéma : un event produit ici et
// refusé à l'ingestion serait perdu au dernier mètre.
func TestChaqueEventProduitEstValide(t *testing.T) {
	for _, fixture := range []string{"flux-complet.jsonl", "flux-succes.jsonl"} {
		t.Run(fixture, func(t *testing.T) {
			evs, _ := traduire(t, fixture)
			if len(evs) == 0 {
				t.Fatal("aucun event produit")
			}
			for i, e := range evs {
				// Le Seq est assigné par le rapporteur, pas ici. On le pose
				// pour que la validation porte sur le reste.
				e.Seq = int64(i)
				if err := e.Validate(); err != nil {
					t.Errorf("event %d refusé par le schéma : %v", i, err)
				}
			}
		})
	}
}

func TestLenveloppeEstRenseignee(t *testing.T) {
	evs, _ := traduire(t, "flux-complet.jsonl")

	var vus int
	for _, e := range evs {
		if e.State == events.StateUnknown {
			continue
		}
		vus++
		if e.Host == "" {
			t.Errorf("event %q sans hôte", e.TaskName)
		}
		if e.TaskName == "" {
			t.Errorf("event sur %q sans nom de tâche", e.Host)
		}
		// Le numéro de tâche n'est PAS émis par ansible-runner : il vient du
		// compte des `playbook_on_task_start`, mémorisé par task_uuid.
		if e.TaskNumber == 0 {
			t.Errorf("event %q/%q sans numéro de tâche", e.Host, e.TaskName)
		}
		if e.OccurredAt.IsZero() || e.OccurredAt.Year() < 2020 {
			t.Errorf("event %q horodaté %v", e.TaskName, e.OccurredAt)
		}
	}
	if vus == 0 {
		t.Fatal("aucun event de tâche dans la fixture")
	}
}

// Les tâches sont numérotées dans leur ordre d'apparition, et deux hôtes de la
// même tâche portent le même numéro.
func TestLesTachesSontNumeroteesDansLOrdre(t *testing.T) {
	evs, _ := traduire(t, "flux-complet.jsonl")

	numeros := map[string]int{}
	for _, e := range evs {
		if e.TaskName == "" || e.State == events.StateUnknown {
			continue
		}
		if n, deja := numeros[e.TaskName]; deja && n != e.TaskNumber {
			t.Errorf("tâche %q numérotée %d puis %d", e.TaskName, n, e.TaskNumber)
		}
		numeros[e.TaskName] = e.TaskNumber
	}

	ordre := []string{"inchangee", "changee", "sautee", "echouee"}
	for i, nom := range ordre {
		if numeros[nom] != i+1 {
			t.Errorf("tâche %q numérotée %d, attendu %d", nom, numeros[nom], i+1)
		}
	}
}

// --- la soupape ---

// La contrainte que l'ADR-0021 pose au traducteur : il vit dans le domaine
// hostile et doit être INCAPABLE de faire échouer un run. Un event qu'il ne
// comprend pas devient `unknown` ; il ne lève pas d'erreur.
func TestUnEventInconnuNeFaitPasEchouerEtGardeSonPayload(t *testing.T) {
	tr := exec.NewTraducteur()

	const inedit = `{"counter":9,"event":"runner_on_teleportation","created":"2031-01-02T03:04:05.000001+00:00",` +
		`"event_data":{"host":"db-01","task":"future","inconnu":{"a":[1,2,3]}}}`

	e, ok := tr.Traduire([]byte(inedit))
	if !ok {
		t.Fatal("un event inconnu a été jeté")
	}
	if e.State != events.StateUnknown {
		t.Errorf("état = %q, attendu %q", e.State, events.StateUnknown)
	}
	// Payload d'origine INTACT : l'ADR-0013 ne purge rien, donc ce qu'on ne
	// stocke pas est perdu pour toujours.
	if string(e.Payload) != inedit {
		t.Errorf("payload modifié :\n  reçu   %s\n  attendu %s", e.Payload, inedit)
	}
	// L'enveloppe est renseignée quand même : c'est ce qui rend un event
	// inconnu requêtable par hôte (§6.5) au lieu d'être un blob opaque.
	if e.Host != "db-01" || e.TaskName != "future" {
		t.Errorf("enveloppe = host %q task %q", e.Host, e.TaskName)
	}
	if e.OccurredAt.Year() != 2031 {
		t.Errorf("horodatage = %v", e.OccurredAt)
	}
}

func TestUneLigneIllisibleEstConserveeEtNonJetee(t *testing.T) {
	tr := exec.NewTraducteur()

	e, ok := tr.Traduire([]byte("[WARNING]: Platform linux on host localhost is using..."))
	if !ok {
		t.Fatal("une ligne illisible a été jetée")
	}
	if e.State != events.StateUnknown {
		t.Errorf("état = %q", e.State)
	}
	if !strings.Contains(string(e.Payload), "WARNING") {
		t.Errorf("la ligne n'a pas été conservée : %s", e.Payload)
	}
	e.Seq = 0
	if err := e.Validate(); err != nil {
		t.Errorf("event refusé par le schéma : %v", err)
	}
}

func TestUneLigneVideNeProduitRien(t *testing.T) {
	tr := exec.NewTraducteur()
	for _, ligne := range []string{"", "   ", "\t"} {
		if _, ok := tr.Traduire([]byte(ligne)); ok {
			t.Errorf("la ligne %q a produit un event", ligne)
		}
	}
}

// Un horodatage de forme inattendue ne doit pas coûter l'event : un event sans
// horodatage est refusé par le schéma, et le perdre pour un champ mal formé
// serait absurde.
func TestUnHorodatageIllisibleRetombeSurLHeureDeLecture(t *testing.T) {
	tr := exec.NewTraducteur()

	avant := time.Now().UTC().Add(-time.Second)
	e, ok := tr.Traduire([]byte(`{"event":"verbose","created":"hier","event_data":{}}`))
	if !ok {
		t.Fatal("event jeté")
	}
	if e.OccurredAt.Before(avant) {
		t.Errorf("horodatage = %v, attendu l'heure de lecture", e.OccurredAt)
	}
}

// --- fin de flux ---

func TestLaTrameDArchiveTermineLeFlux(t *testing.T) {
	if !exec.FinDeFlux([]byte(`{"zipfile": 1601}`)) {
		t.Error("la trame d'archive n'est pas reconnue")
	}
	// Elle ne doit pas être confondue avec un event qui parlerait de zip.
	if exec.FinDeFlux([]byte(`{"event":"runner_on_ok","event_data":{"task":"zipfile"}}`)) {
		t.Error("un event ordinaire est pris pour la fin du flux")
	}
}

func TestSeulesLesTramesDeControlePortentUnStatut(t *testing.T) {
	if s := exec.Status([]byte(`{"status":"successful","runner_ident":"x"}`)); s != "successful" {
		t.Errorf("statut = %q", s)
	}
	// Un event nommé `status` par accident ne doit pas clore le run.
	if s := exec.Status([]byte(`{"event":"runner_on_ok","status":"successful"}`)); s != "" {
		t.Errorf("un event a été pris pour une trame de contrôle : %q", s)
	}
}
