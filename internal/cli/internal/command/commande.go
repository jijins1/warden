package command

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Commande est un nœud de l'arbre d'aide.
//
// L'arbre est la SOURCE : il n'y a plus de texte d'usage recopié à côté des
// commandes, parce qu'un texte à côté finit toujours par décrire autre chose que
// ce que la commande fait.
type Commande struct {
	Nom string

	// Resume tient sur une ligne. C'est ce que l'index affiche, et le seul champ
	// que l'utilisateur lit s'il ne descend pas d'un cran.
	Resume string

	// Usage donne les formes d'invocation, une par ligne, sans le mot « warden ».
	Usage []string

	// Details sont les paragraphes qui expliquent, quand il y a à expliquer.
	Details string

	// Flags nomme les drapeaux à afficher pour CETTE commande, dans cet ordre.
	//
	// Des noms et non des descriptions : le rendu va chercher chaque drapeau dans
	// le `flag.FlagSet` réellement utilisé, de sorte que l'aide ne puisse pas
	// décrire un drapeau qui n'existe pas, ni taire un drapeau qui existe. Un
	// test vérifie la correspondance dans les deux sens.
	Flags []string

	// Exemples sont des invocations commentées.
	Exemples []Exemple

	// Voir renvoie vers les commandes qu'on cherche juste après celle-ci.
	Voir []string

	// Rubrique groupe la commande dans l'index. Vide, elle n'y figure pas.
	Rubrique string

	// Sous sont les verbes d'un groupe.
	Sous []Commande
}

// Exemple est une invocation, et la raison de la lancer.
type Exemple struct {
	Pourquoi string
	Ligne    string
}

// Trouver descend l'arbre le long d'un chemin de commandes.
//
// Rend le nœud atteint et le nombre de segments consommés : `warden help host
// declare` consomme deux segments, `warden help host truc` en consomme un et
// rend le groupe — de quoi dire « truc n'existe pas, voici ce qui existe ».
func (c *Commande) Trouver(chemin []string) (*Commande, int) {
	noeud, n := c, 0
	for _, segment := range chemin {
		suivant := noeud.sous(segment)
		if suivant == nil {
			return noeud, n
		}
		noeud, n = suivant, n+1
	}
	return noeud, n
}

func (c *Commande) sous(nom string) *Commande {
	for i := range c.Sous {
		if c.Sous[i].Nom == nom {
			return &c.Sous[i]
		}
	}
	return nil
}

// Verbes rend les noms des sous-commandes, dans l'ordre de déclaration.
func (c *Commande) Verbes() []string {
	out := make([]string, 0, len(c.Sous))
	for _, s := range c.Sous {
		out = append(out, s.Nom)
	}
	return out
}

