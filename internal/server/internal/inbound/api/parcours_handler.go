package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// fichierArbre et reponseArbre sont la forme publique de `GET /runs/{run_ref}/tree`.
type fichierArbre struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type reponseArbre struct {
	Files     []fichierArbre `json:"files"`
	Truncated bool           `json:"truncated"`
}

// HandleGetRunTree liste les fichiers du code d'un run (ADR-0055).
// GET /runs/{run_ref}/tree
//
// Le commit est l'audit_sha du run, le code qui a tourné. Le contrôle d'accès
// et les refus sont ceux du diff : qui voit le run voit son code.
func (a *Server) HandleGetRunTree(w http.ResponseWriter, r *http.Request) {
	run, ok := a.runParcourable(w, r)
	if !ok {
		return
	}

	arbre, err := a.deps.Files.Tree(r.Context(), run)
	if err != nil {
		repondreEchecParcours(w, err)
		return
	}

	rep := reponseArbre{Files: make([]fichierArbre, 0, len(arbre.Files)), Truncated: arbre.Truncated}
	for _, f := range arbre.Files {
		rep.Files = append(rep.Files, fichierArbre{Path: f.Path, Size: f.Size})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(rep)
}

// HandleGetRunFile rend un fichier du code d'un run (ADR-0055).
// GET /runs/{run_ref}/file?path=<chemin>
//
// Le chemin passe en paramètre de requête et non dans l'URL : un chemin de
// dépôt porte des `/`, des espaces, parfois des `%`, et le routeur n'a pas à
// les interpréter.
func (a *Server) HandleGetRunFile(w http.ResponseWriter, r *http.Request) {
	chemin := r.URL.Query().Get("path")
	if !cheminValide(chemin) {
		// Refusé ICI et non au rôle `git` : celui-ci le refuserait aussi, mais
		// son refus remonterait comme une panne d'un autre rôle (502).
		http.Error(w, "path must be a relative file path inside the tree", http.StatusBadRequest)
		return
	}

	run, ok := a.runParcourable(w, r)
	if !ok {
		return
	}

	contenu, err := a.deps.Files.File(r.Context(), run, chemin)
	if err != nil {
		repondreEchecParcours(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- servi en octet-stream avec nosniff, jamais rendu comme du
	// HTML. Le contenu vient du dépôt audité et n'est pas de confiance.
	_, _ = w.Write(contenu)
}

// runParcourable fait les vérifications communes aux deux routes, dans l'ordre
// du diff : dépendance, accès, artefact.
func (a *Server) runParcourable(w http.ResponseWriter, r *http.Request) (core.Run, bool) {
	// Sans client vers le rôle `git`, la route refuse. Elle ne se rabat pas sur
	// une lecture locale, que le rôle `api` n'a pas le droit de faire (ADR-0006).
	if a.deps.Files == nil {
		http.Error(w, "browsing files unavailable: no client to the git role", http.StatusNotImplemented)
		return core.Run{}, false
	}

	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return core.Run{}, false
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return core.Run{}, false
	}
	if !a.artefactDisponible(w, run) {
		return core.Run{}, false
	}
	return run, true
}

// repondreEchecParcours traduit un échec du rôle `git`, comme le fait le diff,
// plus les deux états propres à un fichier.
func repondreEchecParcours(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrPathMissing):
		w.Header().Set(motifIndisponible, "path_missing")
		http.Error(w, "this path is not in the tree of this run", http.StatusNotFound)
	case errors.Is(err, core.ErrFileTooLarge):
		w.Header().Set(motifIndisponible, "file_too_large")
		http.Error(w, "this file is too large to be shown", http.StatusRequestEntityTooLarge)
	case errors.Is(err, core.ErrArtifactGone):
		w.Header().Set(motifIndisponible, "object_missing")
		http.Error(w, "the code of this run is no longer available", http.StatusGone)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "the git role did not answer in time", http.StatusGatewayTimeout)
	default:
		// La panne est chez un autre rôle : un 500 la revendiquerait.
		http.Error(w, "reading the code of this run failed", http.StatusBadGateway)
	}
}

// cheminValide reprend les règles du rôle `git` sur un chemin d'arbre : relatif,
// sans remontée, sans retour à la ligne ni NUL.
func cheminValide(p string) bool {
	if p == "" || strings.ContainsAny(p, "\n\x00") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}
