package dispatch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// detachTimeout borne les écritures de nettoyage faites après la rupture de la
// connexion.
//
// Le contexte de la requête est annulé dès que le client part : marquer le pod
// injoignable avec ce contexte-là échouerait systématiquement, et le pod
// resterait réclamable alors que plus personne ne le tient.
const detachTimeout = 5 * time.Second

// ServeHTTP sert le canal de dispatch d'un pod (ADR-0018).
//
// Endpoint servi par le rôle `api` sur un PORT DISTINCT de celui de l'API et de
// celui de l'ingestion, avec sa propre règle d'ingress. C'est la troisième
// surface joignable depuis du code arbitraire (§2), et la seule qui distribue
// des credentials.
//
// Rien de ce qui transite ici n'est journalisé, et les réponses d'erreur ne
// disent rien : un porteur de jeton ne doit pas pouvoir apprendre si un pod a
// existé, ni s'il a été libéré.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Sans flush, SSE ne diffuse rien : la réponse s'accumulerait dans un
		// tampon et le pod attendrait un ordre déjà écrit.
		http.Error(w, "streaming indisponible", http.StatusInternalServerError)
		return
	}

	token, err := jetonPorteur(r)
	if err != nil {
		refuser(w)
		return
	}
	identite, err := d.pods.AuthenticatePod(ctx, token)
	if err != nil {
		// Aucune distinction entre jeton inconnu, malformé ou pod libéré.
		refuser(w)
		return
	}

	// L'abonnement est pris AVANT de se déclarer joignable : entre les deux, un
	// claim notifierait dans le vide et le pod attendrait le poll pour rien.
	reveil, premier := d.souscrire(identite.PodID)
	defer func() {
		if dernier := d.desouscrire(identite.PodID, reveil); dernier {
			d.marquer(ctx, identite.PodID, false)
		}
	}()

	if premier {
		if err := d.pods.SetPodReady(ctx, identite.PodID, true); err != nil {
			http.Error(w, "indisponible", http.StatusServiceUnavailable)
			return
		}
	}

	entetesSSE(w)
	if !d.ecrire(w, flusher, wire.Message{Kind: wire.KindHello}) {
		return
	}

	d.boucle(ctx, w, flusher, identite.PodID, reveil)
}

// boucle livre les messages jusqu'à la rupture du canal ou la libération du pod.
//
// Il n'y a pas deux chemins « rattrapage » et « direct » : il n'y en a qu'un,
// qui relit l'état. La notification ne fait que dire quand relire — même
// principe que le flux d'events (ADR-0008), et c'est ce qui rend le routage
// entre réplicas correct sans coordination.
func (d *Dispatcher) boucle(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, podID int64, reveil <-chan struct{}) {
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	maintien := time.NewTicker(keepAlive)
	defer maintien.Stop()

	var ordreLivre, annulationLivree bool

	for {
		etat, existe, err := d.etat(ctx, podID)
		switch {
		case err != nil:
			// Panne de lecture : on ne ferme pas le canal, on réessaiera au
			// tour suivant. Fermer transformerait une indisponibilité passagère
			// de la base en pod définitivement injoignable — or l'ADR-0018 note
			// que le runner ne rouvre PAS de canal.
			if ctx.Err() != nil {
				return
			}
		case !existe:
			// La ligne a disparu sous nos pieds. Il n'y a plus rien à router.
			return
		case etat.Released():
			// Fin de vie du pod (§8.2 : jamais réutilisé).
			return
		default:
			if etat.Claimed() && !ordreLivre {
				ordre, err := d.orders.OrderFor(ctx, etat.RunID)
				if err != nil {
					// Même raisonnement : `key-inventory` indisponible ne doit
					// pas coûter le canal. On réessaie au tour suivant, et le
					// coût d'un échec durable est un run qui ne démarre pas —
					// borné par `activeDeadlineSeconds` et le reaper (§8.7).
					if ctx.Err() != nil {
						return
					}
					break
				}
				if !d.ecrire(w, flusher, wire.Message{Kind: wire.KindOrder, Order: &ordre}) {
					return
				}
				ordreLivre = true
			}
			if etat.CancelRequested() && !annulationLivree {
				if !d.ecrire(w, flusher, wire.Message{
					Kind:   wire.KindCancel,
					Reason: string(core.CauseUserInterrupt),
				}) {
					return
				}
				annulationLivree = true
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-reveil:
		case <-poll.C:
		case <-maintien.C:
			// Commentaire SSE : tient la connexion ouverte à travers un proxy
			// sans inventer de message que le runner devrait comprendre.
			if _, err := w.Write([]byte(": maintien\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ecrire envoie un message et rend false si le canal est rompu.
func (d *Dispatcher) ecrire(w http.ResponseWriter, flusher http.Flusher, m wire.Message) bool {
	if err := wire.Encode(w, m); err != nil {
		// Jamais le message dans un log : il porte des credentials (ADR-0018).
		return false
	}
	flusher.Flush()
	return true
}

// marquer met à jour la disponibilité avec un contexte détaché.
func (d *Dispatcher) marquer(ctx context.Context, podID int64, ready bool) {
	fond, annule := context.WithTimeout(context.WithoutCancel(ctx), detachTimeout)
	defer annule()
	// L'erreur est sans recours ici : la connexion est déjà partie. Le filet
	// est le reconciler du §8.7, qui reprend les pods dont plus personne ne
	// tient le canal — et c'est la dette nommée à l'ADR-0023.
	_ = d.pods.SetPodReady(fond, podID, ready)
}

func entetesSSE(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Neutralise la mise en tampon des proxies, qui retiendrait les ordres
	// jusqu'à ce qu'un tampon se remplisse — c'est-à-dire indéfiniment sur un
	// canal de contrôle à très faible débit.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// refuser répond sans rien apprendre à l'appelant.
func refuser(w http.ResponseWriter) {
	http.Error(w, "", http.StatusUnauthorized)
}

var errPasDeJeton = errors.New("jeton d'amorçage absent")

// jetonPorteur extrait le jeton d'amorçage de l'en-tête Authorization.
//
// En-tête et non paramètre d'URL : une URL se retrouve dans les logs d'accès de
// chaque proxy traversé, et c'est précisément ce que l'ADR-0018 interdit pour ce
// canal.
func jetonPorteur(r *http.Request) (core.PodToken, error) {
	brut := r.Header.Get("Authorization")
	const prefixe = "Bearer "
	if len(brut) <= len(prefixe) || !strings.EqualFold(brut[:len(prefixe)], prefixe) {
		return core.PodToken{}, errPasDeJeton
	}
	return core.ParsePodToken(strings.TrimSpace(brut[len(prefixe):]))
}
