package orchestrator

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
	ingestwire "github.com/jijins1/warden/internal/wire/ingest"
)

// Étiquettes portées par tout pod runner.
//
// Elles ne sont PAS décoratives : c'est par elles que la NetworkPolicy
// sélectionne le pod, donc c'est par elles que son egress est borné (ADR-0007).
// Un pod qui n'en porterait pas serait un pod dont la politique réseau ne
// s'applique pas — c'est-à-dire un pod exécutant du code arbitraire avec un
// egress ouvert. C'est pourquoi une valeur invalide fait ÉCHOUER le lancement au
// lieu d'être omise.
const (
	LabelComposant   = "app.kubernetes.io/name"
	LabelUtilisateur = "warden.io/user"
	LabelDepot       = "warden.io/repo"
	LabelEnvironment = "warden.io/environment"

	// ValeurComposant est la valeur que la NetworkPolicy doit sélectionner.
	ValeurComposant = "warden-runner"
)

// CheminEspace est le volume INSCRIPTIBLE du pod.
//
// Il n'est pas optionnel, et son absence est un défaut qu'on ne voit qu'à
// l'exécution : le conteneur tourne avec `readOnlyRootFilesystem` (§7), donc sans
// ce volume il n'a AUCUN endroit où écrire. git n'y matérialise pas son arbre et
// Ansible échoue en tentant de créer son répertoire temporaire — sur un message
// Python parlant de permissions, très loin de la cause.
//
// Un emptyDir et non un PVC : il meurt avec le pod, ce qui est exactement voulu
// (§8.2 — un pod n'est jamais réutilisé).
const CheminEspace = "/workspace"

// CheminTemporaire est le second volume inscriptible du pod.
//
// Il paraît redondant avec CheminEspace et ne l'est pas. MESURÉ sur le
// cluster : `ansible-runner` enveloppe `ansible-playbook` dans un `ssh-agent`
// dès qu'une clé d'accès est fournie, et `ssh-agent` crée sa socket dans
// `/tmp` — pas ailleurs, pas sous `$HOME`. Avec `readOnlyRootFilesystem` (§7) et
// sans ce volume, il échoue sur « mkdtemp: private socket dir: Read-only file
// system », et le run reste bloqué sans jamais rien rapporter.
//
// La mesure en conteneur ne pouvait pas le montrer : elle tournait avec
// `--tmpfs /tmp:rw`, donc avec le volume que le pod n'avait pas.
//
// Un emptyDir et non une entorse au §7 : la racine reste en lecture seule, et
// ce répertoire meurt avec le pod.
const CheminTemporaire = "/tmp"

// CheminJeton est le point de montage du jeton d'amorçage.
//
// Un volume et non une variable d'environnement : l'objet `Pod` est celui que le
// produit liste, observe et journalise le plus, et un secret dans le champ le
// plus consulté du système est un secret qui finira dans un log (ADR-0023).
const CheminJeton = "/var/run/warden"

// Config configure l'adaptateur.
type Config struct {
	// Namespace est celui où vivent les pods runner. Le produit n'a de droits
	// que dans le sien : l'ADR-0023 a écarté TokenReview précisément parce
	// qu'elle exige un ClusterRole.
	Namespace string

	// DeadlineSeconds borne la vie d'un pod, même si le contrôleur est mort.
	// C'est la décision actée n°13, transposée du Job au Pod — voir LaunchPod.
	DeadlineSeconds int64

	// DispatchURL est l'adresse du canal TELLE QU'UN POD LA JOINT, distincte de
	// l'adresse d'écoute du service : le pod n'est pas dans le même espace réseau
	// que le processus qui sert.
	//
	// Obligatoire. Un pod lancé sans elle démarre, lit son jeton, et s'arrête
	// faute de savoir qui appeler — panne qui se présente comme un défaut du
	// runner alors qu'elle vient d'ici.
	DispatchURL string

	// IngestURL est l'adresse de l'ingestion TELLE QU'UN POD LA JOINT, sur le
	// même principe et un autre port (ADR-0008).
	//
	// Obligatoire, et pour une raison plus dure que celle du dispatch : un pod
	// qui ne sait pas où rapporter EXÉCUTERAIT quand même son playbook, puis
	// s'arrêterait sans que l'audit dise rien de ce qui a été modifié. Un run
	// dont l'audit ne s'écrit pas ne doit pas commencer (§6.3), et le refus doit
	// donc arriver ici, avant que le pod existe.
	IngestURL string
}

