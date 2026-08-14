// Package submit enchaîne ce qu'un run demande avant de démarrer : capturer
// l'arbre, le pousser, déclarer le run.
//
// C'est la séquence du §5.2, et elle tient en deux appels au serveur plus un
// push. L'ADR-0028 a supprimé le troisième — la CLI tire son propre identifiant
// de run au lieu d'aller le demander.
package submit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/upload"
	"github.com/jijins1/warden/internal/git"
)

// Options porte ce que l'utilisateur a demandé.
type Options struct {
	// RepoDir est le dépôt de l'utilisateur. Il n'est JAMAIS modifié — c'est
	// l'exigence non négociable d'adoption de la décision actée n°4, et toute la
	// mécanique de capture existe pour la tenir.
	RepoDir string

	Repo        string // identifiant côté serveur
	Environment string
	Inventory   string
	Playbook    string

	// GitURL est l'adresse du rôle `git`, distincte de celle de l'API : deux
	// ports, deux surfaces, deux politiques réseau (ADR-0027).
	GitURL string

	// RunRef reprend un identifiant déjà tiré, pour REJOUER une soumission
	// interrompue. Vide, un nouvel identifiant est tiré.
	//
	// Ce champ est ce qui rend la reprise réellement possible plutôt que
	// seulement décrite : le commit d'audit est déterministe pour un identifiant
	// donné (ADR-0025), donc rejouer avec le même produit le même SHA, et git
	// répond que la ref est déjà à jour. Sans lui, une reprise tirerait un nouvel
	// identifiant et créerait un second enregistrement du même travail.
	RunRef string
}

// Result est ce que le serveur a retenu du run.
type Result struct {
	RunRef      string
	AuditSHA    string
	MainlineSHA string
	PushCost    *upload.PushCost
}

// API est ce que submit attend du control plane.
//
// Une interface déclarée ICI, chez l'appelant, plutôt qu'exportée par le paquet
// client : c'est ce qui permet de tester la séquence entière sans serveur, et ce
// qui empêche cette orchestration de dépendre de détails du transport.
type API interface {
	IssuePushGrant(ctx context.Context, repo string) (client.Grant, error)
	CreateRun(ctx context.Context, n client.NewRun) (client.RunCreated, error)
}

