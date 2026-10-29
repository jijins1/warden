package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// resoudreEffectif lit la configuration du COMMIT et en compose l'effectif.
//
// Rend `ok == false` et A DÉJÀ RÉPONDU en cas de refus, comme
// autoriserLEnvironnement.
//
// Extraite parce que DEUX chemins y passent — la déclaration par la CLI
// (HandleCreateRun) et le lancement depuis le navigateur (HandleLaunchRun) — et
// que la faire deux fois est la manière dont les deux copies finissent par
// diverger, ici sur la partie qui décide CE QUI S'EXÉCUTE. L'ordre interne est
// celui d'origine de HandleCreateRun, inchangé : configuration du commit,
// composition, forme de l'environnement, autorisation de l'environnement
// EFFECTIF.
//
// `configSHA` est le commit dont on lit `.warden.yml`. C'est le commit
// d'audit sur le chemin CLI. Sur le chemin navigateur, c'est la principale — et
// c'est le même octet, par construction : le commit d'audit d'un run lancé
// depuis l'UI a EXACTEMENT l'arbre de sa base (ADR-0042). L'ADR-0009 est tenue,
// pas contournée.
func (a *Server) resoudreEffectif(
	w http.ResponseWriter, r *http.Request,
	user *core.User, repoName, configSHA, runRef string, req core.Requete,
) (core.Effectif, bool) {
	// La configuration est lue DANS L'ARBRE DU COMMIT (ADR-0009). Ce qui
	// s'applique et ce qui est audité deviennent le même octet, sans rien devoir
	// à la discipline du client.
	projet, err := core.ProjectConfigFor(r.Context(), a.deps.ProjectConfig, repoName, configSHA,
		a.deps.ExigenceConfig)
	if err != nil {
		// Le message de `config.Parse` nomme déjà la clé ou la construction
		// refusée : il est rendu à l'utilisateur, qui est le seul à pouvoir
		// corriger son fichier.
		slog.Warn("configuration de projet refusée",
			slog.String("run_ref", runRef), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return core.Effectif{}, false
	}

	effectif, err := core.Composer(projet, req)
	if err != nil {
		slog.Warn("composition de la configuration refusée",
			slog.String("run_ref", runRef), slog.String("error", err.Error()))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return core.Effectif{}, false
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
			return core.Effectif{}, false
		}
	}

	// Second temps de l'autorisation. Il ne mord que lorsque l'environnement
	// vient du fichier : sinon il porte sur la valeur déjà contrôlée par
	// l'appelant.
	if effectif.Environment != req.Environment &&
		!a.autoriserLEnvironnement(w, r, user, effectif.Environment) {
		return core.Effectif{}, false
	}

	return effectif, true
}

// runADemarrer porte tout ce qui est DÉCIDÉ au moment d'enregistrer un run.
//
// Une structure et non une longue liste de paramètres : la liste est longue
// parce que le run l'est, et une position qui glisse entre deux appels ne se
// voit pas à la relecture.
type runADemarrer struct {
	User        *core.User
	RepoName    string
	RunRef      string
	Playbook    string
	Kind        core.RunKind
	AdhocTarget *core.AdhocTarget

	AuditSHA, BaseSHA        string
	MainlineRef, MainlineSHA string

	Effectif           core.Effectif
	BecomePassword     string
	ConnectionPassword string
}

