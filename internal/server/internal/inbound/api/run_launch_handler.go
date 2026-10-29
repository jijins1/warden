package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/runref"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// launchRunPayload est ce qu'une PAGE envoie pour lancer un run.
//
// Volontairement plus PAUVRE que createRunPayload, et chaque absence est une
// décision :
//
//   - pas de `run_ref` : rien ne le coordonne ici, le serveur le tire ;
//   - pas de `base_sha` : c'est la principale, que le serveur vient de
//     résoudre ;
//   - pas de `kind`/`module`/`host_pattern` : l'ad-hoc depuis le navigateur
//     n'est pas dans ce premier jet (ADR-0042), et le silence vaut `playbook` —
//     `DisallowUnknownFields` refuse `"kind"` plutôt que de l'ignorer ;
//   - `environment` est OBLIGATOIRE, contrairement au chemin CLI où il peut
//     venir du `default_env` du commit. La raison est un ordre : il faut la
//     principale pour lire la configuration, donc le `default_env` obligerait
//     à faire travailler le rôle `git` AVANT toute autorisation. Un champ de
//     formulaire coûte moins cher que cette inversion.
type launchRunPayload struct {
	Environment string `json:"environment"`
	Inventory   string `json:"inventory,omitempty"`
	Playbook    string `json:"playbook"`

	// Flags reste un json.RawMessage pour la même raison que sur createRunPayload :
	// son décodage EST une validation (ADR-0038), et core.FlagsDemandes la fait.
	Flags json.RawMessage `json:"flags,omitempty"`

	BecomePassword     string            `json:"become_password,omitempty"`
	ConnectionPassword string            `json:"connection_password,omitempty"`
	ForwardEnv         map[string]string `json:"forward_env,omitempty"`
}

