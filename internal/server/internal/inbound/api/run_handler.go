package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// createRunPayload est ce que la CLI déclare après avoir poussé son commit
// d'audit (§5.2).
//
// Noter ce qui n'y est PAS : `audit_sha`. Le serveur ne l'accepte pas du client,
// il le résout depuis la ref qu'il compose lui-même — voir HandleCreateRun. Ni
// `user_id` : il vient de l'authentification, et un identifiant d'utilisateur
// dans un corps de requête est au mieux redondant, au pire une tentative.
type createRunPayload struct {
	RunRef      string          `json:"run_ref"`
	Repo        string          `json:"repo"`
	Environment string          `json:"environment"`
	Inventory   string          `json:"inventory"`
	Playbook    string          `json:"playbook"`
	BaseSHA     string          `json:"base_sha"`
	Flags       json.RawMessage `json:"flags,omitempty"`

	RunnerImage       string `json:"runner_image,omitempty"`
	RunnerImageDigest string `json:"runner_image_digest,omitempty"`
}

// createRunResponse rend ce que le serveur a décidé, et non ce que le client a
// demandé.
type createRunResponse struct {
	RunRef      string `json:"run_ref"`
	AuditSHA    string `json:"audit_sha"`
	MainlineRef string `json:"mainline_ref"`
	MainlineSHA string `json:"mainline_sha"`

	// Pod est vide quand le run est enregistré sans être démarré.
	Pod string `json:"pod,omitempty"`
}

const tailleMaxRun = 16 << 10

