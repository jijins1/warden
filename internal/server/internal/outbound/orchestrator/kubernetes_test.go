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
	wire "github.com/jijins1/warden/internal/wire/dispatch"
	ingestwire "github.com/jijins1/warden/internal/wire/ingest"
)

const ns = "warden"

func monter(t *testing.T) (*orchestrator.Kubernetes, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset()
	k, err := orchestrator.New(client, orchestrator.Config{
		Namespace:   ns,
		DispatchURL: "http://warden-dispatch.warden.svc:8084",
		IngestURL:   "http://warden-ingest.warden.svc:8085",
	})
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
	var secretMonte bool
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.MountPath == orchestrator.CheminJeton {
			secretMonte = m.ReadOnly
		}
	}
	if !secretMonte {
		t.Errorf("le jeton n'est pas monté en lecture seule : %+v",
			pod.Spec.Containers[0].VolumeMounts)
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

// TestLePodADeQuoiEcrire épingle un défaut qui ne se voit qu'à l'exécution.
//
// La racine est en lecture seule (§7). Sans volume inscriptible, le conteneur n'a
// AUCUN endroit où écrire : git ne matérialise pas son arbre, et Ansible échoue
// en créant son répertoire temporaire — sur un message Python parlant de
// permissions, qui ne nomme jamais la cause. Le défaut a été trouvé en
// construisant l'image, pas en relisant la spec.
//
// Un emptyDir et non un PVC : il meurt avec le pod, ce qui est exactement voulu
// puisqu'un pod n'est jamais réutilisé (§8.2).
func TestLePodADeQuoiEcrire(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	pod, _ := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})

	var espace *corev1.VolumeMount
	for i, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.MountPath == orchestrator.CheminEspace {
			espace = &pod.Spec.Containers[0].VolumeMounts[i]
		}
	}
	if espace == nil {
		t.Fatalf("aucun volume monté sur %s : le conteneur ne peut rien écrire",
			orchestrator.CheminEspace)
		return // `t.Fatalf` termine déjà ; explicite pour les analyseurs qui ne le prouvent pas
	}
	if espace.ReadOnly {
		t.Error("l'espace de travail est monté en lecture seule")
	}

	var emptyDir bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == espace.Name && v.EmptyDir != nil {
			emptyDir = true
		}
	}
	if !emptyDir {
		t.Error("l'espace de travail n'est pas un emptyDir : il survivrait au pod")
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

// TestLePodSaitQuiAppeler est le test qui manquait, et son absence a coûté un
// maillon entier.
//
// L'orchestrateur posait `WARDEN_TOKEN_FILE` ; le runner exigeait en plus
// `WARDEN_DISPATCH_URL`, que personne ne passait. Les deux moitiés étaient
// cohérentes et testées chacune de son côté — RIEN ne comparait les deux listes.
// Un pod aurait démarré, lu son jeton, et se serait arrêté faute de savoir qui
// appeler : panne se présentant comme un défaut du runner alors qu'elle venait
// de la podspec.
//
// Les noms viennent désormais du contrat de fil, que les deux extrémités lisent,
// ce qui rend la divergence impossible plutôt que testée. Ce test vérifie que les
// valeurs, elles, sont bien posées.
func TestLePodSaitQuiAppeler(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	pod, _ := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})

	vues := map[string]string{}
	for _, e := range pod.Spec.Containers[0].Env {
		vues[e.Name] = e.Value
	}

	for _, nom := range []string{
		wire.EnvTokenFile, wire.EnvDispatchURL, wire.EnvWorkspace,
		// Le sens MONTANT, ajouté au même endroit et pour la même raison : un
		// pod qui ne sait pas où rapporter exécuterait quand même, et l'audit
		// ne dirait rien de ce qui a été modifié (§6.3).
		ingestwire.EnvIngestURL,
	} {
		if vues[nom] == "" {
			t.Errorf("%s n'est pas posée : le pod ne pourrait pas fonctionner", nom)
		}
	}

	// Le jeton lui-même ne doit JAMAIS être une valeur d'environnement — seul son
	// chemin l'est (ADR-0023).
	for nom, valeur := range vues {
		if valeur == spec.Token.Reveal() {
			t.Fatalf("le jeton d'amorçage est passé en clair dans %s", nom)
		}
	}

	// L'espace de travail annoncé est SOUS le volume inscriptible, sans quoi le
	// runner écrirait sur une racine en lecture seule.
	if !strings.HasPrefix(vues[wire.EnvWorkspace], orchestrator.CheminEspace+"/") {
		t.Errorf("espace de travail %q hors du volume inscriptible %q",
			vues[wire.EnvWorkspace], orchestrator.CheminEspace)
	}
}