// HandleLaunchRun lance un run sur la branche protégée, sans qu'aucun client ne
// pousse.
//
// POST /repos/{repo}/runs
//
// UN HANDLER DISTINCT de HandleCreateRun, et non un drapeau dans son corps. Le
// commentaire de celui-ci dit que l'ordre de ses opérations « vaut d'être lu en
// entier » ; une branche à l'intérieur rendrait chacune de ses affirmations
// conditionnelle, donc invérifiable à la lecture. Les deux partagent ce qui
// doit l'être — resoudreEffectif et enregistrerEtDemarrer, dans
// run_pipeline.go — et rien d'autre.
//
// L'ORDRE ici aussi porte la sécurité :
//
//  1. l'utilisateur vient de l'authentification ;
//  2. l'environnement est NOMMÉ et autorisé AVANT que le rôle `git` ne
//     travaille, comme sur POST /runs — c'est ce que l'obligation du champ
//     achète (ADR-0042) ;
//  3. la principale est rafraîchie, et son SHA est la BASE de la ref d'audit.
//     Aucun SHA ne vient du client, sur ce chemin, à aucun moment ;
//  4. la ref d'audit est pointée par le rôle `git` sur ce SHA : même commit que
//     la principale, aucun nouvel objet créé. C'est le SEUL effet avant
//     l'enregistrement, et il vit dans le namespace d'audit de son auteur ;
//  5. la suite est celle de POST /runs, aux mêmes fonctions.
//
// Conséquence à lire une seule fois plutôt qu'à redécouvrir : pour tout run
// lancé par ce chemin, `run.audit_sha == run.base_sha == run.mainline_sha` — le
// même commit, référencé trois fois. Ce n'est pas un défaut ; voir ADR-0042.
func (a *Server) HandleLaunchRun(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if user == nil || user.Disabled {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.deps.Runs == nil || a.deps.Creator == nil || a.deps.Repos == nil ||
		a.deps.Mirrors == nil || a.deps.Audits == nil {
		http.Error(w, "launching a run from the UI is not available", http.StatusNotImplemented)
		return
	}

	repoName := r.PathValue("repo")
	if repoName == "" {
		http.Error(w, "repo is required", http.StatusBadRequest)
		return
	}

	var payload launchRunPayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRun))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "unreadable request body", http.StatusBadRequest)
		return
	}

	if payload.Environment == "" {
		http.Error(w, "environment is required to launch a run from the UI", http.StatusBadRequest)
		return
	}
	if payload.Playbook == "" {
		http.Error(w, "playbook is required", http.StatusBadRequest)
		return
	}
	// La FORME avant l'autorisation : un nom tordu doit produire une erreur de
	// forme, pas un 403 qui laisserait croire à un refus de droit sur un nom
	// simplement mal écrit.
	if err := core.ValidateEnvironment(payload.Environment); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flags, err := core.FlagsDemandes(payload.Flags)
	if err != nil {
		slog.Warn("flags refusés",
			slog.String("repo", repoName), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// L'AUTORISATION AVANT TOUT TRAVAIL, sans condition — contrairement à
	// HandleCreateRun où elle est conditionnelle à un champ facultatif.
	// `environment` est obligatoire ici (voir launchRunPayload), donc ce
	// contrôle est TOUJOURS le premier effet de cette requête.
	if !a.autoriserLEnvironnement(w, r, user, payload.Environment) {
		return
	}

	repo, err := a.deps.Repos.GetRepo(r.Context(), repoName)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			http.Error(w, "this repository has no server-side configuration yet", http.StatusNotFound)
			return
		}
		slog.Error("lecture de la configuration de dépôt en échec",
			slog.String("repo", repoName), slog.String("error", err.Error()))
		http.Error(w, "could not read repository configuration", http.StatusInternalServerError)
		return
	}

	runRef, err := runref.New()
	if err != nil {
		slog.Error("tirage d'un identifiant de run en échec", slog.String("error", err.Error()))
		http.Error(w, "could not launch the run", http.StatusInternalServerError)
		return
	}

	// Le miroir est rafraîchi ICI, après l'autorisation — même position que sur
	// POST /runs, pour la même raison (ADR-0006) : un compte sans droit ne doit
	// jamais faire travailler le rôle `git`.
	mainlineSHA, err := a.deps.Mirrors.RefreshMirror(r.Context(), repo.Name, repo.RemoteURL, repo.MainlineRef)
	if err != nil {
		slog.Error("rafraîchissement du miroir en échec",
			slog.String("repo", repo.Name), slog.String("error", err.Error()))
		http.Error(w, "the protected branch could not be refreshed from the forge", http.StatusBadGateway)
		return
	}

	// La configuration EST celle de la principale : l'artefact d'audit de ce
	// run aura exactement cet arbre, par construction (ADR-0042), donc les lire
	// depuis `mainlineSHA` ne contourne pas ADR-0009, il l'anticipe d'un pas.
	effectif, ok := a.resoudreEffectif(w, r, user, repo.Name, mainlineSHA, runRef, core.Requete{
		Playbook:    payload.Playbook,
		Environment: payload.Environment,
		Inventory:   payload.Inventory,
		ForwardEnv:  payload.ForwardEnv,
		Flags:       flags,
	})
	if !ok {
		return
	}

	// LE seul effet d'écriture avant l'enregistrement : une SECONDE ref sur un
	// commit qui existe déjà, jamais un nouveau commit (ADR-0042).
	auditRef := core.AuditRefFor(user.ID, runRef)
	if err := a.deps.Audits.RecordAuditRef(r.Context(), core.AuditRef{
		Repo: repo.Name, Ref: auditRef, CommitSHA: mainlineSHA,
	}); err != nil {
		if errors.Is(err, core.ErrAuditRefConflict) {
			// Ne peut arriver qu'avec une collision d'identifiant de run, qui
			// n'a pratiquement aucune chance de se produire (80 bits d'aléa) —
			// mais si elle se produit, c'est un conflit et non une panne.
			http.Error(w, "this run identifier is already recorded", http.StatusConflict)
			return
		}
		slog.Error("frappe de la ref d'audit en échec",
			slog.String("repo", repo.Name), slog.String("ref", auditRef), slog.String("error", err.Error()))
		http.Error(w, "the audit reference could not be recorded", http.StatusBadGateway)
		return
	}

	a.enregistrerEtDemarrer(w, r, runADemarrer{
		User: user, RepoName: repo.Name, RunRef: runRef,
		Playbook: payload.Playbook, Kind: core.RunPlaybook,
		// AuditSHA == BaseSHA == MainlineSHA : voir le commentaire de cette
		// fonction. Les trois colonnes tiennent le même fait sous trois noms.
		AuditSHA: mainlineSHA, BaseSHA: mainlineSHA,
		MainlineRef: repo.MainlineRef, MainlineSHA: mainlineSHA,
		Effectif: effectif, BecomePassword: payload.BecomePassword,
		ConnectionPassword: payload.ConnectionPassword,
	})
}
