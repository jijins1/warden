package api

import (
	"net/http"
	"strings"
	"time"
)

// nomCookieSession porte la session dans un navigateur.
//
// Le MÊME jeton opaque que l'en-tête `Authorization: Bearer` de la CLI : le
// cookie n'est qu'un transport. C'est ce qui garde la résolution de session
// identique pour les deux clients — une requête SQL sur `session`, donc aucun
// état en mémoire et aucune affinité de réplica à configurer.
const nomCookieSession = "warden_session"

// enTeteUI est l'en-tête anti-CSRF exigé des écritures authentifiées PAR COOKIE.
//
// Sa valeur n'est jamais lue : seule sa présence compte. Un formulaire ou une
// image d'un site tiers ne peut pas poser un en-tête arbitraire sans déclencher
// une prévol, et la prévol échouera sur une origine non déclarée. C'est le
// second verrou derrière `SameSite=Strict`, pas son remplaçant.
const enTeteUI = "X-Warden-UI"

// origines est l'allowlist des origines navigateur.
//
// UNE seule liste, consommée par le CORS ET par `CheckOrigin` du WebSocket.
// L'unicité est la propriété : le handshake WebSocket n'est PAS soumis au CORS
// et le navigateur y attache les cookies tout seul, de sorte qu'une allowlist
// durcie d'un côté et oubliée de l'autre laisse un cross-site WebSocket
// hijacking grand ouvert. Les faire dériver d'une même valeur rend l'oubli
// impossible plutôt qu'improbable.
type origines struct{ autorisees map[string]struct{} }

func nouvellesOrigines(liste []string) origines {
	set := make(map[string]struct{}, len(liste))
	for _, o := range liste {
		// La barre finale est retirée : `Origin` n'en porte jamais, et une
		// valeur configurée avec en aurait silencieusement tout refusé.
		o = strings.TrimSuffix(strings.TrimSpace(o), "/")
		if o != "" {
			set[o] = struct{}{}
		}
	}
	return origines{autorisees: set}
}

// autorise dit si l'origine est déclarée.
//
// Une allowlist VIDE refuse tout. Le défaut d'un déploiement qui ne se prononce
// pas est donc « aucun navigateur », et non « tous » : l'inverse ouvrirait
// l'API à n'importe quel site le jour où quelqu'un oublie la variable.
func (o origines) autorise(origine string) bool {
	if origine == "" || len(o.autorisees) == 0 {
		return false
	}
	_, ok := o.autorisees[origine]
	return ok
}

// CORSMiddleware autorise les origines déclarées, avec credentials.
//
// Monté sur le routeur RACINE, donc au-dessus de `/auth/login` : sans lui, la
// seule requête qu'un navigateur non connecté peut faire serait bloquée, et
// rien ne pourrait commencer.
func (a *Server) CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origine := r.Header.Get("Origin")
		if origine == "" {
			// La CLI et le runner. Le CORS n'est pas une autorisation — c'est
			// une instruction AU NAVIGATEUR — donc un client qui n'en est pas
			// un traverse exactement comme avant.
			next.ServeHTTP(w, r)
			return
		}

		// `Vary` même quand l'origine est refusée : un cache partagé servirait
		// sinon à une origine la réponse taillée pour une autre.
		w.Header().Add("Vary", "Origin")

		autorisee := a.origines.autorise(origine)
		if autorisee {
			// L'origine EXACTE, jamais `*` : l'étoile est refusée par les
			// navigateurs dès qu'il y a des credentials, donc un serveur qui la
			// renvoie a un CORS qui ne marche que sans cookie.
			w.Header().Set("Access-Control-Allow-Origin", origine)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !autorisee {
				// Refus EXPLICITE plutôt qu'une réponse muette. Le navigateur
				// bloquerait de toute façon, mais un 403 dans les logs dit à
				// l'exploitant que quelqu'un frappe depuis une origine non
				// déclarée — ce qu'un 204 sans en-têtes tairait.
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+enTeteUI)
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// poserCookieSession range le jeton de session dans un cookie.
//
// `SameSite=Strict` et non `Lax` : l'UI ne joint l'API que par XHR, jamais par
// une navigation de premier niveau, donc Strict ne coûte rien ici et ferme le
// cas des navigations cross-site. Il SUPPOSE que l'UI et l'API partagent le
// même domaine enregistrable — sur deux domaines distincts le cookie ne
// partirait pas du tout, et c'est délibérément un échec bruyant plutôt qu'un
// repli silencieux sur `SameSite=None` (ADR-0035).
func (a *Server) poserCookieSession(w http.ResponseWriter, jeton string, expire time.Time) {
	// #nosec G124 -- les trois attributs SONT posés. `Secure` est calculé, donc
	// gosec ne peut pas prouver sa valeur : il vaut `true` sauf si l'opt-in
	// `WARDEN_UI_COOKIE_INSECURE` est explicitement posé pour le développement
	// local, où un cookie `Secure` ne serait de toute façon jamais stocké.
	http.SetCookie(w, &http.Cookie{
		Name:     nomCookieSession,
		Value:    jeton,
		Path:     "/",
		Expires:  expire,
		HttpOnly: true,
		Secure:   !a.cookieNonSecurise,
		SameSite: http.SameSiteStrictMode,
	})
}

// effacerCookieSession expire le cookie.
//
// Appelé par le logout EN PLUS de la révocation. Révoquer sans effacer
// laisserait le navigateur renvoyer un cookie mort à chaque requête : la
// session serait bien coupée, mais l'utilisateur verrait des 401 sans savoir
// pourquoi.
func (a *Server) effacerCookieSession(w http.ResponseWriter) {
	// #nosec G124 -- même cookie, mêmes attributs, valeur vidée. Les répéter à
	// l'identique est ce qui garantit que le navigateur remplace bien celui
	// qu'il a : un attribut qui diffère en poserait un SECOND au lieu.
	http.SetCookie(w, &http.Cookie{
		Name:     nomCookieSession,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !a.cookieNonSecurise,
		SameSite: http.SameSiteStrictMode,
	})
}

// jetonDuCookie extrait le jeton de session du cookie, s'il y en a un.
func jetonDuCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(nomCookieSession)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// methodeMutante dit si la méthode change quelque chose.
//
// La liste des méthodes SÛRES est énumérée, et tout le reste est mutant : une
// méthode inconnue doit tomber du côté prudent, pas passer parce qu'elle n'est
// pas dans une liste noire.
func methodeMutante(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
