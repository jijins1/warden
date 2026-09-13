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
	RunRef      string `json:"run_ref"`
	Repo        string `json:"repo"`
	Environment string `json:"environment"`
	Inventory   string `json:"inventory"`
	Playbook    string `json:"playbook,omitempty"`
	BaseSHA     string `json:"base_sha"`

	// Kind distingue un run ad-hoc d'un run de playbook. Vide vaut `playbook`,
	// comme `core.NewRun.Kind` : c'est ce que la CLI envoie depuis toujours par
	// `warden submit`. L'enrôlement a son propre endpoint
	// (`enroll_handler.go`) et n'emprunte jamais celui-ci.
	Kind string `json:"kind,omitempty"`

	// Module, ModuleArgs et HostPattern ne valent que pour `Kind == "adhoc"`,
	// mutuellement exclusifs avec Playbook — l'équivalent de
	// `ansible <host_pattern> -m <module> -a <module_args>`.
	Module      string `json:"module,omitempty"`
	ModuleArgs  string `json:"module_args,omitempty"`
	HostPattern string `json:"host_pattern,omitempty"`

	// Flags porte les drapeaux d'`ansible-playbook` du §5.5.
	//
	// Reste un `json.RawMessage` À CE NIVEAU, et seulement ici : le décodage en
	// liste de chaînes est une VALIDATION, et la faire faire par le décodeur du
	// corps rendrait « ce n'est pas un tableau » indistinguable de « le corps est
	// illisible ». `core.FlagsDemandes` la fait, et son message nomme ce qui est
	// accepté (ADR-0038).
	Flags json.RawMessage `json:"flags,omitempty"`

	// BecomePassword ne survit PAS à cette requête : il part en consigne
	// éphémère, et la livraison de l'ordre au pod le consomme. Ni journalisé,
	// ni écrit en base (ADR-0018).
	//
	// Il vient d'un HUMAIN et non du serveur : l'ADR-0020 borne son magasin aux
	// credentials que le serveur ÉMET, et celui-ci fait exception au même titre
	// que le mot de passe d'un enrôlement.
	BecomePassword string `json:"become_password,omitempty"`

	// ForwardEnv porte les VALEURS des variables du shell de l'appelant, et
	// elles seules (ADR-0009). Les NOMS ne sont pas lus ici : ils viennent du
	// commit, et une valeur envoyée pour un nom que le commit ne déclare pas est
	// jetée par la composition.
	//
	// C'est le seul champ de cette structure qui ressemble à de la configuration
	// sans en être : les valeurs ne PEUVENT venir que du shell, puisque le
	// serveur n'y a pas accès. Elles suivent le même chemin que le mot de passe
	// d'élévation — consigne éphémère, consommée à la livraison, jamais en base
	// et jamais journalisées.
	ForwardEnv map[string]string `json:"forward_env,omitempty"`
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
//
// La partie qui suit la résolution d'`audit_sha` — lecture de la configuration
// du commit, composition, autorisation de l'environnement effectif, consigne
// des secrets, création et démarrage — vit dans deux fonctions partagées avec
// `HandleLaunchRun` (ADR-0042) : `resoudreEffectif` et `enregistrerEtDemarrer`,
// dans `run_pipeline.go`. Leur ORDRE INTERNE est celui d'origine de cette
// fonction, inchangé ; `run_handler_test.go` l'asserte pour les deux chemins.
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

	// Le GENRE est décidé ICI, avant tout le reste : c'est lui qui dit si
	// `payload.Playbook` ou `payload.Module`/`HostPattern` fait autorité, et un
	// ad-hoc mal formé ne doit pas coûter un rafraîchissement de miroir, pour
	// la même raison que les flags ci-dessous.
	genre := core.RunPlaybook
	if payload.Kind != "" {
		genre = core.RunKind(payload.Kind)
	}
	var adhocTarget *core.AdhocTarget
	switch genre {
	case core.RunPlaybook:
		if payload.Module != "" || payload.HostPattern != "" {
			http.Error(w, "module and host_pattern only apply to an adhoc run", http.StatusBadRequest)
			return
		}
	case core.RunAdhoc:
		if payload.Playbook != "" {
			http.Error(w, "an adhoc run has no playbook: module and host_pattern replace it", http.StatusBadRequest)
			return
		}
		adhocTarget = &core.AdhocTarget{
			Module: payload.Module, ModuleArgs: payload.ModuleArgs, HostPattern: payload.HostPattern,
		}
		if err := adhocTarget.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		// L'enrôlement n'est pas créé par ce chemin (`enroll_handler.go` a son
		// propre endpoint) : un genre qui n'est ni playbook ni adhoc ici est
		// forcément une erreur du client.
		http.Error(w, `kind must be "playbook" or "adhoc"`, http.StatusBadRequest)
		return
	}

	// LA FORME DES FLAGS EST VALIDÉE ICI, avant que le rôle `git` ne travaille :
	// un drapeau refusé ne doit pas coûter un rafraîchissement de miroir. Le
	// message est celui du cœur, en français comme ceux de la configuration de
	// projet — il nomme la liste acceptée, et l'utilisateur est le seul à pouvoir
	// corriger sa ligne de commande.
	//
	// L'APPARTENANCE d'un `--extra-vars` à l'`extra_vars:` du projet ne peut PAS
	// être tranchée ici : elle est déclarée dans le commit, qui n'est pas encore
	// résolu. C'est `core.Composer` qui la contrôle, plus bas (ADR-0039).
	flags, err := core.FlagsDemandes(payload.Flags)
	if err != nil {
		slog.Warn("flags refusés",
			slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// L'AUTORISATION EN DEUX TEMPS, et le découpage n'est pas une commodité.
	//
	// Premier temps, ICI : quand l'utilisateur NOMME son environnement, on le
	// contrôle avant d'avoir rien lu. C'est l'invariant que `POST /runs` tient
	// depuis toujours — un run refusé n'a même pas fait interroger le dépôt — et
	// il est TESTÉ. Le conserver n'est pas de la prudence : sans lui, un compte
	// sans aucun droit ferait travailler le rôle `git`, en instance unique
	// (ADR-0006), à chaque requête.
	//
	// Second temps, après composition : l'environnement EFFECTIF peut venir du
	// `default_env` du commit (ADR-0009), et il n'est connu qu'une fois la
	// configuration lue. Autoriser une valeur puis en exécuter une autre serait
	// exactement le trou que l'ADR-0009 existe pour fermer.
	//
	// Les deux temps ne se recouvrent jamais inutilement : un flag explicite
	// l'emporte sur le fichier (`core.Composer`), donc quand le premier contrôle
	// a lieu le second porte sur la même valeur et ne fait rien.
	if payload.Environment != "" && !a.autoriserLEnvironnement(w, r, user, payload.Environment) {
		return
	}

	// À partir d'ici et jusqu'au second contrôle, tout est en LECTURE SEULE —
	// configuration de dépôt, position d'une ref, blob d'un commit. C'est le
	// critère à vérifier si on retouche cette fonction : les deux effets
	// (rafraîchissement du miroir, consigne des secrets) restent en aval du refus.
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

	// La configuration du commit, sa composition, la forme de l'environnement
	// effectif et la seconde autorisation vivent dans resoudreEffectif : c'est
	// aussi ce que fait HandleLaunchRun pour un run lancé depuis le navigateur
	// (ADR-0042), et dupliquer cette décision est la manière dont les deux
	// finiraient par diverger.
	effectif, ok := a.resoudreEffectif(w, r, user, repo.Name, auditSHA, payload.RunRef, core.Requete{
		Environment: payload.Environment,
		Inventory:   payload.Inventory,
		ForwardEnv:  payload.ForwardEnv,
		Flags:       flags,
	})
	if !ok {
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

	// La consigne des secrets, la création et le démarrage vivent dans
	// enregistrerEtDemarrer, pour la même raison que resoudreEffectif ci-dessus.
	a.enregistrerEtDemarrer(w, r, runADemarrer{
		User: user, RepoName: repo.Name, RunRef: payload.RunRef,
		Playbook: payload.Playbook, Kind: genre, AdhocTarget: adhocTarget,
		AuditSHA: auditSHA, BaseSHA: payload.BaseSHA,
		MainlineRef: repo.MainlineRef, MainlineSHA: mainlineSHA,
		Effectif: effectif, BecomePassword: payload.BecomePassword,
	})
}

// pushGrantResponse porte de quoi pousser, et rien de plus.
//
// « De quoi pousser » inclut l'ADRESSE, et pas seulement le jeton et le préfixe.
// C'est le même raisonnement que l'ADR-0028 tient pour RefPrefix — la CLI ne le
// compose pas elle-même, il vient du serveur : dire avec QUEL jeton et sous QUEL
// préfixe on écrit sans dire OÙ laisserait la moitié de la phrase au client.
//
// Concrètement, ça évite qu'une adresse d'infrastructure soit recopiée dans le
// fichier versionné de chaque projet — où elle devrait être réécrite partout le
// jour où le rôle `git` déménage.
type pushGrantResponse struct {
	Token     string `json:"token"`
	RefPrefix string `json:"ref_prefix"`
	ExpiresAt int64  `json:"expires_at"`

	// PushURL est vide quand le déploiement ne l'a pas configurée : la CLI se
	// rabat alors sur sa variable d'environnement, comme avant. C'est ce qui
	// rend le déploiement de ce champ progressif.
	PushURL string `json:"push_url,omitempty"`
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
		PushURL:   a.deps.GitPushURL,
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}

// autoriserLEnvironnement rend false et A DÉJÀ RÉPONDU quand l'accès est refusé.
//
// Extrait parce qu'il est appelé deux fois (voir HandleCreateRun) et que
// dupliquer la traduction d'un refus d'autorisation est la manière dont les deux
// copies finissent par diverger — l'une rendant 403 et l'autre 500 sur la même
// cause.
//
// Ce que ce contrôle achète exactement : la clé d'accès est délivrée PAR
// ENVIRONNEMENT (ADR-0020), donc sans droit il n'y a pas de credential. Ce qu'il
// n'achète pas : qu'un run légitime reste dans son périmètre — l'egress est
// ouvert, et le §10.2 pose que le périmètre est appliqué par le réseau ou ne
// l'est pas.
func (a *Server) autoriserLEnvironnement(w http.ResponseWriter, r *http.Request, user *core.User, env string) bool {
	err := core.VerifierLAcces(r.Context(), a.deps.Access, user.ID, env)
	if err == nil {
		return true
	}
	if errors.Is(err, core.ErrEnvironnementInterdit) {
		slog.Warn("run refusé : environnement interdit",
			slog.String("sujet", user.Subject), slog.String("environnement", env))
		// 403 et non 404 : contrairement à `/admin`, l'existence d'un
		// environnement n'est pas un secret — l'utilisateur en a lu le nom dans
		// son propre inventaire. Le taire l'enverrait chercher une faute de
		// frappe au lieu de demander un accès.
		http.Error(w, "you have no access to environment "+env, http.StatusForbidden)
		return false
	}
	slog.Error("vérification des droits en échec", slog.String("error", err.Error()))
	http.Error(w, "could not verify your access", http.StatusInternalServerError)
	return false
}
