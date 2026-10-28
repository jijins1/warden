package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

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

	// Les droits des GROUPES (ADR-0053) suivent, sous leur nom `group:…` : c'est
	// ainsi qu'ils s'écrivent dans `grants.yaml`, et la CLI les affiche sans rien
	// savoir de plus. Seulement sans filtre de compte — un groupe n'est pas un
	// compte, et qui en est membre se demande à l'AD.
	if r.URL.Query().Get("subject") == "" {
		groupes, err := a.deps.Access.ListGroupGrants(r.Context())
		if err != nil {
			slog.Error("liste des droits de groupe en échec", slog.String("error", err.Error()))
			http.Error(w, "could not read the grants", http.StatusInternalServerError)
			return
		}
		for _, g := range groupes {
			p := grantPayload{
				Subject: core.PrefixeGroupe + g.Group, Environment: g.Environment,
				GrantedAt: g.GrantedAt.UTC().Format(time.RFC3339),
			}
			if g.Revoked() {
				p.RevokedAt = g.RevokedAt.UTC().Format(time.RFC3339)
			}
			out = append(out, p)
		}
	}
	ecrireJSON(w, out)
}

// Il n'existe AUCUN handler d'écriture des droits, et c'est délibéré.
//
// La source est un document versionné dans le dépôt de déploiement, que le rôle
// `controller` réconcilie (ADR-0033). Une route qui accorderait un droit ferait
// une seconde source de vérité, que le passage suivant du contrôleur écraserait
// sans le dire — un droit qu'on croit avoir accordé et qui disparaît une minute
// plus tard est pire qu'une route absente.
//
// Changer les droits se fait donc en committant. C'est plus lent, c'est
// relisible, et ça laisse une trace que personne ne peut réécrire.
