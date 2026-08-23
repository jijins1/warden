package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"sigs.k8s.io/yaml"

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

// AppliquerDroits réconcilie les droits sur un fichier versionné.
//
// DÉCLARATIF, donc destructeur : pour chaque environnement que le fichier nomme,
// la liste fait autorité, et un compte retiré de la liste est RÉVOQUÉ. C'est ce
// qui donne sa valeur au versionnement — sans quoi le fichier ne serait qu'un
// historique de ce qu'on a ajouté.
//
// Un environnement ABSENT du fichier n'est pas touché.
func AppliquerDroits(ctx context.Context, api *client.Client, chemin string, sec bool, sortie io.Writer) error {
	if chemin == "" {
		return errors.New("-f <fichier> est requis")
	}

	doc, err := lireDocumentDroits(chemin)
	if err != nil {
		return err
	}

	plan, err := api.AppliquerDroits(ctx, doc, sec)
	if err != nil {
		return err
	}

	afficherLePlan(sortie, plan, chemin)
	return nil
}

func lireDocumentDroits(chemin string) (client.DocumentDroits, error) {
	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin donné par l'opérateur
	if err != nil {
		return client.DocumentDroits{}, fmt.Errorf("lecture de %s : %w", chemin, err)
	}

	var doc client.DocumentDroits
	if err := yaml.Unmarshal(brut, &doc); err != nil {
		return client.DocumentDroits{}, fmt.Errorf("%s est illisible : %w", chemin, err)
	}
	return doc, nil
}

// afficherLePlan rend le plan lisible dans une sortie de CI.
//
// Les RÉVOCATIONS en premier, et pas par symétrie : c'est la moitié qui retire
// un accès, donc celle qu'un relecteur pressé doit voir d'abord.
func afficherLePlan(sortie io.Writer, plan client.PlanDroits, chemin string) {
	if len(plan.Revoked) == 0 && len(plan.Granted) == 0 {
		_, _ = fmt.Fprintf(sortie, "%s : rien à changer (%d droit(s) déjà conforme(s))\n",
			chemin, plan.Unchanged)
		return
	}

	for _, c := range plan.Revoked {
		_, _ = fmt.Fprintf(sortie, "  - %s  perd l'accès à %s\n", c.Subject, c.Environment)
	}
	for _, c := range plan.Granted {
		_, _ = fmt.Fprintf(sortie, "  + %s  obtient l'accès à %s\n", c.Subject, c.Environment)
	}

	_, _ = fmt.Fprintf(sortie, "\n%d retrait(s), %d ajout(s), %d inchangé(s)\n",
		len(plan.Revoked), len(plan.Granted), plan.Unchanged)

	if !plan.Applied {
		// Le dire NETTEMENT : un pipeline qui affiche un plan sans cette ligne
		// laisse croire qu'il a appliqué.
		_, _ = fmt.Fprint(sortie, "\nAucun changement appliqué (--dry-run).\n")
		return
	}
	if len(plan.Revoked) > 0 {
		_, _ = fmt.Fprint(sortie, "\nLes retraits prennent effet immédiatement : "+
			"un run qui n'a pas encore reçu sa clé ne l'obtiendra pas.\n")
	}
}
