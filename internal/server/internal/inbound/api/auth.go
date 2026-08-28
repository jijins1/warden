package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// ctxKeyAuthnUser is used to store the authenticated user in request context.
type ctxKeyAuthnUser struct{}

// ContextUser returns the authenticated user from request context, or nil if not authenticated.
func ContextUser(ctx context.Context) *core.User {
	if val, ok := ctx.Value(ctxKeyAuthnUser{}).(*core.User); ok {
		return val
	}
	return nil
}

// withUser stores the user in the request context.
func withUser(r *http.Request, user *core.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyAuthnUser{}, user))
}

// devAuthHeader carries the caller's subject when insecure dev auth is enabled.
// It is asserted by the client, so it is only ever trusted behind an explicit
// opt-in (Config.InsecureDevAuth).
const devAuthHeader = "X-User-Subject"

// ctxKeySession stores the caller's session, when there is one.
type ctxKeySession struct{}

// ContextSession returns the caller's session, or nil when the caller was
// authenticated by some other means (the development header).
func ContextSession(ctx context.Context) *core.Session {
	if val, ok := ctx.Value(ctxKeySession{}).(*core.Session); ok {
		return val
	}
	return nil
}

// AuthnMiddleware resolves the caller into a provisioned user.
//
// Trois chemins, dans cet ordre, et l'ordre est le point :
//
//  1. `Authorization: Bearer` — une session ouverte par `warden login`. C'est le
//     chemin RÉEL : le porteur a prouvé qu'il connaissait un mot de passe.
//  2. le cookie `warden_session` — le MÊME jeton, porté par un navigateur
//     (ADR-0035). Parce qu'il est ambiant, une écriture par ce chemin exige en
//     plus l'en-tête `X-Warden-UI`.
//  3. `X-User-Subject` — une simple affirmation, acceptée derrière l'opt-in
//     explicite `WARDEN_INSECURE_DEV_AUTH`. Elle reste pour le développement et
//     les tests de bout en bout, et poser cet opt-in vaut accepter que n'importe
//     qui se déclare n'importe qui.
//
// Chacun n'est essayé qu'à défaut du précédent. Le porteur EN PREMIER, de sorte
// qu'un jeton invalide ne retombe jamais sur un cookie traînant : la CLI ne doit
// pas changer de comportement selon ce qu'un navigateur a laissé dans le même
// magasin, et une session expirée doit se voir plutôt que de continuer à marcher.
//
// Ce middleware ne provisionne JAMAIS : créer un compte appartient au flux qui a
// vérifié une identité, et un upsert piloté par un en-tête non authentifié
// laisserait n'importe qui faire grossir la table des utilisateurs.
func (a *Server) AuthnMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if jeton, présent := jetonPorteur(r); présent {
			a.parSession(w, r, next, jeton)
			return
		}
		if jeton, présent := jetonDuCookie(r); présent {
			a.parCookie(w, r, next, jeton)
			return
		}
		if a.insecureDevAuth {
			a.parEnTeteDeDev(w, r, next)
			return
		}
		// Ni jeton ni mode de développement. `WWW-Authenticate` dit à un client
		// automatisé ce qu'on attend de lui, ce qu'un 401 nu ne dit pas.
		w.Header().Set("WWW-Authenticate", `Bearer realm="warden"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// jetonPorteur extrait le jeton d'un en-tête `Authorization: Bearer`.
//
// Le booléen distingue « aucun en-tête » de « en-tête illisible ». Sans cette
// distinction, un jeton mal formé retomberait silencieusement sur l'en-tête de
// développement, et un client cassé paraîtrait fonctionner.
func jetonPorteur(r *http.Request) (string, bool) {
	brut := r.Header.Get("Authorization")
	if brut == "" {
		return "", false
	}
	const prefixe = "Bearer "
	if len(brut) <= len(prefixe) || !strings.EqualFold(brut[:len(prefixe)], prefixe) {
		return "", true // présent mais inutilisable : ce sera un 401
	}
	return strings.TrimSpace(brut[len(prefixe):]), true
}

// parCookie authentifie un navigateur, et exige l'en-tête anti-CSRF en écriture.
//
// Le cookie est AMBIANT : le navigateur l'attache sans que le JS de la page le
// demande, y compris sur une requête déclenchée par un autre site.
// `SameSite=Strict` couvre déjà le cas cross-site, mais il suppose que les deux
// hôtes partagent un domaine enregistrable — et un sous-domaine compromis reste
// « same-site ». L'en-tête est le verrou qui ne dépend pas de cette hypothèse :
// le poser exige du JS, donc une prévol, donc une origine déclarée.
//
// En LECTURE il n'est pas exigé : un GET ne change rien, et l'imposer casserait
// la navigation sans rien protéger.
func (a *Server) parCookie(w http.ResponseWriter, r *http.Request, next http.Handler, jeton string) {
	if methodeMutante(r.Method) && r.Header.Get(enTeteUI) == "" {
		http.Error(w, "missing "+enTeteUI+" header", http.StatusForbidden)
		return
	}
	a.parSession(w, r, next, jeton)
}

func (a *Server) parSession(w http.ResponseWriter, r *http.Request, next http.Handler, brut string) {
	if a.deps.Auth == nil {
		http.Error(w, "no authenticator configured", http.StatusNotImplemented)
		return
	}

	jeton, err := core.ParseSessionToken(brut)
	if err != nil {
		refuserLeJeton(w)
		return
	}

	user, session, err := a.deps.Auth.Authentifier(r.Context(), jeton)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			// Inconnu, expiré, révoqué, ou compte désactivé : la même réponse
			// pour les quatre. Les distinguer dirait à qui présente un jeton au
			// hasard lequel de ses essais a touché quelque chose.
			refuserLeJeton(w)
			return
		}
		slog.Error("authentification par session en échec", slog.String("error", err.Error()))
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	r = withUser(r, &user)
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeySession{}, &session)))
}

// refuserLeJeton répond 401 avec le motif que la RFC 6750 prévoit, pour qu'un
// client sache qu'il doit se reconnecter plutôt que réessayer.
func refuserLeJeton(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		`Bearer realm="warden", error="invalid_token"`)
	http.Error(w, "session expired or invalid — run `warden login`", http.StatusUnauthorized)
}

func (a *Server) parEnTeteDeDev(w http.ResponseWriter, r *http.Request, next http.Handler) {
	subject := r.Header.Get(devAuthHeader)
	if subject == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="warden"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	user, err := a.deps.Users.UserBySubject(r.Context(), core.UserLocal, subject)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		http.Error(w, "user lookup failed", http.StatusInternalServerError)
		return
	}
	if user.Disabled {
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	}

	next.ServeHTTP(w, withUser(r, &user))
}

// Policy holds the authorization rules. Every handler that reads audit data
// must consult it; the middleware only establishes who is calling.
// Elle ne porte aucune dépendance : les règles actuelles se décident sur
// l'utilisateur et le run qu'on lui passe. Le jour où « les hôtes que je gère »
// apparaît, c'est un port du cœur qui entrera ici, pas un adaptateur.
type Policy struct{}

// NewPolicy creates a new authorization policy.
func NewPolicy() *Policy { return &Policy{} }

// CanAdministerRepos reports whether the user may read and write the per-repo
// server policy.
//
// A SEPARATE question from CanViewRun, and the separation is deliberate: an
// administrator does not thereby gain the right to read other people's runs.
// Administering the policy and reading someone else's code are different
// capabilities, and there is no reason to bundle them — §2 treats a playbook as
// code, and an audit tree is that code.
//
// Nobody is born an administrator: the flag defaults to false and no endpoint
// grants it. The first one is set by hand in SQL by whoever operates the
// deployment, because any other bootstrap would mean the first person to log in
// on a fresh instance owns the whole organisation's configuration.
func (p *Policy) CanAdministerRepos(_ context.Context, user *core.User) bool {
	return user != nil && !user.Disabled && user.IsAdmin
}

// CanViewRun reports whether the user may read a run and everything hanging off
// it (events, diffs, git diffs).
//
// The rule is ownership of the run and nothing else. An administrator does not
// qualify — see CanAdministerRepos for why that separation is deliberate — and
// neither does someone who operates the machines the run touched.
//
// That second case is §6.5's "everything that touched db-prod-02 this month",
// and its absence is load-bearing rather than cosmetic: it is precisely why
// HandleListByHost filters its result down to the caller instead of serving the
// host's whole history. Widening it needs a notion of who owns a host, which
// does not exist yet; the type comment above says where it would enter.
func (p *Policy) CanViewRun(ctx context.Context, user *core.User, run *core.Run) bool {
	if user == nil || run == nil {
		return false
	}
	return user.ID == run.UserID
}

// CanListRuns reports whether the user may list runs. The listing itself is
// always narrowed to the caller — see VisibleRunFilter.
func (p *Policy) CanListRuns(ctx context.Context, user *core.User) bool {
	return user != nil
}

// VisibleRunFilter narrows a caller-supplied filter to what the user may see.
// It overwrites UserID rather than validating it, so a caller cannot widen its
// own scope by passing someone else's id. ok is false when nothing is visible;
// callers must not fall back to the unfiltered filter, since ListRuns treats a
// non-positive UserID as "no filter at all".
func (p *Policy) VisibleRunFilter(user *core.User, filter core.RunFilter) (core.RunFilter, bool) {
	if user == nil {
		return core.RunFilter{}, false
	}
	filter.UserID = user.ID
	return filter, true
}

// CanViewHost reports whether the user may query runs by host.
func (p *Policy) CanViewHost(ctx context.Context, user *core.User, host string) bool {
	return user != nil
}

// requireRunAccess resolves a run_ref and checks read access in one step.
//
// A run the caller may not see is reported as absent: distinguishing "forbidden"
// from "not found" would turn the endpoint into an oracle for which run_refs
// exist, which is the enumeration property ADR-0021 removed by exposing ULIDs.
func (a *Server) requireRunAccess(w http.ResponseWriter, r *http.Request, runRef string) (core.Run, bool) {
	run, err := a.deps.Runs.RunByRef(r.Context(), runRef)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return core.Run{}, false
	}
	if !a.policy.CanViewRun(r.Context(), ContextUser(r.Context()), &run) {
		http.Error(w, "run not found", http.StatusNotFound)
		return core.Run{}, false
	}
	return run, true
}
