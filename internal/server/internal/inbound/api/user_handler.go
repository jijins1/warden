package api

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// userPayload est un compte local tel qu'il circule.
//
// Il ne porte JAMAIS de mot de passe, dans aucun sens. À la création, le serveur
// en tire un et le rend une fois ; à la lecture, `HasPassword` dit seulement s'il
// en existe un.
type userPayload struct {
	Subject string `json:"subject"`
	Name    string `json:"name,omitempty"`
	Admin   bool   `json:"admin,omitempty"`

	Disabled    bool `json:"disabled,omitempty"`
	HasPassword bool `json:"has_password"`

	LastSeenAt string `json:"last_seen_at,omitempty"`
}

// createdUserPayload rend le mot de passe TIRÉ PAR LE SERVEUR, une seule fois.
//
// Il est tiré ici et non choisi par l'administrateur, et c'est délibéré : un mot
// de passe choisi par un tiers est un mot de passe que ce tiers connaît, et il
// est presque toujours faible parce qu'il doit être dicté. Celui-ci est
// aléatoire, transmis une fois, et son porteur le remplacera par
// `warden passwd`.
type createdUserPayload struct {
	userPayload
	Password string `json:"password,omitempty"`
}

// sujetValide borne ce qu'un identifiant de compte peut contenir.
//
// La contrainte est plus stricte que ce que le schéma exige, et pour une raison
// pratique : ce sujet s'écrit en ligne de commande, se dicte au téléphone et
// s'affiche dans l'audit. Y autoriser des espaces ou de l'unicode ferait des
// comptes qu'on ne sait pas taper — et deux comptes visuellement identiques.
var sujetValide = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

func versUserPayload(a core.LocalAccount) userPayload {
	p := userPayload{
		Subject: a.Subject, Name: a.DisplayName, Admin: a.IsAdmin,
		Disabled: a.Disabled, HasPassword: a.HasPassword,
	}
	if !a.LastSeenAt.IsZero() {
		p.LastSeenAt = a.LastSeenAt.UTC().Format(time.RFC3339)
	}
	return p
}

// HandleListUsers rend les comptes locaux.
//
// GET /admin/users
func (a *Server) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Accounts == nil {
		http.Error(w, "local accounts are not available", http.StatusNotImplemented)
		return
	}

	comptes, err := a.deps.Accounts.ListLocalUsers(r.Context())
	if err != nil {
		slog.Error("liste des comptes en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the accounts", http.StatusInternalServerError)
		return
	}

	out := make([]userPayload, 0, len(comptes))
	for _, c := range comptes {
		out = append(out, versUserPayload(c))
	}
	ecrireJSON(w, out)
}

// HandlePutUser crée un compte local et lui tire un mot de passe.
//
// PUT /admin/users/{subject}?admin=true&name=...
//
// Idempotent sur le sujet : rejouer rafraîchit le nom et le droit
// d'administration. Le mot de passe, lui, n'est tiré QUE si le compte n'en avait
// pas — sans quoi rejouer la création couperait l'accès de quelqu'un qui
// travaille, et un `PUT` ne doit pas être un piège.
func (a *Server) HandlePutUser(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Accounts == nil || a.deps.Auth == nil {
		http.Error(w, "local accounts are not available", http.StatusNotImplemented)
		return
	}

	subject := r.PathValue("subject")
	if !sujetValide.MatchString(subject) {
		http.Error(w, "invalid subject: lowercase letters, digits, dot, dash and underscore only",
			http.StatusBadRequest)
		return
	}

	admin := r.URL.Query().Get("admin") == "true"
	nom := r.URL.Query().Get("name")

	// L'état AVANT, pour savoir s'il faut tirer un mot de passe. La course avec
	// une création concurrente est sans danger : deux tirages simultanés
	// laisseraient le second gagner, et l'administrateur verrait deux mots de
	// passe dont un seul marche — désagréable, jamais une ouverture d'accès.
	avait, err := a.compteAUnMotDePasse(r, subject)
	if err != nil {
		slog.Error("lecture du compte en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the account", http.StatusInternalServerError)
		return
	}

	u, err := a.deps.Accounts.CreateLocalUser(r.Context(), subject, nom, admin)
	if err != nil {
		slog.Error("création de compte en échec",
			slog.String("sujet", subject), slog.String("error", err.Error()))
		http.Error(w, "could not create the account", http.StatusInternalServerError)
		return
	}

	out := createdUserPayload{userPayload: userPayload{
		Subject: u.Subject, Name: u.DisplayName, Admin: u.IsAdmin,
		Disabled: u.Disabled, HasPassword: true,
	}}

	if !avait {
		motDePasse, err := core.MotDePasseTire()
		if err != nil {
			slog.Error("tirage du mot de passe en échec", slog.String("error", err.Error()))
			http.Error(w, "could not create the account", http.StatusInternalServerError)
			return
		}
		if err := a.deps.Auth.PoserMotDePasse(r.Context(), u.ID, []byte(motDePasse), utilisateurID(r)); err != nil {
			slog.Error("pose du mot de passe en échec", slog.String("error", err.Error()))
			http.Error(w, "could not create the account", http.StatusInternalServerError)
			return
		}
		out.Password = motDePasse
	}

	slog.Info("compte local écrit",
		slog.String("sujet", u.Subject), slog.Bool("admin", u.IsAdmin),
		slog.Bool("mot_de_passe_tiré", !avait), slog.Int64("par", utilisateurID(r)))

	ecrireJSON(w, out)
}

