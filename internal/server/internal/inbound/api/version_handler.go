package api

import (
	"net/http"

	"github.com/jijins1/warden/internal/version"
)

// versionResponse annonce la version du serveur.
type versionResponse struct {
	Version string `json:"version"`
}

// HandleVersion rend la version de ce serveur.
//
// GET /version — ANONYME, comme `/auth/login` : la CLI la demande avant toute
// session, et `warden upgrade` doit fonctionner pour qui n'est pas connecté
// (ADR-0049). Elle ne révèle que l'âge du déploiement.
func (a *Server) HandleVersion(w http.ResponseWriter, _ *http.Request) {
	ecrireJSON(w, versionResponse{Version: version.Version})
}
