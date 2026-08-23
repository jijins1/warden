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
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/auth"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
	"github.com/jijins1/warden/internal/cli/internal/session"
	"github.com/jijins1/warden/internal/cli/internal/stream"
	"github.com/jijins1/warden/internal/cli/internal/submit"
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
)

// Run exécute la commande demandée.
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
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
	case "key":
		return commandeCle(ctx, args[1:], os.Stdout)
	case "host":
		return commandeHote(ctx, args[1:], os.Stdout)
	case "enroll":
		return commandeEnroler(ctx, args[1:], os.Stdout)
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}

const texteUsage = `warden — exécution auditée de playbooks Ansible

Ouvrir une session :
  warden login  --subject <compte>             le mot de passe est demandé au
                                               terminal ; --password-file et
                                               --password-stdin existent pour
                                               les usages non interactifs
  warden logout
  warden whoami                                demande au SERVEUR qui il croit
                                               que vous êtes
  warden passwd                                change votre mot de passe et
                                               coupe toutes vos sessions

Administrer les comptes (administrateurs) :
  warden user add    --subject <compte> [--name <nom>] [--admin]
                                               le mot de passe est TIRÉ par le
                                               serveur et affiché une seule fois
  warden user list
  warden user passwd --subject <compte>        en réinitialise un
  warden user disable --subject <compte>
  warden user enable  --subject <compte>

Droits par environnement (administrateurs) :
  warden grant apply  -f <droits.yaml> [--dry-run]
                                               DÉCLARATIF : pour chaque
                                               environnement nommé, la liste fait
                                               autorité. Un compte retiré du
                                               fichier est RÉVOQUÉ.
  warden grant add    --subject <compte> --env <nom>
  warden grant list   [--subject <compte>]     les révoqués sont affichés :
                                               c'est une vue d'audit
  warden grant revoke --subject <compte> --env <nom>

Format d'un document de droits (versionnable, pour GitOps) :
  environments:
    prod:
      - alice
      - bob
    staging: []            # explicitement personne

  Un environnement ABSENT du document n'est pas touché : les droits se découpent
  en plusieurs fichiers, et un fichier tronqué ne révoque pas tout.

  Sans droit, pas de clé d'accès à l'environnement. AVEC le droit, rien ne borne
  ce que le playbook peut joindre — l'egress du runner est ouvert.

Lancer un run :
  warden submit --repo <nom> --playbook <fichier> [--env <environnement>] [--inventory <chemin>]
                [--ask-become-pass]            demande le mot de passe
                                               d'élévation au terminal

Administrer les clés d'accès :
  warden key generate --env <nom>              frappe la paire ; la partie privée
                                               ne quitte jamais le cluster
  warden key import   --env <nom> --key-file <chemin> | --key-stdin
  warden key show     --env <nom>              affiche la clé à poser dans
                                               les authorized_keys
  warden key list

Administrer les machines enrôlées :
  warden host declare -f <fichier.yaml> [--env <nom>]
  warden host list    [--env <nom>]
  warden host forget  --env <nom> --host <machine>

Enrôler une machine (warden pose la clé lui-même) :
  warden enroll --env <nom> --host <machine> --address <ip> --login <compte>
                                               le mot de passe est demandé au
                                               terminal ; --password-file et
                                               --password-stdin existent pour
                                               les usages non interactifs

Format d'un fichier de déclaration :
  hosts:
    - host: db-prod-02        # le nom TEL QUE l'inventaire Ansible le nomme
      address: 10.20.0.42     # une ADRESSE : c'est là qu'on se connecte
      login: ansible          # le compte que warden ouvre
      port: 22                # facultatif
      host_key: "ssh-ed25519 AAAA..."   # facultatif, mais c'est lui qui
                                        # protège du premier contact aveugle

Configuration (environnement) :
  WARDEN_SERVER    adresse de l'API du control plane
  WARDEN_GIT_URL   adresse du rôle git (port d'écriture)
  WARDEN_SUBJECT   identité AFFIRMÉE, acceptée seulement par un serveur en mode
                   de développement. « warden login » la rend inutile.

Le jeton de session est rangé dans ~/.config/warden/credentials.json, en 0600,
indexé par adresse de serveur.`

func usage() error { return errors.New(texteUsage) }