// Run exécute la séquence complète.
//
// L'ORDRE est celui du §5.2 et il n'est pas indifférent :
//
//  1. le jeton d'abord, parce qu'il porte le préfixe de ref que le serveur
//     accorde — la CLI ne le compose pas elle-même ;
//  2. la capture ensuite, dans un magasin d'appoint : le dépôt de l'utilisateur
//     n'est pas touché, et le commit d'audit n'existe QUE là ;
//  3. le push, qui part de ce magasin ;
//  4. la déclaration du run, où le serveur résout lui-même le commit d'audit
//     depuis la ref qu'il compose.
//
// Un échec après l'étape 3 laisse une ref d'audit sans ligne de run. Elle est
// inerte — rien ne la lit — et l'ADR-0013 a déjà tranché qu'aucune purge n'a lieu
// avant mesure. Rejouer avec `Options.RunRef` produit le même commit, donc le
// même SHA, et git répond que la ref est déjà à jour : la reprise est sûre par
// construction (ADR-0025).
func Run(ctx context.Context, api API, opts Options) (*Result, error) {
	if err := valider(opts); err != nil {
		return nil, err
	}

	grant, err := api.IssuePushGrant(ctx, opts.Repo)
	if err != nil {
		return nil, err
	}

	runRef := opts.RunRef
	if runRef == "" {
		var err error
		if runRef, err = upload.NewRunRef(); err != nil {
			return nil, err
		}
	}

	// La ref est composée du préfixe que le SERVEUR a accordé et de l'identifiant
	// tiré ici. Le préfixe n'est pas deviné : un jeton n'ouvre que le namespace
	// que sa signature déclare, et écrire ailleurs est refusé par le hook.
	ref, err := git.NewRef(grant.RefPrefix + runRef)
	if err != nil {
		return nil, fmt.Errorf("le serveur a accordé un préfixe inutilisable : %w", err)
	}

	depot := git.New(opts.RepoDir)
	base, err := depot.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("lecture du commit courant : %w", err)
	}

	scope, err := upload.DetermineScope(ctx, opts.RepoDir, upload.Options{})
	if err != nil {
		return nil, fmt.Errorf("détermination du périmètre d'audit : %w", err)
	}

	// Le message porte l'identifiant du run. Ce n'est pas de la décoration :
	// l'artefact devient autoportant, et deux runs du même arbre cessent de
	// produire le même commit — ce qui rendrait le second push indistinguable
	// d'un rejeu.
	message := "warden run " + runRef

	// L'auteur est DÉTERMINISTE, et ce n'est pas un détail de présentation : le
	// commit d'audit doit produire le même SHA pour le même arbre sur la même
	// base, faute de quoi un push rejoué crée un second commit disant la même
	// chose (ADR-0025). Un `time.Now()` ici casserait la propriété.
	//
	// L'identité n'est pas celle du `git config` de l'utilisateur : elle n'est
	// pas vérifiée, elle peut valoir n'importe quoi, et le serveur enregistre de
	// toute façon l'auteur réel dans `run.user_id` — qui, lui, sort de
	// l'authentification. L'auteur du commit est de la décoration ; l'autorité
	// est ailleurs.
	quand, err := upload.RunRefTime(runRef)
	if err != nil {
		return nil, fmt.Errorf("horodatage du run : %w", err)
	}
	audit, err := upload.CaptureAudit(ctx, opts.RepoDir, base, scope, message, git.Signature{
		Name:  "warden",
		Email: "warden@invalid",
		When:  quand,
	})
	if err != nil {
		return nil, fmt.Errorf("capture de l'arbre d'audit : %w", err)
	}
	defer func() { _ = audit.Close() }()

	if err := audit.Push(ctx, opts.GitURL, grant.Token, ref); err != nil {
		if errors.Is(err, git.ErrRemoteRejected) {
			// Un refus ne réussira pas au réessai, et la cause est du côté de
			// l'utilisateur : secret détecté, objet revendiqué sans être envoyé,
			// ref déjà écrite. Le message du serveur voyage dans l'erreur.
			return nil, fmt.Errorf("le serveur a refusé le commit d'audit : %w", err)
		}
		return nil, fmt.Errorf("envoi du commit d'audit : %w", err)
	}

	cree, err := api.CreateRun(ctx, client.NewRun{
		RunRef:      runRef,
		Repo:        opts.Repo,
		Environment: opts.Environment,
		Inventory:   opts.Inventory,
		Playbook:    opts.Playbook,
		BaseSHA:     base.String(),
	})
	if err != nil {
		// La ref est poussée mais le run n'est pas déclaré. Le dire précisément
		// évite qu'on croie que rien ne s'est passé, et nomme la reprise.
		return nil, fmt.Errorf("le commit d'audit est poussé (%s) mais le run n'a pas été déclaré : %w",
			runRef, err)
	}

	return &Result{
		RunRef:      cree.RunRef,
		AuditSHA:    cree.AuditSHA,
		MainlineSHA: cree.MainlineSHA,
	}, nil
}

func valider(o Options) error {
	manquants := make([]string, 0, 4)
	if o.RepoDir == "" {
		manquants = append(manquants, "répertoire du dépôt")
	}
	if o.Repo == "" {
		manquants = append(manquants, "nom du dépôt côté serveur")
	}
	if o.Playbook == "" {
		manquants = append(manquants, "playbook")
	}
	if o.GitURL == "" {
		manquants = append(manquants, "adresse du rôle git")
	}
	if len(manquants) > 0 {
		return fmt.Errorf("configuration incomplète : %s", strings.Join(manquants, ", "))
	}
	return nil
}
