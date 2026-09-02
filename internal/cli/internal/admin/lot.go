package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/jijins1/warden/internal/cli/internal/ansible"
	"github.com/jijins1/warden/internal/cli/internal/client"
)

// OptionsInventaire est ce que partagent les deux commandes qui travaillent
// depuis l'inventaire du dépôt : `host declare --from-inventory` et
// `enroll --all`.
type OptionsInventaire struct {
	RepoDir     string
	Environment string
	Groupe      string

	// DryRun s'arrête après avoir montré le plan, sans rien écrire ni joindre.
	DryRun bool
}

// Plan est ce qu'une commande de lot s'apprête à faire.
type Plan struct {
	Environnement string
	Chemin        string

	Manquantes []ansible.Machine
	Ecarts     []Ecart
}

// PreparerLot lit l'inventaire, le compare au serveur, et AFFICHE le plan.
//
// Partagée par les deux commandes, et pas seulement par économie : ce que
// l'opérateur voit avant de déclarer et avant d'enrôler doit être la même chose,
// sinon l'un des deux affichages finira par mentir.
func PreparerLot(ctx context.Context, api *client.Client, o OptionsInventaire, sortie io.Writer) (Plan, error) {
	inv, err := Depuis(o.RepoDir, o.Environment)
	if err != nil {
		return Plan{}, err
	}
	if inv.Environnement == "" {
		return Plan{}, errors.New("aucun environnement : passez --env, ou déclarez " +
			"`default_env` dans le fichier de projet")
	}

	machines, err := Filtrer(inv.Machines, o.Groupe)
	if err != nil {
		return Plan{}, err
	}

	connues, err := api.ListHosts(ctx, inv.Environnement)
	if err != nil {
		return Plan{}, err
	}

	manquantes, ecarts := Comparer(machines, connues)
	plan := Plan{
		Environnement: inv.Environnement, Chemin: inv.Chemin,
		Manquantes: manquantes, Ecarts: ecarts,
	}

	_, _ = fmt.Fprintf(sortie, "inventaire  %s\n", inv.Chemin)
	_, _ = fmt.Fprintf(sortie, "environnement  %s\n", inv.Environnement)
	_, _ = fmt.Fprintf(sortie, "%d machine(s) dans l'inventaire, %d déjà connue(s) du serveur\n\n",
		len(machines), len(machines)-len(manquantes))

	afficherEcarts(sortie, ecarts)

	if len(manquantes) == 0 {
		_, _ = fmt.Fprintln(sortie, "Rien à faire : toutes les machines de l'inventaire sont déjà enrôlées.")
		return plan, nil
	}

	_, _ = fmt.Fprintf(sortie, "%d machine(s) à traiter :\n\n", len(manquantes))
	afficherMachines(sortie, manquantes)
	return plan, nil
}

// afficherMachines rend le plan en colonnes.
//
// L'adresse DÉDUITE est marquée comme telle. C'est le seul moment où
// l'opérateur peut objecter qu'un nom ne résoudra pas depuis le cluster, et un
// affichage qui la présenterait comme une adresse ordinaire lui retirerait cette
// occasion (ADR-0036).
func afficherMachines(w io.Writer, machines []ansible.Machine) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MACHINE\tADRESSE\tCOMPTE\tPORT\tGROUPES")
	for _, m := range machines {
		adresse := m.Adresse
		if m.Deduite {
			adresse += "  (nom)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			m.Nom, adresse, compteOuDefaut(m.Login), portOuDefaut(m.Port), joindre(m.Groupes))
	}
	_ = tw.Flush()

	if aUneAdresseDeduite(machines) {
		_, _ = fmt.Fprintf(w, "\n« (nom) » : aucune `ansible_host` dans l'inventaire, "+
			"c'est le nom qui sert d'adresse.\nIl sera résolu DEPUIS LE CLUSTER — "+
			"pas depuis votre poste, et sans lire votre ssh_config.\n")
	}
}

func afficherEcarts(w io.Writer, ecarts []Ecart) {
	if len(ecarts) == 0 {
		return
	}
	// Signalés, jamais corrigés : ces machines sont déjà enrôlées, et changer
	// leur fiche est une décision qui appartient à l'opérateur.
	_, _ = fmt.Fprintf(w, "%d machine(s) déjà enrôlée(s) dont l'inventaire dit autre chose.\n"+
		"Elles ne sont PAS touchées :\n\n", len(ecarts))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MACHINE\tINVENTAIRE ≠ SERVEUR")
	for _, e := range ecarts {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", e.Machine.Nom, joindre(e.Quoi))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
}

func aUneAdresseDeduite(machines []ansible.Machine) bool {
	for _, m := range machines {
		if m.Deduite {
			return true
		}
	}
	return false
}

func compteOuDefaut(login string) string {
	if login == "" {
		return "(aucun)"
	}
	return login
}

func joindre(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	out := s[0]
	for _, v := range s[1:] {
		out += ", " + v
	}
	return out
}