// Index rend la vue de premier niveau : les commandes et leur résumé.
//
// Ce qu'elle N'AFFICHE PAS est le point : ni les drapeaux, ni les formats de
// fichier, ni la configuration. Une page d'accueil qui dit tout ne se lit pas,
// et c'est ce que l'ancienne constante de cent lignes faisait à chaque erreur de
// frappe.
func (c *Commande) Index(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s\n\n", c.Resume)
	_, _ = fmt.Fprintf(w, "Usage :\n  warden <commande> [<sous-commande>] [drapeaux]\n")

	for _, r := range c.rubriques() {
		_, _ = fmt.Fprintf(w, "\n%s :\n", r)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range c.Sous {
			if s.Rubrique != r {
				continue
			}
			nom := s.Nom
			if len(s.Sous) > 0 {
				nom += " <" + strings.Join(s.Verbes(), "|") + ">"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", nom, s.Resume)
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprintf(w, "\n« warden help <commande> » ouvre le détail.\n"+
		"  warden help host            les machines, et leurs verbes\n"+
		"  warden help host declare    un verbe, ses drapeaux et ses exemples\n")
}

// rubriques rend les rubriques dans l'ordre de première apparition.
func (c *Commande) rubriques() []string {
	var out []string
	vues := map[string]struct{}{}
	for _, s := range c.Sous {
		if s.Rubrique == "" {
			continue
		}
		if _, deja := vues[s.Rubrique]; deja {
			continue
		}
		vues[s.Rubrique] = struct{}{}
		out = append(out, s.Rubrique)
	}
	return out
}

// Page rend le détail d'une commande.
//
// `fs` porte les drapeaux RÉELS. Nul, la section des drapeaux est omise plutôt
// qu'inventée — une page d'aide qui décrit des drapeaux de mémoire est le mode
// de panne que cette signature existe pour rendre impossible.
func (c *Commande) Page(w io.Writer, chemin []string, fs *flag.FlagSet) {
	complet := strings.Join(append([]string{"warden"}, chemin...), " ")
	_, _ = fmt.Fprintf(w, "%s — %s\n", complet, c.Resume)

	if len(c.Usage) > 0 {
		_, _ = fmt.Fprintf(w, "\nUsage :\n")
		for _, u := range c.Usage {
			_, _ = fmt.Fprintf(w, "  warden %s\n", u)
		}
	}

	if c.Details != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", strings.TrimRight(c.Details, "\n"))
	}

	if len(c.Sous) > 0 {
		_, _ = fmt.Fprintf(w, "\nSous-commandes :\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, s := range c.Sous {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", s.Nom, s.Resume)
		}
		_ = tw.Flush()
	}

	c.drapeaux(w, fs)

	if len(c.Exemples) > 0 {
		_, _ = fmt.Fprintf(w, "\nExemples :\n")
		for i, e := range c.Exemples {
			if i > 0 {
				_, _ = fmt.Fprintln(w)
			}
			if e.Pourquoi != "" {
				_, _ = fmt.Fprintf(w, "  # %s\n", e.Pourquoi)
			}
			_, _ = fmt.Fprintf(w, "  %s\n", e.Ligne)
		}
	}

	if len(c.Voir) > 0 {
		_, _ = fmt.Fprintf(w, "\nVoir aussi : %s\n", strings.Join(c.Voir, ", "))
	}

	if len(c.Sous) > 0 {
		_, _ = fmt.Fprintf(w, "\n« warden help %s <sous-commande> » pour le détail d'un verbe.\n",
			strings.Join(chemin, " "))
	}
}

// drapeaux rend les drapeaux nommés par la commande, lus dans le FlagSet réel.
func (c *Commande) drapeaux(w io.Writer, fs *flag.FlagSet) {
	if fs == nil || len(c.Flags) == 0 {
		return
	}

	_, _ = fmt.Fprintf(w, "\nDrapeaux :\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, nom := range c.Flags {
		f := fs.Lookup(nom)
		if f == nil {
			// Ne peut arriver qu'entre une modification du code et le test de
			// cohérence qui l'attrape. Le dire vaut mieux que d'omettre en
			// silence un drapeau que la commande accepte pourtant.
			_, _ = fmt.Fprintf(tw, "  %s\t(non déclaré — signalez-le)\n", tiret(nom))
			continue
		}
		_, _ = fmt.Fprintf(tw, "  %s%s\t%s\n", tiret(nom), valeurDe(f), f.Usage)
	}
	_ = tw.Flush()
}

// tiret rend un nom de drapeau comme on le tape : `-K` court, `--env` long.
func tiret(nom string) string {
	if len(nom) == 1 {
		return "-" + nom
	}
	return "--" + nom
}

// valeurDe dit si le drapeau prend une valeur, sans la nommer d'après le type Go.
func valeurDe(f *flag.Flag) string {
	if estBooleen(f) {
		return ""
	}
	return " <valeur>"
}

// estBooleen dit si un drapeau se pose sans valeur, comme `--check` ou `-b`.
//
// Partagée avec la complétion (`Localiser`) : les deux ont besoin de la même
// réponse pour la même raison — savoir si le mot qui SUIT un drapeau lui
// appartient, ou commence autre chose.
func estBooleen(f *flag.Flag) bool {
	_, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok
}
