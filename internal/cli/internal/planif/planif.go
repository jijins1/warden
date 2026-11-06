// Package planif porte `warden schedule` : les runs planifiés (ADR-0057).
//
// Un cron lance la branche protégée TELLE QU'ELLE EST, au nom de celui qui
// l'a créé et sous ses droits du moment. Rien ici ne capture l'arbre de
// travail : contrairement à `warden submit`, ce qui tournera n'est pas ce qui
// est sur le poste, mais ce qui sera sur la principale à l'heure dite.
package planif

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jijins1/warden/internal/cli/internal/client"
)

// Lister affiche les crons visibles.
func Lister(ctx context.Context, api *client.Client, sortie io.Writer) error {
	crons, err := api.ListSchedules(ctx)
	if err != nil {
		return err
	}
	if len(crons) == 0 {
		_, _ = fmt.Fprintln(sortie, "Aucun cron.")
		return nil
	}

	tw := tabwriter.NewWriter(sortie, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNOM\tDÉPÔT\tPLAYBOOK\tENV\tCRON\tPROPRIÉTAIRE\tPROCHAIN\tDERNIER")
	for _, c := range crons {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, c.Name, c.Repo, c.Playbook, c.Environment,
			c.Cron+" ("+c.Timezone+")", c.Owner, prochain(c), dernier(c))
	}
	return tw.Flush()
}

func heure(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func prochain(c client.Schedule) string {
	if !c.Enabled {
		return "désactivé"
	}
	return heure(c.NextFireAt)
}

// dernier résume l'issue du dernier déclenchement : le run parti, ou la
// raison du refus. Le refus est LA chose à lire ici — un cron qui ne part plus
// ne se voit nulle part ailleurs.
func dernier(c client.Schedule) string {
	if c.LastFiredAt == nil {
		return "jamais"
	}
	switch {
	case c.LastError != "" && c.LastRunRef != "":
		return heure(c.LastFiredAt) + " " + c.LastRunRef + " ÉCHEC : " + c.LastError
	case c.LastError != "":
		return heure(c.LastFiredAt) + " REFUSÉ : " + c.LastError
	default:
		return heure(c.LastFiredAt) + " " + c.LastRunRef
	}
}

// Creer crée un cron et rappelle ce qu'il fera.
func Creer(ctx context.Context, api *client.Client, repo string, n client.NewSchedule, sortie io.Writer) error {
	c, err := api.CreateSchedule(ctx, repo, n)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(sortie, "Cron %d « %s » créé.\n\n", c.ID, c.Name)
	_, _ = fmt.Fprintf(sortie, "  %s sur %s, environnement %s\n", c.Playbook, c.Repo, c.Environment)
	if len(c.Flags) > 0 {
		_, _ = fmt.Fprintf(sortie, "  flags : %s\n", strings.Join(c.Flags, " "))
	}
	_, _ = fmt.Fprintf(sortie, "  %s (%s) — prochain déclenchement : %s\n\n", c.Cron, c.Timezone, heure(c.NextFireAt))
	_, _ = fmt.Fprintln(sortie, "Il lancera la branche protégée telle qu'elle sera à cette heure-là, à votre\n"+
		"nom et sous vos droits du moment. Un droit sur l'environnement porté par un\n"+
		"groupe de l'annuaire ne suffit pas : il n'existe que pendant une session.")
	return nil
}

// Basculer active ou désactive un cron.
func Basculer(ctx context.Context, api *client.Client, id int64, actif bool, sortie io.Writer) error {
	c, err := api.SetScheduleEnabled(ctx, id, actif)
	if err != nil {
		return err
	}
	if actif {
		_, _ = fmt.Fprintf(sortie, "Cron %d réactivé ; prochain déclenchement : %s.\n", c.ID, heure(c.NextFireAt))
		return nil
	}
	_, _ = fmt.Fprintf(sortie, "Cron %d désactivé.\n", c.ID)
	return nil
}

// Supprimer supprime un cron.
func Supprimer(ctx context.Context, api *client.Client, id int64, sortie io.Writer) error {
	if err := api.DeleteSchedule(ctx, id); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(sortie, "Cron %d supprimé. Les runs qu'il a lancés en gardent la trace.\n", id)
	return nil
}
