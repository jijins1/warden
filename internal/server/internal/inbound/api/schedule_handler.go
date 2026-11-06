package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// createSchedulePayload est ce qu'un client envoie pour créer un cron
// (ADR-0057).
//
// Aucun champ ne désigne le PROPRIÉTAIRE : c'est l'identité authentifiée de la
// requête, et `DisallowUnknownFields` refuse un `"owner"` plutôt que de
// l'ignorer. Aucun secret non plus — ni mot de passe, ni `forward_env` : un
// cron n'a personne pour les taper, et ils ne s'écrivent jamais en base.
type createSchedulePayload struct {
	Name        string          `json:"name"`
	Cron        string          `json:"cron"`
	Timezone    string          `json:"timezone"`
	Environment string          `json:"environment"`
	Inventory   string          `json:"inventory,omitempty"`
	Playbook    string          `json:"playbook"`
	Flags       json.RawMessage `json:"flags,omitempty"`
}

// scheduleJSON est un cron tel que rendu par l'API.
type scheduleJSON struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Repo        string     `json:"repo"`
	Playbook    string     `json:"playbook"`
	Environment string     `json:"environment"`
	Inventory   string     `json:"inventory,omitempty"`
	Flags       []string   `json:"flags"`
	Cron        string     `json:"cron"`
	Timezone    string     `json:"timezone"`
	Owner       string     `json:"owner"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	NextFireAt  *time.Time `json:"next_fire_at,omitempty"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	LastRunRef  string     `json:"last_run_ref,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type schedulesJSON struct {
	Schedules []scheduleJSON `json:"schedules"`
}

func heureOuRien(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func versScheduleJSON(p core.Planification) scheduleJSON {
	flags := p.Flags
	if flags == nil {
		flags = []string{}
	}
	return scheduleJSON{
		ID: p.ID, Name: p.Nom, Repo: p.Repo, Playbook: p.Playbook,
		Environment: p.Environment, Inventory: p.Inventory, Flags: flags,
		Cron: p.Expression, Timezone: p.Fuseau, Owner: p.ProprietaireSujet,
		Enabled: p.Active, CreatedAt: p.CreeLe.UTC(),
		NextFireAt: heureOuRien(p.ProchainDeclenche), LastFiredAt: heureOuRien(p.DernierDeclenche),
		LastRunRef: p.DernierRunRef, LastError: p.DerniereErreur,
	}
}

func (a *Server) planifications() core.Planifications {
	return core.Planifications{Store: a.deps.Schedules, Repos: a.deps.Repos, Access: a.deps.Access}
}

// utilisateurOuRien rend l'utilisateur authentifié, ou répond 401.
func utilisateurOuRien(w http.ResponseWriter, r *http.Request) *core.User {
	user := ContextUser(r.Context())
	if user == nil || user.Disabled {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return nil
	}
	return user
}

// HandleCreateSchedule crée un cron au nom de l'utilisateur authentifié.
//
// POST /repos/{repo}/schedules
//
// Sous `/repos/`, comme le lancement depuis le navigateur, et jamais sous
// `/admin/` : planifier est l'action de lancer, différée. Le droit exigé est
// celui sur l'environnement (ADR-0033), pas l'administration.
func (a *Server) HandleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	user := utilisateurOuRien(w, r)
	if user == nil {
		return
	}

	var payload createSchedulePayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRun))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "unreadable request body", http.StatusBadRequest)
		return
	}
	flags, err := core.FlagsDemandes(payload.Flags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cree, err := a.planifications().Creer(r.Context(), user, core.DemandeDePlanification{
		Nom: payload.Name, Repo: r.PathValue("repo"), Playbook: payload.Playbook,
		Environment: payload.Environment, Inventory: payload.Inventory, Flags: flags,
		Expression: payload.Cron, Fuseau: payload.Timezone,
	})
	if err != nil {
		repondreAuRefus(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(versScheduleJSON(cree)); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}

// HandleListSchedules rend les crons visibles : les siens, tous pour un
// administrateur.
//
// GET /schedules
func (a *Server) HandleListSchedules(w http.ResponseWriter, r *http.Request) {
	user := utilisateurOuRien(w, r)
	if user == nil {
		return
	}
	liste, err := a.planifications().Lister(r.Context(), user)
	if err != nil {
		repondreAuRefus(w, err)
		return
	}
	out := schedulesJSON{Schedules: make([]scheduleJSON, 0, len(liste))}
	for _, p := range liste {
		out.Schedules = append(out.Schedules, versScheduleJSON(p))
	}
	ecrireJSON(w, out)
}

func idDeCron(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "schedule id must be a positive integer", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// basculer sert les deux routes enable/disable.
func (a *Server) basculer(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := utilisateurOuRien(w, r)
		if user == nil {
			return
		}
		id, ok := idDeCron(w, r)
		if !ok {
			return
		}
		p, err := a.planifications().Basculer(r.Context(), user, id, active)
		if err != nil {
			repondreAuRefus(w, err)
			return
		}
		ecrireJSON(w, versScheduleJSON(p))
	}
}

// HandleEnableSchedule réactive un cron, sans rattraper les échéances passées.
//
// POST /schedules/{id}/enable
func (a *Server) HandleEnableSchedule(w http.ResponseWriter, r *http.Request) {
	a.basculer(true)(w, r)
}

// HandleDisableSchedule désactive un cron : le propriétaire, ou un
// administrateur — c'est le bouton d'arrêt de l'exploitant.
//
// POST /schedules/{id}/disable
func (a *Server) HandleDisableSchedule(w http.ResponseWriter, r *http.Request) {
	a.basculer(false)(w, r)
}

// HandleDeleteSchedule supprime un cron. Les runs qu'il a lancés gardent leur
// référence vers lui.
//
// DELETE /schedules/{id}
func (a *Server) HandleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	user := utilisateurOuRien(w, r)
	if user == nil {
		return
	}
	id, ok := idDeCron(w, r)
	if !ok {
		return
	}
	if err := a.planifications().Supprimer(r.Context(), user, id); err != nil {
		repondreAuRefus(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
