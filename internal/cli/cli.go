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
	"github.com/jijins1/warden/internal/cli/internal/projet"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
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
	case "repo":
		return commandeDepot(ctx, args[1:], os.Stdout)
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

Droits par environnement (lecture seule) :
  warden grant list   [--subject <compte>]     les révoqués sont affichés :
                                               c'est une vue d'audit

  AUCUNE commande ne modifie les droits. Ils viennent d'un document versionné
  dans le dépôt de déploiement, que le rôle « controller » réconcilie :

    environments:
      prod:
        - alice
        - bob
      staging: []          # explicitement personne

  Pour chaque environnement NOMMÉ, la liste fait autorité : un compte retiré du
  document est révoqué. Un environnement ABSENT n'est pas touché, donc les droits
  se découpent en plusieurs fichiers et un fichier tronqué ne révoque pas tout.

  Sans droit, pas de clé d'accès à l'environnement. AVEC le droit, rien ne borne
  ce que le playbook peut joindre — l'egress du runner est ouvert.

Lancer un run :
  warden submit --playbook <fichier> [--repo <nom>] [--env <environnement>] [--inventory <chemin>]
                [-K | --ask-become-pass]       demande le mot de passe
                                               d'élévation au terminal ; « -K »
                                               est celui d'ansible-playbook

Administrer les dépôts (administrateurs) :
  warden repo declare                          déclare LE DÉPÔT COURANT : nom,
                                               remote et branche de référence
                                               sont déduits de git, affichés,
                                               puis confirmés
              [--name <nom>] [--remote-url <url>] [--mainline-ref refs/heads/<b>]
              [--remote <origin>] [--yes]
  warden repo list

  Le serveur ne sait cloner qu'en http(s). Si votre « origin » est une URL SSH,
  donnez la forme http du même dépôt avec --remote-url.

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
  warden enroll --env <nom> --host <machine> --address <ip|nom> --login <compte>
                                               le mot de passe est demandé au
                                               terminal ; --password-file et
                                               --password-stdin existent pour
                                               les usages non interactifs

Format d'un fichier de déclaration :
  hosts:
    - host: db-prod-02        # le nom TEL QUE l'inventaire Ansible le nomme
      address: 10.20.0.42     # où l'on se connecte : une adresse, ou un nom
                              # que LE POD sait résoudre (pas votre ssh_config)
      login: ansible          # le compte que warden ouvre
      port: 22                # facultatif
      host_key: "ssh-ed25519 AAAA..."   # facultatif, mais c'est lui qui
                                        # protège du premier contact aveugle

Fichier de projet (.ansibleproxy.yml, commité à la racine du dépôt) :
  server: https://warden.example.com   où joindre le control plane
  repo: mon-depot                      nom du dépôt côté serveur ; rend --repo
                                       inutile. « warden repo declare » affiche
                                       la ligne à coller
  default_env: staging                 environnement quand --env est absent
  inventory: inventories/{{ env }}     chemin de l'inventaire ; « {{ env }} » est
                                       le seul substituable reconnu
  runner_image: registry/…/runner:2.3  image du pod d'exécution
  forward_env:                         NOMS des variables du shell à transmettre
    - VAULT_ADDR                       (les valeurs ne sont jamais journalisées)
  audit_include:                       chemins à joindre au commit d'audit
    - inventories/**                   malgré les règles d'ignore

  Le fichier est lu par le SERVEUR dans l'arbre du commit : ce qui s'applique est
  ce qui est audité. La lecture locale ne sert qu'à joindre le serveur, à savoir
  quelles variables lire dans le shell, et à composer le commit.

Configuration (environnement) :
  WARDEN_SERVER    adresse de l'API ; prime sur « server » du fichier
  WARDEN_GIT_URL   adresse du rôle git (port d'écriture). Facultative : le
                   serveur l'annonce avec le jeton de push. Ne sert que pour un
                   déploiement qui ne l'annonce pas encore.
  WARDEN_SUBJECT   identité AFFIRMÉE, acceptée seulement par un serveur en mode
                   de développement. « warden login » la rend inutile.

Le jeton de session est rangé dans ~/.config/warden/credentials.json, en 0600,
indexé par adresse de serveur.`

// usage rend l'aide sous forme d'erreur, pour que `main` la traite comme un
// échec — taper une commande incomplète ne doit pas rendre un code de sortie nul.
//
// erreurs, où une majuscule ou un point final se retrouve au milieu d'une
// phrase. Celui-ci est un texte d'aide de cinquante lignes, affiché tel quel et
// jamais enveloppé : le ponctuer correctement est l'inverse d'un défaut.
//
//nolint:staticcheck // ST1005 vise les messages composables dans d'autres
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
	// `-K` est le nom qu'`ansible-playbook` lui donne. Voir Abreger.
	become.Abreger(fs, "K", "abréviation d'--ask-become-pass, comme ansible-playbook")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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

// commandeDepot déclare ou liste les dépôts connus du serveur.
//
// Elle est la SEULE commande d'administration à s'exécuter dans un dépôt, et
// c'est ce qui la distingue des autres : elle lit `server:` du fichier de projet
// comme `warden submit`, là où `key`, `host` et `user` exigent WARDEN_SERVER.
// Exiger la variable ici obligerait à la poser pour déclarer le dépôt qui la
// porte, ce qui est exactement à l'envers.
func commandeDepot(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage : warden repo <declare|list>\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("repo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nom := fs.String("name", "", "nom du dépôt côté serveur")
	remoteURL := fs.String("remote-url", "", "URL http(s) que le serveur clonera")
	mainline := fs.String("mainline-ref", "", "branche de référence, en refs/heads/<nom>")
	remote := fs.String("remote", "", "remote git interrogé pour la déduction")
	oui := fs.Bool("yes", false, "ne pas demander confirmation")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
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
		return fmt.Errorf("usage : warden grant list [--subject <compte>]\n\n%s", texteUsage)
	}

	fs := flag.NewFlagSet("grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	subject := fs.String("subject", "", "identifiant du compte")

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
	default:
		return fmt.Errorf("sous-commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}
