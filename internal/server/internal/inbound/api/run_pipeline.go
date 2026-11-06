package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Lanceur assemble le cas d'usage de création de run depuis les ports montés.
//
// Une méthode de Deps et non un champ : l'assemblage (`cmd/wardend`) en a
// besoin pour le planificateur (ADR-0057), et les deux doivent être construits
// depuis les MÊMES ports. Une seconde liste de ports, tenue à la main dans
// server.go, finirait par en oublier un — typiquement Protected, et le gel
// d'un environnement ne s'appliquerait plus qu'aux runs lancés à la main.
func (d Deps) Lanceur() core.Lanceur {
	return core.Lanceur{
		Repos:          d.Repos,
		Mirrors:        d.Mirrors,
		Audits:         d.Audits,
		ProjectConfig:  d.ProjectConfig,
		ExigenceConfig: d.ExigenceConfig,
		Access:         d.Access,
		Protected:      d.Protected,
		Stash:          d.Stash,
		Creator:        d.Creator,
		Starter:        d.Starter,
		Running:        d.Running,
	}
}

// lanceur est construit à chaque appel : les tests modifient `deps` après New,
// et une copie figée à la construction ne verrait pas ces modifications.
func (a *Server) lanceur() core.Lanceur { return a.deps.Lanceur() }

// statutDuRefus traduit la cause d'un refus du cœur en code HTTP.
func statutDuRefus(c core.CauseDeRefus) int {
	switch c {
	case core.RefusForme:
		return http.StatusBadRequest
	case core.RefusInterdit:
		return http.StatusForbidden
	case core.RefusIntrouvable:
		return http.StatusNotFound
	case core.RefusConflit:
		return http.StatusConflict
	case core.RefusIndisponible:
		return http.StatusNotImplemented
	case core.RefusAmont:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// repondreAuRefus écrit un refus du cœur. Le message est celui du cœur, rendu
// tel quel : il est écrit pour le demandeur et ne cite aucun secret.
func repondreAuRefus(w http.ResponseWriter, err error) {
	var r *core.RefusDeLancement
	if errors.As(err, &r) {
		http.Error(w, r.Message, statutDuRefus(r.Cause))
		return
	}
	slog.Error("lancement en échec", slog.String("error", err.Error()))
	http.Error(w, "could not launch the run", http.StatusInternalServerError)
}

// autoriserLEnvironnement rend false et A DÉJÀ RÉPONDU quand le run est refusé.
// Voir core.Lanceur.AutoriserLEnvironnement.
func (a *Server) autoriserLEnvironnement(w http.ResponseWriter, r *http.Request, user *core.User, env string) bool {
	if err := a.lanceur().AutoriserLEnvironnement(r.Context(), user, env); err != nil {
		repondreAuRefus(w, err)
		return false
	}
	return true
}

// resoudreEffectif rend `ok == false` et A DÉJÀ RÉPONDU en cas de refus. Voir
// core.Lanceur.ResoudreEffectif.
func (a *Server) resoudreEffectif(
	w http.ResponseWriter, r *http.Request,
	user *core.User, repoName, configSHA, runRef string, req core.Requete,
) (core.Effectif, bool) {
	effectif, err := a.lanceur().ResoudreEffectif(r.Context(), user, repoName, configSHA, runRef, req)
	if err != nil {
		repondreAuRefus(w, err)
		return core.Effectif{}, false
	}
	return effectif, true
}

// enregistrerEtDemarrer consigne les secrets, crée le run, le démarre, et
// répond. Voir core.Lanceur.EnregistrerEtDemarrer.
func (a *Server) enregistrerEtDemarrer(w http.ResponseWriter, r *http.Request, in core.RunADemarrer) {
	demarre, err := a.lanceur().EnregistrerEtDemarrer(r.Context(), in)
	if err != nil {
		repondreAuRefus(w, err)
		return
	}
	repondreRunCree(w, demarre)
}

// repondreRunCree écrit la réponse 201 d'un run créé.
func repondreRunCree(w http.ResponseWriter, d core.Demarre) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(createRunResponse{
		RunRef:      d.RunRef,
		AuditSHA:    d.AuditSHA,
		MainlineRef: d.MainlineRef,
		MainlineSHA: d.MainlineSHA,
		Pod:         d.Pod,
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}
