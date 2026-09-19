// Package cli est le point d'entrée de la CLI utilisateur.
//
// Domaine de confiance : la machine de l'utilisateur (ADR-0011). Rien de ce que
// ce domaine calcule ne fait autorité — la configuration effective d'un run est
// résolue par le serveur depuis le commit (ADR-0009). La seule exception est le
// champ `server:` du fichier de projet : on ne peut pas demander au serveur où
// est le serveur.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/auth"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/command"
	"github.com/jijins1/warden/internal/cli/internal/projet"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/render"
	"github.com/jijins1/warden/internal/cli/internal/session"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/cli/internal/submit"
	"github.com/jijins1/warden/internal/config"
)

// Variables d'environnement de la configuration.
//
// Pas encore de fichier de projet : le §5.4 en décrit un, avec une politique
// serveur qui n'existe pas non plus. Le poser maintenant figerait un format
// avant de savoir ce que le serveur en accepte. L'environnement tient le rôle en
// attendant, et il ne prétend pas être la réponse.
const (
	EnvServeur = "WARDEN_SERVER"  // API du control plane
	EnvGit     = "WARDEN_GIT_URL" // rôle `git`, port d'écriture
	EnvSujet   = "WARDEN_SUBJECT" // identité, en mode de développement

	// configFichier n'est cité que dans les messages : le nom qui fait autorité
	// est `config.File`, et la CLI le lit à travers le paquet `projet`.
	configFichier = ".ansibleproxy.yml"
)

// Run exécute la commande demandée.
//
// L'AIDE est traitée en premier, et centralement : `warden help host declare`,
// `warden host declare --help` et `warden --help` sont la même question, posée
// à trois endroits différents de la ligne. Une commande qui l'interpréterait
// elle-même répondrait par une erreur de drapeau à quelqu'un qui bute au milieu
// d'une invocation.
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		// Rien de demandé n'est pas une aide demandée : l'index part sur stderr
		// et le code de sortie est non nul, parce que taper `warden` seul n'a
		// rien accompli.
		command.Arbre().Index(os.Stderr)
		return ErrDejaDit
	}
	if args[0] == "__complete" {
		// Interceptée AVANT `command.Demandee` : les mots qu'un shell transmet
		// pour compléter une ligne peuvent contenir un `--help` que
		// l'utilisateur a déjà tapé — ce n'est pas une demande d'aide, c'est
		// justement ce qu'on est en train de compléter.
		return commandeInterneComplete(ctx, args[1:], os.Stdout)
	}
	if command.Demandee(args) {
		return aide(os.Stdout, args)
	}

	switch args[0] {
	case "login":
		return commandeLogin(ctx, args[1:], os.Stdout)
	case "logout":
		return commandeLogout(ctx, args[1:], os.Stdout)
	case "whoami":
		return commandeWhoami(ctx, args[1:], os.Stdout)
	case "passwd":
		return commandePasswd(ctx, args[1:], os.Stdout)
	case "user":
		return commandeUtilisateur(ctx, args[1:], os.Stdout)
	case "grant":
		return commandeDroit(ctx, args[1:], os.Stdout)
	case "submit":
		return commandeSubmit(ctx, args[1:], os.Stdout)
	case "run":
		return commandeRun(ctx, args[1:], os.Stdout)
	case "logs":
		return commandeLogs(ctx, args[1:], os.Stdout)
	case "key":
		return commandeCle(ctx, args[1:], os.Stdout)
	case "repo":
		return commandeDepot(ctx, args[1:], os.Stdout)
	case "host":
		return commandeHote(ctx, args[1:], os.Stdout)
	case "enroll":
		return commandeEnroler(ctx, args[1:], os.Stdout)
	case "completion":
		return commandeCompletion(args[1:], os.Stdout)
	default:
		return command.Inconnue(nil, args[0])
	}
}

