package auth

import (
	"context"
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

// Il n'existe AUCUNE fonction d'attribution ni de révocation, et c'est
// délibéré : les droits viennent d'un document versionné que le rôle
// « controller » réconcilie (ADR-0033). Une commande qui les modifierait ferait
// une seconde source de vérité, écrasée sans bruit au passage suivant.
