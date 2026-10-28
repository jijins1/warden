// Étage 1 de l'ADR-0010 : un faux démon en mémoire, derrière l'interface
// Moteur. Il reproduit ce que l'adaptateur observe — conflits de nom, image
// absente, états, stdin — et rien d'autre.

package docker_test

import (
	"context"
	"io"
	"iter"
	"net"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

type fauxConteneur struct {
	id      string
	nom     string
	config  *container.Config
	host    *container.HostConfig
	etat    container.ContainerState
	code    int
	oom     bool
	cree    time.Time
	fini    time.Time
	attache bool
	stdin   chan string // ce que le serveur a écrit, reçu à la fermeture
}

type fauxDemon struct {
	mu         sync.Mutex
	now        func() time.Time
	images     map[string]bool
	conteneurs map[string]*fauxConteneur // par nom
	n          int
	tirages    []string
	errStart   error

	// demarreSansAttache : un démarrage a précédé l'attachement à stdin.
	demarreSansAttache bool
}

func nouveauDemon(images ...string) *fauxDemon {
	d := &fauxDemon{
		now:        time.Now,
		images:     map[string]bool{},
		conteneurs: map[string]*fauxConteneur{},
	}
	for _, i := range images {
		d.images[i] = true
	}
	return d
}

func (d *fauxDemon) parID(id string) *fauxConteneur {
	for _, c := range d.conteneurs {
		if c.id == id || c.nom == id {
			return c
		}
	}
	return nil
}

func (d *fauxDemon) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.conteneurs[o.Name]; ok {
		return client.ContainerCreateResult{}, cerrdefs.ErrConflict
	}
	if !d.images[o.Config.Image] {
		return client.ContainerCreateResult{}, cerrdefs.ErrNotFound
	}
	d.n++
	c := &fauxConteneur{
		id: "id-" + o.Name + "-" + string(rune('a'+d.n)), nom: o.Name,
		config: o.Config, host: o.HostConfig,
		etat: container.StateCreated, cree: d.now(),
		stdin: make(chan string, 1),
	}
	d.conteneurs[o.Name] = c
	return client.ContainerCreateResult{ID: c.id}, nil
}

func (d *fauxDemon) ContainerAttach(_ context.Context, id string, o client.ContainerAttachOptions) (client.ContainerAttachResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.parID(id)
	if c == nil {
		return client.ContainerAttachResult{}, cerrdefs.ErrNotFound
	}
	c.attache = o.Stdin
	serveur, cote := net.Pipe()
	go func() {
		lu, _ := io.ReadAll(serveur)
		c.stdin <- string(lu)
	}()
	return client.ContainerAttachResult{HijackedResponse: client.NewHijackedResponse(cote, "")}, nil
}

func (d *fauxDemon) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.errStart != nil {
		return client.ContainerStartResult{}, d.errStart
	}
	c := d.parID(id)
	if c == nil {
		return client.ContainerStartResult{}, cerrdefs.ErrNotFound
	}
	if !c.attache {
		d.demarreSansAttache = true
	}
	c.etat = container.StateRunning
	return client.ContainerStartResult{}, nil
}

func (d *fauxDemon) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.parID(id)
	if c == nil {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	fini := ""
	if !c.fini.IsZero() {
		fini = c.fini.Format(time.RFC3339Nano)
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID:     c.id,
		Name:   "/" + c.nom,
		Config: c.config,
		State: &container.State{
			Status: c.etat, ExitCode: c.code, OOMKilled: c.oom, FinishedAt: fini,
		},
	}}, nil
}

func (d *fauxDemon) ContainerList(_ context.Context, o client.ContainerListOptions) (client.ContainerListResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var res client.ContainerListResult
	for _, c := range d.conteneurs {
		if !correspond(o.Filters, c.config.Labels) {
			continue
		}
		res.Items = append(res.Items, container.Summary{
			ID: c.id, Names: []string{"/" + c.nom}, Labels: c.config.Labels,
			State: c.etat, Created: c.cree.Unix(),
		})
	}
	return res, nil
}

func correspond(f client.Filters, labels map[string]string) bool {
	for l := range f["label"] {
		cle, valeur, _ := strings.Cut(l, "=")
		if labels[cle] != valeur {
			return false
		}
	}
	return true
}

func (d *fauxDemon) ContainerKill(_ context.Context, id string, _ client.ContainerKillOptions) (client.ContainerKillResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.parID(id)
	if c == nil {
		return client.ContainerKillResult{}, cerrdefs.ErrNotFound
	}
	c.etat, c.code, c.fini = container.StateExited, 137, d.now()
	return client.ContainerKillResult{}, nil
}

func (d *fauxDemon) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.parID(id)
	if c == nil {
		return client.ContainerRemoveResult{}, cerrdefs.ErrNotFound
	}
	delete(d.conteneurs, c.nom)
	return client.ContainerRemoveResult{}, nil
}

func (d *fauxDemon) ImagePull(_ context.Context, ref string, _ client.ImagePullOptions) (client.ImagePullResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tirages = append(d.tirages, ref)
	d.images[ref] = true
	return tirageFini{io.NopCloser(strings.NewReader(""))}, nil
}

// tirageFini est un pull déjà terminé.
type tirageFini struct{ io.ReadCloser }

func (tirageFini) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}
func (tirageFini) Wait(context.Context) error { return nil }

// poser ajoute un conteneur arbitraire, pour les cas qu'aucun lancement ne
// produit : un conteneur étranger, un état donné.
func (d *fauxDemon) poser(nom string, labels map[string]string, etat container.ContainerState) *fauxConteneur {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	c := &fauxConteneur{
		id: "id-" + nom, nom: nom,
		config: &container.Config{Labels: labels},
		etat:   etat, cree: d.now(), stdin: make(chan string, 1),
	}
	d.conteneurs[nom] = c
	return c
}