// erreurDrapeaux enveloppe un refus du paquet `flag` en renvoyant vers la page
// de la commande — et non vers le manuel entier, ce que faisait l'ancien
// `texteUsage`.
//
// ST1005 vise les messages qu'on ENVELOPPE, où un point final se retrouve au
// milieu d'une phrase. Celui-ci est terminal : `main` l'imprime tel quel.
//
//nolint:staticcheck // ST1005 : message terminal, jamais enveloppé à son tour.
func erreurDrapeaux(commande string, err error) error {
	return fmt.Errorf("%w\n\n« warden %s --help » décrit les drapeaux.", err, commande)
}

// ErrDejaDit signale une erreur dont le message a DÉJÀ été écrit sur stderr.
//
// Elle existe pour l'index d'aide, qui fait cinquante lignes : le faire voyager
// dans une valeur d'erreur le ferait préfixer de « warden: » par `main`, ce qui
// est le contraire de ce qu'on veut lire en tête d'un manuel.
var ErrDejaDit = errors.New("aucune commande")

func commandeSubmit(ctx context.Context, args []string, sortie io.Writer) error {
	fs, f := fsSubmit()
	repo, playbook, environnement := f.repo, f.playbook, f.environnement
	inventaire, runRef, become := f.inventaire, f.runRef, f.become

	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("submit", err)
	}

	// Les flags Ansible sont composés et refusés ICI, avant tout le reste : la
	// séquence de `submit` capture l'arbre puis le pousse, et découvrir un
	// `--limit` mal formé après ce travail donnerait un message exact sur une
	// cause que personne ne relierait (§5.7). Le serveur reste l'autorité et les
	// revalide (ADR-0038).
	flagsAnsible, err := submit.FlagsAnsible{
		Check: *f.check, Diff: *f.diff, Limit: *f.limit, ExtraVars: *f.extraVars,
		Become: *f.elever, BecomeUser: *f.eleverVers, Verbosite: f.verbeux.niveau(),
	}.Composer()
	if err != nil {
		return err
	}

	// Le répertoire courant, et non un chemin en option : la capture s'applique
	// au dépôt dans lequel on se trouve, et un drapeau inviterait à auditer un
	// dépôt qu'on ne regarde pas.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}

	// Le fichier de projet est lu ICI, et cette lecture n'est PAS autoritaire :
	// le serveur relira le même fichier dans l'arbre du commit (ADR-0009). Elle
	// ne sert qu'aux trois choses que le serveur ne peut pas faire à notre place
	// — savoir où l'appeler, savoir quels noms chercher dans le shell, et savoir
	// quoi mettre dans le commit. Voir le doc du paquet `projet`.
	config, err := projet.Lire(cwd)
	if err != nil {
		return err
	}

	adresse, err := adresseServeurDe(config)
	if err != nil {
		return err
	}

	// L'adresse du rôle `git` n'est plus exigée ici : le serveur l'annonce avec
	// le jeton de push, et celle-ci n'est qu'un repli pour les déploiements qui
	// ne l'annoncent pas encore.
	git := os.Getenv(EnvGit)

	api, err := clientPour(adresse)
	if err != nil {
		return err
	}

	// Facultatif : il n'est demandé que si on l'a réclamé. C'est ce que
	// `Fournie` distingue.
	var motDePasseBecome prompt.Secret
	if become.Fournie() {
		motDePasseBecome, err = become.Lire(ctx)
		if err != nil {
			return err
		}
		defer motDePasseBecome.Wipe()
	}

	// Le drapeau prime sur le fichier : viser un autre dépôt le temps d'une
	// commande ne doit pas demander d'éditer un fichier versionné.
	nomDuDepot := *repo
	if nomDuDepot == "" {
		nomDuDepot = config.Repo
	}

	res, err := submit.Run(ctx, api, submit.Options{
		RepoDir:     cwd,
		Repo:        nomDuDepot,
		Environment: *environnement,
		Inventory:   *inventaire,
		Playbook:    *playbook,
		Flags:       flagsAnsible,
		GitURL:      git,
		RunRef:      *runRef,

		BecomePassword: motDePasseBecome,

		// Les VALEURS des variables déclarées, lues dans le shell. Seuls les noms
		// déclarés sont consultés : l'environnement n'est jamais parcouru en
		// entier.
		ForwardEnv:   projet.Valeurs(config.ForwardEnv, os.LookupEnv),
		AuditInclude: config.AuditInclude,
	})
	if err != nil {
		return err
	}

	// L'identifiant en premier : c'est ce qu'on recopie dans un ticket, et ce
	// qu'il faut passer à --run-ref pour reprendre.
	//
	// Une écriture sur la sortie standard qui échoue signifie que le tube est
	// fermé — le run a bien eu lieu, et transformer ça en code de sortie non nul
	// ferait croire à un échec de soumission.
	_, _ = fmt.Fprintf(sortie, "run %s\n", res.RunRef)
	_, _ = fmt.Fprintf(sortie, "  audit   %s\n", res.AuditSHA)
	_, _ = fmt.Fprintf(sortie, "  contre  %s\n", res.MainlineSHA)

	if *f.sansSuivi {
		// Détaché, la commande ne sait RIEN de la suite : elle rend 0 parce que
		// la soumission a réussi, et c'est tout ce qu'elle peut affirmer.
		// `warden logs` rattrape le déroulement.
		_, _ = fmt.Fprintf(sortie, "\n« warden logs %s » pour en suivre le déroulement.\n", res.RunRef)
		return nil
	}

	return suivreLeRun(ctx, adresse, res.RunRef, sortie)
}

