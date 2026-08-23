package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// loginPayload est ce que `warden login` envoie.
//
// Le mot de passe voyage dans un CORPS JSON et jamais dans une URL : les chemins
// finissent dans les logs d'accès, dans l'historique du navigateur et dans les
// en-têtes `Referer`. Un corps n'y va pas.
type loginPayload struct {
	Subject  string `json:"subject"`
	Password string `json:"password"`
}

// loginResponse est ce que la CLI range dans son fichier d'identifiants.
type loginResponse struct {
	// Token est la seule fois où un jeton de session sort du serveur. Le champ
	// est une `string` et non un `core.SessionToken` : ce type masque sa valeur à
	// la sérialisation, précisément pour que ce chemin-ci soit le seul explicite.
	Token     string `json:"token"`
	Subject   string `json:"subject"`
	Name      string `json:"name,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// tailleMaxLogin borne le corps accepté. Un sujet et un mot de passe ; le
// plafond en laisse passer largement, et arrête un corps de plusieurs mégaoctets
// envoyé sans authentification.
const tailleMaxLogin = 8 << 10

// HandleLogin ouvre une session.
//
// POST /auth/login — HORS du middleware d'authentification, forcément : on ne
// peut pas exiger une session pour en ouvrir une. C'est donc la seule surface du
// produit qu'un appelant anonyme peut atteindre, et tout ce qui la protège est
// dans `core.Authenticator` : file d'attente bornée, délai plancher, empreinte
// leurre pour le compte inconnu.
func (a *Server) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if a.deps.Auth == nil {
		http.Error(w, "no authenticator configured", http.StatusNotImplemented)
		return
	}

	var p loginPayload
	if !decoderJSON(w, r, tailleMaxLogin, &p) {
		return
	}

	ouverture, err := a.deps.Auth.Login(r.Context(), p.Subject, []byte(p.Password))
	if err != nil {
		if errors.Is(err, core.ErrIdentifiantsRefuses) {
			// Le SUJET est journalisé, pas le motif détaillé : côté serveur la
			// distinction a une valeur d'exploitation, côté client elle
			// confirmerait qu'un compte existe.
			slog.Warn("connexion refusée", slog.String("sujet", p.Subject))
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		slog.Error("connexion en échec",
			slog.String("sujet", p.Subject), slog.String("error", err.Error()))
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}

	slog.Info("session ouverte",
		slog.String("sujet", ouverture.User.Subject),
		slog.Int64("compte", ouverture.User.ID))

	ecrireJSON(w, loginResponse{
		// `Reveal` est explicite ici, et c'est le seul endroit de tout le serveur
		// où il l'est pour un jeton de session.
		Token:     ouverture.Token.Reveal(),
		Subject:   ouverture.User.Subject,
		Name:      ouverture.User.DisplayName,
		Admin:     ouverture.User.IsAdmin,
		ExpiresAt: ouverture.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// HandleLogout révoque la session de l'appelant.
//
// POST /auth/logout — DERRIÈRE le middleware : révoquer suppose de savoir quelle
// session, et le corps de la requête ne peut pas le dire. Accepter un
// identifiant de session dans un body laisserait n'importe qui déconnecter
// n'importe qui.
//
// Idempotent, et sans erreur quand l'appelant n'a pas de session : il s'est
// authentifié par l'en-tête de développement, il n'y a rien à couper, et
// échouer ferait ressembler un mode dégradé connu à une panne.
func (a *Server) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if a.deps.Auth == nil {
		http.Error(w, "no authenticator configured", http.StatusNotImplemented)
		return
	}

	session := ContextSession(r.Context())
	if session == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := a.deps.Auth.Logout(r.Context(), session.ID); err != nil {
		slog.Error("révocation de session en échec", slog.String("error", err.Error()))
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}

	slog.Info("session révoquée", slog.Int64("compte", utilisateurID(r)))
	w.WriteHeader(http.StatusNoContent)
}

// whoamiResponse dit à l'appelant qui le serveur croit qu'il est.
type whoamiResponse struct {
	Subject string `json:"subject"`
	Name    string `json:"name,omitempty"`
	Admin   bool   `json:"admin,omitempty"`

	// ExpiresAt est vide quand l'appelant n'a pas de session — c'est-à-dire en
	// mode de développement. Le vide DIT quelque chose : il n'y a pas de session
	// parce qu'il n'y a pas eu de connexion.
	ExpiresAt string `json:"expires_at,omitempty"`
}

// HandleWhoami rend l'identité de l'appelant.
//
// GET /auth/whoami — c'est ce qui permet de répondre « au fait, je suis qui, et
// jusqu'à quand ? » sans lancer un run pour le découvrir.
func (a *Server) HandleWhoami(w http.ResponseWriter, r *http.Request) {
	u := ContextUser(r.Context())
	if u == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	out := whoamiResponse{Subject: u.Subject, Name: u.DisplayName, Admin: u.IsAdmin}
	if s := ContextSession(r.Context()); s != nil {
		out.ExpiresAt = s.ExpiresAt.UTC().Format(time.RFC3339)
	}
	ecrireJSON(w, out)
}

// passwordPayload porte un changement de mot de passe.
type passwordPayload struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// HandleChangePassword change le mot de passe de l'appelant.
//
// POST /auth/password — l'ANCIEN est exigé, bien que l'appelant soit déjà
// authentifié. Un poste laissé déverrouillé suffirait sinon à s'approprier le
// compte de façon durable, là où un jeton volé expire et se révoque.
//
// Le succès COUPE toutes les sessions, y compris celle qui vient de servir : on
// change un mot de passe parce qu'on le croit compromis, et laisser vivre les
// sessions ouvertes ne protégerait de rien. La CLI le sait et redemande une
// connexion.
func (a *Server) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	if a.deps.Auth == nil {
		http.Error(w, "no authenticator configured", http.StatusNotImplemented)
		return
	}
	u := ContextUser(r.Context())
	if u == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	var p passwordPayload
	if !decoderJSON(w, r, tailleMaxLogin, &p) {
		return
	}

	err := a.deps.Auth.ChangerMotDePasse(r.Context(), *u, []byte(p.Current), []byte(p.New))
	switch {
	case errors.Is(err, core.ErrIdentifiantsRefuses):
		slog.Warn("changement de mot de passe refusé", slog.String("sujet", u.Subject))
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	case errors.Is(err, core.ErrMotDePasseFaible):
		// Le SEUL message d'erreur qui doit être précis : l'appelant doit savoir
		// ce qu'on attend de lui, et cette information ne renseigne personne sur
		// l'existence d'un compte.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		slog.Error("changement de mot de passe en échec",
			slog.String("sujet", u.Subject), slog.String("error", err.Error()))
		http.Error(w, "could not change the password", http.StatusInternalServerError)
		return
	}

	slog.Info("mot de passe changé",
		slog.String("sujet", u.Subject),
		slog.String("effet", "toutes les sessions du compte sont révoquées"))
	w.WriteHeader(http.StatusNoContent)
}
