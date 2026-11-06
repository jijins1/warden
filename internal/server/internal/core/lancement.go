package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jijins1/warden/internal/runref"
)

// CauseDeRefus classe un refus de lancement.
//
// Une classe et non un statut HTTP : le cœur ne sait pas qui l'appelle. Le
// handler HTTP en fait un code, le planificateur une ligne de `last_error`
// (ADR-0057).
type CauseDeRefus int

const (
	// RefusForme : la demande est mal formée ou refusée par la configuration du
	// commit. Seul le demandeur peut la corriger.
	RefusForme CauseDeRefus = iota + 1
	// RefusInterdit : droit absent, compte désactivé, ou environnement gelé.
	RefusInterdit
	// RefusIntrouvable : le dépôt n'a pas de configuration serveur.
	RefusIntrouvable
	// RefusConflit : l'identifiant de run est déjà enregistré.
	RefusConflit
	// RefusIndisponible : ce déploiement n'a pas monté le port nécessaire.
	RefusIndisponible
	// RefusAmont : la forge ou le rôle `git` n'a pas répondu.
	RefusAmont
	// RefusInterne : une panne du control plane lui-même.
	RefusInterne
)

// RefusDeLancement est l'erreur rendue par le Lanceur.
//
// `Message` est destiné au DEMANDEUR : il ne cite jamais de secret, et il est
// rendu tel quel. `Err` porte la cause technique quand il y en a une, pour les
// journaux.
type RefusDeLancement struct {
	Cause   CauseDeRefus
	Message string
	Err     error
}

func (r *RefusDeLancement) Error() string {
	if r.Err != nil {
		return r.Message + " : " + r.Err.Error()
	}
	return r.Message
}

func (r *RefusDeLancement) Unwrap() error { return r.Err }

func refus(cause CauseDeRefus, message string, err error) *RefusDeLancement {
	return &RefusDeLancement{Cause: cause, Message: message, Err: err}
}

// LecteurDeDepot lit la configuration serveur d'un dépôt. Sous-ensemble de
// RepoStore : lancer un run n'a pas à pouvoir écrire cette configuration.
type LecteurDeDepot interface {
	GetRepo(ctx context.Context, name string) (Repo, error)
}

// Lanceur porte la séquence de création d'un run, commune à TOUS les chemins
// qui en créent un : la déclaration par la CLI (`POST /runs`), le lancement
// depuis le navigateur (`POST /repos/{repo}/runs`, ADR-0042) et le
// planificateur (ADR-0057).
//
// Elle vivait dans l'adaptateur HTTP et écrivait ses refus dans la réponse ; le
// planificateur, qui n'a pas de réponse à écrire, ne pouvait pas l'emprunter.
// La recopier aurait été la manière dont trois chemins finissent par décider
// différemment de CE QUI S'EXÉCUTE. L'ordre interne est celui d'origine,
// inchangé.
//
// Une valeur, sans état : tous les champs sont des ports, et une valeur nulle
// d'un port facultatif garde le sens qu'elle a dans `api.Deps`.
type Lanceur struct {
	Repos          LecteurDeDepot
	Mirrors        MirrorRefresher
	Audits         AuditRecorder
	ProjectConfig  ProjectConfigReader
	ExigenceConfig ExigenceConfig
	Access         EnvironmentGrants
	Protected      ProtectedEnvironments
	Stash          SecretStash
	Creator        RunCreator
	Starter        RunLauncher
	Running        RunStateWriter
}