// commandeRun soumet un run AD-HOC : un module Ansible contre un motif de
// machines, sans playbook — l'équivalent de `ansible <motif> -m <module>
// -a <args>`, par opposition à `warden submit` (`ansible-playbook`).
//
// Calquée sur `commandeSubmit`, dont elle réutilise la séquence entière — un
// ad-hoc capture et pousse un commit d'audit exactement comme un playbook,
// pour résoudre son motif dans le même inventaire. Seul « quoi exécuter »
// diffère.
func commandeRun(ctx context.Context, args []string, sortie io.Writer) error {
	fs, f := fsRun()
	repo, environnement, inventaire := f.repo, f.environnement, f.inventaire
	runRef, become, module, moduleArgs := f.runRef, f.become, f.module, f.args

	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("run", err)
	}
	if fs.NArg() != 1 {
		//nolint:staticcheck // ST1005 : message terminal, jamais enveloppé.
		return errors.New("« warden run » attend un motif de machines, comme `ansible " +
			"<motif> -m <module>`.\nExemple : « warden run all -m ping ».")
	}
	motif := fs.Arg(0)
	if *module == "" {
		return errors.New("--module (ou -m) est requis : un run ad-hoc exécute un module")
	}

	flagsAnsible, err := submit.FlagsAnsible{
		Check: *f.check, Diff: *f.diff, ExtraVars: *f.extraVars,
		Become: *f.elever, BecomeUser: *f.eleverVers, Verbosite: f.verbeux.niveau(),
	}.Composer()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}

	config, err := projet.Lire(cwd)
	if err != nil {
		return err
	}

	adresse, err := adresseServeurDe(config)
	if err != nil {
		return err
	}

	git := os.Getenv(EnvGit)

	api, err := clientPour(adresse)
	if err != nil {
		return err
	}

	var motDePasseBecome prompt.Secret
	if become.Fournie() {
		motDePasseBecome, err = become.Lire(ctx)
		if err != nil {
			return err
		}
		defer motDePasseBecome.Wipe()
	}

	nomDuDepot := *repo
	if nomDuDepot == "" {
		nomDuDepot = config.Repo
	}

	res, err := submit.Run(ctx, api, submit.Options{
		RepoDir:     cwd,
		Repo:        nomDuDepot,
		Environment: *environnement,
		Inventory:   *inventaire,
		Module:      *module,
		ModuleArgs:  *moduleArgs,
		HostPattern: motif,
		Flags:       flagsAnsible,
		GitURL:      git,
		RunRef:      *runRef,

		BecomePassword: motDePasseBecome,

		ForwardEnv:   projet.Valeurs(config.ForwardEnv, os.LookupEnv),
		AuditInclude: config.AuditInclude,
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "run %s\n", res.RunRef)
	_, _ = fmt.Fprintf(sortie, "  audit   %s\n", res.AuditSHA)
	_, _ = fmt.Fprintf(sortie, "  contre  %s\n", res.MainlineSHA)

	if *f.sansSuivi {
		_, _ = fmt.Fprintf(sortie, "\n« warden logs %s » pour en suivre le déroulement.\n", res.RunRef)
		return nil
	}

	return suivreLeRun(ctx, adresse, res.RunRef, sortie)
}

