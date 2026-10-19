package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
	ingestwire "github.com/jijins1/warden/internal/wire/ingest"
)

// Labels posés sur chaque conteneur runner. Mêmes clés que les étiquettes des
// pods, plus l'instance : un démon Docker n'a pas de namespace, et deux
// déploiements de warden sur le même hôte ne doivent pas se voir.
const (
	LabelComposant   = "app.kubernetes.io/name"
	LabelInstance    = "warden.io/instance"
	LabelUtilisateur = "warden.io/user"
	LabelDepot       = "warden.io/repo"
	LabelEnvironment = "warden.io/environment"

	ValeurComposant = "warden-runner"
)

// Chemins dans le conteneur, les mêmes que dans le pod.
const (
	CheminEspace     = "/workspace"
	CheminTemporaire = "/tmp"
)

// optionsTmpfs : `exec` explicitement, parce que Docker monte un tmpfs en
// `noexec` par défaut alors qu'un emptyDir Kubernetes ne l'est pas — un
// playbook qui marche dans un pod doit marcher ici. `mode=1777` parce que le
// tmpfs appartient à root et que le runner tourne en 65534.
const optionsTmpfs = "rw,exec,nosuid,nodev,mode=1777"

// utilisateurRunner est celui du pod : `nobody`.
const utilisateurRunner = "65534:65534"

// DefaultDeadlineSeconds est la borne par défaut, la même que Kubernetes.
const DefaultDeadlineSeconds int64 = 2 * 60 * 60