// enregistrerEtDemarrer consigne les secrets, crée le run, le démarre, et
// répond.
//
// Extraite pour la même raison que resoudreEffectif : deux chemins y passent.
// L'ORDRE est celui d'origine de HandleCreateRun et il porte deux garanties
// qu'il faut relire avant d'y toucher :
//
//   - les consignes AVANT la création : si un secret n'arrive pas, RIEN n'est
//     créé. L'inverse laisserait un run qui démarre sans pouvoir s'élever, et
//     échouerait tâche par tâche sur la production ;
//   - le démarrage APRÈS l'enregistrement : le run naît `interrupted`/`unknown`
//     (§6.4), donc un échec du lancement laisse une ligne qui dit la vérité. Un
//     lancement d'abord laisserait un pod que rien ne référence.
func (a *Server) enregistrerEtDemarrer(w http.ResponseWriter, r *http.Request, in runADemarrer) {
	effectif := in.Effectif

	// Les consignes AVANT la création, et pour la même raison que sur un
	// enrôlement : si un secret n'arrive pas à destination, rien n'a été créé.
	if in.BecomePassword != "" {
		if a.deps.Stash == nil {
			http.Error(w, "become password is not supported on this deployment",
				http.StatusNotImplemented)
			return
		}
		if err := a.deps.Stash.Stash(r.Context(), core.CleBecome(in.RunRef),
			[]byte(in.BecomePassword)); err != nil {
			slog.Error("consigne du mot de passe d'élévation en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
	}

	// Le mot de passe de connexion (ADR-0048), même chemin et même raison.
	if in.ConnectionPassword != "" {
		if a.deps.Stash == nil {
			http.Error(w, "connection password is not supported on this deployment",
				http.StatusNotImplemented)
			return
		}
		if err := a.deps.Stash.Stash(r.Context(), core.CleConnexion(in.RunRef),
			[]byte(in.ConnectionPassword)); err != nil {
			slog.Error("consigne du mot de passe de connexion en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
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
				slog.String("run_ref", in.RunRef))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
		if err := a.deps.Stash.Stash(r.Context(), core.CleForwardEnv(in.RunRef), brut); err != nil {
			slog.Error("consigne des variables transmises en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			http.Error(w, "could not prepare the run", http.StatusInternalServerError)
			return
		}
		// LE RÉSULTAT EFFECTIF est journalisé (§5.4.2), et seulement les noms.
		slog.Info("variables transmises au run",
			slog.String("run_ref", in.RunRef),
			slog.Any("noms", core.NomsTransmis(effectif.ForwardEnv)))
	}

	id, err := a.deps.Creator.CreateRun(r.Context(), core.NewRun{
		RunRef: in.RunRef,
		UserID: in.User.ID,
		Repo:   in.RepoName,
		// Les valeurs EFFECTIVES, et non ce que le client a demandé (§5.4.2).
		// C'est cette ligne qui rend l'audit fidèle : `run.inventory` porte le
		// chemin résolu, tandis que le commit garde le template. Les deux
		// questions — « quelle convention » et « qu'a-t-elle donné ce jour-là » —
		// se lisent alors séparément.
		Environment: effectif.Environment,
		Inventory:   effectif.Inventory,
		Playbook:    in.Playbook,
		Kind:        in.Kind,
		AdhocTarget: in.AdhocTarget,
		// Les flags EFFECTIFS, comme les deux au-dessus (§5.4.2). Ils coïncident
		// avec les demandés — la composition refuse au lieu d'amputer — et passer
		// par elle est ce qui garde cette ligne vraie le jour où ce ne serait
		// plus le cas.
		Flags:       effectif.Flags,
		AuditSHA:    in.AuditSHA,
		BaseSHA:     in.BaseSHA,
		MainlineRef: in.MainlineRef,
		MainlineSHA: in.MainlineSHA,
		// L'image vient du COMMIT, et de lui seul (ADR-0014).
		RunnerImage: effectif.RunnerImage,
	})
	if err != nil {
		slog.Error("création du run en échec",
			slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
		http.Error(w, "could not create the run", http.StatusInternalServerError)
		return
	}

	slog.Info("run créé",
		slog.String("run_ref", in.RunRef), slog.Int64("id", id),
		slog.String("repo", in.RepoName), slog.Int64("user", in.User.ID))

	// Le démarrage vient APRÈS l'enregistrement, et l'ordre porte une garantie :
	// le run naît `interrupted` / `unknown` (§6.4), donc un échec ici laisse une
	// ligne qui dit la vérité — le run existe, il n'a pas tourné. L'inverse —
	// lancer puis enregistrer — laisserait un pod en vie que rien ne référence.
	pod := ""
	if a.deps.Starter != nil {
		pod, err = a.deps.Starter.Start(r.Context(), core.Run{
			ID: id, RunRef: in.RunRef, UserID: in.User.ID, Repo: in.RepoName,
			Environment: effectif.Environment, Inventory: effectif.Inventory,
			Playbook: in.Playbook, AuditSHA: in.AuditSHA,
			Kind: in.Kind, AdhocTarget: in.AdhocTarget,
		})
		if err != nil {
			slog.Error("démarrage du run en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			// Le run est ENREGISTRÉ, et son état dit déjà qu'il n'a pas tourné.
			// Le message nomme les deux faits et l'identifiant : sans lui,
			// l'utilisateur croit que rien ne s'est passé et cherche un artefact
			// qu'il ne sait pas nommer.
			http.Error(w, "run "+in.RunRef+" is recorded but could not be started",
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
					slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(createRunResponse{
		RunRef:      in.RunRef,
		AuditSHA:    in.AuditSHA,
		MainlineRef: in.MainlineRef,
		MainlineSHA: in.MainlineSHA,
		Pod:         pod,
	}); err != nil {
		slog.Error("réponse non écrite", slog.String("error", err.Error()))
	}
}