// suivreLeRun s'attache au flux d'un run et en tire le code de sortie.
//
// Le code de sortie NON NUL sur un run en échec est ce qui rend `warden submit`
// utilisable dans un pipeline, et c'est ce qu'`ansible-playbook` fait. Il n'est
// possible QUE branché : détachée, la commande ne connaît pas l'issue.
//
// L'erreur rendue est `ErrDejaDit` : l'issue vient d'être écrite sur la sortie,
// et la faire préfixer de « warden: » par `main` la répéterait sous une forme
// qui ressemblerait à un second échec.
func suivreLeRun(ctx context.Context, adresse, runRef string, sortie io.Writer) error {
	suiveur, err := suiveurPour(adresse)
	if err != nil {
		return err
	}

	fin, err := render.Run(ctx, suiveur, runRef, sortie)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// Ctrl-C DÉTACHE, il n'annule pas : l'annulation du §5.6 demande un
			// endpoint que le serveur n'expose pas. Le dire est le minimum
			// honnête — sans ça, l'utilisateur croit avoir arrêté un run qui
			// continue de modifier ses machines.
			_, _ = fmt.Fprintf(sortie,
				"\ndétaché — le run %s CONTINUE côté serveur.\n"+
					"« warden logs %s » pour s'y rattacher.\n", runRef, runRef)
			return err
		}
		return fmt.Errorf("suivi du run %s : %w", runRef, err)
	}

	if render.Issue(sortie, runRef, fin) {
		return ErrDejaDit
	}
	return nil
}

// commandeLogs rattache la sortie standard au flux d'un run.
//
// Elle existe parce que `--no-follow` et le Ctrl-C en créent le besoin : sans
// elle, se détacher serait sans retour, et un drapeau qui mène à une impasse
// n'est pas une option, c'est un piège.
func commandeLogs(ctx context.Context, args []string, sortie io.Writer) error {
	fs := fsLogs()
	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("logs", err)
	}
	if fs.NArg() != 1 {
		//nolint:staticcheck // ST1005 : message terminal, jamais enveloppé.
		return errors.New("« warden logs » attend un identifiant de run.\n" +
			"« warden submit » l'imprime en tête de sa sortie.")
	}
	runRef := fs.Arg(0)

	// Le dépôt courant a son mot à dire sur l'adresse : on suit le plus souvent
	// un run qu'on vient de soumettre, donc depuis le même dépôt.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}
	adresse, err := adresseServeurDans(cwd)
	if err != nil {
		return err
	}
	return suivreLeRun(ctx, adresse, runRef, sortie)
}

// apiSeule construit le client pour les commandes qui ne touchent pas à git.
//
// `WARDEN_GIT_URL` n'est PAS exigée ici : administrer des clés et des machines ne
// pousse rien. L'exiger quand même ferait échouer `warden key list` sur une
// variable qui n'a rien à voir avec ce qu'on demande.
func apiSeule() (*client.Client, error) {
	adresse, err := adresseServeur()
	if err != nil {
		return nil, err
	}
	return clientPour(adresse)
}

