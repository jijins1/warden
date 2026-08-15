// Étage 1 de l'ADR-0010 : aucun cluster. Le clientset factice de client-go est
// exactement la couture que le doc.go de ce paquet dit non négociable.

package orchestrator_test

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
)

const ns = "warden"

func monter(t *testing.T) (*orchestrator.Kubernetes, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset()
	k, err := orchestrator.New(client, orchestrator.Config{Namespace: ns})
	if err != nil {
		t.Fatalf("orchestrator.New : %v", err)
	}
	return k, client
}

func specValide(t *testing.T) core.PodSpec {
	t.Helper()
	jeton, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("jeton : %v", err)
	}
	return core.PodSpec{
		Name:  "warden-run-01jzq000000000000000000abc",
		Image: "registry.invalid/runner:1",
		Token: jeton,
		Pool:  core.PodPool{UserID: 7, Repo: "depot", Environment: "prod"},
	}
}

// TestLeSecretPrecedeLePod : l'ordre qu'impose l'ADR-0023.
//
// Le Secret doit exister avant que le kubelet monte le volume, sinon le pod
// attend au montage — et l'attente au démarrage est ce que le produit cherche à
// supprimer.
func TestLeSecretPrecedeLePod(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)

	var ordre []string
	client.PrependReactor("create", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		ordre = append(ordre, action.GetResource().Resource)
		return false, nil, nil
	})

	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	if len(ordre) < 2 || ordre[0] != "secrets" || ordre[1] != "pods" {
		t.Fatalf("ordre de création %v, attendu secrets puis pods", ordre)
	}
}

// TestLeJetonEstMonteEtJamaisDansLaSpec.
//
// L'objet `Pod` est celui que le produit liste, observe et journalise le plus
// (ADR-0023). Un jeton dans une variable d'environnement finirait dans le premier
// `kubectl describe` d'incident.
func TestLeJetonEstMonteEtJamaisDansLaSpec(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)

	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}

	pod, err := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lecture du pod : %v", err)
	}

	rendu := rendreSpec(t, pod)
	if strings.Contains(rendu, spec.Token.Reveal()) {
		t.Fatal("LE JETON APPARAÎT DANS LA SPEC DU POD")
	}

	// Il arrive par un volume, en lecture seule.
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].Secret == nil {
		t.Fatalf("volume de secret absent : %+v", pod.Spec.Volumes)
	}
	monte := pod.Spec.Containers[0].VolumeMounts
	if len(monte) != 1 || !monte[0].ReadOnly {
		t.Errorf("montage inattendu : %+v", monte)
	}

	// Et il est bien dans le Secret.
	sec, err := client.CoreV1().Secrets(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lecture du secret : %v", err)
	}
	if sec.StringData["token"] != spec.Token.Reveal() {
		t.Error("le secret ne porte pas le jeton attendu")
	}
}

// TestLeDurcissementDuPodEstApplique — §7.
func TestLeDurcissementDuPodEstApplique(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	pod, _ := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("le jeton de ServiceAccount est monté : une capacité de plus dans un conteneur hostile")
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds <= 0 {
		t.Error("aucune borne de vie : un run qui pend coûte indéfiniment (décision n°13)")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("politique de redémarrage %q : un run rejoué en silence fausse l'audit", pod.Spec.RestartPolicy)
	}
	sc := pod.Spec.SecurityContext
	if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("le conteneur peut tourner en root")
	}
	cs := pod.Spec.Containers[0].SecurityContext
	if cs == nil || cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation {
		t.Error("l'élévation de privilèges est permise")
	}
	if cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem {
		t.Error("le système de fichiers racine est inscriptible")
	}
	if len(cs.Capabilities.Drop) == 0 || cs.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capacités non abandonnées : %+v", cs.Capabilities)
	}
}