func commandeSubmit(ctx context.Context, args []string, sortie io.Writer) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // les messages d'erreur sont les nôtres

	repo := fs.String("repo", "", "nom du dépôt côté serveur")
	playbook := fs.String("playbook", "", "playbook à exécuter")
	environnement := fs.String("env", "", "environnement cible")
	inventaire := fs.String("inventory", "", "inventaire")
	// Rejouer une soumission interrompue produit le même commit d'audit, donc le
	// même SHA (ADR-0025) : la reprise est sûre, à condition de reprendre le même
	// identifiant.
	runRef := fs.String("run-ref", "", "reprendre une soumission interrompue")
	// La MÊME mécanique de saisie que pour une clé ou un enrôlement : trois
	// drapeaux nommés pareil, jamais argv, jamais l'environnement.
	become := prompt.Declarer(fs, "become-pass", "le mot de passe d'élévation")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
	}

	adresse, git := os.Getenv(EnvServeur), os.Getenv(EnvGit)
	if adresse == "" || git == "" {
		return fmt.Errorf("%s et %s doivent être définies\n\n%s", EnvServeur, EnvGit, texteUsage)
	}

	api, err := clientPour(adresse)
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

	res, err := submit.Run(ctx, api, submit.Options{
		RepoDir:     cwd,
		Repo:        *repo,
		Environment: *environnement,
		Inventory:   *inventaire,
		Playbook:    *playbook,
		GitURL:      git,
		RunRef:      *runRef,

		BecomePassword: motDePasseBecome,
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
	return nil
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

func adresseServeur() (string, error) {
	adresse := os.Getenv(EnvServeur)
	if adresse == "" {
		return "", fmt.Errorf("%s doit être définie\n\n%s", EnvServeur, texteUsage)
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
		return fmt.Errorf("usage : warden key <generate|import|show|list>\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	env := fs.String("env", "", "environnement")
	// Multiligne : une clé PEM ne se saisit pas à l'aveugle, l'utilisateur ne
	// verrait rien de ce qu'il colle. Le registre le sait et refuse l'invite en
	// le disant.
	cle := prompt.DeclarerMultiligne(fs, "key", "la clé privée")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("sous-commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}

func commandeHote(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage : warden host <declare|list|forget>\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fichier := fs.String("f", "", "fichier de déclaration (YAML ou JSON)")
	env := fs.String("env", "", "environnement")
	hote := fs.String("host", "", "machine")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("sous-commande inconnue %q\n\n%s", args[0], texteUsage)
	}
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
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	env := fs.String("env", "", "environnement")
	hote := fs.String("host", "", "nom de la machine, tel que l'inventaire Ansible la nomme")
	adresse := fs.String("address", "", "adresse où se connecter")
	port := fs.Int("port", 22, "port SSH")
	login := fs.String("login", "", "compte à ouvrir sur la machine")
	runRef := fs.String("run-ref", "", "reprendre un enrôlement interrompu")
	// Sans provenance explicite, la saisie au terminal est prise par défaut :
	// c'est le geste attendu de cette commande.
	mdp := prompt.Declarer(fs, "password", "le mot de passe du compte")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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

// --- la session ---

func commandeLogin(ctx context.Context, args []string, sortie io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	subject := fs.String("subject", "", "identifiant de votre compte")
	// La MÊME mécanique de saisie que partout ailleurs : jamais argv, jamais
	// l'environnement, refus net quand deux provenances sont données.
	mdp := prompt.Declarer(fs, "password", "votre mot de passe")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("warden logout ne prend pas d'argument\n\n%s", texteUsage)
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
		return fmt.Errorf("warden whoami ne prend pas d'argument\n\n%s", texteUsage)
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
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	ancien := prompt.Declarer(fs, "current-password", "votre mot de passe actuel")
	nouveau := prompt.Declarer(fs, "new-password", "votre nouveau mot de passe")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("usage : warden user <add|list|passwd|disable|enable>\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	subject := fs.String("subject", "", "identifiant du compte")
	nom := fs.String("name", "", "nom affiché dans l'audit")
	admin := fs.Bool("admin", false, "accorder l'administration")

	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("sous-commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}

func commandeDroit(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage : warden grant <apply|add|list|revoke>\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	subject := fs.String("subject", "", "identifiant du compte")
	env := fs.String("env", "", "environnement")
	fichier := fs.String("f", "", "document de droits, pour `apply`")
	// `--dry-run` est ce qui rend la revue possible : un changement de droits sur
	// la production doit être lisible dans une pull request AVANT d'être
	// appliqué, sans quoi le versionnement n'achète qu'un historique.
	sec := fs.Bool("dry-run", false, "afficher le plan sans l'appliquer")

	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
	}

	api, err := apiSeule()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return auth.ListerDroits(ctx, api, *subject, sortie)
	case "add":
		return auth.AccorderDroit(ctx, api, *subject, *env, sortie)
	case "revoke":
		return auth.RevoquerDroit(ctx, api, *subject, *env, sortie)
	case "apply":
		return auth.AppliquerDroits(ctx, api, *fichier, *sec, sortie)
	default:
		return fmt.Errorf("sous-commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}