// adresseServeurDans lit l'adresse du serveur pour une commande qui s'exécute
// DANS un dépôt.
//
// Ce n'est pas `adresseServeur` : celle-ci n'a que l'environnement, parce que
// `key`, `host` et `user` ne sont attachées à aucun dépôt. Ici le fichier de
// projet a son mot à dire, et c'est ce qui permet de déclarer un dépôt sans
// avoir d'abord posé une variable d'environnement pour le désigner.
func adresseServeurDans(repoDir string) (string, error) {
	// Une lecture qui échoue n'est PAS rattrapée par WARDEN_SERVER : un fichier
	// de projet présent mais invalide est une faute à corriger, pas un fichier
	// dont on se passe. `projet.Lire` rend un document nul sans erreur quand le
	// fichier est absent, ce qui est le cas légitime.
	config, err := projet.Lire(repoDir)
	if err != nil {
		return "", err
	}
	return adresseServeurDe(config)
}

// adresseServeurDe applique la précédence entre l'environnement et le fichier.
//
// La variable d'environnement PRIME sur le fichier : c'est la surcharge d'un
// utilisateur qui vise délibérément un autre serveur, et elle ne doit pas
// demander d'éditer un fichier versionné pour le reste de l'équipe.
func adresseServeurDe(p config.Project) (string, error) {
	if adresse := os.Getenv(EnvServeur); adresse != "" {
		return adresse, nil
	}
	if p.Server != "" {
		return p.Server, nil
	}
	return "", fmt.Errorf("aucune adresse de serveur : posez %s, ou déclarez `server` dans %s",
		EnvServeur, configFichier)
}

func adresseServeur() (string, error) {
	adresse := os.Getenv(EnvServeur)
	if adresse == "" {
		return "", fmt.Errorf("%s doit être définie : c'est l'adresse de l'API du "+
			"control plane", EnvServeur)
	}
	return adresse, nil
}

// clientPour construit un client porteur de la session rangée pour ce serveur.
//
// L'ABSENCE de session n'est pas une erreur : il reste `WARDEN_SUBJECT`, que les
// serveurs en mode de développement acceptent. Le refus vient du serveur, qui
// est le seul à savoir ce qu'il accepte — le décider ici rendrait la CLI
// inutilisable contre un déploiement de développement parfaitement valide.
//
// Une session EXPIRÉE, en revanche, est signalée tout de suite : l'envoyer
// produirait un 401 dont le message parlerait du serveur alors que la cause est
// locale et connue d'avance.
func clientPour(adresse string) (*client.Client, error) {
	cfg := client.Config{BaseURL: adresse, Subject: os.Getenv(EnvSujet)}

	s, err := session.Charger(adresse)
	switch {
	case errors.Is(err, session.ErrPasDeSession):
		// Pas de session : on s'en remet à WARDEN_SUBJECT, s'il est posé.
	case err != nil:
		return nil, err
	case s.Expiree():
		return nil, fmt.Errorf("votre session sur %s a expiré — relancez `warden login`", adresse)
	default:
		cfg.Token = s.Token
	}

	return client.New(cfg)
}

func commandeCle(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return command.Manquante([]string{"key"})
	}

	fs, f := fsCle()
	env, cle := f.env, f.cle
	if err := fs.Parse(args[1:]); err != nil {
		return erreurDrapeaux("key", err)
	}

	if err := command.VerifierVerbe([]string{"key"}, args[0]); err != nil {
		return err
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}

	switch args[0] {
	case "generate":
		return admin.PoserCle(ctx, api, admin.OptionsCle{Environment: *env, Generate: true}, sortie)
	case "import":
		return admin.PoserCle(ctx, api, admin.OptionsCle{Environment: *env, Cle: cle}, sortie)
	case "show":
		return admin.MontrerCle(ctx, api, *env, sortie)
	case "list":
		return admin.ListerCles(ctx, api, sortie)
	default:
		return command.Inconnue([]string{"key"}, args[0])
	}
}

