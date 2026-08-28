// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Ce que ces tests protègent est le critère de sortie de la phase 2 : « tout
// run, y compris tué brutalement, produit un enregistrement d'audit avec une
// cause CORRECTE ». Une cause fausse est pire qu'une cause absente — elle se lit
// comme un fait en post-mortem.

package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// runsEnVol double la lecture des runs en vol.
type runsEnVol struct {
	encours []core.RunEnCours
	err     error

	// clos retient ce qu'on a demandé de clore, et avec quelle cause.
	clos map[int64]core.TerminationCause
	// dejaClos simule un run que le runner a clos lui-même : la garde doit
	// alors rendre false.
	dejaClos map[int64]bool
	errClose error
}

func runsEnVolDe(encours ...core.RunEnCours) *runsEnVol {
	return &runsEnVol{
		encours:  encours,
		clos:     map[int64]core.TerminationCause{},
		dejaClos: map[int64]bool{},
	}
}

func (r *runsEnVol) RunsEnCours(context.Context) ([]core.RunEnCours, error) {
	return r.encours, r.err
}

func (r *runsEnVol) CloseRunSiOuvert(_ context.Context, id int64, c core.TerminationCause) (bool, error) {
	if r.errClose != nil {
		return false, r.errClose
	}
	if r.dejaClos[id] {
		return false, nil
	}
	r.clos[id] = c
	return true, nil
}

// podsFactices double l'observation du cluster.
type podsFactices struct {
	faits     map[string]core.PodFacts
	err       error
	supprimes []string
	errDelete error
}

func (p *podsFactices) ObserverPods(context.Context) (map[string]core.PodFacts, error) {
	return p.faits, p.err
}

func (p *podsFactices) DeletePod(_ context.Context, nom string) error {
	if p.errDelete != nil {
		return p.errDelete
	}
	p.supprimes = append(p.supprimes, nom)
	return nil
}

// vieux rend un run assez ancien pour sortir de la période de grâce.
func vieux(id int64, pod string) core.RunEnCours {
	return core.RunEnCours{
		ID: id, RunRef: "01JZQ0000000000000000000AA", PodName: pod,
		StartedAt: time.Now().Add(-time.Hour),
	}
}

func monter(t *testing.T, runs *runsEnVol, pods *podsFactices) *core.Reconciler {
	t.Helper()
	r, err := core.NewReconciler(runs, pods)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return r
}

// --- la traduction des faits en causes ---

// LE test de la phase 2. Chaque situation observable doit produire SA cause, et
// pas une autre : « interrompu » sans la bonne cause est presque inutile (§6.4).
func TestChaqueTerminaisonDonneSaCause(t *testing.T) {
	t.Parallel()

	cas := map[string]struct {
		faits   core.PodFacts
		attendu core.TerminationCause
	}{
		"éviction": {
			core.PodFacts{Termine: true, PodReason: core.RaisonEvicted},
			core.CausePodEvicted,
		},
		"nœud perdu": {
			core.PodFacts{Termine: true, PodReason: core.RaisonNodeLost},
			core.CauseNodeLost,
		},
		"mémoire": {
			core.PodFacts{Termine: true, ContainerReason: core.RaisonOOMKilled, ExitCode: 137},
			core.CausePodOOMKilled,
		},
		"délai dépassé": {
			core.PodFacts{Termine: true, PodReason: core.RaisonDeadlineExceeded},
			core.CauseServerTaskTimeout,
		},
		"mort sans rien dire": {
			core.PodFacts{Termine: true, ContainerReason: "Error", ExitCode: 1},
			core.CauseUnknown,
		},
		"terminé proprement sans avoir clos": {
			// Code de sortie ZÉRO et run toujours en vol : le processus est mort
			// proprement, ce qui ne dit rien de ce que le playbook a fait
			// (§6.4). Ne PAS conclure au succès.
			core.PodFacts{Termine: true, ContainerReason: "Completed", ExitCode: 0},
			core.CauseUnknown,
		},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			if got := core.CauseDeTerminaison(c.faits); got != c.attendu {
				t.Errorf("cause = %q, attendu %q", got, c.attendu)
			}
		})
	}
}

// Une éviction se manifeste AUSSI par un conteneur terminé, et un OOM par un
// code de sortie. Le fait le plus spécifique doit l'emporter, sans quoi tout
// deviendrait `unknown`.
func TestLeFaitLePlusSpecifiqueLEmporte(t *testing.T) {
	t.Parallel()

	// Évincé ET conteneur en erreur : c'est l'éviction qui explique.
	f := core.PodFacts{
		Termine: true, PodReason: core.RaisonEvicted,
		ContainerReason: "Error", ExitCode: 137,
	}
	if got := core.CauseDeTerminaison(f); got != core.CausePodEvicted {
		t.Errorf("cause = %q, attendu %q", got, core.CausePodEvicted)
	}

	// Nœud perdu ET OOM : le nœud explique le pod, l'OOM n'est qu'un symptôme.
	f = core.PodFacts{
		Termine: true, PodReason: core.RaisonNodeLost,
		ContainerReason: core.RaisonOOMKilled,
	}
	if got := core.CauseDeTerminaison(f); got != core.CauseNodeLost {
		t.Errorf("cause = %q, attendu %q", got, core.CauseNodeLost)
	}
}

