package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/ingest"
)

// Pods résout un jeton d'amorçage en identité de pod.
//
// Déclaré ICI, du côté qui l'appelle (ADR-0019), et satisfait par
// `outbound/store` sans qu'il ait à le nommer. C'est ce qui permet à ce paquet
// d'ignorer complètement PostgreSQL et de se tester sans base (ADR-0010).
//
// Une seule méthode, volontairement : c'est LE point où le lien `pod -> run`
// est établi, et il l'est côté serveur. Une interface plus large redonnerait à
// ce handler l'accès à tout l'adaptateur, et la frontière ne vaudrait plus rien.
type Pods interface {
	AuthenticatePod(ctx context.Context, token core.PodToken) (core.PodIdentity, error)
}

// Journal écrit les events d'un run, et le clôt.
//
// Les deux méthodes plutôt qu'une seule à `*core.Closure` : le cas ordinaire
// est un lot sans clôture, et l'appelant doit voir la différence entre « ajoute »
// et « ajoute et clôt ». Ce sont deux GRANT différents derrière.
type Journal interface {
	AppendEvents(ctx context.Context, runID int64, evs []events.Event) error
	AppendAndClose(ctx context.Context, runID int64, evs []events.Event, c core.Closure) error
}

// Handler sert l'endpoint d'ingestion des events du runner (ADR-0008).
type Handler struct {
	pods    Pods
	journal Journal
}

// New construit le handler.
//
// Il échoue à froid plutôt qu'en vol : un handler sans résolution de pods
// accepterait tout, et un handler sans journal accuserait réception de ce qu'il
// jette. Les deux modes de défaillance sont silencieux, ce qui est le pire
// qu'on puisse faire à un produit d'audit.
func New(pods Pods, journal Journal) (*Handler, error) {
	if pods == nil || journal == nil {
		return nil, errors.New("ingestion : résolution des pods et journal sont requis")
	}
	return &Handler{pods: pods, journal: journal}, nil
}

// Path est le chemin servi, repris du CONTRAT DE FIL et non redéclaré : c'est
// le runner qui le compose à l'autre bout, et deux littéraux jumeaux finissent
// par diverger.
const Path = wire.Path

// Routes rend le multiplexeur de l'ingestion.
//
// Il ne sert QUE cet endpoint. Pas de `/healthz`, pas de `/metrics` : cette
// surface est joignable depuis du code arbitraire (§2), et tout ce qui y répond
// est une chose de plus qu'un pod hostile peut sonder.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST "+Path, http.HandlerFunc(h.postEvents))
	return mux
}

func (h *Handler) postEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	identite, ok := h.authentifier(ctx, w, r)
	if !ok {
		return
	}

	// Le corps est borné AVANT d'être lu. Sans cette borne, un pod hostile fait
	// tenir un lot arbitraire dans la mémoire du rôle d'ingestion — et ce rôle
	// sert tous les runs du cluster.
	r.Body = http.MaxBytesReader(w, r.Body, wire.MaxBatchBytes)

	var lot wire.Batch
	dec := json.NewDecoder(r.Body)
	// Un champ inconnu est refusé : le contrat de fil est la frontière entre
	// deux domaines de confiance, et accepter ce qu'on ne comprend pas y est
	// une façon lente de ne plus savoir ce qui circule.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&lot); err != nil {
		// Le message ne cite PAS le corps : il vient d'un pod hostile et
		// finirait dans les logs du control plane.
		refuser(w, http.StatusBadRequest, "lot illisible")
		return
	}

	if err := lot.Validate(); err != nil {
		// Ici le message est repris tel quel, et c'est délibéré : il nomme le
		// seq fautif, sans quoi le runner ne peut que tout réémettre — et
		// échouer de la même façon. Il ne contient aucune donnée du payload.
		refuser(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.ecrire(ctx, identite.RunID, lot); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			// Le pod est authentifié et réclamé, mais son run a disparu. Ce
			// n'est pas la faute de l'appelant et rien ne le rattrapera en
			// réessayant.
			refuser(w, http.StatusGone, "le run de ce pod n'existe plus")
			return
		}
		slog.Error("lot d'events non journalisé",
			slog.Int64("run", identite.RunID), slog.String("error", err.Error()))
		// 503 et non 500 : l'indisponibilité de la base est passagère, et le
		// runner DOIT réessayer plutôt qu'arrêter le run. L'insertion étant
		// idempotente sur (run_id, seq), la réémission est sans danger.
		refuser(w, http.StatusServiceUnavailable, "journal indisponible")
		return
	}

	repondre(w, wire.Ack{AckedSeq: dernierSeq(lot)})
}

