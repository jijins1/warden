package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	wire "github.com/jijins1/warden/internal/wire/dispatch"
)

// RunByIDReader lit un run par son identifiant technique.
//
// Séparé de RunReader, qui ne parle qu'en identifiants publics. Celui-ci n'a
// qu'un appelant — la construction de l'ordre, qui tient un `run_id` parce que
// la table des pods le porte — et l'isoler garde `RunReader` hors de portée d'un
// identifiant devinable.
type RunByIDReader interface {
	RunByID(ctx context.Context, id int64) (Run, error)
}

// OrderBuilder compose l'ordre de run remis au pod.
//
// Il vit dans le cœur parce que ce qu'un runner reçoit est une décision de
// POLITIQUE et non de transport : quel credential, valable combien de temps, sur
// quel périmètre. L'ADR-0018 fait d'ailleurs construire l'ordre AU MOMENT DE LA
// LIVRAISON et non au claim, ce qui n'est pas un détail — c'est ce qui fait
// commencer la vie du jeton quand le pod le reçoit, et non quand le contrôleur
// décide qu'il le recevra.
type OrderBuilder struct {
	runs   RunByIDReader
	grants FetchGrantIssuer
	keys   KeyIssuer
	vault  VaultIssuer

	// droits porte l'autorisation par environnement (§10.2). Nil interdit TOUT :
	// `VerifierLAcces` refuse quand le magasin manque, parce qu'un contrôle qui
	// s'efface avec sa dépendance n'est pas un contrôle.
	droits  EnvironmentGrants
	gitBase string
	fetchTL time.Duration

	// Les trois suivants ne servent qu'aux runs d'enrôlement. Facultatifs
	// ensemble : sans eux, un enrôlement ne peut pas être composé et la
	// livraison échoue en le disant — ce qui vaut mieux qu'un ordre incomplet.
	hosts    HostCatalog
	cles     KeyCatalog
	consigne SecretStash
}

// AvecEnrolement branche de quoi composer un ordre d'enrôlement.
//
// Une option et non trois paramètres de plus : `NewOrderBuilder` en a déjà
// quatre, et l'enrôlement est un chemin qu'un déploiement peut parfaitement ne
// pas avoir.
//
// `cles` est le CATALOGUE et non l'émetteur : ce qu'un enrôlement pose est la
// partie PUBLIQUE, et demander sa partie privée pour n'en lire que la moitié
// ferait passer une opération d'administration par le magasin de secrets.
// AvecDroits branche l'autorisation par environnement (§10.2).
//
// Une option et non un paramètre du constructeur, comme `AvecEnrolement` : les
// appelants existants continuent de compiler. Sans elle, `cleDAcces` refuse de
// délivrer la moindre clé — c'est le défaut voulu.
func (b *OrderBuilder) AvecDroits(droits EnvironmentGrants) *OrderBuilder {
	b.droits = droits
	return b
}

func (b *OrderBuilder) AvecEnrolement(hosts HostCatalog, cles KeyCatalog, consigne SecretStash) *OrderBuilder {
	b.hosts, b.cles, b.consigne = hosts, cles, consigne
	return b
}

// AvecVault branche l'émetteur de clés Ansible Vault (ADR-0045).
//
// Une option et non un paramètre du constructeur, comme AvecEnrolement : les
// appelants existants continuent de compiler. FACULTATIF au même sens que
// `keys` — un déploiement sans magasin de vault compose des ordres sans clé
// de vault, et c'est un état légitime pour tout environnement dont aucun
// playbook n'utilise `!vault |`.
func (b *OrderBuilder) AvecVault(vault VaultIssuer) *OrderBuilder {
	b.vault = vault
	return b
}

// FetchGrantTTL borne la vie du jeton de lecture remis au runner.
//
// Beaucoup plus courte que celle du jeton de push, et pour une raison qui n'a
// rien de symétrique : l'ADR-0007 dit sans détour que ce jeton est lisible DANS
// le pod, donc par le playbook, donc par du code arbitraire (§2). Un jeton qui
// survit à son run est un jeton qui traîne dans un conteneur terminé.
//
// Elle doit couvrir un run entier, `activeDeadlineSeconds` compris, sans quoi un
// run long verrait son fetch refusé au milieu — panne bien plus difficile à
// diagnostiquer qu'un refus au démarrage.
const FetchGrantTTL = 4 * time.Hour