// DefaultDeadlineSeconds borne un run à deux heures par défaut.
const DefaultDeadlineSeconds int64 = 2 * 60 * 60

// Kubernetes lance et supprime les pods runner.
//
// Il prend `kubernetes.Interface` et non un client concret : le `doc.go` de ce
// paquet pose que la couture simulable n'est pas du confort mais le prérequis de
// l'ADR-0010, qui a décidé une stratégie de test sans cluster jetable. Les tests
// de ce paquet tournent contre le clientset factice de client-go.
type Kubernetes struct {
	client kubernetes.Interface
	cfg    Config
}

// New construit l'adaptateur et échoue à froid.
func New(client kubernetes.Interface, cfg Config) (*Kubernetes, error) {
	if client == nil {
		return nil, fmt.Errorf("orchestrateur : client Kubernetes absent")
	}
	if cfg.Namespace == "" {
		// Sans namespace, les appels partiraient vers `default` — c'est-à-dire
		// hors du seul endroit où le produit a des droits, et potentiellement
		// dans un namespace qui appartient à quelqu'un d'autre.
		return nil, fmt.Errorf("orchestrateur : namespace absent")
	}
	if cfg.DispatchURL == "" {
		return nil, fmt.Errorf("orchestrateur : adresse du canal de dispatch absente, " +
			"les pods lancés ne sauraient pas qui appeler")
	}
	if cfg.IngestURL == "" {
		return nil, fmt.Errorf("orchestrateur : adresse de l'ingestion absente, " +
			"les pods lancés exécuteraient sans pouvoir rien rapporter")
	}
	if cfg.DeadlineSeconds <= 0 {
		cfg.DeadlineSeconds = DefaultDeadlineSeconds
	}
	return &Kubernetes{client: client, cfg: cfg}, nil
}

// nomKubernetes borne ce qu'un nom d'objet peut contenir (RFC 1123).
var nomKubernetes = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// valeurEtiquette borne ce qu'une étiquette peut valoir.
var valeurEtiquette = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)

// LaunchPod crée le Secret du jeton puis le pod.
//
// L'ORDRE est imposé par l'ADR-0023 : le Secret doit exister avant que le kubelet
// monte le volume, sans quoi le pod attend au montage — et l'attente au démarrage
// est précisément ce que le produit cherche à supprimer.
//
// Un POD et non un Job, alors que la décision actée n°13 parle du Job. La raison
// est le NOM : le produit enregistre `pod_name` en base avant de créer l'objet, et
// résout `pod -> run` par ce nom et par le jeton. Un Job crée un pod dont le nom
// porte un suffixe aléatoire, donc inconnu de la base au moment de l'insertion.
// `activeDeadlineSeconds` existe aussi sur le Pod, et c'est lui qui portait la
// décision — borner le coût d'un run qui pend même si le contrôleur est mort.
//
// Idempotent sur le nom : la leader election par Lease ne garantit pas l'unicité,
// et un contrôleur qui a perdu son bail sans le savoir peut agir en double.
func (k *Kubernetes) LaunchPod(ctx context.Context, spec core.PodSpec) error {
	if !nomKubernetes.MatchString(spec.Name) {
		return fmt.Errorf("nom de pod %q invalide pour Kubernetes", spec.Name)
	}
	if spec.Image == "" {
		return fmt.Errorf("pod %s : image absente", spec.Name)
	}
	if spec.Token.Zero() {
		return fmt.Errorf("pod %s : jeton d'amorçage absent", spec.Name)
	}

	etiquettes, err := k.etiquettes(spec.Pool)
	if err != nil {
		return fmt.Errorf("pod %s : %w", spec.Name, err)
	}

	secret, err := k.creerSecret(ctx, spec, etiquettes)
	if err != nil {
		return err
	}

	pod, err := k.creerPod(ctx, spec, etiquettes)
	if err != nil {
		return err
	}

	// Le Secret devient possession du pod : sa suppression l'emporte, y compris
	// si le contrôleur meurt entre-temps. Il ne peut pas être posé à la création
	// puisque le pod n'existait pas encore — d'où cette seconde écriture.
	//
	// Son échec n'annule pas le lancement : le run est en route, et un Secret
	// orphelin est un objet inerte que le reaper du §8.7 ramasse. Refuser le run
	// pour cela coûterait davantage que ce que ça protège.
	k.rattacher(ctx, secret, pod)
	return nil
}