func commandeHote(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return command.Manquante([]string{"host"})
	}

	fs, f := fsHote()
	fichier, env, hote := f.fichier, f.env, f.hote
	depuisInv, groupe, dryRun := f.depuisInv, f.groupe, f.dryRun
	if err := fs.Parse(args[1:]); err != nil {
		return erreurDrapeaux("host", err)
	}
	// Le verbe AVANT tout le reste : une faute de frappe doit être signalée comme
	// telle, et non comme le premier obstacle que la commande rencontre ensuite.
	if err := command.VerifierVerbe([]string{"host"}, args[0]); err != nil {
		return err
	}

	// `--from-inventory` s'exécute DANS un dépôt et lit son fichier de projet,
	// là où les autres verbes n'ont que l'environnement. Le cas est traité avant
	// la construction du client, qui n'est pas la même des deux côtés.
	if *depuisInv {
		if *fichier != "" {
			return errors.New("-f et --from-inventory désignent deux sources de " +
				"machines : donnez l'une ou l'autre")
		}
		return declarerDepuisInventaire(ctx, args[0], *env, *groupe, *dryRun, sortie)
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}

	switch args[0] {
	case "declare":
		return admin.DeclarerHotes(ctx, api, *fichier, *env, sortie)
	case "list":
		return admin.ListerHotes(ctx, api, *env, sortie)
	case "forget":
		return admin.OublierHote(ctx, api, *env, *hote, sortie)
	default:
		return command.Inconnue([]string{"host"}, args[0])
	}
}

// commandeDepot déclare ou liste les dépôts connus du serveur.
//
// Elle est la SEULE commande d'administration à s'exécuter dans un dépôt, et
// c'est ce qui la distingue des autres : elle lit `server:` du fichier de projet
// comme `warden submit`, là où `key`, `host` et `user` exigent WARDEN_SERVER.
// Exiger la variable ici obligerait à la poser pour déclarer le dépôt qui la
// porte, ce qui est exactement à l'envers.
func commandeDepot(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return command.Manquante([]string{"repo"})
	}

	fs, f := fsDepot()
	nom, remoteURL, mainline := f.nom, f.remoteURL, f.mainline
	remote, oui := f.remote, f.oui
	if err := fs.Parse(args[1:]); err != nil {
		return erreurDrapeaux("repo", err)
	}

	if err := command.VerifierVerbe([]string{"repo"}, args[0]); err != nil {
		return err
	}

	// Le répertoire courant, et non un chemin en option : la même raison que
	// pour `submit` — un drapeau inviterait à déclarer un dépôt qu'on ne regarde
	// pas.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}

	adresse, err := adresseServeurDans(cwd)
	if err != nil {
		return err
	}
	api, err := clientPour(adresse)
	if err != nil {
		return err
	}

	switch args[0] {
	case "declare":
		o := admin.OptionsDepot{
			RepoDir: cwd, Name: *nom, RemoteURL: *remoteURL,
			MainlineRef: *mainline, Remote: *remote,
			Confirmer: admin.ConfirmerDepotAuTerminal(os.Stdin, sortie),
		}
		if *oui {
			o.Confirmer = nil
		}
		return admin.DeclarerDepot(ctx, api, o, sortie)
	case "list":
		return admin.ListerDepots(ctx, api, sortie)
	default:
		return command.Inconnue([]string{"repo"}, args[0])
	}
}

// declarerDepuisInventaire enregistre les machines de l'inventaire du dépôt.
func declarerDepuisInventaire(ctx context.Context, verbe, env, groupe string, dryRun bool, sortie io.Writer) error {
	if verbe != "declare" {
		return fmt.Errorf("--from-inventory ne s'applique qu'à `warden host declare`, pas à %q", verbe)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}
	adresse, err := adresseServeurDans(cwd)
	if err != nil {
		return err
	}
	api, err := clientPour(adresse)
	if err != nil {
		return err
	}

	return admin.DeclarerDepuisInventaire(ctx, api, admin.OptionsInventaire{
		RepoDir: cwd, Environment: env, Groupe: groupe, DryRun: dryRun,
	}, sortie)
}