// HandleResetPassword tire un nouveau mot de passe pour un compte.
//
// POST /admin/users/{subject}/password
//
// SANS l'ancien, contrairement à `/auth/password` : c'est le chemin de
// l'administrateur qui remet en service un compte dont le mot de passe est
// perdu. Il coupe toutes les sessions du compte, comme tout changement de mot de
// passe — c'est la même transaction qui l'impose.
func (a *Server) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Auth == nil || a.deps.Users == nil {
		http.Error(w, "local accounts are not available", http.StatusNotImplemented)
		return
	}

	subject := r.PathValue("subject")
	u, err := a.deps.Users.UserBySubject(r.Context(), core.UserLocal, subject)
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("lecture du compte en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read the account", http.StatusInternalServerError)
		return
	}

	motDePasse, err := core.MotDePasseTire()
	if err != nil {
		slog.Error("tirage du mot de passe en échec", slog.String("error", err.Error()))
		http.Error(w, "could not reset the password", http.StatusInternalServerError)
		return
	}
	if err := a.deps.Auth.PoserMotDePasse(r.Context(), u.ID, []byte(motDePasse), utilisateurID(r)); err != nil {
		slog.Error("pose du mot de passe en échec", slog.String("error", err.Error()))
		http.Error(w, "could not reset the password", http.StatusInternalServerError)
		return
	}

	slog.Warn("mot de passe réinitialisé par un administrateur",
		slog.String("sujet", subject), slog.Int64("par", utilisateurID(r)),
		slog.String("effet", "toutes les sessions du compte sont révoquées"))

	ecrireJSON(w, createdUserPayload{
		userPayload: userPayload{Subject: u.Subject, Name: u.DisplayName,
			Admin: u.IsAdmin, HasPassword: true},
		Password: motDePasse,
	})
}

// HandleDisableUser coupe l'accès d'un compte.
//
// POST /admin/users/{subject}/disable
//
// La désactivation RÉVOQUE les sessions, dans la même transaction : sans cela,
// couper un accès ne ferait rien avant l'expiration du jeton — jusqu'à trente
// jours après avoir cru le couper.
func (a *Server) HandleDisableUser(w http.ResponseWriter, r *http.Request) {
	a.basculerCompte(w, r, true)
}

// HandleEnableUser remet un compte en service.
//
// POST /admin/users/{subject}/enable
func (a *Server) HandleEnableUser(w http.ResponseWriter, r *http.Request) {
	a.basculerCompte(w, r, false)
}

func (a *Server) basculerCompte(w http.ResponseWriter, r *http.Request, desactive bool) {
	if !a.adminOuRien(w, r) {
		return
	}
	if a.deps.Accounts == nil {
		http.Error(w, "local accounts are not available", http.StatusNotImplemented)
		return
	}

	subject := r.PathValue("subject")

	// Se désactiver soi-même laisserait un déploiement sans administrateur
	// joignable, et le seul recours serait la base. Le refus est ici.
	if u := ContextUser(r.Context()); u != nil && u.Subject == subject && desactive {
		http.Error(w, "refusing to disable your own account", http.StatusBadRequest)
		return
	}

	u, err := a.deps.Accounts.SetUserDisabled(r.Context(), subject, desactive)
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("bascule du compte en échec",
			slog.String("sujet", subject), slog.String("error", err.Error()))
		http.Error(w, "could not update the account", http.StatusInternalServerError)
		return
	}

	slog.Warn("compte basculé",
		slog.String("sujet", subject), slog.Bool("désactivé", desactive),
		slog.Int64("par", utilisateurID(r)))

	ecrireJSON(w, userPayload{
		Subject: u.Subject, Name: u.DisplayName, Admin: u.IsAdmin,
		Disabled: u.Disabled, HasPassword: true,
	})
}

// compteAUnMotDePasse dit si le compte existe DÉJÀ avec des identifiants.
//
// L'absence de compte n'est pas une erreur : c'est le cas nominal d'une
// création.
func (a *Server) compteAUnMotDePasse(r *http.Request, subject string) (bool, error) {
	comptes, err := a.deps.Accounts.ListLocalUsers(r.Context())
	if err != nil {
		return false, err
	}
	for _, c := range comptes {
		if c.Subject == subject {
			return c.HasPassword, nil
		}
	}
	return false, nil
}
