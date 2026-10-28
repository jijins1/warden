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
	"github.com/jijins1/warden/internal/cli/internal/prompt"
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

	// Module, ModuleArgs et HostPattern décrivent un run AD-HOC — l'équivalent
	// de `ansible <host-pattern> -m <module> -a <args>` — mutuellement
	// exclusifs avec Playbook. `warden run` les renseigne ; `warden submit` ne
	// les renseigne jamais.
	Module      string
	ModuleArgs  string
	HostPattern string

	// Flags sont les drapeaux d'`ansible-playbook` du §5.5, déjà composés par
	// `FlagsAnsible.Composer`.
	//
	// Ils ne sont PAS de la configuration lue depuis le commit, et c'est ce qui
	// les autorise à voyager dans la requête sans contredire l'ADR-0009 : ils
	// disent COMMENT jouer ce commit-là — en simulation, en montrant les diffs,
	// sur un sous-ensemble de machines — et non quoi jouer. Le serveur les
	// revalide et les enregistre dans `run.flags`.
	Flags []string

	// BecomePassword est le mot de passe d'élévation du §5.5, vide quand
	// l'utilisateur ne l'a pas demandé.
	//
	// Il ne transite QUE par le corps de `POST /runs` : jamais argv, jamais
	// l'environnement, et le serveur ne le persiste pas.
	BecomePassword prompt.Secret

	// ConnectionPassword est le mot de passe de connexion SSH (ADR-0048),
	// vide quand l'utilisateur ne l'a pas demandé. Même trajet que
	// BecomePassword.
	ConnectionPassword prompt.Secret

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

	// ForwardEnv porte les VALEURS lues dans le shell pour les noms que le
	// fichier de projet déclare (§5.4).
	//
	// Non autoritaire : le serveur relit les noms dans le commit et n'en retient
	// que l'intersection (ADR-0009). Ce qui est envoyé ici sans être déclaré dans
	// le commit ne passera pas.
	ForwardEnv map[string]string

	// AuditInclude élargit le périmètre de la capture (§5.3).
	//
	// Nécessairement client : il décide de ce qui ENTRE dans le commit, donc le
	// lire depuis le commit serait circulaire. La divergence reste détectable
	// après coup — le fichier est dans l'arbre — mais pas empêchable à la source.
	AuditInclude []string
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
// `Options.GitURL` n'est PLUS obligatoire : le serveur peut annoncer l'adresse
// avec le jeton de push. Elle reste acceptée comme repli, et son absence n'est
// détectée qu'au moment du push — c'est-à-dire au seul moment où l'on sait
// vraiment si elle manquait.
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

	// L'adresse du rôle `git` telle que le SERVEUR l'annonce prime sur celle que
	// l'appelant a configurée. C'est le même principe que le préfixe de ref : le
	// serveur dit avec quel jeton, sous quel préfixe et OÙ, d'une seule phrase.
	// La valeur locale reste le repli d'un déploiement qui ne l'annonce pas.
	gitURL := opts.GitURL
	if grant.PushURL != "" {
		gitURL = grant.PushURL
	}
	if gitURL == "" {
		// Contrôlé ICI et pas dans `valider` : avant l'appel au serveur, on ne
		// sait pas encore s'il annonce l'adresse, et exiger la variable locale
		// d'un déploiement qui l'annonce serait une exigence fausse.
		//
		// Mais contrôlé AVANT la capture, et c'est ce qui compte : sans ça,
		// l'adresse vide descendrait jusqu'au push et git répondrait « URL
		// malformée » après que l'utilisateur a payé la capture de son arbre —
		// un message exact, sur une cause qu'il ne relierait pas (§5.7).
		return nil, fmt.Errorf("aucune adresse pour le rôle git : ce serveur ne l'annonce pas, " +
			"posez WARDEN_GIT_URL")
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

	scope, err := upload.DetermineScope(ctx, opts.RepoDir, upload.Options{Include: opts.AuditInclude})
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

	if err := audit.Push(ctx, gitURL, grant.Token, ref); err != nil {
		if errors.Is(err, git.ErrRemoteRejected) {
			// Un refus ne réussira pas au réessai, et la cause est du côté de
			// l'utilisateur : secret détecté, objet revendiqué sans être envoyé,
			// ref déjà écrite. Le message du serveur voyage dans l'erreur.
			return nil, fmt.Errorf("le serveur a refusé le commit d'audit : %w", err)
		}
		return nil, fmt.Errorf("envoi du commit d'audit : %w", err)
	}

	flags, err := encoder(opts.Flags)
	if err != nil {
		return nil, err
	}

	genre := ""
	if opts.Module != "" {
		genre = "adhoc"
	}

	cree, err := api.CreateRun(ctx, client.NewRun{
		RunRef:      runRef,
		Repo:        opts.Repo,
		Environment: opts.Environment,
		Inventory:   opts.Inventory,
		Playbook:    opts.Playbook,
		BaseSHA:     base.String(),
		Flags:       flags,
		Kind:        genre,
		Module:      opts.Module,
		ModuleArgs:  opts.ModuleArgs,
		HostPattern: opts.HostPattern,
		// `Reveal` : le seul endroit où le mot de passe d'élévation sort de son
		// enveloppe masquée, et il doit se voir en revue.
		BecomePassword:     string(opts.BecomePassword.Reveal()),
		ConnectionPassword: string(opts.ConnectionPassword.Reveal()),
		ForwardEnv:         opts.ForwardEnv,
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

	adhoc := o.Module != "" || o.HostPattern != ""
	switch {
	case o.Playbook != "" && adhoc:
		return errors.New("playbook et module ad-hoc à la fois : les deux disent quoi exécuter")
	case adhoc:
		if o.Module == "" {
			manquants = append(manquants, "module")
		}
		if o.HostPattern == "" {
			manquants = append(manquants, "motif de machines")
		}
	case o.Playbook == "":
		manquants = append(manquants, "playbook")
	}

	if len(manquants) > 0 {
		return fmt.Errorf("configuration incomplète : %s", strings.Join(manquants, ", "))
	}
	return nil
}