// NewOrderBuilder construit le composeur d'ordres.
// NewOrderBuilder prend la BASE de l'adresse du rôle `git`, pas une origine
// complète.
//
// La distinction n'est pas de forme : un dépôt bare par dépôt (ADR-0006), donc
// l'origine dépend du RUN. Une origine constante ferait cloner le même dépôt à
// tous les runs — et le premier symptôme serait un runner qui exécute le code
// d'un autre projet, ce que rien en aval ne rattraperait.
// `keys` est FACULTATIF, et l'absence est un état légitime : un playbook en
// `connection: local` n'a besoin d'aucune clé, et c'est le cas de tous les runs
// du produit jusqu'ici. Sans magasin de clés, l'ordre part sans clé et le run
// tourne — il échouera sur un `unreachable` si son inventaire vise de vraies
// machines, ce qui est un diagnostic honnête.
func NewOrderBuilder(runs RunByIDReader, grants FetchGrantIssuer, keys KeyIssuer, gitBase string) (*OrderBuilder, error) {
	if runs == nil || grants == nil {
		return nil, fmt.Errorf("construction des ordres : lecture des runs et frappe des jetons sont requises")
	}
	if gitBase == "" {
		// Sans elle, l'ordre partirait avec une origine vide et le runner
		// échouerait sur un message de git parlant d'URL malformée, très loin de
		// la cause.
		return nil, fmt.Errorf("construction des ordres : adresse du rôle git absente")
	}
	return &OrderBuilder{
		runs: runs, grants: grants, keys: keys,
		gitBase: strings.TrimRight(gitBase, "/"),
		fetchTL: FetchGrantTTL,
	}, nil
}

