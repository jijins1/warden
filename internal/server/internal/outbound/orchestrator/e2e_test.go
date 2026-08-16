//go:build e2e

// Étage 3 de l'ADR-0010 : un vrai cluster.
//
// Ce que cet étage établit et que les deux autres ne peuvent PAS établir : le
// clientset factice accepte n'importe quelle podspec. Il ne connaît ni
// l'admission, ni le kubelet, ni le montage d'un volume. Une spec qu'il valide
// joyeusement peut être refusée par le serveur d'API, ou acceptée puis rester
// bloquée en ContainerCreating parce que le Secret n'existe pas.
//
// L'assertion centrale tient dans cette différence : un pod qui atteint Running
// est un pod dont le volume a été monté. Si le Secret manquait, ou si le contexte
// de sécurité était refusé, il n'y arriverait jamais.
//
// Le test crée son propre namespace et le supprime, y compris en cas d'échec. Il
// ne touche à rien d'autre.

package orchestrator_test

import (
	"context"
	"flag"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
)

// nsE2E est créé puis détruit par ce fichier. Un nom qui dit ce qu'il est : qui
// le trouve sur un cluster sait qu'il est le reste d'un test interrompu.
var nsE2E = flag.String("e2e-namespace", "warden-e2e", "namespace jetable du test e2e")

// imagePause reste Running sans rien faire, sans shell et sans écrire.
//
// Choisie pour cela : le test doit prouver que le kubelet a MONTÉ le volume, et
// un conteneur qui sort immédiatement ne le prouve pas — il pourrait sortir avant
// ou après. Rester Running le prouve. Elle est multi-arch, ce que ce cluster
// exige : ses nœuds ne sont pas tous de la même architecture.
const imagePause = "registry.k8s.io/pause:3.10"

func clientCluster(t *testing.T) *kubernetes.Clientset {
	t.Helper()

	regles := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		regles, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Skipf("aucun accès cluster : %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("client Kubernetes : %v", err)
	}
	return client
}

// namespaceJetable crée le namespace et garantit sa suppression.
func namespaceJetable(t *testing.T, client *kubernetes.Clientset) string {
	t.Helper()
	ctx := context.Background()

	// La suppression d'un namespace n'est pas instantanée : il passe par
	// Terminating, pendant lequel il EXISTE encore mais refuse toute création.
	//
	// Le piège est là : `Create` répond alors AlreadyExists, exactement comme
	// pour un namespace bien vivant. Traiter les deux pareil — ce que faisait la
	// première version — laisse le test continuer contre un namespace mourant, et
	// l'échec survient une ligne plus loin, sur la création du Secret. Il faut
	// attendre la DISPARITION, pas l'absence de conflit.
	limite := time.Now().Add(2 * time.Minute)
	for {
		existant, err := client.CoreV1().Namespaces().Get(ctx, *nsE2E, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: *nsE2E},
			}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatalf("création du namespace %s : %v", *nsE2E, err)
			}
			goto pret
		case err != nil:
			t.Fatalf("lecture du namespace %s : %v", *nsE2E, err)
		case existant.Status.Phase == corev1.NamespaceActive:
			goto pret
		}
		if !time.Now().Before(limite) {
			t.Fatalf("le namespace %s est resté en Terminating", *nsE2E)
		}
		time.Sleep(2 * time.Second)
	}
pret:

	t.Cleanup(func() {
		// Contexte neuf : celui du test peut être annulé, et le nettoyage doit
		// avoir lieu quand même. Un namespace oublié sur un cluster de trois ans
		// est exactement le genre de résidu qu'on ne retrouve jamais.
		arret, annuler := context.WithTimeout(context.Background(), 2*time.Minute)
		defer annuler()
		if err := client.CoreV1().Namespaces().Delete(arret, *nsE2E, metav1.DeleteOptions{}); err != nil {
			t.Logf("ATTENTION : namespace %s non supprimé : %v", *nsE2E, err)
		}
	})
	return *nsE2E
}

func specE2E(t *testing.T) core.PodSpec {
	t.Helper()
	jeton, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("jeton : %v", err)
	}
	return core.PodSpec{
		Name:  "warden-run-e2e",
		Image: imagePause,
		Token: jeton,
		Pool:  core.PodPool{UserID: 1, Repo: "depot", Environment: "e2e"},
	}
}

