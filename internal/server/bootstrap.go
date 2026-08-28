package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// BootstrapUser crée ou remet en service un compte local, depuis le cluster.
//
// C'est la sortie du problème de l'œuf et de la poule : créer un compte par
// l'API demande d'être administrateur, et un déploiement neuf n'en a aucun.
//
// Son autorisation n'est pas un mot de passe mais l'ACCÈS À LA BASE — c'est-à-dire
// le fait d'être l'opérateur du déploiement. C'est le même raisonnement que
// `wardend migrate`, et c'est pourquoi elle vit ici et non dans la CLI.
//
// Le mot de passe est TIRÉ et affiché une fois. Il n'est jamais accepté en
// argument : la ligne de commande d'un processus est lisible par tout le monde
// sur la machine, et un `kubectl exec` la fait en plus apparaître dans l'audit du
// cluster.
//
//	wardend user add --subject alice [--name "Alice"] [--admin]
//	wardend user passwd --subject alice
//	wardend user list
func BootstrapUser(ctx context.Context, args []string, sortie io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage : wardend user <add|passwd|list> [--subject <nom>]")
	}

	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	subject := fs.String("subject", "", "identifiant du compte")
	nom := fs.String("name", "", "nom affiché dans l'audit")
	admin := fs.Bool("admin", false, "accorder l'administration")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("ouverture du magasin: %w", err)
	}
	defer st.Close()

	switch args[0] {
	case "add":
		return ajouterCompte(ctx, st, sortie, *subject, *nom, *admin)
	case "passwd":
		return renouvelerMotDePasse(ctx, st, sortie, *subject)
	case "list":
		return listerComptes(ctx, st, sortie)
	default:
		return fmt.Errorf("sous-commande inconnue %q (attendu : add|passwd|list)", args[0])
	}
}

func ajouterCompte(ctx context.Context, st *store.Store, sortie io.Writer, subject, nom string, admin bool) error {
	if subject == "" {
		return errors.New("--subject est requis")
	}

	u, err := st.CreateLocalUser(ctx, subject, nom, admin)
	if err != nil {
		return err
	}
	motDePasse, err := poserUnMotDePasseTire(ctx, st, u.ID)
	if err != nil {
		return err
	}

	// L'admonestation vaut la peine : ce mot de passe transite par un canal qu'on
	// ne maîtrise pas, et il n'y a aucune raison qu'il survive à sa première
	// utilisation.
	_, _ = fmt.Fprintf(sortie, "compte %s créé", u.Subject)
	if u.IsAdmin {
		_, _ = fmt.Fprint(sortie, " (administrateur)")
	}
	_, _ = fmt.Fprintf(sortie, "\n  mot de passe  %s\n", motDePasse)
	_, _ = fmt.Fprint(sortie, "\nIl n'est affiché qu'une fois. Transmettez-le, "+
		"puis faites-le remplacer par `warden passwd`.\n")
	return nil
}

func renouvelerMotDePasse(ctx context.Context, st *store.Store, sortie io.Writer, subject string) error {
	if subject == "" {
		return errors.New("--subject est requis")
	}

	u, err := st.UserBySubject(ctx, core.UserLocal, subject)
	if err != nil {
		return err
	}
	motDePasse, err := poserUnMotDePasseTire(ctx, st, u.ID)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "mot de passe de %s renouvelé\n  %s\n", u.Subject, motDePasse)
	_, _ = fmt.Fprint(sortie, "\nToutes ses sessions ouvertes ont été coupées.\n")
	return nil
}

// poserUnMotDePasseTire tire un mot de passe et l'écrit.
//
// `parQui` est ZÉRO, ce qui devient NULL en base : l'amorçage n'a aucun
// utilisateur pour le porter, et désigner quelqu'un serait faux.
func poserUnMotDePasseTire(ctx context.Context, st *store.Store, userID int64) (string, error) {
	motDePasse, err := core.MotDePasseTire()
	if err != nil {
		return "", err
	}
	empreinte, err := core.Empreinte([]byte(motDePasse))
	if err != nil {
		return "", err
	}
	if err := st.PoserMotDePasse(ctx, userID, empreinte, 0); err != nil {
		return "", err
	}
	return motDePasse, nil
}

func listerComptes(ctx context.Context, st *store.Store, sortie io.Writer) error {
	comptes, err := st.ListLocalUsers(ctx)
	if err != nil {
		return err
	}
	if len(comptes) == 0 {
		_, _ = fmt.Fprint(sortie, "aucun compte local\n")
		return nil
	}

	for _, c := range comptes {
		etat := "actif"
		if c.Disabled {
			etat = "désactivé"
		}
		if !c.HasPassword {
			// Un compte sans mot de passe existe et ne peut pas se connecter.
			// Sans cette mention il se lit comme un compte cassé.
			etat += ", sans mot de passe"
		}
		role := ""
		if c.IsAdmin {
			role = "  [admin]"
		}
		_, _ = fmt.Fprintf(sortie, "%-24s %s%s\n", c.Subject, etat, role)
	}
	return nil
}