// OrderFor compose l'ordre du run attribué à un pod.
//
// Le `run_id` vient de la table des pods et JAMAIS d'une requête : c'est la
// condition de validité que le schéma initial pose explicitement, et l'ADR-0023
// la formule ainsi — le serveur résout `pod -> run` depuis sa propre base, et un
// `run_id` figurant dans un corps de requête est au mieux redondant, au pire une
// tentative.
func (b *OrderBuilder) OrderFor(ctx context.Context, runID int64) (wire.Order, error) {
	run, err := b.runs.RunByID(ctx, runID)
	if err != nil {
		return wire.Order{}, fmt.Errorf("lecture du run %d : %w", runID, err)
	}
	if run.Kind == RunEnrollment {
		// Le dispatcher demande « l'ordre de ce run » sans savoir de quel genre
		// il est. Rendre une erreur ici plutôt qu'un ordre vide : un ordre de
		// playbook composé pour un enrôlement ferait cloner un dépôt inexistant.
		return wire.Order{}, fmt.Errorf("run %s est un enrôlement, pas un playbook", run.RunRef)
	}

	// L'ordre ne part PAS sans commit à exécuter. Un runner qui recevrait un
	// audit_sha vide clonerait, ne trouverait rien, et signalerait une panne de
	// dépôt — alors que la cause serait un run déclaré sans push.
	if run.AuditSHA == "" {
		return wire.Order{}, fmt.Errorf("run %s sans commit d'audit : rien à exécuter", run.RunRef)
	}
	if run.Repo == "" {
		return wire.Order{}, fmt.Errorf("run %s sans dépôt : son artefact est introuvable", run.RunRef)
	}

	// Le jeton est frappé ICI, à la livraison. Sa portée est l'espace de noms du
	// run et rien d'autre : ce que le runner peut obtenir en abusant de son accès
	// est exactement ce qu'il allait recevoir (ADR-0007).
	jeton, err := b.grants.IssueFetchGrant(ctx, run.Repo,
		RunNamespaceFor(run.RunRef), time.Now().Add(b.fetchTL))
	if err != nil {
		return wire.Order{}, fmt.Errorf("frappe du jeton de lecture du run %s : %w", run.RunRef, err)
	}

	// La clé d'accès est émise ICI, à la livraison, pour la même raison que le
	// jeton git : c'est ce qui garantit qu'aucun credential n'est persisté, ni
	// en base ni dans le WAL par une notification (ADR-0018 amendée).
	cle, err := b.cleDAcces(ctx, run)
	if err != nil {
		return wire.Order{}, err
	}

	// La clé de vault est émise ICI, à la livraison, pour la même raison que la
	// clé d'accès et le jeton git : aucun credential n'est persisté, ni en
	// base ni dans le WAL (ADR-0045, miroir de l'ADR-0018 amendée).
	cleVault, err := b.cleDeVault(ctx, run)
	if err != nil {
		return wire.Order{}, err
	}

	// Le mot de passe d'élévation, s'il a été consigné. Lu à la LIVRAISON et
	// détruit par sa lecture, comme celui d'un enrôlement : les deux viennent
	// d'un humain et ne font que passer.
	var become wire.Secret
	if b.consigne != nil {
		if v, err := b.consigne.Take(ctx, CleBecome(run.RunRef)); err == nil {
			become = wire.Secret(v)
		} else if !errors.Is(err, ErrNoStash) {
			return wire.Order{}, fmt.Errorf("mot de passe d'élévation du run %s : %w", run.RunRef, err)
		}
	}

	// Les variables du shell, si elles ont été consignées. Même chemin et même
	// destin que le mot de passe d'élévation : lues à la LIVRAISON, détruites par
	// leur lecture, jamais écrites en base ni dans le WAL.
	forward, err := b.variablesTransmises(ctx, run.RunRef)
	if err != nil {
		return wire.Order{}, err
	}

	order := wire.Order{
		RunRef:      run.RunRef,
		AuditSHA:    run.AuditSHA,
		Environment: run.Environment,
		Inventory:   run.Inventory,
		Playbook:    run.Playbook,

		// Les flags du §5.5, relus de la ligne de run et déjà passés par
		// l'allowlist deux fois — à la création et à la relecture (ADR-0038).
		//
		// Cette ligne MANQUAIT, et son absence rendait faux tout ce qui la
		// suivait : `POST /runs` acceptait un `flags`, la colonne le gardait, et
		// l'ordre partait sans. Un `--check` demandé aurait été enregistré comme
		// tel puis exécuté pour de vrai — la pire des deux pannes possibles.
		Flags: run.Flags,

		// L'origine est composée à partir du dépôt DU RUN. Le nom a déjà traversé
		// la validation de `depots.dir` côté `git` et la contrainte de la base ;
		// il ne peut pas contenir de barre ni de `..`.
		GitURL:   b.gitBase + "/" + run.Repo + ".git",
		GitToken: wire.Secret(jeton),

		// La clé d'accès de l'ADR-0020, vide quand l'environnement n'en a pas.
		//
		// Ce qu'elle N'EST PAS, et il faut le garder en tête : une autorisation.
		// L'ADR-0020 pose qu'en v1 l'autorisation repose sur la NetworkPolicy
		// egress, et celle-ci est aujourd'hui ouverte. Cette clé ouvre donc
		// tout ce qui l'accepte.
		SSHKey:         cle,
		BecomePassword: become,

		// La clé de vault de l'ADR-0045, vide quand l'environnement n'en a
		// pas — état légitime, voir cleDeVault.
		VaultPassword: cleVault,

		// Les NOMS ont été filtrés par la composition (§5.4.1) et journalisés à
		// la création du run ; les VALEURS ne le sont jamais et ne font que
		// passer.
		ForwardEnv: forward,
	}

	// Un run ad-hoc n'a pas de playbook : ce qu'il porte à la place, c'est sa
	// cible. Tout le reste de l'ordre — jeton git, clé SSH, become, forward-env
	// — vient d'être composé exactement comme pour un playbook, parce que c'est
	// exactement ce qu'un ad-hoc partage avec lui.
	if run.Kind == RunAdhoc {
		if run.AdhocTarget == nil {
			return wire.Order{}, fmt.Errorf("run %s : ad-hoc sans cible", run.RunRef)
		}
		order.Module = run.AdhocTarget.Module
		order.ModuleArgs = run.AdhocTarget.ModuleArgs
		order.HostPattern = run.AdhocTarget.HostPattern
	}

	return order, nil
}