// TestUnOrchestrateurSansAdresseRefuseDeDemarrer : échouer à froid plutôt que
// de lancer des pods qui ne peuvent rien faire.
//
// Les deux adresses sont exigées, et la seconde plus durement que la première :
// sans dispatch le pod s'arrête sans rien avoir fait, sans ingestion il EXÉCUTE
// et personne n'en saura rien.
func TestUnOrchestrateurSansAdresseRefuseDeDemarrer(t *testing.T) {
	cas := map[string]orchestrator.Config{
		"sans dispatch":  {Namespace: ns, IngestURL: "http://i:8085"},
		"sans ingestion": {Namespace: ns, DispatchURL: "http://d:8084"},
	}
	for nom, cfg := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := orchestrator.New(fake.NewSimpleClientset(), cfg); err == nil {
				t.Fatal("un orchestrateur incomplet a été construit")
			}
		})
	}
}

// Un run d'ENRÔLEMENT n'a pas de dépôt : il ne tire aucun code. L'étiquette est
// alors absente plutôt que vide — Kubernetes refuse une valeur vide, ce qui
// faisait échouer le lancement sur un message parlant de politique réseau, très
// loin de la cause.
func TestUnPodSansDepotEstLancableEtResteSelectionnable(t *testing.T) {
	k, client := monter(t)

	spec := specValide(t)
	spec.Pool.Repo = "" // un enrôlement
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}

	pod, err := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod absent : %v", err)
	}
	if _, present := pod.Labels[orchestrator.LabelDepot]; present {
		t.Errorf("l'étiquette de dépôt est posée à vide : %v", pod.Labels)
	}
	// Ce que la NetworkPolicy sélectionne doit rester là, quoi qu'il arrive.
	if pod.Labels[orchestrator.LabelComposant] != orchestrator.ValeurComposant {
		t.Errorf("le pod n'est plus sélectionnable par la politique réseau : %v", pod.Labels)
	}
	if pod.Labels[orchestrator.LabelEnvironment] == "" {
		t.Error("l'étiquette d'environnement a disparu")
	}
}

// `/tmp` doit être INSCRIPTIBLE, et ce n'est pas du confort.
//
// MESURÉ sur le cluster : `ansible-runner` enveloppe `ansible-playbook` dans un
// `ssh-agent` dès qu'une clé d'accès est fournie, et `ssh-agent` crée sa socket
// dans `/tmp`. Avec `readOnlyRootFilesystem` et sans ce volume, il échoue sur
// « mkdtemp: private socket dir: Read-only file system » — et le run se BLOQUE
// au lieu d'échouer.
func TestLePodADeQuoiEcrireDansTmp(t *testing.T) {
	k, client := monter(t)
	spec := specValide(t)
	if err := k.LaunchPod(t.Context(), spec); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	pod, _ := client.CoreV1().Pods(ns).Get(t.Context(), spec.Name, metav1.GetOptions{})

	var monte string
	for _, vm := range pod.Spec.Containers[0].VolumeMounts {
		if vm.MountPath == orchestrator.CheminTemporaire {
			monte = vm.Name
		}
	}
	if monte == "" {
		t.Fatalf("%s n'est pas monté : ssh-agent ne pourrait pas créer sa socket",
			orchestrator.CheminTemporaire)
	}

	// Et c'est bien un volume éphémère, pas une entorse au §7 : la racine reste
	// en lecture seule.
	var ephemere bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == monte && v.EmptyDir != nil {
			ephemere = true
		}
	}
	if !ephemere {
		t.Errorf("le volume %q n'est pas un emptyDir", monte)
	}
	if ro := pod.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem; ro == nil || !*ro {
		t.Error("la racine n'est plus en lecture seule (§7)")
	}
}