// authentifier résout le porteur en identité de pod RÉCLAMÉ.
//
// Deux refus, un seul message. Un pod chaud authentifié mais non réclamé ne
// doit rien obtenir — c'est le test négatif que l'ADR-0018 exige — et lui
// répondre autre chose qu'à un inconnu lui apprendrait qu'il existe.
func (h *Handler) authentifier(ctx context.Context, w http.ResponseWriter, r *http.Request) (core.PodIdentity, bool) {
	jeton, err := jetonPorteur(r)
	if err != nil {
		refuserOpaque(w)
		return core.PodIdentity{}, false
	}
	identite, err := h.pods.AuthenticatePod(ctx, jeton)
	if err != nil {
		// Aucune distinction entre jeton inconnu, malformé ou pod libéré.
		refuserOpaque(w)
		return core.PodIdentity{}, false
	}
	if !identite.Claimed() {
		refuserOpaque(w)
		return core.PodIdentity{}, false
	}
	return identite, true
}

// ecrire journalise le lot, avec ou sans clôture.
//
// Le `runID` vient de l'identité résolue côté serveur, JAMAIS du corps de la
// requête. C'est la condition de validité que l'ADR-0021 pose sur le schéma :
// l'`id` de run est un entier séquentiel, donc devinable, et il ne pose aucun
// problème PARCE QUE personne ne l'accepte d'un pod.
func (h *Handler) ecrire(ctx context.Context, runID int64, lot wire.Batch) error {
	if lot.Final == nil {
		return h.journal.AppendEvents(ctx, runID, lot.Events)
	}
	cloture, err := traduire(*lot.Final)
	if err != nil {
		return err
	}
	return h.journal.AppendAndClose(ctx, runID, lot.Events, cloture)
}

// traduire convertit la trame terminale du fil en clôture du domaine.
//
// La traduction est explicite et non un transtypage de chaînes, parce que les
// deux vocabulaires ne coïncident PAS et ne doivent pas coïncider par accident :
// le §6.4 connaît huit causes de terminaison, le contrat de fil deux — celles
// qu'un runner peut constater. Un `core.TerminationCause(f.Cause)` laisserait un
// pod se déclarer `pod_oom_killed`, c'est-à-dire mentir sur une cause
// d'infrastructure que seul le control plane peut établir.
func traduire(f wire.Final) (core.Closure, error) {
	var etat core.RunState
	switch f.Status {
	case wire.StatusSucceeded:
		etat = core.RunSucceeded
	case wire.StatusFailed:
		etat = core.RunFailed
	case wire.StatusInterrupted:
		etat = core.RunInterrupted
	default:
		return core.Closure{}, fmt.Errorf("statut de run %q inconnu", f.Status)
	}

	var cause core.TerminationCause
	if etat == core.RunInterrupted {
		switch f.Cause {
		case wire.CauseUserInterrupt:
			cause = core.CauseUserInterrupt
		case wire.CauseUnknown:
			cause = core.CauseUnknown
		default:
			return core.Closure{}, fmt.Errorf("cause de terminaison %q inconnue", f.Cause)
		}
	}

	return core.Closure{State: etat, Cause: cause, SecretsUnmasked: f.SecretsUnmasked}, nil
}

// dernierSeq rend l'offset le plus haut du lot.
//
// Zéro n'est pas une valeur sentinelle ici : un lot qui ne porte qu'une trame
// terminale n'a rien à acquitter, et le runner qui l'a émis n'attend plus rien.
func dernierSeq(lot wire.Batch) int64 {
	if len(lot.Events) == 0 {
		return 0
	}
	// Validate a déjà imposé des seq strictement croissants.
	return lot.Events[len(lot.Events)-1].Seq
}

var errJetonAbsent = errors.New("jeton d'amorçage absent")

// jetonPorteur extrait le jeton d'amorçage de l'en-tête Authorization.
//
// En-tête et non paramètre d'URL, pour la raison qui vaut déjà côté dispatch :
// une URL se retrouve dans les logs d'accès de chaque proxy traversé.
func jetonPorteur(r *http.Request) (core.PodToken, error) {
	brut := r.Header.Get("Authorization")
	const prefixe = "Bearer "
	if len(brut) <= len(prefixe) || !strings.EqualFold(brut[:len(prefixe)], prefixe) {
		return core.PodToken{}, errJetonAbsent
	}
	return core.ParsePodToken(strings.TrimSpace(brut[len(prefixe):]))
}

// refuserOpaque répond sans rien apprendre à l'appelant.
func refuserOpaque(w http.ResponseWriter) {
	http.Error(w, "", http.StatusUnauthorized)
}

func refuser(w http.ResponseWriter, code int, motif string) {
	http.Error(w, motif, code)
}

func repondre(w http.ResponseWriter, ack wire.Ack) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(ack); err != nil {
		// La réponse est déjà partie, il n'y a plus de recours. Le lot EST
		// journalisé : le runner le réémettra faute d'accusé, et l'insertion
		// idempotente absorbera la répétition.
		slog.Error("accusé de réception non écrit", slog.String("error", err.Error()))
	}
}