// variablesTransmises reprend la consigne des valeurs de variables du run.
//
// L'absence est un état NORMAL et rend nil sans erreur : la plupart des runs ne
// transmettent rien, et un `forward_env` vide ne mérite pas de consigne.
//
// La lecture DÉTRUIT la valeur, comme pour le mot de passe d'élévation. C'est ce
// qui fait qu'un rejeu de livraison ne retrouve pas les variables — et c'est
// voulu : elles viennent du shell de l'appelant, qui est le seul à pouvoir les
// redonner.
func (b *OrderBuilder) variablesTransmises(ctx context.Context, runRef string) (map[string]wire.Secret, error) {
	if b.consigne == nil {
		return nil, nil
	}

	brut, err := b.consigne.Take(ctx, CleForwardEnv(runRef))
	if errors.Is(err, ErrNoStash) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("variables transmises du run %s : %w", runRef, err)
	}

	var valeurs map[string]string
	if err := json.Unmarshal(brut, &valeurs); err != nil {
		// Le message ne cite JAMAIS le contenu : il porte des valeurs sensibles,
		// et une erreur de décodage est le premier endroit où on les recopierait
		// dans un journal sans y penser.
		return nil, fmt.Errorf("variables transmises du run %s : consigne illisible", runRef)
	}
	if len(valeurs) == 0 {
		return nil, nil
	}

	out := make(map[string]wire.Secret, len(valeurs))
	for nom, v := range valeurs {
		out[nom] = wire.Secret(v)
	}
	return out, nil
}

// cleDAcces émet la clé de l'environnement du run, ou rien.
//
// Un environnement sans clé n'est PAS une erreur — voir NewOrderBuilder. Il est
// en revanche journalisé : c'est la cause la plus probable d'un run qui échouera
// en `unreachable` quelques secondes plus tard, et la trace évite de la chercher
// dans Ansible.
func (b *OrderBuilder) cleDAcces(ctx context.Context, run Run) (wire.Secret, error) {
	if b.keys == nil {
		return nil, nil
	}

	// LE DROIT est revérifié ICI, à la livraison, alors que `POST /runs` l'a déjà
	// contrôlé à la création. Ce n'est pas une redondance : c'est ce qui donne un
	// sens à la RÉVOCATION. Sans ce second appel, retirer un droit ne ferait rien
	// aux runs déjà enregistrés, et le §10.3 exige que le changement prenne
	// effet. C'est aussi le dernier point avant que la clé sorte.
	if err := VerifierLAcces(ctx, b.droits, run.UserID, run.Environment); err != nil {
		return nil, fmt.Errorf("clé d'accès du run %s : %w", run.RunRef, err)
	}

	// `hostScope` est nil : le périmètre par utilisateur du §10.2 n'existe pas.
	// Le paramètre est passé quand même, parce que c'est lui qui rendra
	// l'adaptateur Vault indolore.
	cle, err := b.keys.Issue(ctx, strconv.FormatInt(run.UserID, 10), run.Environment, nil)
	if errors.Is(err, ErrNoKey) {
		slog.Info("run sans clé d'accès",
			slog.String("run_ref", run.RunRef), slog.String("environnement", run.Environment))
		return nil, nil
	}
	if err != nil {
		// Une panne du magasin de clés FAIT ÉCHOUER la livraison plutôt que de
		// livrer un ordre sans clé : envoyer un run sans credential parce que
		// Kubernetes n'a pas répondu produirait un échec `unreachable` qui
		// accuserait la machine cible.
		return nil, fmt.Errorf("clé d'accès du run %s : %w", run.RunRef, err)
	}
	return wire.Secret(cle.Reveal()), nil
}

// cleDeVault émet la clé de vault de l'environnement du run, ou rien
// (ADR-0045).
//
// Un environnement sans clé de vault n'est PAS une erreur, pour la même raison
// que cleDAcces : la plupart des environnements n'ont aucun secret `!vault |`
// dans leurs playbooks. Le droit est revérifié ICI et non supposé acquis par
// cleDAcces : `b.keys` peut être nil sans que `b.vault` le soit, et
// cleDAcces renvoie alors sans avoir consulté `b.droits`.
func (b *OrderBuilder) cleDeVault(ctx context.Context, run Run) (wire.Secret, error) {
	if b.vault == nil {
		return nil, nil
	}

	if err := VerifierLAcces(ctx, b.droits, run.UserID, run.Environment); err != nil {
		return nil, fmt.Errorf("clé de vault du run %s : %w", run.RunRef, err)
	}

	cle, err := b.vault.Issue(ctx, run.Environment)
	if errors.Is(err, ErrNoVaultKey) {
		slog.Info("run sans clé de vault",
			slog.String("run_ref", run.RunRef), slog.String("environnement", run.Environment))
		return nil, nil
	}
	if err != nil {
		// Même conduite que cleDAcces : une panne du magasin FAIT ÉCHOUER la
		// livraison plutôt que de livrer un ordre qui échouerait plus loin, au
		// premier `!vault |` que le playbook ne pourrait pas déchiffrer.
		return nil, fmt.Errorf("clé de vault du run %s : %w", run.RunRef, err)
	}
	return wire.Secret(cle.Reveal()), nil
}

