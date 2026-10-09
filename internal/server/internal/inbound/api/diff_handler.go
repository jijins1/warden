package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// HandleGetDiff retrieves the diff for a specific event.
// GET /runs/{run_ref}/events/{seq}/diff
//
// Response: JSON raw message (the diff payload) or null if no diff.
// Security: accessed via warden_diff_reader role (separate from events).
func (a *Server) HandleGetDiff(w http.ResponseWriter, r *http.Request) {
	// Parse path parameters
	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	seqStr := r.PathValue("seq")
	if seqStr == "" {
		http.Error(w, "seq required", http.StatusBadRequest)
		return
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid seq: %v", err), http.StatusBadRequest)
		return
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}

	// Query the diff via the separate DiffReader role. The run carries its own
	// partition key, so this stays a primary-key lookup.
	diff, err := a.deps.Diffs.DiffForRun(r.Context(), run, seq)
	if err != nil {
		http.Error(w, "querying diff failed", http.StatusInternalServerError)
		return
	}

	// Return the diff (or null if none). The payload is attacker-influenced —
	// it is module output — so forbid content sniffing: a browser must not be
	// talked into rendering it as HTML.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if diff == nil {
		_, _ = w.Write([]byte("null"))
	} else {
		// #nosec G705 -- servi en application/json avec nosniff, jamais en HTML.
		_, _ = w.Write(diff)
	}
}

// motifIndisponible nomme, en en-tête, la raison d'un 410 ou d'un 404.
//
// Le corps reste du texte brut, comme les autres handlers ; l'en-tête donne à
// l'interface d'audit de quoi distinguer les cas sans analyser une phrase.
const motifIndisponible = "X-Warden-Reason"

// HandleGetRunDiff sert les deux diffs d'audit du §6.5.
// GET /runs/{run_ref}/diff?vs=mainline|previous
//
// vs=mainline : mainline_sha -> audit_sha. « Qu'avait-il modifié en local ? »
// vs=previous : audit_sha du run PRÉCÉDENT sur le même triplet (dépôt,
// environnement, inventaire) -> audit_sha de ce run. « Qu'est-ce qui a changé
// depuis que ça marchait ? »
//
// Réponse : le diff unifié en texte brut, vide quand les deux états coïncident.
func (a *Server) HandleGetRunDiff(w http.ResponseWriter, r *http.Request) {
	// Sans client vers le rôle `git`, l'endpoint refuse. Il ne se rabat PAS sur
	// une lecture de dépôt local : le rôle `api` n'a jamais accès au filesystem
	// git (ADR-0006), c'est `outbound/gitclient` qui porte cette arête.
	if a.deps.Repo == nil {
		http.Error(w, "git diffs unavailable: no client to the git role", http.StatusNotImplemented)
		return
	}

	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	vs := r.URL.Query().Get("vs")
	if vs != "mainline" && vs != "previous" && vs != "current" {
		http.Error(w, "vs must be 'mainline', 'previous' or 'current'", http.StatusBadRequest)
		return
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}

	// Deux cas connus localement, tranchés sans appeler le rôle `git`.
	if !a.artefactDisponible(w, run) {
		return
	}

	var (
		diff string
		err  error
	)
	switch vs {
	case "mainline":
		diff, err = a.deps.Repo.DiffMainline(r.Context(), run)

	case "current":
		// Deux dépendances facultatives de plus que "mainline" : la politique de
		// dépôt (pour sa branche protégée) et le résolveur de refs (pour la
		// position ACTUELLE de cette branche dans le miroir). Un déploiement qui
		// ne les monte pas reste valide pour tout le reste de l'audit — c'est le
		// même principe que `Repo` ci-dessus.
		if a.deps.Repos == nil || a.deps.Refs == nil {
			http.Error(w, "diffing against the current playbook unavailable: repo policy or ref resolver not configured",
				http.StatusNotImplemented)
			return
		}
		repoCfg, errRepo := a.deps.Repos.GetRepo(r.Context(), run.Repo)
		if errRepo != nil {
			http.Error(w, "repository configuration unavailable", http.StatusBadGateway)
			return
		}
		currentSHA, errResolve := a.deps.Refs.ResolveRef(r.Context(), run.Repo, repoCfg.MainlineRef)
		if errResolve != nil {
			http.Error(w, "resolving the current mainline failed", http.StatusBadGateway)
			return
		}
		diff, err = a.deps.Repo.DiffAgainst(r.Context(), run, currentSHA)

	case "previous":
		precedent, errPrec := a.deps.Runs.PreviousRun(r.Context(), run)
		if errPrec != nil {
			if errors.Is(errPrec, core.ErrNotFound) {
				w.Header().Set(motifIndisponible, "no_previous_run")
				http.Error(w, "no previous run on this inventory", http.StatusNotFound)
				return
			}
			http.Error(w, "resolving previous run failed", http.StatusInternalServerError)
			return
		}

		// LE CONTRÔLE QUI COMPTE. Le run précédent sur le même inventaire
		// appartient souvent à quelqu'un d'autre, et le diff exposerait SON
		// arbre — c'est-à-dire son code — à un appelant qui n'a pas le droit de
		// voir ce run. `requireRunAccess` n'a vérifié que le run demandé.
		//
		// Le refus se présente comme une absence, pour la même raison que
		// `requireRunAccess` : distinguer « interdit » de « inexistant »
		// ferait de l'endpoint un oracle.
		if !a.policy.CanViewRun(r.Context(), ContextUser(r.Context()), &precedent) {
			w.Header().Set(motifIndisponible, "no_previous_run")
			http.Error(w, "no previous run on this inventory", http.StatusNotFound)
			return
		}

		if !a.artefactDisponible(w, precedent) {
			return
		}

		diff, err = a.deps.Repo.DiffPrevious(r.Context(), run, precedent)
	}

	if err != nil {
		switch {
		case errors.Is(err, core.ErrArtifactGone):
			// L'objet a disparu du dépôt entre-temps, ou n'y a jamais été.
			w.Header().Set(motifIndisponible, "object_missing")
			http.Error(w, "the code of this run is no longer available", http.StatusGone)
		case errors.Is(err, context.DeadlineExceeded):
			http.Error(w, "the git role did not answer in time", http.StatusGatewayTimeout)
		default:
			// La panne est chez un autre rôle : un 500 la revendiquerait.
			http.Error(w, "computing diff failed", http.StatusBadGateway)
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- diff unifié servi en text/plain avec nosniff, jamais rendu
	// comme du HTML. Le contenu vient du dépôt audité et n'est pas de confiance.
	_, _ = w.Write([]byte(diff))
}

// artefactDisponible répond au client et rend false quand le code d'un run n'est
// plus calculable, sans solliciter le rôle `git`.
//
// 410 et non 404 : le run a bel et bien existé, c'est son arbre qui n'est plus
// là. L'ADR-0013 veut ce cas traité en première classe dès maintenant, et non le
// jour de la première purge.
func (a *Server) artefactDisponible(w http.ResponseWriter, run core.Run) bool {
	if run.ArtifactPruned() {
		w.Header().Set(motifIndisponible, "artifact_pruned")
		http.Error(w, "the code of this run has been pruned", http.StatusGone)
		return false
	}
	// Run antérieur à la migration 00004 : on ne sait pas dans quel dépôt
	// chercher. Indiscernable d'une purge pour qui consulte.
	if run.Repo == "" {
		w.Header().Set(motifIndisponible, "repo_unknown")
		http.Error(w, "the repository of this run is unknown", http.StatusGone)
		return false
	}
	return true
}