// Toute cause produite doit être acceptée par le schéma : une cause inventée
// ferait échouer l'écriture sur une violation de contrainte, donc laisserait le
// run en vol — l'inverse exact du but.
func TestToutesLesCausesProduitesSontValides(t *testing.T) {
	t.Parallel()

	for _, f := range []core.PodFacts{
		{Termine: true, PodReason: core.RaisonEvicted},
		{Termine: true, PodReason: core.RaisonNodeLost},
		{Termine: true, PodReason: core.RaisonDeadlineExceeded},
		{Termine: true, ContainerReason: core.RaisonOOMKilled},
		{Termine: true, ContainerReason: "n'importe quoi"},
		{},
	} {
		if c := core.CauseDeTerminaison(f); !c.Valid() {
			t.Errorf("cause %q refusée par le schéma", c)
		}
	}
}

// --- le passage de réconciliation ---

func TestUnPodMortFaitCloreSonRun(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-abc"))
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-abc": {Name: "warden-run-abc", Termine: true, ContainerReason: core.RaisonOOMKilled},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if bilan.Clos != 1 {
		t.Errorf("runs clos = %d, attendu 1", bilan.Clos)
	}
	if runs.clos[7] != core.CausePodOOMKilled {
		t.Errorf("cause = %q, attendu %q", runs.clos[7], core.CausePodOOMKilled)
	}
}

// Un pod qui TOURNE ne doit pas faire clore son run : il est peut-être
// simplement long. Clore ici tuerait l'audit d'un run vivant.
func TestUnPodVivantNeFaitRien(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-abc"))
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-abc": {Name: "warden-run-abc", Termine: false},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if bilan.Clos != 0 || len(runs.clos) != 0 {
		t.Errorf("un run vivant a été clos : %v", runs.clos)
	}
	if len(pods.supprimes) != 0 {
		t.Errorf("un pod vivant a été supprimé : %v", pods.supprimes)
	}
}

// Un pod DISPARU : il a existé — `MarkRunning` suit sa création — et rien de ce
// qu'on observe ne dit pourquoi il n'est plus là. `unknown` est l'aveu honnête ;
// `admin_killed` supposerait un acteur qu'on ne connaît pas.
func TestUnPodDisparuDonneUneCauseInconnue(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-disparu"))
	pods := &podsFactices{faits: map[string]core.PodFacts{}}

	if _, err := monter(t, runs, pods).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if runs.clos[7] != core.CauseUnknown {
		t.Errorf("cause = %q, attendu %q", runs.clos[7], core.CauseUnknown)
	}
}

// LA garde qui rend le réconciliateur sûr. Le runner clôt son propre run par
// l'ingestion (§6.4, marche 2), et le pod se termine juste après : la course est
// permanente. Une clôture non gardée remplacerait « succeeded » par
// « interrupted / pod_evicted » sur un run parfaitement terminé.
func TestUnRunDejaClosNEstPasEcrase(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-abc"))
	runs.dejaClos[7] = true
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-abc": {Name: "warden-run-abc", Termine: true, ContainerReason: "Completed"},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if bilan.Clos != 0 {
		t.Errorf("clos = %d : un run déjà clos a été réécrit", bilan.Clos)
	}
}

// La période de grâce protège de la LISTE, pas de `MarkRunning` : un pod créé à
// l'instant peut ne pas encore apparaître, et le conclure absent clôturerait un
// run parfaitement vivant.
func TestUnRunTropJeuneEstEpargne(t *testing.T) {
	t.Parallel()

	jeune := core.RunEnCours{
		ID: 7, PodName: "warden-run-neuf",
		StartedAt: time.Now(), // à l'instant
	}
	runs := runsEnVolDe(jeune)
	pods := &podsFactices{faits: map[string]core.PodFacts{}} // pas encore visible

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if bilan.Examines != 0 || len(runs.clos) != 0 {
		t.Errorf("un run trop jeune a été réconcilié : %v", runs.clos)
	}
}

// Idempotence : la leader election par Lease ne garantit pas l'unicité, et deux
// passages ne doivent pas produire deux clôtures.
func TestDeuxPassagesNeClosentQuUneFois(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-abc"))
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-abc": {Name: "warden-run-abc", Termine: true},
	}}
	r := monter(t, runs, pods)

	premier, _ := r.Reconcile(context.Background())
	runs.dejaClos[7] = true // ce que la base ferait après la première clôture
	second, _ := r.Reconcile(context.Background())

	if premier.Clos != 1 || second.Clos != 0 {
		t.Errorf("clôtures = %d puis %d, attendu 1 puis 0", premier.Clos, second.Clos)
	}
}