// EnrollFor compose l'ordre d'enrôlement d'un run.
//
// Symétrique d'`OrderFor`, et séparée d'elle : les deux ne partagent que la
// lecture du run. Les fondre obligerait chaque appelant à se demander lequel
// des deux retours est renseigné.
//
// Le MOT DE PASSE est lu ici, à la livraison, et sa lecture le DÉTRUIT. C'est
// le même principe que le jeton git et la clé d'accès — rien de sensible n'est
// composé avant le moment où ça part.
func (b *OrderBuilder) EnrollFor(ctx context.Context, runID int64) (wire.Enroll, error) {
	run, err := b.runs.RunByID(ctx, runID)
	if err != nil {
		return wire.Enroll{}, fmt.Errorf("lecture du run %d : %w", runID, err)
	}
	if run.Kind != RunEnrollment {
		return wire.Enroll{}, fmt.Errorf("run %s n'est pas un enrôlement", run.RunRef)
	}
	if run.EnrollTarget == nil {
		return wire.Enroll{}, fmt.Errorf("run %s : enrôlement sans cible", run.RunRef)
	}
	if b.hosts == nil || b.cles == nil || b.consigne == nil {
		return wire.Enroll{}, fmt.Errorf("run %s : l'enrôlement n'est pas configuré sur ce déploiement", run.RunRef)
	}

	cible := *run.EnrollTarget

	// La clé PUBLIQUE de l'environnement : c'est elle qu'on va poser. Un
	// environnement sans clé ne peut pas enrôler — contrairement à un run de
	// playbook, où l'absence est un état légitime.
	descripteur, err := b.cles.KeyByEnvironment(ctx, run.Environment)
	if err != nil {
		return wire.Enroll{}, fmt.Errorf("run %s : %w", run.RunRef, err)
	}

	// L'empreinte ATTENDUE vient de l'inventaire quand la machine y est déjà :
	// réenrôler une machine connue est alors épinglé, sans que personne ait à y
	// penser. Une machine inconnue n'en a pas, et c'est la première rencontre.
	var empreinte string
	if connue, err := b.hosts.HostByName(ctx, run.Environment, cible.Host); err == nil {
		empreinte = connue.HostKey
	} else if !errors.Is(err, ErrNoHost) {
		return wire.Enroll{}, fmt.Errorf("run %s : %w", run.RunRef, err)
	}

	motDePasse, err := b.consigne.Take(ctx, run.RunRef)
	if err != nil {
		return wire.Enroll{}, fmt.Errorf("run %s : mot de passe d'enrôlement : %w", run.RunRef, err)
	}

	return wire.Enroll{
		Host:      cible.Host,
		Address:   cible.Address,
		Port:      cible.Port,
		Login:     cible.Login,
		Password:  wire.Secret(motDePasse),
		PublicKey: descripteur.PublicKey,
		HostKey:   empreinte,
	}, nil
}

// CleBecome nomme la consigne du mot de passe d'élévation.
//
// EXPORTÉE et non recomposée de chaque côté : l'endpoint la range sous ce nom,
// le composeur d'ordres la lit sous ce nom. Deux compositions jumelles finissent
// par diverger, et la panne ne se voit alors qu'à l'exécution — c'est la leçon
// que ce dépôt a déjà payée avec `WARDEN_DISPATCH_URL`.
//
// Distincte de celle d'un enrôlement — le `run_ref` nu — parce qu'un run ne peut
// pas être les deux, mais que deux clefs homonymes se marcheraient dessus le
// jour où quelque chose change.
func CleBecome(runRef string) string { return runRef + "-become" }
