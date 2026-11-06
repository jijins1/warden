package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

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
// doit l'être — la séquence du cœur, `core.Lanceur` — et rien d'autre.
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

	flags, err := core.FlagsDemandes(payload.Flags)
	if err != nil {
		slog.Warn("flags refusés",
			slog.String("repo", repoName), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// La séquence — forme, autorisation AVANT tout travail du rôle `git`,
	// rafraîchissement de la principale, configuration du commit, ref d'audit,
	// enregistrement, démarrage — est celle du cœur, partagée avec le
	// planificateur (ADR-0057). Ce handler ne fait que décoder et traduire.
	demarre, err := a.lanceur().LancerSurLaPrincipale(r.Context(), user, core.LancementSurLaPrincipale{
		Repo:               repoName,
		Environment:        payload.Environment,
		Inventory:          payload.Inventory,
		Playbook:           payload.Playbook,
		Flags:              flags,
		BecomePassword:     payload.BecomePassword,
		ConnectionPassword: payload.ConnectionPassword,
		ForwardEnv:         payload.ForwardEnv,
	})
	if err != nil {
		repondreAuRefus(w, err)
		return
	}
	repondreRunCree(w, demarre)
}
