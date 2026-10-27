package docker_test

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/docker"
)

func nos() map[string]string {
	return map[string]string{docker.LabelComposant: docker.ValeurComposant, docker.LabelInstance: "warden-test"}
}

func TestLesFaitsDUnConteneurTermine(t *testing.T) {
	d, demon := monter(t, image)
	reussi := demon.poser("warden-ok", nos(), container.StateExited)
	reussi.fini = time.Now()
	echoue := demon.poser("warden-ko", nos(), container.StateExited)
	echoue.code, echoue.fini = 2, time.Now()
	oom := demon.poser("warden-oom", nos(), container.StateExited)
	oom.code, oom.oom, oom.fini = 137, true, time.Now()
	demon.poser("warden-vivant", nos(), container.StateRunning)

	faits, err := d.ObserverPods(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	attendus := map[string]core.PodFacts{
		"warden-ok":     {Name: "warden-ok", Termine: true, ContainerReason: "Completed"},
		"warden-ko":     {Name: "warden-ko", Termine: true, ContainerReason: "Error", ExitCode: 2},
		"warden-oom":    {Name: "warden-oom", Termine: true, ContainerReason: core.RaisonOOMKilled, ExitCode: 137},
		"warden-vivant": {Name: "warden-vivant"},
	}
	for nom, attendu := range attendus {
		if faits[nom] != attendu {
			t.Errorf("%s : %+v, attendu %+v", nom, faits[nom], attendu)
		}
	}
	if core.CauseDeTerminaison(faits["warden-oom"]) != core.CausePodOOMKilled {
		t.Error("un OOM n'est pas reconnu comme tel par le cœur")
	}
}

// Ni un conteneur d'une autre instance, ni un conteneur sans labels.
func TestLObservationNeVoitQueLesConteneursDeLInstance(t *testing.T) {
	d, demon := monter(t, image)
	demon.poser("warden-a", nos(), container.StateRunning)
	demon.poser("autre-instance", map[string]string{
		docker.LabelComposant: docker.ValeurComposant, docker.LabelInstance: "voisine",
	}, container.StateRunning)
	demon.poser("postgres", map[string]string{}, container.StateRunning)

	faits, err := d.ObserverPods(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(faits) != 1 || faits["warden-a"].Name == "" {
		t.Errorf("faits : %+v", faits)
	}
}

// La borne de durée : un conteneur qui la dépasse est tué, et le cœur lit la
// cause comme pour `activeDeadlineSeconds` — y compris aux passages suivants.
func TestUnConteneurQuiDepasseSaBorneEstTue(t *testing.T) {
	d, demon := monter(t, image)
	debut := time.Now().Add(-2 * time.Hour)
	demon.now = func() time.Time { return debut }
	c := demon.poser("warden-long", nos(), container.StateRunning)
	demon.now = time.Now

	faits, err := d.ObserverPods(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.etat != container.StateExited {
		t.Error("le conteneur n'a pas été tué")
	}
	f := faits["warden-long"]
	if !f.Termine || core.CauseDeTerminaison(f) != core.CauseServerTaskTimeout {
		t.Errorf("faits : %+v", f)
	}

	// Passage suivant, éventuellement par une autre réplique : la cause se
	// relit depuis l'état du conteneur.
	faits, err = d.ObserverPods(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if core.CauseDeTerminaison(faits["warden-long"]) != core.CauseServerTaskTimeout {
		t.Errorf("cause perdue au passage suivant : %+v", faits["warden-long"])
	}
}

// Un conteneur créé et jamais démarré n'attend pas indéfiniment : passé la
// borne, il est rapporté terminé et sera ramassé.
func TestUnConteneurJamaisDemarreEstBorneAussi(t *testing.T) {
	d, demon := monter(t, image)
	debut := time.Now().Add(-2 * time.Hour)
	demon.now = func() time.Time { return debut }
	demon.poser("warden-fige", nos(), container.StateCreated)
	demon.now = time.Now

	faits, err := d.ObserverPods(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f := faits["warden-fige"]; !f.Termine || f.PodReason != core.RaisonDeadlineExceeded {
		t.Errorf("faits : %+v", f)
	}
}
