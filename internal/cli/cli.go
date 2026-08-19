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
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
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
	case "submit":
		return commandeSubmit(ctx, args[1:], os.Stdout)
	case "key":
		return commandeCle(ctx, args[1:], os.Stdout)
	case "host":
		return commandeHote(ctx, args[1:], os.Stdout)
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}

const texteUsage = `warden — exécution auditée de playbooks Ansible

Lancer un run :
  warden submit --repo <nom> --playbook <fichier> [--env <environnement>] [--inventory <chemin>]

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
  WARDEN_SUBJECT   identité, tant qu'aucun authentificateur n'est configuré`

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

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, texteUsage)
	}

	adresse, git := os.Getenv(EnvServeur), os.Getenv(EnvGit)
	if adresse == "" || git == "" {
		return fmt.Errorf("%s et %s doivent être définies\n\n%s", EnvServeur, EnvGit, texteUsage)
	}

	api, err := client.New(client.Config{BaseURL: adresse, Subject: os.Getenv(EnvSujet)})
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

	res, err := submit.Run(ctx, api, submit.Options{
		RepoDir:     cwd,
		Repo:        *repo,
		Environment: *environnement,
		Inventory:   *inventaire,
		Playbook:    *playbook,
		GitURL:      git,
		RunRef:      *runRef,
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
	adresse := os.Getenv(EnvServeur)
	if adresse == "" {
		return nil, fmt.Errorf("%s doit être définie\n\n%s", EnvServeur, texteUsage)
	}
	return client.New(client.Config{BaseURL: adresse, Subject: os.Getenv(EnvSujet)})
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
