package docker_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/docker"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

const image = "ghcr.io/jijins1/warden-runner:test"

func monter(t *testing.T, images ...string) (*docker.Docker, *fauxDemon) {
	t.Helper()
	demon := nouveauDemon(images...)
	d, err := docker.New(demon, docker.Config{
		Instance:        "warden-test",
		Network:         "warden-runners",
		DeadlineSeconds: 3600,
		DispatchURL:     "http://wardend:8084",
		IngestURL:       "http://wardend:8085",
	})
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	return d, demon
}

func spec(t *testing.T, nom string) core.PodSpec {
	t.Helper()
	jeton, err := core.NewPodToken()
	if err != nil {
		t.Fatal(err)
	}
	return core.PodSpec{
		Name: nom, Image: image, Token: jeton,
		Pool: core.PodPool{UserID: 7, Repo: "depot", Environment: "prod"},
	}
}

// Le jeton arrive sur stdin, et nulle part ailleurs dans l'objet Docker.
func TestLeJetonPasseParStdinEtNullePartAilleurs(t *testing.T) {
	d, demon := monter(t, image)
	s := spec(t, "warden-run-1")

	if err := d.LaunchPod(t.Context(), s); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	c := demon.conteneurs["warden-run-1"]
	if c == nil || c.etat != container.StateRunning {
		t.Fatalf("conteneur non démarré : %+v", c)
	}
	if demon.demarreSansAttache {
		t.Error("démarré avant l'attachement : le runner pourrait lire un stdin fermé")
	}
	select {
	case recu := <-c.stdin:
		if strings.TrimSpace(recu) != s.Token.Reveal() {
			t.Error("le jeton reçu sur stdin n'est pas celui du lancement")
		}
	case <-time.After(time.Second):
		t.Fatal("rien reçu sur stdin")
	}

	for _, e := range c.config.Env {
		if strings.Contains(e, s.Token.Reveal()) {
			t.Errorf("le jeton est dans l'environnement : %s", strings.SplitN(e, "=", 2)[0])
		}
	}
	if !slices.Contains(c.config.Env, wire.EnvTokenFile+"="+wire.TokenFileStdin) {
		t.Error("le runner n'est pas averti de lire son jeton sur stdin")
	}
	if !c.config.OpenStdin || !c.config.StdinOnce {
		t.Error("stdin doit être ouvert, et fermé après le premier client")
	}
}

// Le durcissement du pod, transposé tel quel.
func TestLeConteneurEstDurci(t *testing.T) {
	d, demon := monter(t, image)
	if err := d.LaunchPod(t.Context(), spec(t, "warden-run-1")); err != nil {
		t.Fatal(err)
	}
	c := demon.conteneurs["warden-run-1"]
	h := c.host

	if !h.ReadonlyRootfs || h.Privileged || h.AutoRemove {
		t.Errorf("racine/privilège/suppression : %v %v %v", h.ReadonlyRootfs, h.Privileged, h.AutoRemove)
	}
	if !slices.Equal(h.CapDrop, []string{"ALL"}) {
		t.Errorf("capacités : %v", h.CapDrop)
	}
	if !slices.Contains(h.SecurityOpt, "no-new-privileges") {
		t.Errorf("options de sécurité : %v", h.SecurityOpt)
	}
	if c.config.User != "65534:65534" {
		t.Errorf("utilisateur : %q", c.config.User)
	}
	if string(h.NetworkMode) != "warden-runners" {
		t.Errorf("réseau : %q", h.NetworkMode)
	}
	for _, chemin := range []string{docker.CheminEspace, docker.CheminTemporaire} {
		if !strings.Contains(h.Tmpfs[chemin], "exec") || strings.Contains(h.Tmpfs[chemin], "noexec") {
			t.Errorf("tmpfs %s : %q", chemin, h.Tmpfs[chemin])
		}
	}
}