// TestUnPodReelDemarreAvecSonJeton est l'assertion que l'étage 1 ne peut pas
// faire.
//
// Elle couvre d'un coup ce qu'un clientset factice ignore : l'admission du
// serveur d'API, la validité du contexte de sécurité pour un vrai kubelet, et le
// montage effectif du Secret. Un pod qui atteint Running a franchi les trois.
func TestUnPodReelDemarreAvecSonJeton(t *testing.T) {
	client := clientCluster(t)
	ns := namespaceJetable(t, client)
	ctx := context.Background()

	k, err := orchestrator.New(client, orchestrator.Config{
		Namespace:   ns,
		DispatchURL: "http://warden-dispatch.warden.svc:8084",
		IngestURL:   "http://warden-ingest.warden.svc:8085",
	})
	if err != nil {
		t.Fatalf("orchestrator.New : %v", err)
	}
	spec := specE2E(t)

	if err := k.LaunchPod(ctx, spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	t.Cleanup(func() {
		arret, annuler := context.WithTimeout(context.Background(), time.Minute)
		defer annuler()
		_ = k.DeletePod(arret, spec.Name)
	})

	pod := attendreRunning(t, client, ns, spec.Name, 3*time.Minute)

	// Le durcissement a survécu à l'admission. Le relire depuis le serveur d'API
	// et non depuis notre objet local est le point : c'est l'état que le cluster a
	// réellement retenu.
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("le jeton de ServiceAccount est monté")
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsNonRoot == nil ||
		!*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("runAsNonRoot n'a pas survécu")
	}
	cs := pod.Spec.Containers[0].SecurityContext
	if cs == nil || cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem n'a pas survécu")
	}

	// Le Secret existe et appartient au pod : sa suppression l'emportera, même si
	// le contrôleur meurt entre-temps.
	sec, err := client.CoreV1().Secrets(ns).Get(ctx, spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lecture du secret : %v", err)
	}
	rattache := false
	for _, ref := range sec.OwnerReferences {
		if ref.UID == pod.UID {
			rattache = true
		}
	}
	if !rattache {
		t.Error("le secret n'appartient pas au pod : il survivrait à sa suppression")
	}
}

// TestLaSuppressionNeLaissseRienDerriere : un jeton d'amorçage qui survit à son
// pod est exactement ce que l'ADR-0023 cherche à éviter.
func TestLaSuppressionNeLaisseRienDerriere(t *testing.T) {
	client := clientCluster(t)
	ns := namespaceJetable(t, client)
	ctx := context.Background()

	k, err := orchestrator.New(client, orchestrator.Config{
		Namespace:   ns,
		DispatchURL: "http://warden-dispatch.warden.svc:8084",
		IngestURL:   "http://warden-ingest.warden.svc:8085",
	})
	if err != nil {
		t.Fatalf("orchestrator.New : %v", err)
	}
	spec := specE2E(t)
	spec.Name = "warden-run-e2e-suppression"

	if err := k.LaunchPod(ctx, spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	attendreRunning(t, client, ns, spec.Name, 3*time.Minute)

	if err := k.DeletePod(ctx, spec.Name); err != nil {
		t.Fatalf("DeletePod : %v", err)
	}

	// Le pod part en Terminating : ce qui compte est que le SECRET, lui,
	// disparaisse tout de suite — c'est lui qui porte le credential.
	if _, err := client.CoreV1().Secrets(ns).Get(ctx, spec.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("le secret survit à la suppression du pod : %v", err)
	}

	// Idempotente : le reaper du §8.7 rappelle DeletePod sur des pods dont il n'a
	// pas vu la fin.
	if err := k.DeletePod(ctx, spec.Name); err != nil {
		t.Errorf("seconde suppression : %v", err)
	}
}

// attendreRunning attend que le pod démarre, et dit POURQUOI s'il n'y arrive pas.
//
// Le message d'échec porte les events du pod : « le pod n'est pas Running » est
// inexploitable, alors que « FailedMount: secret warden-run-e2e not found » nomme
// la cause en une ligne.
func attendreRunning(t *testing.T, client *kubernetes.Clientset, ns, nom string, delai time.Duration) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	limite := time.Now().Add(delai)

	var dernier *corev1.Pod
	for time.Now().Before(limite) {
		pod, err := client.CoreV1().Pods(ns).Get(ctx, nom, metav1.GetOptions{})
		if err == nil {
			dernier = pod
			if pod.Status.Phase == corev1.PodRunning {
				return pod
			}
			if pod.Status.Phase == corev1.PodFailed {
				t.Fatalf("le pod a échoué : %s\n%s", pod.Status.Reason, evenements(t, client, ns, nom))
			}
		}
		time.Sleep(2 * time.Second)
	}

	phase := "inconnue"
	if dernier != nil {
		phase = string(dernier.Status.Phase)
	}
	t.Fatalf("le pod n'a pas démarré en %s (phase %s)\n%s",
		delai, phase, evenements(t, client, ns, nom))
	return nil
}

func evenements(t *testing.T, client *kubernetes.Clientset, ns, nom string) string {
	t.Helper()
	liste, err := client.CoreV1().Events(ns).List(context.Background(), metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + nom,
	})
	if err != nil {
		return "(events illisibles : " + err.Error() + ")"
	}
	var b []byte
	for _, e := range liste.Items {
		b = append(b, "  "+e.Reason+": "+e.Message+"\n"...)
	}
	if len(b) == 0 {
		return "  (aucun event)"
	}
	return string(b)
}
