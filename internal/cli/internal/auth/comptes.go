package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jijins1/warden/internal/cli/internal/client"
)

// ListerComptes affiche les comptes locaux.
func ListerComptes(ctx context.Context, api *client.Client, sortie io.Writer) error {
	comptes, err := api.ListerComptes(ctx)
	if err != nil {
		return err
	}
	if len(comptes) == 0 {
		_, _ = fmt.Fprintln(sortie, "aucun compte local")
		return nil
	}

	w := tabwriter.NewWriter(sortie, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "COMPTE\tNOM\tÉTAT\tRÔLE\tVU")
	for _, c := range comptes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			c.Subject, c.Name, etatDuCompte(c), roleDuCompte(c), vuLe(c.LastSeenAt))
	}
	return w.Flush()
}

// etatDuCompte dit ce qui empêche ce compte de se connecter, s'il y a lieu.
//
// « sans mot de passe » n'est PAS un détail : un compte créé dont personne n'a
// jamais posé le mot de passe existe, apparaît dans la liste, et ne peut pas se
// connecter. Sans cette mention il se lit comme un compte cassé.
func etatDuCompte(c client.Compte) string {
	switch {
	case c.Disabled:
		return "désactivé"
	case !c.HasPassword:
		return "sans mot de passe"
	default:
		return "actif"
	}
}

func roleDuCompte(c client.Compte) string {
	if c.Admin {
		return "admin"
	}
	return ""
}

func vuLe(brut string) string {
	if brut == "" {
		return "jamais"
	}
	t, err := time.Parse(time.RFC3339, brut)
	if err != nil {
		return brut
	}
	return t.Local().Format("2006-01-02 15:04")
}

// AjouterCompte crée un compte et affiche le mot de passe tiré par le serveur.
//
// Le mot de passe est tiré par le SERVEUR et non choisi ici, et c'est la
// décision qui compte : un mot de passe choisi par un administrateur est un mot
// de passe que l'administrateur connaît, et il est presque toujours faible parce
// qu'il doit être dicté.
func AjouterCompte(ctx context.Context, api *client.Client, subject, nom string, admin bool, sortie io.Writer) error {
	if subject == "" {
		return errors.New("--subject est requis : c'est l'identifiant du compte à créer")
	}

	c, err := api.CreerCompte(ctx, subject, nom, admin)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "compte %s", c.Subject)
	if c.Admin {
		_, _ = fmt.Fprint(sortie, " (administrateur)")
	}

	if c.Password == "" {
		// Le compte existait déjà avec un mot de passe : rejouer la création n'y
		// touche pas. Le DIRE, sinon l'absence de mot de passe affiché se lirait
		// comme un défaut.
		_, _ = fmt.Fprint(sortie, " mis à jour\n")
		_, _ = fmt.Fprint(sortie, "  son mot de passe est inchangé — "+
			"`warden user passwd` en tire un nouveau\n")
		return nil
	}

	_, _ = fmt.Fprintf(sortie, " créé\n  mot de passe  %s\n", c.Password)
	afficherLAvertissement(sortie)
	return nil
}

// ReinitialiserMotDePasse en tire un nouveau pour un compte.
func ReinitialiserMotDePasse(ctx context.Context, api *client.Client, subject string, sortie io.Writer) error {
	if subject == "" {
		return errors.New("--subject est requis")
	}

	c, err := api.ReinitialiserMotDePasse(ctx, subject)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "mot de passe de %s renouvelé\n  %s\n", c.Subject, c.Password)
	_, _ = fmt.Fprint(sortie, "\nToutes ses sessions ouvertes ont été coupées.\n")
	afficherLAvertissement(sortie)
	return nil
}

// BasculerCompte désactive ou réactive un compte.
func BasculerCompte(ctx context.Context, api *client.Client, subject string, desactive bool, sortie io.Writer) error {
	if subject == "" {
		return errors.New("--subject est requis")
	}

	c, err := api.BasculerCompte(ctx, subject, desactive)
	if err != nil {
		return err
	}

	if c.Disabled {
		_, _ = fmt.Fprintf(sortie, "compte %s désactivé\n", c.Subject)
		// La révocation immédiate est le point : sans elle, couper un accès ne
		// ferait rien avant l'expiration du jeton.
		_, _ = fmt.Fprint(sortie, "  ses sessions ouvertes ont été coupées\n")
		return nil
	}
	_, _ = fmt.Fprintf(sortie, "compte %s réactivé\n", c.Subject)
	_, _ = fmt.Fprint(sortie, "  il doit se reconnecter — la réactivation ne "+
		"rouvre pas les sessions coupées\n")
	return nil
}

// afficherLAvertissement rappelle ce que vaut ce mot de passe.
//
// Il transite par un canal qu'on ne maîtrise pas — un message, un ticket — et il
// n'y a aucune raison qu'il survive à sa première utilisation.
func afficherLAvertissement(sortie io.Writer) {
	_, _ = fmt.Fprint(sortie, "\nIl n'est affiché qu'une fois. Transmettez-le, "+
		"puis faites-le remplacer par `warden passwd`.\n")
}