// AutoriserLEnvironnement refuse un environnement interdit à l'utilisateur, ou
// gelé.
//
// Les droits d'abord (ADR-0033), le gel ensuite (ADR-0044) : les deux sont des
// raisons de refuser un run, et le demandeur n'a pas à les distinguer pour
// corriger sa demande — le message le fait.
func (l Lanceur) AutoriserLEnvironnement(ctx context.Context, user *User, env string) error {
	if err := VerifierLAcces(ctx, l.Access, user.ID, env); err != nil {
		if errors.Is(err, ErrEnvironnementInterdit) {
			slog.Warn("run refusé : environnement interdit",
				slog.String("sujet", user.Subject), slog.String("environnement", env))
			// Interdit et non introuvable : l'existence d'un environnement n'est
			// pas un secret — l'utilisateur en a lu le nom dans son propre
			// inventaire. Le taire l'enverrait chercher une faute de frappe au
			// lieu de demander un accès.
			return refus(RefusInterdit, "you have no access to environment "+env, err)
		}
		slog.Error("vérification des droits en échec", slog.String("error", err.Error()))
		return refus(RefusInterne, "could not verify your access", err)
	}

	if err := VerifierRunAutorise(ctx, l.Protected, env); err != nil {
		if errors.Is(err, ErrRunBloque) {
			slog.Warn("run refusé : environnement gelé",
				slog.String("sujet", user.Subject), slog.String("environnement", env))
			return refus(RefusInterdit, "runs are currently frozen on environment "+env, err)
		}
		// Une panne de LECTURE du gel ne bloque PAS, contrairement à une panne de
		// lecture des droits — voir VerifierRunAutorise. Elle est journalisée,
		// pour que l'exploitant sache que le gel n'a pas pu être vérifié.
		slog.Warn("vérification du gel en échec, le run est autorisé par défaut",
			slog.String("environnement", env), slog.String("error", err.Error()))
	}
	return nil
}

// ResoudreEffectif lit la configuration du COMMIT et en compose l'effectif.
//
// L'ordre est celui d'origine : configuration du commit, composition, forme de
// l'environnement, autorisation de l'environnement EFFECTIF.
//
// `configSHA` est le commit dont on lit `.warden.yml` : le commit d'audit sur
// le chemin CLI, la principale sur les deux autres — et c'est le même octet,
// par construction (ADR-0042). L'ADR-0009 est tenue, pas contournée.
func (l Lanceur) ResoudreEffectif(
	ctx context.Context, user *User, repoName, configSHA, runRef string, req Requete,
) (Effectif, error) {
	projet, err := ProjectConfigFor(ctx, l.ProjectConfig, repoName, configSHA, l.ExigenceConfig)
	if err != nil {
		// Le message de `config.Parse` nomme déjà la clé ou la construction
		// refusée : il est rendu au demandeur, seul à pouvoir corriger son fichier.
		slog.Warn("configuration de projet refusée",
			slog.String("run_ref", runRef), slog.String("error", err.Error()))
		return Effectif{}, refus(RefusForme, err.Error(), nil)
	}

	effectif, err := Composer(projet, req)
	if err != nil {
		slog.Warn("composition de la configuration refusée",
			slog.String("run_ref", runRef), slog.String("error", err.Error()))
		return Effectif{}, refus(RefusForme, err.Error(), nil)
	}

	// La FORME de l'environnement effectif, et pas seulement son autorisation :
	// il compose un CHEMIN D'INVENTAIRE. Vide reste légitime — un playbook en
	// `connection: local` n'a pas d'environnement.
	if effectif.Environment != "" {
		if err := ValidateEnvironment(effectif.Environment); err != nil {
			return Effectif{}, refus(RefusForme, err.Error(), nil)
		}
	}

	// Second temps de l'autorisation. Il ne mord que lorsque l'environnement
	// vient du fichier : sinon il porte sur la valeur déjà contrôlée par
	// l'appelant.
	if effectif.Environment != req.Environment {
		if err := l.AutoriserLEnvironnement(ctx, user, effectif.Environment); err != nil {
			return Effectif{}, err
		}
	}

	return effectif, nil
}

// RunADemarrer porte tout ce qui est DÉCIDÉ au moment d'enregistrer un run.
//
// Une structure et non une longue liste de paramètres : la liste est longue
// parce que le run l'est, et une position qui glisse entre deux appels ne se
// voit pas à la relecture.
type RunADemarrer struct {
	User        *User
	RepoName    string
	RunRef      string
	Playbook    string
	Kind        RunKind
	AdhocTarget *AdhocTarget

	AuditSHA, BaseSHA        string
	MainlineRef, MainlineSHA string

	Effectif           Effectif
	BecomePassword     string
	ConnectionPassword string

	// ScheduleID désigne le cron qui a déclenché le run, nil pour un run lancé
	// par une personne (ADR-0057).
	ScheduleID *int64
}