// etiquettes compose les étiquettes du pod, ou échoue.
//
// Échouer plutôt qu'omettre est le point pour les étiquettes qui S'APPLIQUENT :
// une valeur invalide signifie que la NetworkPolicy ne sélectionne pas le pod,
// donc qu'un conteneur exécutant du code arbitraire tourne avec un egress non
// borné. Mieux vaut un run refusé qu'un run non confiné.
//
// Le DÉPÔT fait exception, et c'est une distinction et non un assouplissement :
// un run d'enrôlement n'en a pas — il ne tire aucun code — et l'étiquette est
// alors ABSENTE plutôt que vide. Inventer une valeur mettrait un nom de dépôt
// faux sur l'objet que le produit liste et journalise le plus ; une valeur vide
// est refusée par Kubernetes, ce qui faisait échouer le lancement sur un
// message parlant de politique réseau — très loin de la cause.
//
// L'étiquette de COMPOSANT, elle, reste obligatoire : c'est la seule sur
// laquelle la NetworkPolicy sélectionne aujourd'hui.
func (k *Kubernetes) etiquettes(pool core.PodPool) (map[string]string, error) {
	if pool.UserID <= 0 {
		return nil, fmt.Errorf("clé de pool sans utilisateur : la politique réseau ne pourrait pas s'appliquer")
	}
	etiq := map[string]string{
		LabelComposant:   ValeurComposant,
		LabelUtilisateur: strconv.FormatInt(pool.UserID, 10),
		LabelEnvironment: pool.Environment,
	}
	if pool.Repo != "" {
		etiq[LabelDepot] = pool.Repo
	}
	for cle, valeur := range etiq {
		if !valeurEtiquette.MatchString(valeur) {
			return nil, fmt.Errorf("étiquette %s=%q refusée par Kubernetes : "+
				"le pod ne serait pas sélectionné par la politique réseau", cle, valeur)
		}
	}
	return etiq, nil
}

func (k *Kubernetes) creerSecret(ctx context.Context, spec core.PodSpec, etiq map[string]string) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: k.cfg.Namespace,
			Labels:    etiq,
		},
		Type: corev1.SecretTypeOpaque,
		// StringData plutôt que Data : l'encodage base64 est fait par l'API, et
		// le faire ici ajouterait un endroit où le jeton existe en clair sous une
		// autre forme.
		StringData: map[string]string{"token": spec.Token.Reveal()},
	}

	cree, err := k.client.CoreV1().Secrets(k.cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Relance du même lancement. Le jeton de la base est celui qui fait foi —
		// c'est son empreinte qui authentifie — donc un Secret préexistant porte
		// forcément le même, ou appartient à un pod que le reaper nettoiera.
		return k.client.CoreV1().Secrets(k.cfg.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
	}
	if err != nil {
		// Le message ne cite jamais le contenu du Secret.
		return nil, fmt.Errorf("création du secret du pod %s : %w", spec.Name, err)
	}
	return cree, nil
}