// HandleCreateRun enregistre un run après le push de son commit d'audit.
//
// POST /runs
//
// L'ORDRE des opérations porte la sécurité, et il vaut d'être lu en entier :
//
//  1. l'utilisateur vient de l'authentification, jamais du corps ;
//  2. la ref d'audit est COMPOSÉE par le serveur à partir de cet utilisateur et
//     de l'identifiant de run — dont la forme est vérifiée juste avant ;
//  3. `audit_sha` est la position de CETTE ref, demandée au rôle `git`.
//
// Sans le point 3, le produit avait une fuite qui n'empruntait pas le protocole
// git mais son propre modèle de données : le diff d'audit du §6.5 compare
// `mainline_sha` à `audit_sha` sans se demander à qui ce dernier appartient, et
// `CanViewRun` autorise le propriétaire du run. Un utilisateur déclarant
// l'`audit_sha` d'un autre lisait donc le code de cet autre à travers le diff de
// SON PROPRE run — auquel il a légitimement droit. Trois contrôles fermaient
// déjà cette lecture côté git ; celui-ci ferme la porte de derrière.
func (a *Server) HandleCreateRun(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if user == nil || user.Disabled {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.deps.Runs == nil || a.deps.Creator == nil || a.deps.Repos == nil ||
		a.deps.Mirrors == nil || a.deps.Refs == nil {
		http.Error(w, "run creation is not available", http.StatusNotImplemented)
		return
	}

	var payload createRunPayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, tailleMaxRun))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "unreadable request body", http.StatusBadRequest)
		return
	}

	// La forme du ULID est vérifiée AVANT de composer quoi que ce soit : il entre
	// dans un nom de ref git et dans une URL, et il est tiré par la CLI (ADR-0028),
	// donc c'est de l'entrée non fiable.
	if !core.ValidRunRef(payload.RunRef) {
		http.Error(w, "run_ref must be a 26-character ULID", http.StatusBadRequest)
		return
	}
	if payload.Repo == "" {
		http.Error(w, "repo is required", http.StatusBadRequest)
		return
	}

	repo, err := a.deps.Repos.GetRepo(r.Context(), payload.Repo)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			// Un dépôt non configuré est une action d'administrateur en attente,
			// pas une panne. Le dire permet à l'utilisateur de savoir quoi
			// demander.
			http.Error(w, "this repository has no server-side configuration yet", http.StatusNotFound)
			return
		}
		slog.Error("lecture de la configuration de dépôt en échec",
			slog.String("repo", payload.Repo), slog.String("error", err.Error()))
		http.Error(w, "could not read repository configuration", http.StatusInternalServerError)
		return
	}

	// Le miroir est rafraîchi ICI et non avant le push, parce qu'il n'y a plus
	// d'appel avant le push (ADR-0028). Ce n'est pas un problème de correction :
	// le contrôle de fraîcheur exclut ce que le serveur A ANNONCÉ, donc il est
	// cohérent avec lui-même quel que soit l'âge du miroir. C'est un problème de
	// TAILLE — un miroir vieux d'un mois fait transporter un mois d'historique —
	// et un rafraîchissement planifié le réduira sans rien changer ici.
	mainlineSHA, err := a.deps.Mirrors.RefreshMirror(r.Context(), repo.Name, repo.RemoteURL, repo.MainlineRef)
	if err != nil {
		slog.Error("rafraîchissement du miroir en échec",
			slog.String("repo", repo.Name), slog.String("error", err.Error()))
		http.Error(w, "the protected branch could not be refreshed from the forge", http.StatusBadGateway)
		return
	}

	// LE point de la fonction. La ref vient de l'identité authentifiée et du
	// ULID validé ; rien de ce que le client a envoyé ne la compose.
	auditRef := core.AuditRefFor(user.ID, payload.RunRef)
	auditSHA, err := a.deps.Refs.ResolveRef(r.Context(), repo.Name, auditRef)
	if err != nil {
		// Le cas nominal de cette erreur est un client qui déclare un run sans
		// avoir poussé, ou qui s'est trompé d'identifiant. Le dire précisément
		// évite une chasse au fantôme.
		http.Error(w, "no audit commit was pushed for this run", http.StatusBadRequest)
		return
	}

	id, err := a.deps.Creator.CreateRun(r.Context(), core.NewRun{
		RunRef:            payload.RunRef,
		UserID:            user.ID,
		Repo:              repo.Name,
		Environment:       payload.Environment,
		Inventory:         payload.Inventory,
		Playbook:          payload.Playbook,
		AuditSHA:          auditSHA,
		BaseSHA:           payload.BaseSHA,
		MainlineRef:       repo.MainlineRef,
		MainlineSHA:       mainlineSHA,
		Flags:             payload.Flags,
		RunnerImage:       payload.RunnerImage,
		RunnerImageDigest: payload.RunnerImageDigest,
	})
	if err != nil {
		slog.Error("création du run en échec",
			slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
		http.Error(w, "could not create the run", http.StatusInternalServerError)
		return
	}

	slog.Info("run créé",
		slog.String("run_ref", payload.RunRef), slog.Int64("id", id),
		slog.String("repo", repo.Name), slog.Int64("user", user.ID))

	// Le démarrage vient APRÈS l'enregistrement, et l'ordre porte une garantie :
	// le run naît `interrupted` / `unknown` (§6.4), donc un échec ici laisse une
	// ligne qui dit la vérité — le run existe, il n'a pas tourné. L'inverse —
	// lancer puis enregistrer — laisserait un pod en vie que rien ne référence.
	pod := ""
	if a.deps.Starter != nil {
		pod, err = a.deps.Starter.Start(r.Context(), core.Run{
			ID: id, RunRef: payload.RunRef, UserID: user.ID, Repo: repo.Name,
			Environment: payload.Environment, Inventory: payload.Inventory,
			Playbook: payload.Playbook, AuditSHA: auditSHA,
		})
		if err != nil {
			slog.Error("démarrage du run en échec",
				slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
			// Le run est ENREGISTRÉ, et son état dit déjà qu'il n'a pas tourné.
			// Le message nomme les deux faits et l'identifiant : sans lui,
			// l'utilisateur croit que rien ne s'est passé et cherche un artefact
			// qu'il ne sait pas nommer.
			http.Error(w, "run "+payload.RunRef+" is recorded but could not be started",
				http.StatusBadGateway)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(createRunResponse{
		RunRef:      payload.RunRef,
		AuditSHA:    auditSHA,
		MainlineRef: repo.MainlineRef,
		MainlineSHA: mainlineSHA,
		Pod:         pod,
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}

// pushGrantResponse porte de quoi pousser, et rien de plus.
type pushGrantResponse struct {
	Token     string `json:"token"`
	RefPrefix string `json:"ref_prefix"`
	ExpiresAt int64  `json:"expires_at"`
}

// GrantTTL borne la vie d'un jeton de push.
//
// C'est la SEULE borne : l'ADR-0028 assume l'absence de révocation, au motif que
// la portée se limite au namespace d'audit de son porteur et qu'une ref d'audit
// est écrite une fois. Une journée est assez longue pour que la CLI garde son
// jeton entre deux runs — ce qui est le gain de la portée par préfixe — et assez
// courte pour qu'un jeton oublié dans un historique de shell cesse de valoir.
const GrantTTL = 24 * time.Hour

// HandleIssuePushGrant frappe le jeton qui autorise le push d'audit.
//
// POST /repos/{repo}/push-grant
//
// Un endpoint distinct de la création de run, et c'est ce qui supprime un
// aller-retour par run : le jeton porte un PRÉFIXE, donc la CLI le réutilise pour
// tous ses runs jusqu'à expiration (ADR-0028).
func (a *Server) HandleIssuePushGrant(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if user == nil || user.Disabled {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if a.deps.Grants == nil || a.deps.Repos == nil {
		http.Error(w, "push grants are not available", http.StatusNotImplemented)
		return
	}

	// Le dépôt doit être configuré : frapper un jeton pour un dépôt inconnu
	// produirait un push que le rôle `git` refuse, et un message d'erreur au
	// mauvais endroit.
	repo, err := a.deps.Repos.GetRepo(r.Context(), r.PathValue("repo"))
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			http.Error(w, "this repository has no server-side configuration yet", http.StatusNotFound)
			return
		}
		slog.Error("lecture de la configuration de dépôt en échec", slog.String("error", err.Error()))
		http.Error(w, "could not read repository configuration", http.StatusInternalServerError)
		return
	}

	expires := time.Now().Add(GrantTTL)
	// Le préfixe est DÉRIVÉ de l'identité authentifiée. C'est ce qui fait qu'un
	// utilisateur ne peut pas se faire délivrer le namespace d'un autre, quoi
	// qu'il envoie.
	token, err := a.deps.Grants.IssuePushGrant(r.Context(), repo.Name,
		core.AuditPrefixFor(user.ID), expires)
	if err != nil {
		slog.Error("frappe du jeton de push en échec", slog.String("error", err.Error()))
		http.Error(w, "could not issue a push grant", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Un jeton porteur n'a rien à faire dans un cache, ni intermédiaire ni local.
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(pushGrantResponse{
		Token:     token,
		RefPrefix: core.AuditPrefixFor(user.ID),
		ExpiresAt: expires.Unix(),
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}