// suiveurPour construit le client de flux, porteur de la même session que le
// client HTTP : les deux traversent le même middleware d'authentification.
func suiveurPour(adresse string) (*stream.Client, error) {
	var jeton string
	if s, err := session.Charger(adresse); err == nil {
		jeton = s.Token
	}
	return stream.New(adresse, jeton, os.Getenv(EnvSujet))
}

func commandeEnroler(ctx context.Context, args []string, sortie io.Writer) error {
	fs, f := fsEnroler()
	env, hote, adresse := f.env, f.hote, f.adresse
	port, login, runRef, mdp := f.port, f.login, f.runRef, f.mdp
	toutes, groupe, dryRun, oui := f.toutes, f.groupe, f.dryRun, f.oui
	parMachine, accepter, arreter := f.parMachine, f.accepter, f.arreter

	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("enroll", err)
	}

	if *toutes {
		return enrolerLeLot(ctx, lotDemande{
			Env: *env, Groupe: *groupe, DryRun: *dryRun, Oui: *oui,
			ParMachine: *parMachine, Accepter: *accepter, Arreter: *arreter,
			MotDePasse: mdp,
			Seule:      *hote != "" || *adresse != "" || *login != "",
		}, sortie)
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}
	suiveur, err := suiveurPour(os.Getenv(EnvServeur))
	if err != nil {
		return err
	}

	return admin.Enroler(ctx, api, suiveur, admin.OptionsEnrolement{
		Environment: *env, Host: *hote, Address: *adresse, Port: *port,
		Login: *login, RunRef: *runRef, MotDePasse: mdp,
		// La revue humaine du premier contact. Elle lit l'entrée standard, qui
		// est un terminal dans le cas nominal — et si elle ne l'est pas, la
		// réponse ne sera pas un oui franc et la déclaration sera abandonnée.
		Confirmer: admin.ConfirmerAuTerminal(os.Stdin, sortie),
	}, sortie)
}

// lotDemande porte ce que les drapeaux de `--all` expriment.
type lotDemande struct {
	Env, Groupe              string
	DryRun, Oui, ParMachine  bool
	Accepter, Arreter, Seule bool
	MotDePasse               *prompt.Source
}

// enrolerLeLot enrôle toutes les machines manquantes de l'inventaire.
//
// Elle s'exécute DANS un dépôt, comme `warden repo declare` : c'est l'inventaire
// du projet qui dit quelles machines existent.
func enrolerLeLot(ctx context.Context, d lotDemande, sortie io.Writer) error {
	if d.Seule {
		// Mélanger les deux formes ne veut rien dire, et l'accepter en ignorant
		// l'une des deux ferait enrôler autre chose que ce qui était demandé.
		return errors.New("--all enrôle les machines de l'inventaire : " +
			"--host, --address et --login ne s'y appliquent pas")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("répertoire courant illisible : %w", err)
	}

	adresse, err := adresseServeurDans(cwd)
	if err != nil {
		return err
	}
	api, err := clientPour(adresse)
	if err != nil {
		return err
	}
	suiveur, err := suiveurPour(adresse)
	if err != nil {
		return err
	}

	o := admin.OptionsLot{
		OptionsInventaire: admin.OptionsInventaire{
			RepoDir: cwd, Environment: d.Env, Groupe: d.Groupe, DryRun: d.DryRun,
		},
		MotDePasse:            d.MotDePasse,
		ParMachine:            d.ParMachine,
		AccepterNouvelles:     d.Accepter,
		ArreterAuPremierEchec: d.Arreter,
		Confirmer:             admin.ConfirmerLotAuTerminal(os.Stdin, sortie),
		ConfirmerEmpreinte:    admin.ConfirmerAuTerminal(os.Stdin, sortie),
	}
	if d.Oui {
		o.Confirmer = nil
	}
	if d.Accepter {
		// L'absence de question se lit comme une absence, pas comme un « oui »
		// prononcé par le produit à la place de quelqu'un.
		o.ConfirmerEmpreinte = nil
	}
	return admin.EnrolerLot(ctx, api, suiveur, o, sortie)
}

