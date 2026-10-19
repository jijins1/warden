package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// codeTue est le code de sortie d'un processus tué par SIGKILL, celui que
// Docker rapporte après ContainerKill.
const codeTue = 137

// ObserverPods rend les faits sur tous les conteneurs runner de l'instance,
// indexés par nom.
//
// Il porte aussi la borne de durée, faute d'`activeDeadlineSeconds` : un
// conteneur qui la dépasse est tué ici, et rapporté terminé en
// `DeadlineExceeded`. La cause se relit aux passages suivants depuis l'état
// du conteneur — fin postérieure à la borne — sans rien retenir en mémoire,
// donc sans dépendre de la réplique qui l'a tué.
func (d *Docker) ObserverPods(ctx context.Context) (map[string]core.PodFacts, error) {
	filtres := client.Filters{}.
		Add("label", LabelComposant+"="+ValeurComposant).
		Add("label", LabelInstance+"="+d.cfg.Instance)
	liste, err := d.moteur.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filtres})
	if err != nil {
		return nil, fmt.Errorf("observation des conteneurs runner : %w", err)
	}

	borne := time.Duration(d.cfg.DeadlineSeconds) * time.Second
	maintenant := d.now()
	faits := make(map[string]core.PodFacts, len(liste.Items))

	for _, s := range liste.Items {
		// Le filtre du démon est la première barrière, pas la seule.
		if !d.estNotre(s.Labels) || len(s.Names) == 0 {
			continue
		}
		nom := strings.TrimPrefix(s.Names[0], "/")
		cree := time.Unix(s.Created, 0)
		f := core.PodFacts{Name: nom}

		switch s.State {
		case container.StateExited, container.StateDead:
			f, err = d.faitsDuTermine(ctx, nom, cree, borne)
			if cerrdefs.IsNotFound(err) {
				// Supprimé entre la liste et l'inspection : il n'est plus là,
				// et c'est ce que le reconciler doit voir.
				continue
			}
			if err != nil {
				return nil, err
			}

		case container.StateRunning, container.StateCreated:
			if maintenant.Sub(cree) <= borne {
				break // il tourne, ou démarre : son run est peut-être long
			}
			if s.State == container.StateRunning {
				_, err := d.moteur.ContainerKill(ctx, s.ID, client.ContainerKillOptions{Signal: "KILL"})
				if err != nil && !cerrdefs.IsNotFound(err) {
					return nil, fmt.Errorf("arrêt du conteneur %s à sa borne de durée : %w", nom, err)
				}
			}
			f = core.PodFacts{
				Name:            nom,
				Termine:         true,
				PodReason:       core.RaisonDeadlineExceeded,
				ContainerReason: "Error",
				ExitCode:        codeTue,
			}
		}
		faits[nom] = f
	}
	return faits, nil
}

// faitsDuTermine inspecte un conteneur arrêté : la liste ne donne ni le code
// de sortie ni l'OOM.
func (d *Docker) faitsDuTermine(ctx context.Context, nom string, cree time.Time, borne time.Duration) (core.PodFacts, error) {
	c, err := d.inspecterNotre(ctx, nom)
	if err != nil {
		return core.PodFacts{}, err
	}
	f := core.PodFacts{Name: nom, Termine: true}
	if c.State == nil {
		return f, nil
	}
	f.ExitCode = int32(c.State.ExitCode) // #nosec G115 -- un code de sortie tient sur 8 bits

	switch {
	case c.State.OOMKilled:
		f.ContainerReason = core.RaisonOOMKilled
	case c.State.ExitCode == 0:
		f.ContainerReason = "Completed"
	default:
		f.ContainerReason = "Error"
	}

	if fin, err := time.Parse(time.RFC3339Nano, c.State.FinishedAt); err == nil && fin.Sub(cree) > borne {
		f.PodReason = core.RaisonDeadlineExceeded
	}
	return f, nil
}
