package dispatch

import (
	"context"
	"sync"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// pollInterval est le filet du routage.
//
// La correction ne dépend pas de NOTIFY : elle vient de l'état en base. Un
// réplica qui ne recevrait aucune notification — pooler en mode transaction,
// connexion d'écoute tombée — livre quand même tous les ordres, avec cette
// latence. C'est la valeur qui définit à quel point une panne du routage est
// bénigne, et elle se paie ici en démarrage de run, pas en correction.
const pollInterval = 2 * time.Second

// keepAlive espace les trames de maintien.
//
// Un canal de dispatch est oisif pendant toute la durée de vie d'un pod chaud —
// le TTL du §8.3 se compte en minutes. Sans trafic, un proxy ou un load
// balancer referme la connexion, et l'ADR-0018 précise que le runner ne
// réessaie PAS d'ouvrir un canal. Le maintien n'est donc pas un confort.
const keepAlive = 20 * time.Second

// Pods est le port de résolution des pods.
//
// Déclaré ici, où il est utilisé, et implémenté par `outbound/store`. C'est ce
// qui permet à ce paquet d'ignorer complètement PostgreSQL : il n'est qu'une
// logique de routage et un handler HTTP, testable sans base (ADR-0010).
type Pods interface {
	// AuthenticatePod résout un jeton d'amorçage. C'est le seul endroit où le
	// lien `pod -> run` est établi, et il l'est CÔTÉ SERVEUR (ADR-0023).
	AuthenticatePod(ctx context.Context, token core.PodToken) (core.PodIdentity, error)
	// SetPodReady dit si ce réplica tient une connexion pour ce pod. C'est le
	// prédicat « connexion oisive » du claim (ADR-0018).
	SetPodReady(ctx context.Context, podID int64, ready bool) error
	// PodStates lit l'état des pods. En lot : c'est le chemin du poll.
	PodStates(ctx context.Context, podIDs []int64) ([]core.PodState, error)
}

// Orders construit l'ordre de run au moment de la livraison.
//
// Au moment de la LIVRAISON, et c'est la propriété centrale : l'ordre porte le
// jeton git de l'ADR-0007 et la clé d'accès de l'ADR-0020. Les assembler ici
// plutôt qu'au claim est ce qui garantit qu'aucun credential n'est jamais
// persisté — ni en base, ni dans le WAL par une notification (ADR-0018
// amendée).
type Orders interface {
	OrderFor(ctx context.Context, runID int64) (wire.Order, error)

	// EnrollFor compose l'ordre d'enrôlement. Séparée d'OrderFor parce que les
	// deux genres n'ont rien en commun : l'un porte un jeton de lecture git,
	// l'autre le mot de passe d'un humain.
	EnrollFor(ctx context.Context, runID int64) (wire.Enroll, error)
}

// Kinds dit de quel genre est un run.
//
// Le dispatcher ne peut pas le deviner : il tient un `run_id` et rien d'autre.
// Un port d'une méthode plutôt qu'un champ sur `PodState` — celui-ci décrit le
// POD, et le genre appartient au run.
type Kinds interface {
	RunKindByID(ctx context.Context, runID int64) (core.RunKind, error)
}

// Dispatcher route les ordres vers les pods connectés à CE réplica.
//
// Double nature assumée (ADR-0018) : c'est un adaptateur SORTANT du cœur — le
// cœur dit « lance ce run » — servi par un endpoint HTTP ENTRANT, puisque c'est
// le pod qui ouvre la connexion. L'asymétrie est réelle et reste visible dans le
// code plutôt que masquée derrière une abstraction qui prétendrait l'inverse.
type Dispatcher struct {
	pods   Pods
	orders Orders
	genres Kinds

	mu     sync.Mutex
	canaux map[int64]map[chan struct{}]struct{}
}

// New construit un dispatcher.
//
// `genres` est facultatif : sans lui, tout run est traité comme un playbook —
// ce qu'était le seul genre existant avant la migration 00009.
func New(pods Pods, orders Orders, genres Kinds) *Dispatcher {
	return &Dispatcher{
		pods:   pods,
		orders: orders,
		genres: genres,
		canaux: make(map[int64]map[chan struct{}]struct{}),
	}
}

// composer rend le message à livrer pour un run, quel que soit son genre.
func (d *Dispatcher) composer(ctx context.Context, runID int64) (wire.Message, error) {
	genre := core.RunPlaybook
	if d.genres != nil {
		g, err := d.genres.RunKindByID(ctx, runID)
		if err != nil {
			return wire.Message{}, err
		}
		if g != "" {
			genre = g
		}
	}

	if genre == core.RunEnrollment {
		e, err := d.orders.EnrollFor(ctx, runID)
		if err != nil {
			return wire.Message{}, err
		}
		return wire.Message{Kind: wire.KindEnroll, Enroll: &e}, nil
	}

	o, err := d.orders.OrderFor(ctx, runID)
	if err != nil {
		return wire.Message{}, err
	}
	return wire.Message{Kind: wire.KindOrder, Order: &o}, nil
}

// Wake réveille les canaux d'un pod.
//
// Branché sur le watcher de `store`, qui reçoit les notifications PostgreSQL.
// Ne bloque jamais : le canal de réveil a une case, et un canal en retard n'a
// pas besoin de N réveils — il a besoin d'UN réveil et relira l'état complet.
// Un envoi bloquant ferait dépendre la connexion d'écoute, partagée par tous
// les pods du réplica, de la vitesse du pod le plus lent.
func (d *Dispatcher) Wake(podID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for reveil := range d.canaux[podID] {
		select {
		case reveil <- struct{}{}:
		default:
		}
	}
}

// Connected dit combien de pods ce réplica tient.
//
// Le dimensionnement du rôle `api` dépend de ce nombre (ADR-0018) : une
// connexion par pod chaud, épinglée à un réplica.
func (d *Dispatcher) Connected() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.canaux)
}

// souscrire enregistre un canal et dit s'il est le premier pour ce pod.
//
// Le comptage est nécessaire : un pod peut ouvrir deux connexions — rien ne
// l'en empêche, et ce n'est pas un problème de sécurité puisqu'il ne recevrait
// que ce qu'il a déjà. Ce serait en revanche un problème si la fermeture de la
// première marquait le pod injoignable alors que la seconde tient toujours.
func (d *Dispatcher) subscribe(podID int64) (chan struct{}, bool) {
	reveil := make(chan struct{}, 1)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.canaux[podID] == nil {
		d.canaux[podID] = make(map[chan struct{}]struct{})
	}
	d.canaux[podID][reveil] = struct{}{}
	return reveil, len(d.canaux[podID]) == 1
}

// desouscrire retire un canal et dit s'il était le dernier pour ce pod.
func (d *Dispatcher) unsubscribe(podID int64, reveil chan struct{}) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.canaux[podID], reveil)
	if len(d.canaux[podID]) == 0 {
		delete(d.canaux, podID)
		return true
	}
	return false
}

// etat lit l'état d'un pod.
func (d *Dispatcher) state(ctx context.Context, podID int64) (core.PodState, bool, error) {
	etats, err := d.pods.PodStates(ctx, []int64{podID})
	if err != nil {
		return core.PodState{}, false, err
	}
	if len(etats) == 0 {
		return core.PodState{}, false, nil
	}
	return etats[0], true, nil
}
