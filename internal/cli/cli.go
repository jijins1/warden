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

	"github.com/jijins1/warden/internal/cli/internal/client"
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
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("commande inconnue %q\n\n%s", args[0], texteUsage)
	}
}

const texteUsage = `warden submit — capture l'état du dépôt et déclare un run

Usage :
  warden submit --repo <nom> --playbook <fichier> [--env <environnement>] [--inventory <chemin>]

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