// Moteur est le sous-ensemble du client moby dont l'adaptateur a besoin. Une
// interface étroite, pour que l'étage 1 de l'ADR-0010 se passe de démon.
type Moteur interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerAttach(ctx context.Context, id string, options client.ContainerAttachOptions) (client.ContainerAttachResult, error)
	ContainerStart(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerInspect(ctx context.Context, id string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerKill(ctx context.Context, id string, options client.ContainerKillOptions) (client.ContainerKillResult, error)
	ContainerRemove(ctx context.Context, id string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

var _ Moteur = (*client.Client)(nil)

// Config décrit le lancement des runners.
type Config struct {
	// Instance distingue ce déploiement des autres sur le même démon. Elle
	// tient le rôle du namespace Kubernetes.
	Instance string

	// Network est le réseau Docker des runners, sur lequel DispatchURL et
	// IngestURL sont joignables.
	Network string

	// DeadlineSeconds borne la durée de vie d'un conteneur, voir ObserverPods.
	DeadlineSeconds int64

	DispatchURL string
	IngestURL   string

	// RegistryAuth est l'en-tête `X-Registry-Auth` du pull, vide sur un
	// registre public. Il vient du déploiement, jamais du projet — même
	// règle que les PullSecrets Kubernetes.
	RegistryAuth string
}

// Docker est l'adaptateur.
type Docker struct {
	moteur Moteur
	cfg    Config
	now    func() time.Time
}

// New construit l'adaptateur et échoue à froid.
func New(moteur Moteur, cfg Config) (*Docker, error) {
	if moteur == nil {
		return nil, errors.New("docker : client absent")
	}
	if !valeurLabel.MatchString(cfg.Instance) {
		return nil, fmt.Errorf("docker : instance %q invalide", cfg.Instance)
	}
	if cfg.Network == "" {
		// Sans réseau dédié, le runner atterrirait sur `bridge` — sans
		// garantie de joindre le serveur, et à côté de n'importe quoi d'autre.
		return nil, errors.New("docker : réseau des runners absent")
	}
	if cfg.DispatchURL == "" {
		return nil, errors.New("docker : adresse du canal de dispatch absente, " +
			"les conteneurs lancés ne sauraient pas qui appeler")
	}
	if cfg.IngestURL == "" {
		return nil, errors.New("docker : adresse de l'ingestion absente, " +
			"les conteneurs lancés exécuteraient sans pouvoir rien rapporter")
	}
	if cfg.DeadlineSeconds <= 0 {
		cfg.DeadlineSeconds = DefaultDeadlineSeconds
	}
	return &Docker{moteur: moteur, cfg: cfg, now: time.Now}, nil
}

// nomConteneur : le nom d'un pod (RFC 1123), qui est aussi un nom de
// conteneur valide.
var nomConteneur = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// valeurLabel : la même borne que les étiquettes Kubernetes, pour qu'une
// valeur acceptée ici le soit aussi là-bas.
var valeurLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)

// LaunchPod crée le conteneur, lui passe le jeton sur stdin et le démarre.
//
// Idempotent sur le nom, comme le cœur l'exige. Un conflit n'est un succès que
// si le conteneur existant a démarré : un lanceur mort entre la création et
// l'écriture du jeton laisse un conteneur `created` sans jeton, qui est
// supprimé et recréé.
func (d *Docker) LaunchPod(ctx context.Context, spec core.PodSpec) error {
	if !nomConteneur.MatchString(spec.Name) {
		return fmt.Errorf("nom de conteneur %q invalide", spec.Name)
	}
	if spec.Image == "" {
		return fmt.Errorf("conteneur %s : image absente", spec.Name)
	}
	if spec.Token.Zero() {
		return fmt.Errorf("conteneur %s : jeton d'amorçage absent", spec.Name)
	}
	labels, err := d.labels(spec.Pool)
	if err != nil {
		return fmt.Errorf("conteneur %s : %w", spec.Name, err)
	}

	id, err := d.creer(ctx, spec, labels)
	if errors.Is(err, errDejaDemarre) {
		return nil
	}
	if err != nil {
		return err
	}

	if err := d.demarrer(ctx, id, spec.Token.Reveal()); err != nil {
		// Un conteneur créé mais jamais nourri ne doit pas rester : le
		// prochain essai le recréerait de toute façon, et le reaper ne le
		// verrait qu'à la borne de durée.
		_, _ = d.moteur.ContainerRemove(context.WithoutCancel(ctx), id, client.ContainerRemoveOptions{Force: true})
		return fmt.Errorf("conteneur %s : %w", spec.Name, err)
	}
	return nil
}

// errDejaDemarre signale un conteneur du même nom, à nous, déjà parti.
var errDejaDemarre = errors.New("conteneur déjà démarré")

// creer crée le conteneur, en tirant l'image si elle manque, et rend son
// identifiant.
func (d *Docker) creer(ctx context.Context, spec core.PodSpec, labels map[string]string) (string, error) {
	options := d.optionsCreation(spec, labels)

	for essai := 0; ; essai++ {
		cree, err := d.moteur.ContainerCreate(ctx, options)
		switch {
		case err == nil:
			return cree.ID, nil

		case cerrdefs.IsConflict(err) && essai == 0:
			existant, err := d.inspecterNotre(ctx, spec.Name)
			if err != nil {
				return "", err
			}
			if existant.State != nil && existant.State.Status != container.StateCreated {
				return "", errDejaDemarre
			}
			if _, err := d.moteur.ContainerRemove(ctx, existant.ID, client.ContainerRemoveOptions{Force: true}); err != nil &&
				!cerrdefs.IsNotFound(err) {
				return "", fmt.Errorf("conteneur %s jamais démarré, non supprimé : %w", spec.Name, err)
			}

		case cerrdefs.IsNotFound(err) && essai == 0:
			// L'image manque sur le démon : la tirer, puis recréer.
			if err := d.tirer(ctx, spec.Image); err != nil {
				return "", fmt.Errorf("conteneur %s : %w", spec.Name, err)
			}

		default:
			return "", fmt.Errorf("création du conteneur %s : %w", spec.Name, err)
		}
	}
}

func (d *Docker) optionsCreation(spec core.PodSpec, labels map[string]string) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Name: spec.Name,
		Config: &container.Config{
			Image:  spec.Image,
			User:   utilisateurRunner,
			Labels: labels,

			// stdin ouvert pour le jeton, fermé après le premier client
			// détaché : le playbook lancé ensuite n'y trouve rien.
			AttachStdin: true,
			OpenStdin:   true,
			StdinOnce:   true,

			// Les NOMS viennent du contrat de fil, comme pour le pod. Le jeton
			// n'y est pas : TokenFileStdin dit seulement où le lire.
			Env: []string{
				wire.EnvTokenFile + "=" + wire.TokenFileStdin,
				wire.EnvDispatchURL + "=" + d.cfg.DispatchURL,
				wire.EnvWorkspace + "=" + CheminEspace + "/src",
				ingestwire.EnvIngestURL + "=" + d.cfg.IngestURL,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode(d.cfg.Network),
			ReadonlyRootfs: true,
			Privileged:     false,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Tmpfs: map[string]string{
				CheminEspace:     optionsTmpfs,
				CheminTemporaire: optionsTmpfs,
			},
			// Le reconciler doit pouvoir lire l'état d'un conteneur mort avant
			// que DeletePod le supprime.
			AutoRemove: false,
		},
	}
}

// demarrer s'attache au stdin, démarre le conteneur, écrit le jeton et
// ferme. L'attachement AVANT le démarrage : sinon le runner pourrait lire un
// stdin déjà fermé.
func (d *Docker) demarrer(ctx context.Context, id, jeton string) error {
	attache, err := d.moteur.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return fmt.Errorf("attachement à stdin : %w", err)
	}
	defer attache.Close()

	if _, err := d.moteur.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("démarrage : %w", err)
	}
	if _, err := io.WriteString(attache.Conn, jeton+"\n"); err != nil {
		// Le message ne cite jamais le jeton.
		return errors.New("écriture du jeton d'amorçage sur stdin")
	}
	if err := attache.CloseWrite(); err != nil {
		return fmt.Errorf("fermeture de stdin : %w", err)
	}
	return nil
}