// Demarre est ce qu'un lancement réussi rend à son appelant.
type Demarre struct {
	ID          int64
	RunRef      string
	AuditSHA    string
	MainlineRef string
	MainlineSHA string
	// Pod est vide quand ce déploiement enregistre sans démarrer.
	Pod string
}

// EnregistrerEtDemarrer consigne les secrets, crée le run et le démarre.
//
// L'ORDRE porte deux garanties qu'il faut relire avant d'y toucher :
//
//   - les consignes AVANT la création : si un secret n'arrive pas, RIEN n'est
//     créé. L'inverse laisserait un run qui démarre sans pouvoir s'élever, et
//     échouerait tâche par tâche sur la production ;
//   - le démarrage APRÈS l'enregistrement : le run naît `interrupted`/`unknown`
//     (§6.4), donc un échec du lancement laisse une ligne qui dit la vérité. Un
//     lancement d'abord laisserait un pod que rien ne référence.
//
// Un échec du DÉMARRAGE rend à la fois un Demarre (le run existe) et un refus
// RefusAmont : l'appelant doit pouvoir nommer le run qui n'a pas tourné.
func (l Lanceur) EnregistrerEtDemarrer(ctx context.Context, in RunADemarrer) (Demarre, error) {
	effectif := in.Effectif

	if in.BecomePassword != "" {
		if l.Stash == nil {
			return Demarre{}, refus(RefusIndisponible, "become password is not supported on this deployment", nil)
		}
		if err := l.Stash.Stash(ctx, CleBecome(in.RunRef), []byte(in.BecomePassword)); err != nil {
			slog.Error("consigne du mot de passe d'élévation en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			return Demarre{}, refus(RefusInterne, "could not prepare the run", err)
		}
	}

	// Le mot de passe de connexion (ADR-0048), même chemin et même raison.
	if in.ConnectionPassword != "" {
		if l.Stash == nil {
			return Demarre{}, refus(RefusIndisponible, "connection password is not supported on this deployment", nil)
		}
		if err := l.Stash.Stash(ctx, CleConnexion(in.RunRef), []byte(in.ConnectionPassword)); err != nil {
			slog.Error("consigne du mot de passe de connexion en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			return Demarre{}, refus(RefusInterne, "could not prepare the run", err)
		}
	}

	// Les VALEURS des variables suivent le même chemin que le mot de passe :
	// consigne éphémère, consommée à la livraison de l'ordre, jamais écrites en
	// base (§5.4). Le journal ne porte que les noms.
	if len(effectif.ForwardEnv) > 0 {
		if l.Stash == nil {
			return Demarre{}, refus(RefusIndisponible,
				"forwarded environment variables are not supported on this deployment", nil)
		}
		brut, err := json.Marshal(effectif.ForwardEnv)
		if err != nil {
			// Le message ne cite JAMAIS les valeurs.
			slog.Error("encodage des variables transmises en échec", slog.String("run_ref", in.RunRef))
			return Demarre{}, refus(RefusInterne, "could not prepare the run", nil)
		}
		if err := l.Stash.Stash(ctx, CleForwardEnv(in.RunRef), brut); err != nil {
			slog.Error("consigne des variables transmises en échec",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
			return Demarre{}, refus(RefusInterne, "could not prepare the run", err)
		}
		slog.Info("variables transmises au run",
			slog.String("run_ref", in.RunRef), slog.Any("noms", NomsTransmis(effectif.ForwardEnv)))
	}

	if l.Creator == nil {
		return Demarre{}, refus(RefusIndisponible, "run creation is not available", nil)
	}
	id, err := l.Creator.CreateRun(ctx, NewRun{
		RunRef: in.RunRef,
		UserID: in.User.ID,
		Repo:   in.RepoName,
		// Les valeurs EFFECTIVES, et non ce que le client a demandé (§5.4.2).
		Environment: effectif.Environment,
		Inventory:   effectif.Inventory,
		Playbook:    in.Playbook,
		Kind:        in.Kind,
		AdhocTarget: in.AdhocTarget,
		Flags:       effectif.Flags,
		AuditSHA:    in.AuditSHA,
		BaseSHA:     in.BaseSHA,
		MainlineRef: in.MainlineRef,
		MainlineSHA: in.MainlineSHA,
		// L'image vient du COMMIT, et de lui seul (ADR-0014).
		RunnerImage: effectif.RunnerImage,
		ScheduleID:  in.ScheduleID,
	})
	if err != nil {
		slog.Error("création du run en échec",
			slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
		return Demarre{}, refus(RefusInterne, "could not create the run", err)
	}

	slog.Info("run créé",
		slog.String("run_ref", in.RunRef), slog.Int64("id", id),
		slog.String("repo", in.RepoName), slog.Int64("user", in.User.ID))

	demarre := Demarre{
		ID: id, RunRef: in.RunRef, AuditSHA: in.AuditSHA,
		MainlineRef: in.MainlineRef, MainlineSHA: in.MainlineSHA,
	}
	if l.Starter == nil {
		return demarre, nil
	}

	pod, err := l.Starter.Start(ctx, Run{
		ID: id, RunRef: in.RunRef, UserID: in.User.ID, Repo: in.RepoName,
		Environment: effectif.Environment, Inventory: effectif.Inventory,
		Playbook: in.Playbook, AuditSHA: in.AuditSHA,
		Kind: in.Kind, AdhocTarget: in.AdhocTarget,
	})
	if err != nil {
		slog.Error("démarrage du run en échec",
			slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
		// Le run est ENREGISTRÉ, et son état dit déjà qu'il n'a pas tourné. Le
		// message nomme les deux faits et l'identifiant.
		return demarre, refus(RefusAmont, "run "+in.RunRef+" is recorded but could not be started", err)
	}
	demarre.Pod = pod

	// Un échec ici ne remet PAS le run en cause : le pod tourne, il exécutera
	// et se clôra lui-même.
	if l.Running != nil {
		if err := l.Running.MarkRunning(ctx, id, pod); err != nil {
			slog.Error("run non marqué courant",
				slog.String("run_ref", in.RunRef), slog.String("error", err.Error()))
		}
	}
	return demarre, nil
}

// LancementSurLaPrincipale est une demande de run sur la branche protégée telle
// qu'elle est : rien à capturer, rien à pousser (ADR-0042).
type LancementSurLaPrincipale struct {
	Repo        string
	Environment string
	Inventory   string
	Playbook    string
	// Flags déjà décodés ; leur forme est revalidée ici.
	Flags []string

	BecomePassword     string
	ConnectionPassword string
	ForwardEnv         map[string]string

	ScheduleID *int64
}

// LancerSurLaPrincipale lance un run sur la principale, sans qu'aucun client
// ne pousse. Chemin du navigateur (ADR-0042) et du planificateur (ADR-0057).
//
// L'ORDRE porte la sécurité :
//
//  1. le compte n'est pas désactivé — le planificateur n'a pas de middleware
//     d'authentification pour le vérifier à sa place ;
//  2. l'environnement est NOMMÉ et autorisé AVANT que le rôle `git` ne
//     travaille : un compte sans droit ne le fait jamais travailler ;
//  3. la principale est rafraîchie, et son SHA est la BASE de la ref d'audit.
//     Aucun SHA ne vient du demandeur ;
//  4. la ref d'audit est pointée sur ce SHA : même commit, aucun nouvel objet ;
//  5. la suite est celle de `POST /runs`, aux mêmes fonctions.
//
// Pour tout run lancé par ce chemin, `audit_sha == base_sha == mainline_sha`.
func (l Lanceur) LancerSurLaPrincipale(ctx context.Context, user *User, d LancementSurLaPrincipale) (Demarre, error) {
	if user == nil || user.Disabled {
		return Demarre{}, refus(RefusInterdit, "this account is disabled", nil)
	}
	if l.Repos == nil || l.Mirrors == nil || l.Audits == nil || l.Creator == nil {
		return Demarre{}, refus(RefusIndisponible, "launching a run from the UI is not available", nil)
	}
	if d.Repo == "" {
		return Demarre{}, refus(RefusForme, "repo is required", nil)
	}
	if d.Environment == "" {
		return Demarre{}, refus(RefusForme, "environment is required to launch a run from the UI", nil)
	}
	if d.Playbook == "" {
		return Demarre{}, refus(RefusForme, "playbook is required", nil)
	}
	// La FORME avant l'autorisation : un nom tordu doit produire une erreur de
	// forme, pas un refus de droit sur un nom simplement mal écrit.
	if err := ValidateEnvironment(d.Environment); err != nil {
		return Demarre{}, refus(RefusForme, err.Error(), nil)
	}
	if len(d.Flags) > 0 {
		if err := ValiderFlags(d.Flags); err != nil {
			return Demarre{}, refus(RefusForme, err.Error(), nil)
		}
	}

	if err := l.AutoriserLEnvironnement(ctx, user, d.Environment); err != nil {
		return Demarre{}, err
	}

	repo, err := l.Repos.GetRepo(ctx, d.Repo)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Demarre{}, refus(RefusIntrouvable, "this repository has no server-side configuration yet", nil)
		}
		slog.Error("lecture de la configuration de dépôt en échec",
			slog.String("repo", d.Repo), slog.String("error", err.Error()))
		return Demarre{}, refus(RefusInterne, "could not read repository configuration", err)
	}

	runRef, err := runref.New()
	if err != nil {
		slog.Error("tirage d'un identifiant de run en échec", slog.String("error", err.Error()))
		return Demarre{}, refus(RefusInterne, "could not launch the run", err)
	}

	// Le miroir est rafraîchi ICI, après l'autorisation (ADR-0006).
	mainlineSHA, err := l.Mirrors.RefreshMirror(ctx, repo.Name, repo.RemoteURL, repo.MainlineRef)
	if err != nil {
		slog.Error("rafraîchissement du miroir en échec",
			slog.String("repo", repo.Name), slog.String("error", err.Error()))
		return Demarre{}, refus(RefusAmont, "the protected branch could not be refreshed from the forge", err)
	}

	effectif, err := l.ResoudreEffectif(ctx, user, repo.Name, mainlineSHA, runRef, Requete{
		Playbook:    d.Playbook,
		Environment: d.Environment,
		Inventory:   d.Inventory,
		ForwardEnv:  d.ForwardEnv,
		Flags:       d.Flags,
	})
	if err != nil {
		return Demarre{}, err
	}

	// LE seul effet d'écriture avant l'enregistrement : une SECONDE ref sur un
	// commit qui existe déjà, jamais un nouveau commit (ADR-0042).
	auditRef := AuditRefFor(user.ID, runRef)
	if err := l.Audits.RecordAuditRef(ctx, AuditRef{
		Repo: repo.Name, Ref: auditRef, CommitSHA: mainlineSHA,
	}); err != nil {
		if errors.Is(err, ErrAuditRefConflict) {
			// Ne peut arriver qu'avec une collision d'identifiant de run (80 bits
			// d'aléa) — mais si elle se produit, c'est un conflit, pas une panne.
			return Demarre{}, refus(RefusConflit, "this run identifier is already recorded", err)
		}
		slog.Error("frappe de la ref d'audit en échec",
			slog.String("repo", repo.Name), slog.String("ref", auditRef), slog.String("error", err.Error()))
		return Demarre{}, refus(RefusAmont, "the audit reference could not be recorded", err)
	}

	return l.EnregistrerEtDemarrer(ctx, RunADemarrer{
		User: user, RepoName: repo.Name, RunRef: runRef,
		Playbook: d.Playbook, Kind: RunPlaybook,
		AuditSHA: mainlineSHA, BaseSHA: mainlineSHA,
		MainlineRef: repo.MainlineRef, MainlineSHA: mainlineSHA,
		Effectif: effectif, BecomePassword: d.BecomePassword,
		ConnectionPassword: d.ConnectionPassword,
		ScheduleID:         d.ScheduleID,
	})
}

// MessageDeRefus rend le message destiné au demandeur, quelle que soit l'erreur.
func MessageDeRefus(err error) string {
	var r *RefusDeLancement
	if errors.As(err, &r) {
		return r.Message
	}
	return "could not launch the run"
}
