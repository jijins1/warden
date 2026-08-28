package orchestrator_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
)

// podRunner fabrique un pod tel que `LaunchPod` l'étiquette.
func podRunner(nom string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nom,
			Namespace: "warden-test",
			Labels: map[string]string{
				orchestrator.LabelComposant: orchestrator.ValeurComposant,
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// termine pose un statut de conteneur terminé sur le pod.
func termine(pod *corev1.Pod, raison string, code int32) *corev1.Pod {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: orchestrator.NomConteneur,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: raison, ExitCode: code,
		}},
	}}
	return pod
}

// --- l'extraction des faits ---

func TestUnPodQuiTourneNEstPasTermine(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodRunning)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  orchestrator.NomConteneur,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}

	f := orchestrator.FaitsDuPod(pod)
	if f.Termine {
		t.Error("un pod en cours d'exécution a été rapporté terminé")
	}
	if f.Name != "warden-run-abc" {
		t.Errorf("nom = %q", f.Name)
	}
}

// La cause qui compte le plus souvent, et celle que le §6.4 nomme.
func TestUnOOMEstLuSurLeConteneur(t *testing.T) {
	t.Parallel()

	pod := termine(podRunner("warden-run-abc", corev1.PodFailed), core.RaisonOOMKilled, 137)

	f := orchestrator.FaitsDuPod(pod)
	if !f.Termine || f.ContainerReason != core.RaisonOOMKilled || f.ExitCode != 137 {
		t.Fatalf("faits = %+v", f)
	}
	if c := core.CauseDeTerminaison(f); c != core.CausePodOOMKilled {
		t.Errorf("cause = %q, attendu %q", c, core.CausePodOOMKilled)
	}
}

// Une éviction se lit sur le POD et pas sur le conteneur : c'est le kubelet qui
// l'écrit, et le conteneur peut n'avoir aucun statut du tout.
func TestUneEvictionEstLueSurLePod(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodFailed)
	pod.Status.Reason = core.RaisonEvicted

	f := orchestrator.FaitsDuPod(pod)
	if !f.Termine {
		t.Fatal("un pod évincé n'a pas été rapporté terminé")
	}
	if c := core.CauseDeTerminaison(f); c != core.CausePodEvicted {
		t.Errorf("cause = %q, attendu %q", c, core.CausePodEvicted)
	}
}

// `activeDeadlineSeconds` est ce qui borne un run qui pend même contrôleur mort
// (décision actée n°13). Kubernetes l'écrit dans `status.reason`.
func TestUnDelaiDepasseEstLuSurLePod(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodFailed)
	pod.Status.Reason = core.RaisonDeadlineExceeded

	if c := core.CauseDeTerminaison(orchestrator.FaitsDuPod(pod)); c != core.CauseServerTaskTimeout {
		t.Errorf("cause = %q, attendu %q", c, core.CauseServerTaskTimeout)
	}
}

// Le repli sur `lastState` doit rester conditionné à la phase : sans ça, une
// terminaison ancienne ferait conclure à la mort d'un pod bien vivant.
func TestLaTerminaisonPrecedenteNeTuePasUnPodVivant(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodRunning)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  orchestrator.NomConteneur,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: core.RaisonOOMKilled, ExitCode: 137,
		}},
	}}

	f := orchestrator.FaitsDuPod(pod)
	if f.Termine {
		t.Error("un pod vivant a été rapporté terminé sur sa terminaison PRÉCÉDENTE")
	}
	if f.ContainerReason != "" {
		t.Errorf("raison = %q : lue sur une terminaison qui n'est pas la bonne", f.ContainerReason)
	}
}

// Quand le kubelet a nettoyé l'état courant, `lastState` est la dernière trace de
// l'OOM. La perdre rendrait « unknown » sur le cas le plus explicitement nommé.
func TestLaTerminaisonPrecedenteSertQuandLaPhaseEstFinale(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodFailed)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: orchestrator.NomConteneur,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: core.RaisonOOMKilled, ExitCode: 137,
		}},
	}}

	if c := core.CauseDeTerminaison(orchestrator.FaitsDuPod(pod)); c != core.CausePodOOMKilled {
		t.Errorf("cause = %q, attendu %q", c, core.CausePodOOMKilled)
	}
}

// Le statut est cherché PAR NOM. Le jour où un sidecar apparaît, prendre le rang
// zéro rapporterait la cause de terminaison de l'autre conteneur.
func TestLeStatutEstCherchleParNom(t *testing.T) {
	t.Parallel()

	pod := podRunner("warden-run-abc", corev1.PodFailed)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name: "un-sidecar",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: core.RaisonOOMKilled, ExitCode: 137,
			}},
		},
		{
			Name: orchestrator.NomConteneur,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Completed", ExitCode: 0,
			}},
		},
	}

	f := orchestrator.FaitsDuPod(pod)
	if f.ContainerReason != "Completed" || f.ExitCode != 0 {
		t.Errorf("faits = %+v : lus sur le mauvais conteneur", f)
	}
}

// Un pod sans aucun statut de conteneur — le cas d'un pod tué avant de démarrer.
// Il ne doit pas paniquer, et il doit rester lisible.
func TestUnPodSansStatutDeConteneur(t *testing.T) {
	t.Parallel()

	f := orchestrator.FaitsDuPod(podRunner("warden-run-abc", corev1.PodFailed))
	if !f.Termine || f.ContainerReason != "" {
		t.Errorf("faits = %+v", f)
	}
	if c := core.CauseDeTerminaison(f); c != core.CauseUnknown {
		t.Errorf("cause = %q, attendu %q", c, core.CauseUnknown)
	}
}

// --- la liste ---

// LE contrôle qui protège le ramassage : le réconciliateur SUPPRIME ce qu'il
// observe, donc observer un pod qui n'est pas à nous serait supprimer le pod de
// quelqu'un d'autre.
func TestObserverPodsNeVoitQueLesPodsRunner(t *testing.T) {
	t.Parallel()

	autre := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "postgres-0", Namespace: "warden-test",
		Labels: map[string]string{"app.kubernetes.io/name": "postgres"},
	}}
	client := fake.NewSimpleClientset(
		termine(podRunner("warden-run-abc", corev1.PodFailed), core.RaisonOOMKilled, 137),
		autre,
	)

	k, err := orchestrator.New(client, orchestrator.Config{
		Namespace:   "warden-test",
		DispatchURL: "https://dispatch.invalid",
		IngestURL:   "https://ingest.invalid",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	faits, err := k.ObserverPods(context.Background())
	if err != nil {
		t.Fatalf("ObserverPods: %v", err)
	}
	if len(faits) != 1 {
		t.Fatalf("faits = %v : la liste n'était pas filtrée", faits)
	}
	if _, vu := faits["postgres-0"]; vu {
		t.Error("un pod qui n'appartient pas au produit a été observé")
	}
	if f := faits["warden-run-abc"]; f.ContainerReason != core.RaisonOOMKilled {
		t.Errorf("faits = %+v", f)
	}
}

func TestObserverPodsSurUnNamespaceVide(t *testing.T) {
	t.Parallel()

	k, err := orchestrator.New(fake.NewSimpleClientset(), orchestrator.Config{
		Namespace:   "warden-test",
		DispatchURL: "https://dispatch.invalid",
		IngestURL:   "https://ingest.invalid",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	faits, err := k.ObserverPods(context.Background())
	if err != nil {
		t.Fatalf("ObserverPods: %v", err)
	}
	if len(faits) != 0 {
		t.Errorf("faits = %v", faits)
	}
}
