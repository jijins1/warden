package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// repoPayload est la forme échangée sur l'API d'administration.
//
// Distincte de core.Repo bien qu'elle en soit le miroir : le jour où la
// configuration gagne un champ que l'API ne doit pas exposer — ou l'inverse —
// c'est ici que la divergence se déclare, plutôt que dans un type du cœur qui se
// mettrait à porter des étiquettes JSON.
type repoPayload struct {
	Name        string `json:"name"`
	RemoteURL   string `json:"remote_url"`
	MainlineRef string `json:"mainline_ref"`
}

// tailleMaxConfig borne le corps accepté. Une configuration de dépôt fait
// quelques centaines d'octets.
const tailleMaxConfig = 4 << 10

// HandleListRepos rend la configuration de tous les dépôts.
//
// GET /admin/repos
//
// Réservé aux administrateurs, et pas seulement parce que c'est de la
// configuration : la liste des dépôts et de leurs URL de forge est une carte de
// l'organisation, utile à qui cherche par où entrer.
func (a *Server) HandleListRepos(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if !a.policy.CanAdministerRepos(r.Context(), user) {
		// 404 et non 403 : un 403 confirmerait l'existence de la surface
		// d'administration à un utilisateur ordinaire. Il n'a aucune raison de
		// savoir qu'elle existe.
		http.NotFound(w, r)
		return
	}

	if a.deps.Repos == nil {
		http.Error(w, "repository configuration is not available", http.StatusNotImplemented)
		return
	}

	repos, err := a.deps.Repos.ListRepos(r.Context())
	if err != nil {
		slog.Error("liste des dépôts en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read repository configuration", http.StatusInternalServerError)
		return
	}

	out := make([]repoPayload, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repoPayload{
			Name: repo.Name, RemoteURL: repo.RemoteURL, MainlineRef: repo.MainlineRef,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}

// HandlePutRepo écrit la configuration d'un dépôt.
//
// PUT /admin/repos/{repo}
//
// Le nom vient du CHEMIN et non du corps. Deux sources pour le même
// identifiant, c'est la garantie qu'un jour elles ne s'accorderont pas — et ici
// le désaccord signifierait écrire la configuration d'un dépôt en croyant en
// modifier un autre.
func (a *Server) HandlePutRepo(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if !a.policy.CanAdministerRepos(r.Context(), user) {
		http.NotFound(w, r)
		return
	}

	if a.deps.Repos == nil {
		http.Error(w, "repository configuration is not available", http.StatusNotImplemented)
		return
	}

	var payload repoPayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxConfig))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "unreadable request body", http.StatusBadRequest)
		return
	}
	if payload.Name != "" && payload.Name != r.PathValue("repo") {
		http.Error(w, "the repository name in the body contradicts the path", http.StatusBadRequest)
		return
	}

	repo := core.Repo{
		Name:        r.PathValue("repo"),
		RemoteURL:   payload.RemoteURL,
		MainlineRef: payload.MainlineRef,
	}
	// Validé ici pour que le message dise ce qu'il fallait écrire. Une violation
	// de contrainte PostgreSQL nomme la contrainte, ce qui n'aide personne à
	// corriger sa requête.
	if err := repo.Valid(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Le dépôt bare est créé AVANT que la configuration soit écrite, et l'ordre
	// est réfléchi. Dans ce sens, un échec laisse l'administrateur sans
	// configuration et sans dépôt : il réessaie, rien n'est à défaire. Dans
	// l'autre, il laisserait une configuration que le premier run ferait échouer
	// sur « dépôt inconnu », c'est-à-dire un état cohérent en apparence et
	// inutilisable en pratique.
	//
	// Un dépôt créé dont la configuration échoue ensuite est, lui, inerte : un
	// répertoire vide que la prochaine tentative adoptera.
	if a.deps.RepoMaker != nil {
		if err := a.deps.RepoMaker.CreateRepo(r.Context(), repo.Name); err != nil {
			slog.Error("création du dépôt sur le volume en échec",
				slog.String("repo", repo.Name), slog.String("error", err.Error()))
			http.Error(w, "the repository could not be created on the git role's volume",
				http.StatusBadGateway)
			return
		}
	}

	if err := a.deps.Repos.UpsertRepo(r.Context(), repo, user.ID); err != nil {
		slog.Error("écriture de la configuration de dépôt en échec",
			slog.String("repo", repo.Name), slog.String("error", err.Error()))
		http.Error(w, "could not write repository configuration", http.StatusInternalServerError)
		return
	}

	slog.Info("configuration de dépôt écrite",
		slog.String("repo", repo.Name), slog.Int64("by", user.ID))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(repoPayload{
		Name: repo.Name, RemoteURL: repo.RemoteURL, MainlineRef: repo.MainlineRef,
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}
