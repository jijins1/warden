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
	Playbook    string `json:"playbook"`
	BaseSHA     string `json:"base_sha"`

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

	// La configuration est lue DANS L'ARBRE DU COMMIT qu'on vient de résoudre
	// (ADR-0009). Ce qui s'applique et ce qui est audité deviennent le même
	// octet, sans rien devoir à la discipline du client.
	projet, err := core.ProjectConfigFor(r.Context(), a.deps.ProjectConfig, repo.Name, auditSHA,
		a.deps.ExigenceConfig)
	if err != nil {
		// Le message de `config.Parse` nomme déjà la clé ou la construction
		// refusée : il est rendu à l'utilisateur, qui est le seul à pouvoir
		// corriger son fichier.
		slog.Warn("configuration de projet refusée",
			slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	effectif, err := core.Composer(projet, core.Requete{
		Environment: payload.Environment,
		Inventory:   payload.Inventory,
		ForwardEnv:  payload.ForwardEnv,
		Flags:       flags,
	})
	if err != nil {
		slog.Warn("composition de la configuration refusée",
			slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// La FORME de l'environnement est vérifiée ici, et pas seulement son
	// autorisation. Elle ne l'était nulle part sur ce chemin : seuls le magasin
	// de clés, les droits et l'enrôlement l'appelaient. Ce qui fermait la porte
	// jusqu'ici était INDIRECT — un nom tordu n'a pas de droit associé, donc le
	// run est refusé — et s'appuyer sur une jointure en base pour garantir la
	// forme d'une valeur qui compose un CHEMIN D'INVENTAIRE serait fragile.
	//
	// Vide reste légitime : un playbook en `connection: local` n'a pas
	// d'environnement, et `VerifierLAcces` le laisse passer depuis toujours.
	if effectif.Environment != "" {
		if err := core.ValidateEnvironment(effectif.Environment); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Second temps de l'autorisation. Il ne mord que lorsque l'environnement
	// vient du fichier : sinon il porte sur la valeur déjà contrôlée plus haut.
	if effectif.Environment != payload.Environment &&
		!a.autoriserLEnvironnement(w, r, user, effectif.Environment) {
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

	// Les consignes AVANT la création, et pour la même raison que sur un
	// enrôlement : si un secret n'arrive pas à destination, rien n'a été créé.
	// L'inverse laisserait un run qui démarre sans pouvoir s'élever, et
	// échouerait tâche par tâche sur la production.
	if payload.BecomePassword != "" {
		if a.deps.Stash == nil {
			http.Error(w, "become password is not supported on this deployment",
				http.StatusNotImplemented)
			return
		}
		if err := a.deps.Stash.Stash(r.Context(), core.CleBecome(payload.RunRef),
			[]byte(payload.BecomePassword)); err != nil {
			slog.Error("consigne du mot de passe d'élévation en échec",
				slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
	}

	// Les VALEURS des variables suivent le même chemin que le mot de passe :
	// consigne éphémère, consommée à la livraison de l'ordre, jamais écrites en
	// base. C'est ce qui tient la promesse du §5.4 — les noms sont auditables,
	// les valeurs ne le sont pas, et le journal ci-dessous ne porte que les noms.
	if len(effectif.ForwardEnv) > 0 {
		if a.deps.Stash == nil {
			http.Error(w, "forwarded environment variables are not supported on this deployment",
				http.StatusNotImplemented)
			return
		}
		brut, err := json.Marshal(effectif.ForwardEnv)
		if err != nil {
			// Le message ne cite JAMAIS les valeurs.
			slog.Error("encodage des variables transmises en échec",
				slog.String("run_ref", payload.RunRef))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
		if err := a.deps.Stash.Stash(r.Context(), core.CleForwardEnv(payload.RunRef), brut); err != nil {
			slog.Error("consigne des variables transmises en échec",
				slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
		// LE RÉSULTAT EFFECTIF est journalisé (§5.4.2), et seulement les noms.
		slog.Info("variables transmises au run",
			slog.String("run_ref", payload.RunRef),
			slog.Any("noms", core.NomsTransmis(effectif.ForwardEnv)))
	}

	id, err := a.deps.Creator.CreateRun(r.Context(), core.NewRun{
		RunRef: payload.RunRef,
		UserID: user.ID,
		Repo:   repo.Name,
		// Les valeurs EFFECTIVES, et non ce que le client a demandé (§5.4.2).
		// C'est cette ligne qui rend l'audit fidèle : `run.inventory` porte le
		// chemin résolu, tandis que le commit garde le template. Les deux
		// questions — « quelle convention » et « qu'a-t-elle donné ce jour-là » —
		// se lisent alors séparément.
		Environment: effectif.Environment,
		Inventory:   effectif.Inventory,
		Playbook:    payload.Playbook,
		// Les flags EFFECTIFS, comme les deux au-dessus (§5.4.2). Ils coïncident
		// avec les demandés — la composition refuse au lieu d'amputer — et passer
		// par elle est ce qui garde cette ligne vraie le jour où ce ne serait
		// plus le cas.
		Flags:       effectif.Flags,
		AuditSHA:    auditSHA,
		BaseSHA:     payload.BaseSHA,
		MainlineRef: repo.MainlineRef,
		MainlineSHA: mainlineSHA,
		// L'image vient du COMMIT, et de lui seul (ADR-0014). Elle ne transite
		// plus par la requête : `runner_image` y était accepté sans qu'aucun
		// client ne l'envoie, ce qui contredisait l'ADR-0009 en laissant une
		// porte ouverte que personne n'empruntait.
		RunnerImage: effectif.RunnerImage,
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
			Environment: effectif.Environment, Inventory: effectif.Inventory,
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

		// Le pod existe : le run passe `running`, avec le nom du pod pour la
		// corrélation avec les logs d'infrastructure (§6.4). Un échec ici ne
		// remet PAS le run en cause — le pod tourne, il exécutera et se clôra
		// lui-même. Faire échouer la réponse laisserait l'utilisateur croire
		// que rien ne s'est passé alors que son playbook est parti.
		if a.deps.Running != nil {
			if err := a.deps.Running.MarkRunning(r.Context(), id, pod); err != nil {
				slog.Error("run non marqué courant",
					slog.String("run_ref", payload.RunRef), slog.String("error", err.Error()))
			}
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