// --- la session ---

func commandeLogin(ctx context.Context, args []string, sortie io.Writer) error {
	fs, f := fsLogin()
	subject, mdp := f.subject, f.mdp

	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("login", err)
	}

	adresse, err := adresseServeur()
	if err != nil {
		return err
	}

	// Un client SANS jeton : on est en train d'en obtenir un, et présenter une
	// session périmée ferait échouer la connexion avec un message sur la session
	// alors qu'on cherche justement à la renouveler.
	api, err := client.New(client.Config{BaseURL: adresse})
	if err != nil {
		return err
	}
	return auth.Login(ctx, api, adresse, *subject, mdp, sortie)
}

func commandeLogout(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) > 0 {
		return errors.New("warden logout ne prend pas d'argument")
	}
	adresse, err := adresseServeur()
	if err != nil {
		return err
	}

	// Le client peut manquer — session déjà absente, serveur injoignable. Ce
	// n'est pas une raison de refuser d'oublier le jeton local, qui est
	// précisément ce que l'utilisateur demande.
	api, _ := clientPour(adresse)
	return auth.Logout(ctx, api, adresse, sortie)
}

func commandeWhoami(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) > 0 {
		return errors.New("warden whoami ne prend pas d'argument")
	}
	adresse, err := adresseServeur()
	if err != nil {
		return err
	}
	api, err := clientPour(adresse)
	if err != nil {
		return err
	}
	return auth.Whoami(ctx, api, adresse, sortie)
}

func commandePasswd(ctx context.Context, args []string, sortie io.Writer) error {
	fs, f := fsPasswd()
	ancien, nouveau := f.ancien, f.nouveau

	if err := fs.Parse(args); err != nil {
		return erreurDrapeaux("passwd", err)
	}

	adresse, err := adresseServeur()
	if err != nil {
		return err
	}
	api, err := clientPour(adresse)
	if err != nil {
		return err
	}
	return auth.Passwd(ctx, api, adresse, ancien, nouveau, sortie)
}

// --- l'administration des comptes ---

func commandeUtilisateur(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return command.Manquante([]string{"user"})
	}

	fs, f := fsUtilisateur()
	subject, nom, admin := f.subject, f.nom, f.admin

	if err := fs.Parse(args[1:]); err != nil {
		return erreurDrapeaux("user", err)
	}

	if err := command.VerifierVerbe([]string{"user"}, args[0]); err != nil {
		return err
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return auth.ListerComptes(ctx, api, sortie)
	case "add":
		return auth.AjouterCompte(ctx, api, *subject, *nom, *admin, sortie)
	case "passwd":
		return auth.ReinitialiserMotDePasse(ctx, api, *subject, sortie)
	case "disable":
		return auth.BasculerCompte(ctx, api, *subject, true, sortie)
	case "enable":
		return auth.BasculerCompte(ctx, api, *subject, false, sortie)
	default:
		return command.Inconnue([]string{"user"}, args[0])
	}
}

func commandeDroit(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return command.Manquante([]string{"grant"})
	}

	fs, f := fsDroit()
	subject := f.subject

	if err := fs.Parse(args[1:]); err != nil {
		return erreurDrapeaux("grant", err)
	}

	if err := command.VerifierVerbe([]string{"grant"}, args[0]); err != nil {
		return err
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return auth.ListerDroits(ctx, api, *subject, sortie)
	default:
		return command.Inconnue([]string{"grant"}, args[0])
	}
}