// tirer tire l'image et attend la fin du pull.
func (d *Docker) tirer(ctx context.Context, image string) error {
	rep, err := d.moteur.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: d.cfg.RegistryAuth})
	if err != nil {
		return fmt.Errorf("tirage de l'image %s : %w", image, err)
	}
	defer func() { _ = rep.Close() }()
	if err := rep.Wait(ctx); err != nil {
		return fmt.Errorf("tirage de l'image %s : %w", image, err)
	}
	return nil
}

// labels compose les labels du conteneur, ou échoue — même règle que les
// étiquettes du pod : le dépôt est absent plutôt que vide sur un run
// d'enrôlement, le reste est obligatoire.
func (d *Docker) labels(pool core.PodPool) (map[string]string, error) {
	if pool.UserID <= 0 {
		return nil, errors.New("clé de pool sans utilisateur")
	}
	l := map[string]string{
		LabelComposant:   ValeurComposant,
		LabelInstance:    d.cfg.Instance,
		LabelUtilisateur: strconv.FormatInt(pool.UserID, 10),
		LabelEnvironment: pool.Environment,
	}
	if pool.Repo != "" {
		l[LabelDepot] = pool.Repo
	}
	for cle, valeur := range l {
		if !valeurLabel.MatchString(valeur) {
			return nil, fmt.Errorf("label %s=%q invalide", cle, valeur)
		}
	}
	return l, nil
}

// estNotre dit si un conteneur porte nos labels. C'est le confinement de
// l'ADR-0010 : sans lui, DeletePod supprimerait n'importe quel conteneur du
// démon dont on lui donne le nom.
func (d *Docker) estNotre(labels map[string]string) bool {
	return labels[LabelComposant] == ValeurComposant && labels[LabelInstance] == d.cfg.Instance
}

// inspecterNotre inspecte un conteneur et refuse ce qui n'est pas à nous.
func (d *Docker) inspecterNotre(ctx context.Context, nom string) (container.InspectResponse, error) {
	res, err := d.moteur.ContainerInspect(ctx, nom, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, err
	}
	if res.Container.Config == nil || !d.estNotre(res.Container.Config.Labels) {
		return container.InspectResponse{}, fmt.Errorf("le conteneur %s n'appartient pas à cette instance de warden", nom)
	}
	return res.Container, nil
}

// DeletePod supprime le conteneur. Idempotent : une absence n'est pas une
// erreur. Un conteneur du même nom qui n'est pas à nous est refusé.
func (d *Docker) DeletePod(ctx context.Context, name string) error {
	if !nomConteneur.MatchString(name) {
		return fmt.Errorf("nom de conteneur %q invalide", name)
	}
	existant, err := d.inspecterNotre(ctx, name)
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("suppression du conteneur %s : %w", name, err)
	}
	_, err = d.moteur.ContainerRemove(ctx, existant.ID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("suppression du conteneur %s : %w", name, err)
	}
	return nil
}