func (k *Kubernetes) creerPod(ctx context.Context, spec core.PodSpec, etiq map[string]string) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: k.cfg.Namespace,
			Labels:    etiq,
			// Pas de finalizer, délibérément : un finalizer dont le contrôleur ne
			// tourne plus bloque le pod en Terminating pour toujours (§8.7).
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,

			// Borne le coût d'un run qui pend, MÊME SI le contrôleur est mort.
			// C'est la décision actée n°13, et elle vaut surtout pour le cas où
			// plus personne ne surveille.
			ActiveDeadlineSeconds: &k.cfg.DeadlineSeconds,

			// Le runner n'a aucune raison de parler à l'API Kubernetes, et
			// l'ADR-0007 exige que son egress se réduise au rôle `git`, à
			// l'ingestion et au périmètre d'hôtes. Un jeton de ServiceAccount
			// monté serait une capacité de plus dans un conteneur qui exécute du
			// code arbitraire.
			AutomountServiceAccountToken: faux(),

			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   vrai(),
				RunAsUser:      entier(65534),
				RunAsGroup:     entier(65534),
				FSGroup:        entier(65534),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},

			Volumes: []corev1.Volume{
				{
					Name: "warden-token",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  spec.Name,
							DefaultMode: mode(0o400),
						},
					},
				},
				{
					// L'espace de travail : arbre matérialisé et artefacts. Voir
					// CheminEspace.
					Name:         "warden-workspace",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
				{
					// `/tmp`, sans lequel `ssh-agent` ne peut pas créer sa socket
					// et tout run portant une clé d'accès se bloque. Voir
					// CheminTemporaire.
					Name:         "warden-tmp",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},

			Containers: []corev1.Container{{
				Name:  "runner",
				Image: spec.Image,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: faux(),
					ReadOnlyRootFilesystem:   vrai(),
					Privileged:               faux(),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "warden-token",
						MountPath: CheminJeton,
						ReadOnly:  true,
					},
					{
						Name:      "warden-workspace",
						MountPath: CheminEspace,
					},
					{
						Name:      "warden-tmp",
						MountPath: CheminTemporaire,
					},
				},
				// Les NOMS viennent du contrat de fil, que le runner lit aussi :
				// deux listes tenues séparément divergent, et la divergence ne se
				// voit qu'au démarrage d'un pod.
				//
				// Le CHEMIN du jeton voyage ici, jamais le jeton : un chemin n'est
				// pas un secret.
				Env: []corev1.EnvVar{
					{Name: wire.EnvTokenFile, Value: CheminJeton + "/token"},
					{Name: wire.EnvDispatchURL, Value: k.cfg.DispatchURL},
					{Name: wire.EnvWorkspace, Value: CheminEspace + "/src"},
					{Name: ingestwire.EnvIngestURL, Value: k.cfg.IngestURL},
				},
			}},
		},
	}

	cree, err := k.client.CoreV1().Pods(k.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return k.client.CoreV1().Pods(k.cfg.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("création du pod %s : %w", spec.Name, err)
	}
	return cree, nil
}

// rattacher fait du pod le propriétaire de son Secret.
func (k *Kubernetes) rattacher(ctx context.Context, secret *corev1.Secret, pod *corev1.Pod) {
	if secret == nil || pod == nil || pod.UID == "" {
		return
	}
	for _, ref := range secret.OwnerReferences {
		if ref.UID == pod.UID {
			return // déjà rattaché : relance du même lancement
		}
	}

	secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
	})
	_, _ = k.client.CoreV1().Secrets(k.cfg.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
}

// DeletePod supprime le pod et son Secret.
//
// Les deux explicitement, bien que le rattachement les lie : si le rattachement a
// échoué au lancement, le Secret est orphelin et le ramassage de Kubernetes ne le
// verra jamais. Un jeton d'amorçage qui survit à son pod est exactement ce que
// l'ADR-0023 cherche à éviter.
//
// Idempotent : une absence n'est pas une erreur. Le reaper du §8.7 rappelle cette
// fonction sur des pods dont il n'a pas vu la fin.
func (k *Kubernetes) DeletePod(ctx context.Context, name string) error {
	if !nomKubernetes.MatchString(name) {
		return fmt.Errorf("nom de pod %q invalide", name)
	}

	err := k.client.CoreV1().Pods(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("suppression du pod %s : %w", name, err)
	}

	err = k.client.CoreV1().Secrets(k.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("suppression du secret du pod %s : %w", name, err)
	}
	return nil
}

func vrai() *bool           { v := true; return &v }
func faux() *bool           { v := false; return &v }
func entier(n int64) *int64 { return &n }
func mode(m int32) *int32   { return &m }
