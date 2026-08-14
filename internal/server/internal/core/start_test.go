package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// journal retient l'ORDRE des opérations : c'est lui que ces tests vérifient,
// parce que c'est lui qui porte la correction.
type journal struct {
	etapes []string

	pods    []core.NewPod
	espaces []string
	lances  []core.PodSpec

	echecEspace error
	echecLancer error
}

func (j *journal) RegisterPod(_ context.Context, n core.NewPod) (int64, error) {
	j.etapes = append(j.etapes, "register")
	j.pods = append(j.pods, n)
	return int64(len(j.pods)), nil
}

func (j *journal) CreateRunNamespace(_ context.Context, _, ns, _ string) error {
	j.etapes = append(j.etapes, "namespace")
	if j.echecEspace != nil {
		return j.echecEspace
	}
	j.espaces = append(j.espaces, ns)
	return nil
}

func (j *journal) IssueFetchGrant(context.Context, string, string, time.Time) (string, error) {
	return "jeton", nil
}

func (j *journal) LaunchPod(_ context.Context, spec core.PodSpec) error {
	j.etapes = append(j.etapes, "launch")
	if j.echecLancer != nil {
		return j.echecLancer
	}
	j.lances = append(j.lances, spec)
	return nil
}

func (j *journal) DeletePod(context.Context, string) error { return nil }

func runADemarrer() core.Run {
	return core.Run{
		ID: 42, RunRef: "01JZQ000000000000000000ABC", UserID: 7,
		Repo: "depot", Environment: "prod",
		AuditSHA: "1111111111111111111111111111111111111111",
	}
}

// TestLOrdreDesEtapesEstCeluiQuiEvite Les Courses.
//
// Le jeton est enregistré avant que le pod existe (ADR-0023 : le Secret doit
// précéder le montage), et la ref d'espace de noms est créée AVANT le lancement.
// L'ordre inverse ouvrirait une course où un pod rapide fetch une ref absente —
// échec qui se présenterait comme un dépôt vide, très loin de la cause.
func TestLOrdreDesEtapesEstCeluiQuiEviteLesCourses(t *testing.T) {
	j := &journal{}
	s, err := core.NewRunStarter(j, j, j, "registry.invalid/runner:1")
	if err != nil {
		t.Fatalf("NewRunStarter : %v", err)
	}

	nom, err := s.Start(t.Context(), runADemarrer())
	if err != nil {
		t.Fatalf("Start : %v", err)
	}

	attendu := []string{"register", "namespace", "launch"}
	if strings.Join(j.etapes, ",") != strings.Join(attendu, ",") {
		t.Errorf("ordre %v, attendu %v", j.etapes, attendu)
	}
	if nom == "" || !strings.Contains(nom, strings.ToLower(runADemarrer().RunRef)) {
		t.Errorf("nom de pod inattendu : %q", nom)
	}
}

// TestLePodNaitAttacheASonRun : sans pool, il n'a jamais été destiné à un autre.
//
// Le faire naître libre pour le réclamer aussitôt passerait par le
// `ready_at IS NOT NULL` du claim, donc obligerait à attendre qu'il se connecte
// avant de lui attribuer le run pour lequel il vient d'être créé.
func TestLePodNaitAttacheASonRun(t *testing.T) {
	j := &journal{}
	s, _ := core.NewRunStarter(j, j, j, "registry.invalid/runner:1")

	if _, err := s.Start(t.Context(), runADemarrer()); err != nil {
		t.Fatalf("Start : %v", err)
	}
	if j.pods[0].RunID != 42 {
		t.Errorf("pod enregistré avec run_id %d, attendu 42", j.pods[0].RunID)
	}
	if j.pods[0].Token.Zero() {
		t.Error("pod enregistré sans jeton d'amorçage")
	}
	if j.pods[0].Pool.UserID != 7 || j.pods[0].Pool.Repo != "depot" {
		t.Errorf("clé de pool inattendue : %+v", j.pods[0].Pool)
	}
}

// TestLeJetonDAmorcageNeFuitPasDansLeNom : le nom d'un pod est listé, observé et
// journalisé partout. Le jeton n'a rien à y faire.
func TestLeJetonDAmorcageNeFuitPasDansLeNom(t *testing.T) {
	j := &journal{}
	s, _ := core.NewRunStarter(j, j, j, "registry.invalid/runner:1")

	nom, err := s.Start(t.Context(), runADemarrer())
	if err != nil {
		t.Fatalf("Start : %v", err)
	}
	if strings.Contains(nom, j.pods[0].Token.Reveal()) {
		t.Fatal("le jeton d'amorçage apparaît dans le nom du pod")
	}
	// Et il voyage jusqu'au lanceur, qui le monte par un Secret dédié.
	if j.lances[0].Token.Reveal() != j.pods[0].Token.Reveal() {
		t.Error("le pod est lancé avec un jeton différent de celui enregistré")
	}
}

// TestUnEspaceDeNomsEnEchecNeLancePasLePod.
//
// Lancer quand même produirait un pod qui démarre, tire, ne trouve rien, et
// signale un dépôt vide — alors que la cause est une ref jamais créée.
func TestUnEspaceDeNomsEnEchecNeLancePasLePod(t *testing.T) {
	j := &journal{echecEspace: errors.New("rôle git injoignable")}
	s, _ := core.NewRunStarter(j, j, j, "registry.invalid/runner:1")

	if _, err := s.Start(t.Context(), runADemarrer()); err == nil {
		t.Fatal("le pod a été lancé malgré l'échec de l'espace de noms")
	}
	if len(j.lances) != 0 {
		t.Error("un pod a été lancé")
	}
}

// TestUnRunInexecutableNeLanceRien : refuser avant de créer quoi que ce soit.
func TestUnRunInexecutableNeLanceRien(t *testing.T) {
	sansCommit := runADemarrer()
	sansCommit.AuditSHA = ""
	sansDepot := runADemarrer()
	sansDepot.Repo = ""

	for nom, run := range map[string]core.Run{
		"sans commit": sansCommit,
		"sans dépôt":  sansDepot,
	} {
		t.Run(nom, func(t *testing.T) {
			j := &journal{}
			s, _ := core.NewRunStarter(j, j, j, "registry.invalid/runner:1")

			if _, err := s.Start(t.Context(), run); err == nil {
				t.Fatal("un run inexécutable a été démarré")
			}
			if len(j.etapes) != 0 {
				t.Errorf("des opérations ont eu lieu : %v", j.etapes)
			}
		})
	}
}

// TestUneImageAbsenteEchoueAuDemarrage : à froid plutôt qu'au premier run.
func TestUneImageAbsenteEchoueAuDemarrage(t *testing.T) {
	j := &journal{}
	if _, err := core.NewRunStarter(j, j, j, ""); err == nil {
		t.Fatal("un démarreur sans image a été construit")
	}
}
