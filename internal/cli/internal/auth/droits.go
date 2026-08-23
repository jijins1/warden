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

// ListerDroits affiche les droits par environnement.
//
// Les RÉVOQUÉS sont affichés avec leur date, et pas masqués : c'est une vue
// d'audit. « Qui avait accès à prod le 3 mars » ne se répond pas sans eux.
func ListerDroits(ctx context.Context, api *client.Client, subject string, sortie io.Writer) error {
	droits, err := api.ListerDroits(ctx, subject)
	if err != nil {
		return err
	}
	if len(droits) == 0 {
		_, _ = fmt.Fprintln(sortie, "aucun droit")
		return nil
	}

	w := tabwriter.NewWriter(sortie, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "COMPTE\tENVIRONNEMENT\tÉTAT\tDEPUIS\tACCORDÉ PAR")
	for _, d := range droits {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			d.Subject, d.Environment, etatDuDroit(d), jour(d.GrantedAt), accordePar(d))
	}
	return w.Flush()
}

func etatDuDroit(d client.Droit) string {
	if d.RevokedAt != "" {
		return "révoqué " + jour(d.RevokedAt)
	}
	return "actif"
}

// accordePar dit qui a décidé. « historique » désigne les droits repris par la
// migration : personne ne les a accordés, et l'écrire évite de laisser croire
// qu'une décision a été prise.
func accordePar(d client.Droit) string {
	if d.GrantedBy == "" {
		return "(historique)"
	}
	return d.GrantedBy
}

func jour(brut string) string {
	if brut == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, brut)
	if err != nil {
		return brut
	}
	return t.Local().Format("2006-01-02")
}

// AccorderDroit donne l'accès à un environnement.
func AccorderDroit(ctx context.Context, api *client.Client, subject, environment string, sortie io.Writer) error {
	if subject == "" || environment == "" {
		return errors.New("--subject et --env sont requis")
	}

	d, err := api.AccorderDroit(ctx, subject, environment)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "%s peut désormais lancer des runs sur %s\n", d.Subject, d.Environment)
	// Ce que ce droit N'ACHÈTE PAS doit être dit à l'endroit où on l'accorde,
	// pas seulement dans un ADR que personne ne relit au moment d'agir.
	_, _ = fmt.Fprint(sortie, "  il obtiendra la clé d'accès de cet environnement ; "+
		"rien ne borne ensuite ce que son playbook peut joindre\n")
	return nil
}

// RevoquerDroit retire l'accès à un environnement.
func RevoquerDroit(ctx context.Context, api *client.Client, subject, environment string, sortie io.Writer) error {
	if subject == "" || environment == "" {
		return errors.New("--subject et --env sont requis")
	}

	if err := api.RevoquerDroit(ctx, subject, environment); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(sortie, "%s n'a plus accès à %s\n", subject, environment)
	// L'effet immédiat vaut d'être dit : la clé est revérifiée à la livraison.
	_, _ = fmt.Fprint(sortie, "  effet immédiat : un run qui n'a pas encore reçu "+
		"sa clé ne l'obtiendra pas\n")
	return nil
}
