package api

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// environnementValide reprend la contrainte du SCHÉMA, mot pour mot.
//
// La dupliquer ici n'est pas une redondance mais un choix de MESSAGE : sans ce
// contrôle, un environnement mal formé passerait et échouerait sur une violation
// de contrainte PostgreSQL, que l'API rendrait en 500. Un droit refusé pour une
// majuscule doit le dire.
var environnementValide = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,54}$`)

// grantPayload est un droit d'accès tel qu'il circule.
type grantPayload struct {
	Subject     string `json:"subject"`
	Environment string `json:"environment"`

	GrantedAt string `json:"granted_at"`
	GrantedBy string `json:"granted_by,omitempty"`

	// RevokedAt vide = droit vivant. Les droits révoqués sont RENDUS, parce que
	// c'est une vue d'audit : « qui avait accès à prod le 3 mars » ne se répond
	// pas sans eux.
	RevokedAt string `json:"revoked_at,omitempty"`
}

func versGrantPayload(g core.EnvironmentGrant) grantPayload {
	p := grantPayload{
		Subject: g.Subject, Environment: g.Environment,
		GrantedAt: g.GrantedAt.UTC().Format(time.RFC3339),
		GrantedBy: g.GrantedBySubject,
	}
	if g.Revoked() {
		p.RevokedAt = g.RevokedAt.UTC().Format(time.RFC3339)
	}
	return p
}

// HandleListGrants rend les droits par environnement.
//
// GET /admin/grants[?subject=alice]
func (a *Server) HandleListGrants(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Access == nil {
		http.Error(w, "environment grants are not available", http.StatusNotImplemented)
		return
	}

	droits, err := a.deps.Access.ListGrants(r.Context(), r.URL.Query().Get("subject"))
	if err != nil {
		slog.Error("liste des droits en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the grants", http.StatusInternalServerError)
		return
	}

	out := make([]grantPayload, 0, len(droits))
	for _, g := range droits {
		out = append(out, versGrantPayload(g))
	}
	ecrireJSON(w, out)
}

// HandlePutGrant accorde un droit sur un environnement.
//
// PUT /admin/grants/{subject}/{environment}
//
// Idempotent : réaccorder un droit vivant le rend tel quel, sans réécrire sa
// date. `granted_at` répond à « depuis quand », et la faire glisser à chaque
// rejeu effacerait la réponse.
//
// AUCUN raccourci pour les administrateurs. `is_admin` ne donne pas
// implicitement accès à tous les environnements : un administrateur qui veut
// lancer sur `prod` s'accorde le droit, et cela laisse une LIGNE. Sans quoi
// « qui pouvait atteindre prod » se répondrait sur deux sources — cette table,
// plus la liste des administrateurs — et une question d'audit à deux sources est
// une question à laquelle personne ne répond.
func (a *Server) HandlePutGrant(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Access == nil {
		http.Error(w, "environment grants are not available", http.StatusNotImplemented)
		return
	}

	subject, env := r.PathValue("subject"), r.PathValue("environment")
	if !environnementValide.MatchString(env) {
		http.Error(w, "invalid environment: lowercase letters, digits and dashes only",
			http.StatusBadRequest)
		return
	}

	g, err := a.deps.Access.Grant(r.Context(), subject, env, utilisateurID(r))
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("attribution du droit en échec",
			slog.String("sujet", subject), slog.String("environnement", env),
			slog.String("error", err.Error()))
		http.Error(w, "could not grant the access", http.StatusInternalServerError)
		return
	}

	slog.Warn("droit accordé",
		slog.String("sujet", subject), slog.String("environnement", env),
		slog.Int64("par", utilisateurID(r)))

	ecrireJSON(w, versGrantPayload(g))
}

// HandleRevokeGrant éteint un droit.
//
// DELETE /admin/grants/{subject}/{environment}
//
// L'effet est IMMÉDIAT sur les runs en vol : la clé d'accès est revérifiée à la
// livraison de l'ordre, donc un pod qui n'a pas encore reçu la sienne ne
// l'obtiendra pas. C'est ce que le §10.3 exige d'un changement de droits.
//
// Le droit n'est pas supprimé mais DATÉ : la ligne reste lisible, parce que
// « qui avait accès à prod le 3 mars » est une question d'audit.
func (a *Server) HandleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Access == nil {
		http.Error(w, "environment grants are not available", http.StatusNotImplemented)
		return
	}

	subject, env := r.PathValue("subject"), r.PathValue("environment")
	if err := a.deps.Access.Revoke(r.Context(), subject, env); err != nil {
		slog.Error("révocation du droit en échec",
			slog.String("sujet", subject), slog.String("environnement", env),
			slog.String("error", err.Error()))
		http.Error(w, "could not revoke the access", http.StatusInternalServerError)
		return
	}

	slog.Warn("droit révoqué",
		slog.String("sujet", subject), slog.String("environnement", env),
		slog.Int64("par", utilisateurID(r)),
		slog.String("effet", "les runs qui n'ont pas encore reçu leur clé ne l'obtiendront pas"))

	w.WriteHeader(http.StatusNoContent)
}