// Relancer un lancement déjà abouti ne fait rien.
func TestLaunchPodEstIdempotent(t *testing.T) {
	d, demon := monter(t, image)
	s := spec(t, "warden-run-1")
	if err := d.LaunchPod(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	id := demon.conteneurs["warden-run-1"].id
	if err := d.LaunchPod(t.Context(), s); err != nil {
		t.Fatalf("second lancement : %v", err)
	}
	if demon.conteneurs["warden-run-1"].id != id {
		t.Error("un conteneur démarré a été remplacé")
	}
}

// Un lanceur mort entre la création et le jeton laisse un conteneur `created`
// sans jeton : le lancement suivant le remplace au lieu de s'en contenter.
func TestUnConteneurJamaisDemarreEstRecree(t *testing.T) {
	d, demon := monter(t, image)
	s := spec(t, "warden-run-1")
	orphelin := demon.poser("warden-run-1", map[string]string{
		docker.LabelComposant: docker.ValeurComposant, docker.LabelInstance: "warden-test",
	}, container.StateCreated)

	if err := d.LaunchPod(t.Context(), s); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	c := demon.conteneurs["warden-run-1"]
	if c.id == orphelin.id || c.etat != container.StateRunning {
		t.Errorf("le conteneur sans jeton n'a pas été remplacé : %+v", c)
	}
}

// Un conteneur du même nom qui n'est pas à nous n'est ni pris pour le nôtre,
// ni supprimé.
func TestUnConteneurEtrangerNEstJamaisTouche(t *testing.T) {
	d, demon := monter(t, image)
	demon.poser("warden-run-1", map[string]string{"autre": "chose"}, container.StateCreated)

	if err := d.LaunchPod(t.Context(), spec(t, "warden-run-1")); err == nil {
		t.Error("lancement accepté par-dessus un conteneur étranger")
	}
	if err := d.DeletePod(t.Context(), "warden-run-1"); err == nil {
		t.Error("suppression d'un conteneur étranger acceptée")
	}
	if _, ok := demon.conteneurs["warden-run-1"]; !ok {
		t.Error("le conteneur étranger a disparu")
	}
}

func TestLImageAbsenteEstTireeAvecLAuthDuDeploiement(t *testing.T) {
	d, demon := monter(t) // aucune image sur le démon
	if err := d.LaunchPod(t.Context(), spec(t, "warden-run-1")); err != nil {
		t.Fatalf("LaunchPod : %v", err)
	}
	if !slices.Equal(demon.tirages, []string{image}) {
		t.Errorf("tirages : %v", demon.tirages)
	}
}

// Un démarrage raté ne laisse pas de conteneur sans jeton derrière lui.
func TestUnDemarrageRateNeLaissePasDeConteneur(t *testing.T) {
	d, demon := monter(t, image)
	demon.errStart = errors.New("réseau introuvable")
	if err := d.LaunchPod(t.Context(), spec(t, "warden-run-1")); err == nil {
		t.Fatal("lancement accepté malgré l'échec du démarrage")
	}
	if len(demon.conteneurs) != 0 {
		t.Error("un conteneur jamais démarré est resté")
	}
}

func TestDeletePodEstIdempotent(t *testing.T) {
	d, demon := monter(t, image)
	if err := d.LaunchPod(t.Context(), spec(t, "warden-run-1")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := d.DeletePod(t.Context(), "warden-run-1"); err != nil {
			t.Fatalf("DeletePod : %v", err)
		}
	}
	if len(demon.conteneurs) != 0 {
		t.Error("le conteneur est toujours là")
	}
}

func TestUnSpecIncompletEstRefuse(t *testing.T) {
	d, _ := monter(t, image)
	cas := map[string]func(*core.PodSpec){
		"nom":         func(s *core.PodSpec) { s.Name = "Majuscules" },
		"image":       func(s *core.PodSpec) { s.Image = "" },
		"jeton":       func(s *core.PodSpec) { s.Token = core.PodToken{} },
		"utilisateur": func(s *core.PodSpec) { s.Pool.UserID = 0 },
		"label":       func(s *core.PodSpec) { s.Pool.Environment = "a b" },
	}
	for nom, gate := range cas {
		t.Run(nom, func(t *testing.T) {
			s := spec(t, "warden-run-1")
			gate(&s)
			if err := d.LaunchPod(t.Context(), s); err == nil {
				t.Error("spec acceptée")
			}
		})
	}
}

func TestNewEchoueAFroid(t *testing.T) {
	complet := docker.Config{Instance: "i", Network: "n", DispatchURL: "d", IngestURL: "i"}
	cas := map[string]func(*docker.Config){
		"instance": func(c *docker.Config) { c.Instance = "" },
		"réseau":   func(c *docker.Config) { c.Network = "" },
		"dispatch": func(c *docker.Config) { c.DispatchURL = "" },
		"ingest":   func(c *docker.Config) { c.IngestURL = "" },
	}
	for nom, gate := range cas {
		c := complet
		gate(&c)
		if _, err := docker.New(nouveauDemon(), c); err == nil {
			t.Errorf("%s absent accepté", nom)
		}
	}
	if _, err := docker.New(nil, complet); err == nil {
		t.Error("client absent accepté")
	}
}