// Un run en échec ne doit pas empêcher les suivants d'être traités : abandonner
// sur le premier laisserait tous les autres en vol.
func TestUneErreurNInterromptPasLePassage(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(1, "pod-a"), vieux(2, "pod-b"))
	runs.errClose = errors.New("base indisponible")
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"pod-a": {Termine: true},
		"pod-b": {Termine: true},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err == nil {
		t.Fatal("l'erreur n'a pas été rendue")
	}
	if bilan.Examines != 2 {
		t.Errorf("examinés = %d, attendu 2 : le passage s'est arrêté au premier échec", bilan.Examines)
	}
}

// --- le ramassage (§8.7) ---

// Un pod n'est jamais réutilisé (§8.2) : terminé et son run clos, il n'a plus
// d'usage. Sans ramassage ils s'accumulent — c'est ce qu'on observe en vrai.
func TestLesPodsTerminesSansRunSontRamasses(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe() // plus aucun run en vol
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-vieux": {Name: "warden-run-vieux", Termine: true},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if bilan.Ramasses != 1 || len(pods.supprimes) != 1 {
		t.Errorf("ramassés = %d (%v)", bilan.Ramasses, pods.supprimes)
	}
}

// Le ramassage ne touche JAMAIS un pod dont un run est encore en vol :
// supprimer un pod vivant tuerait un run en cours d'exécution.
func TestLeRamassageNeTuePasUnRunEnVol(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-encours"))
	pods := &podsFactices{faits: map[string]core.PodFacts{
		// Terminé, mais son run est encore listé comme en vol : on le clôt, on
		// ne le ramasse qu'au passage suivant.
		"warden-run-encours": {Termine: true},
	}}

	bilan, err := monter(t, runs, pods).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(pods.supprimes) != 0 {
		t.Errorf("un pod dont le run était en vol a été supprimé : %v", pods.supprimes)
	}
	if bilan.Clos != 1 {
		t.Errorf("le run aurait dû être clos : %+v", bilan)
	}
}

// --- la boucle ---

// Un passage IMMÉDIAT au démarrage, avant le premier tick. Le cas qui compte le
// plus est le redémarrage du contrôleur après un incident, où des runs attendent
// déjà d'être clos — les faire attendre un intervalle de plus serait perdre du
// temps précisément quand il y a du travail.
func TestBoucleReconcilieAvantLePremierTick(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe(vieux(7, "warden-run-abc"))
	pods := &podsFactices{faits: map[string]core.PodFacts{
		"warden-run-abc": {Termine: true},
	}}

	ctx, annuler := context.WithCancel(context.Background())
	passages := make(chan core.Bilan, 1)

	go func() {
		_ = monter(t, runs, pods).Boucler(ctx, time.Hour, func(b core.Bilan, _ error) {
			select {
			case passages <- b:
			default:
			}
		})
	}()

	select {
	case b := <-passages:
		if b.Clos != 1 {
			t.Errorf("bilan = %+v", b)
		}
	case <-time.After(2 * time.Second):
		t.Error("aucun passage avant le premier tick")
	}
	annuler()
}

// Une erreur de passage n'arrête PAS la boucle : la base ou l'API Kubernetes
// peuvent être indisponibles un moment, et s'arrêter là laisserait les runs en
// vol jusqu'au prochain redémarrage — exactement la panne qu'on répare.
func TestUneErreurNArretePasLaBoucle(t *testing.T) {
	t.Parallel()

	runs := runsEnVolDe()
	runs.err = errors.New("base indisponible")
	pods := &podsFactices{}

	ctx, annuler := context.WithCancel(context.Background())
	defer annuler()

	erreurs := make(chan error, 4)
	go func() {
		_ = monter(t, runs, pods).Boucler(ctx, 10*time.Millisecond, func(_ core.Bilan, err error) {
			select {
			case erreurs <- err:
			default:
			}
		})
	}()

	// Deux passages en échec : le second prouve que le premier n'a pas arrêté.
	for i := range 2 {
		select {
		case err := <-erreurs:
			if err == nil {
				t.Fatalf("passage %d : l'erreur n'a pas été rapportée", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("passage %d : la boucle s'est arrêtée sur une erreur", i)
		}
	}
}

func TestLaBoucleSArreteAvecLeContexte(t *testing.T) {
	t.Parallel()

	ctx, annuler := context.WithCancel(context.Background())
	annuler()

	err := monter(t, runsEnVolDe(), &podsFactices{}).Boucler(ctx, time.Millisecond, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, attendu context.Canceled", err)
	}
}

func TestNewReconcilerRefuseUnMontageIncomplet(t *testing.T) {
	t.Parallel()

	if _, err := core.NewReconciler(nil, &podsFactices{}); err == nil {
		t.Error("un réconciliateur sans lecture des runs a été construit")
	}
	if _, err := core.NewReconciler(runsEnVolDe(), nil); err == nil {
		t.Error("un réconciliateur sans observation des pods a été construit")
	}
}
