package core

import (
	"context"
	"fmt"
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
	grants  RunNamespacer
	gitURL  string
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
func NewOrderBuilder(runs RunByIDReader, grants RunNamespacer, gitURL string) (*OrderBuilder, error) {
	if runs == nil || grants == nil {
		return nil, fmt.Errorf("construction des ordres : lecture des runs et frappe des jetons sont requises")
	}
	if gitURL == "" {
		// Sans elle, l'ordre partirait avec une origine vide et le runner
		// échouerait sur un message de git parlant d'URL malformée, très loin de
		// la cause.
		return nil, fmt.Errorf("construction des ordres : adresse du rôle git absente")
	}
	return &OrderBuilder{runs: runs, grants: grants, gitURL: gitURL, fetchTL: FetchGrantTTL}, nil
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

	return wire.Order{
		RunRef:      run.RunRef,
		AuditSHA:    run.AuditSHA,
		Environment: run.Environment,
		Inventory:   run.Inventory,
		Playbook:    run.Playbook,
		GitURL:      b.gitURL,
		GitToken:    wire.Secret(jeton),

		// SSHKey reste vide : `key-inventory` n'a pas d'implémentation, et
		// l'ADR-0020 a constaté que l'environnement cible n'a pas de gestionnaire
		// de secrets. En v1 l'autorisation d'accès aux hôtes repose ENTIÈREMENT
		// sur la NetworkPolicy egress. Mettre une valeur factice ici donnerait
		// l'illusion inverse.
		//
		// ForwardEnv reste vide de même : les variables du shell de l'utilisateur
		// transitent par la CLI (§5.4), qui ne les envoie pas encore.
	}, nil
}