// TestUneEtiquetteInvalideRefuseLeLancement est la propriété qui compte le plus
// dans ce fichier.
//
// Les étiquettes ne sont pas décoratives : c'est par elles que la NetworkPolicy
// sélectionne le pod. Une étiquette omise parce qu'invalide donnerait un pod
// exécutant du code arbitraire avec un egress NON BORNÉ — un run refusé vaut
// mieux qu'un run non confiné.
func TestUneEtiquetteInvalideRefuseLeLancement(t *testing.T) {
	cas := map[string]core.PodPool{
		"environnement avec espace": {UserID: 7, Repo: "depot", Environment: "prod eu"},
		"environnement avec barre":  {UserID: 7, Repo: "depot", Environment: "prod/eu"},
		"dépôt trop long":           {UserID: 7, Repo: strings.Repeat("a", 64), Environment: "prod"},
		"utilisateur absent":        {Repo: "depot", Environment: "prod"},
		"environnement vide":        {UserID: 7, Repo: "depot"},
	}

	for nom, pool := range cas {
		t.Run(nom, func(t *testing.T) {
			k, client := monter(t)
			spec := specValide(t)
			spec.Pool = pool

			if err := k.LaunchPod(t.Context(), spec); err == nil {
				t.Fatal("un pod a été lancé sans étiquette valide")
			}
			pods, _ := client.CoreV1().Pods(ns).List(t.Context(), metav1.ListOptions{})
			if len(pods.Items) != 0 {
				t.Error("un pod existe malgré le refus")
			}
		})
	}
}

// TestLeLancementEstIdempotent : la leader election par Lease ne garantit pas
// l'unicité, et un contrôleur qui a perdu son bail sans le savoir peut agir en
// double.
func TestLeLancementEstIdempotent(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)

	for range 3 {
		if err := k.LaunchPod(t.Context(), spec); err != nil {
			t.Fatalf("LaunchPod : %v", err)
		}
	}

	pods, _ := client.CoreV1().Pods(ns).List(t.Context(), metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Errorf("%d pods créés", len(pods.Items))
	}
	secrets, _ := client.CoreV1().Secrets(ns).List(t.Context(), metav1.ListOptions{})
	if len(secrets.Items) != 1 {
		t.Errorf("%d secrets créés", len(secrets.Items))
	}
}

// TestLaSuppressionEmporteLeSecret.
//
// Explicitement, bien que le rattachement les lie : si le rattachement a échoué au
// lancement, le Secret est orphelin et le ramassage de Kubernetes ne le verra
// jamais. Un jeton d'amorçage qui survit à son pod est exactement ce que
// l'ADR-0023 cherche à éviter.
func TestLaSuppressionEmporteLeSecret(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}

	if err := k.DeletePod(t.Context(), spec.Name); err != nil {
		t.Fatalf("DeletePod : %v", err)
	}

	pods, _ := client.CoreV1().Pods(ns).List(t.Context(), metav1.ListOptions{})
	secrets, _ := client.CoreV1().Secrets(ns).List(t.Context(), metav1.ListOptions{})
	if len(pods.Items) != 0 || len(secrets.Items) != 0 {
		t.Errorf("restes : %d pod(s), %d secret(s)", len(pods.Items), len(secrets.Items))
	}

	// Idempotente : le reaper du §8.7 rappelle cette fonction sur des pods dont
	// il n'a pas vu la fin.
	if err := k.DeletePod(t.Context(), spec.Name); err != nil {
		t.Errorf("seconde suppression : %v", err)
	}
}

// TestUnNamespaceAbsentEchoueAFroid : sans lui, les appels partiraient vers
// `default`, hors du seul endroit où le produit a des droits.
func TestUnNamespaceAbsentEchoueAFroid(t *testing.T) {
	if _, err := orchestrator.New(fake.NewSimpleClientset(), orchestrator.Config{}); err == nil {
		t.Fatal("un orchestrateur sans namespace a été construit")
	}
	if _, err := orchestrator.New(nil, orchestrator.Config{Namespace: ns}); err == nil {
		t.Fatal("un orchestrateur sans client a été construit")
	}
}

func rendreSpec(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	b, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}
	return string(b)
}
