package command

import (
	"flag"
	"strings"
)

// Localiser descend l'arbre le long des mots qui PRÉCÈDENT le mot en cours de
// complétion, en sautant chaque drapeau et sa valeur. Elle rend le nœud
// atteint, le chemin qui y mène, et son FlagSet réel — la MÊME source que
// `Afficher` utilise pour l'aide, de sorte que la complétion ne puisse jamais
// proposer un verbe ou un drapeau que l'aide ne connaît pas.
//
// `flagsDe` reçoit le chemin déjà résolu, exactement comme `Afficher` : c'est
// ce qui permet de savoir, pour un drapeau donné, s'il attend une valeur — et
// donc si le mot suivant lui appartient plutôt que de nommer un verbe.
func (c *Commande) Localiser(avant []string, flagsDe func([]string) *flag.FlagSet) (*Commande, []string, *flag.FlagSet) {
	noeud := c
	var chemin []string

	for i := 0; i < len(avant); i++ {
		mot := avant[i]
		if strings.HasPrefix(mot, "-") {
			// Un drapeau qui attend une valeur emporte le mot suivant avec lui —
			// il ne nomme pas un verbe, même s'il ne commence pas par un tiret.
			if fs := flagsDe(chemin); fs != nil {
				if f := fs.Lookup(strings.TrimLeft(mot, "-")); f != nil && !estBooleen(f) && i+1 < len(avant) {
					i++
				}
			}
			continue
		}

		suivant := noeud.sous(mot)
		if suivant == nil {
			// Le mot ne nomme aucun verbe d'ici : on reste au nœud le plus
			// profond atteint plutôt que d'échouer — c'est encore de quoi
			// proposer quelque chose.
			break
		}
		noeud, chemin = suivant, append(chemin, mot)
	}

	return noeud, chemin, flagsDe(chemin)
}

// CandidatsDrapeaux rend les formes tiretées (`-K`, `--env`) des drapeaux que
// CE nœud documente, filtrées par ceux que son FlagSet confirme exister — le
// même garde-fou que `drapeaux` applique déjà au rendu de l'aide.
func (c *Commande) CandidatsDrapeaux(fs *flag.FlagSet) []string {
	if fs == nil {
		return nil
	}
	out := make([]string, 0, len(c.Flags))
	for _, nom := range c.Flags {
		if fs.Lookup(nom) != nil {
			out = append(out, tiret(nom))
		}
	}
	return out
}
