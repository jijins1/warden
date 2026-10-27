package api

import (
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// environmentsPayload est la réponse de GET /environments.
type environmentsPayload struct {
	Environments []string `json:"environments"`
}

// HandleListMyEnvironments rend les environnements sur lesquels l'appelant a
// un droit vivant.
//
// GET /environments
//
// PAS derrière `adminOuRien`, contrairement à `GET /admin/keys` : ce n'est pas
// la carte de l'organisation mais celle de l'appelant, la même que celle qui
// borne la liste de ses runs (core.RunsVisibles). Un administrateur sans droit
// n'y voit rien de plus — l'administration n'élargit pas l'accès.
func (a *Server) HandleListMyEnvironments(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if user == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	ecrireJSON(w, environmentsPayload{Environments: core.EnvironnementsVisibles(r.Context(), a.deps.Access, user.ID)})
}
