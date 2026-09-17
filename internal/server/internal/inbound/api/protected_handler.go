package api

import (
	"log/slog"
	"net/http"
)

// protectedPayload est la réponse de GET /environments/protected.
type protectedPayload struct {
	Environments []string `json:"environments"`
}

// HandleListProtectedEnvironments rend les environnements marqués protégés.
//
// GET /environments/protected
//
// PAS derrière `adminOuRien` : voir l'enregistrement de la route. Un
// utilisateur sans droit d'administration doit pouvoir voir le badge se
// colorer sur ses propres runs.
func (a *Server) HandleListProtectedEnvironments(w http.ResponseWriter, r *http.Request) {
	if a.deps.Protected == nil {
		// Facultatif au sens du montage, et SANS la conséquence d'`Access` : ce
		// n'est pas un contrôle d'accès, donc son absence rend une liste vide
		// plutôt qu'un 501 — l'interface se dégrade en silence, sans badge.
		ecrireJSON(w, protectedPayload{Environments: []string{}})
		return
	}

	options, err := a.deps.Protected.ListOptions(r.Context())
	if err != nil {
		slog.Error("liste des environnements protégés en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the protected environments", http.StatusInternalServerError)
		return
	}

	// Le BADGE seul, pas les options complètes : cette route sert le navigateur
	// pour toute personne authentifiée (voir l'enregistrement de la route),
	// alors que `allow_run` — un gel opérationnel — n'a besoin d'être lu que
	// par le serveur lui-même, au lancement d'un run.
	environnements := make([]string, 0, len(options))
	for _, o := range options {
		if o.Protected {
			environnements = append(environnements, o.Environment)
		}
	}

	ecrireJSON(w, protectedPayload{Environments: environnements})
}
