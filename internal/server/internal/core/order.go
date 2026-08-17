package core

import (
	"context"
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
	runs    RunByIDReader
	grants  FetchGrantIssuer
	keys    KeyIssuer
	gitBase string
	fetchTL time.Duration
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

	return wire.Order{
		RunRef:      run.RunRef,
		AuditSHA:    run.AuditSHA,
		Environment: run.Environment,
		Inventory:   run.Inventory,
		Playbook:    run.Playbook,
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
		SSHKey: cle,

		// ForwardEnv reste vide : les variables du shell de l'utilisateur
		// transitent par la CLI (§5.4), qui ne les envoie pas encore.
	}, nil
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
