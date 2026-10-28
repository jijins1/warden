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

// Candidat est une proposition de complétion, et ce qu'on affiche à côté.
//
// La description est ce qui permet de choisir SANS ouvrir l'aide : le résumé
// d'un verbe, ou un exemple de ce qu'un drapeau attend.
type Candidat struct {
	Mot         string
	Description string
}

// CandidatsVerbes rend les sous-commandes, chacune avec son résumé — le même
// que l'index de `warden help`.
func (c *Commande) CandidatsVerbes() []Candidat {
	out := make([]Candidat, 0, len(c.Sous))
	for _, s := range c.Sous {
		out = append(out, Candidat{Mot: s.Nom, Description: s.Resume})
	}
	return out
}

// CandidatsDrapeaux rend les formes tiretées (`-K`, `--env`) des drapeaux que
// CE nœud documente, filtrées par ceux que son FlagSet confirme exister — le
// même garde-fou que `drapeaux` applique déjà au rendu de l'aide.
//
// Un drapeau à valeur est décrit par un EXEMPLE de valeur : c'est ce qu'on
// cherche au moment de le taper. Un booléen n'en a pas ; il est décrit par
// son texte d'aide.
func (c *Commande) CandidatsDrapeaux(fs *flag.FlagSet) []Candidat {
	if fs == nil {
		return nil
	}
	out := make([]Candidat, 0, len(c.Flags))
	for _, nom := range c.Flags {
		f := fs.Lookup(nom)
		if f == nil {
			continue
		}
		description := f.Usage
		if exemple := c.ExempleDeValeur(nom); exemple != "" && !estBooleen(f) {
			description = exemple
		}
		out = append(out, Candidat{Mot: tiret(nom), Description: description})
	}
	return out
}

// ExempleDeValeur rend une valeur plausible pour le drapeau `nom` sur CE nœud :
// celle que le nœud déclare dans `Valeurs`, sinon celle que le drapeau prend
// partout ailleurs.
func (c *Commande) ExempleDeValeur(nom string) string {
	if v, ok := c.Valeurs[nom]; ok {
		return v
	}
	return exemplesDeValeur[nom]
}

// exemplesDeValeur donne, par nom de drapeau, une valeur qu'on pourrait
// réellement taper. Un drapeau porte le même sens d'une commande à l'autre
// (`--env` est toujours un environnement) : la table est donc partagée, et
// `Commande.Valeurs` ne sert qu'aux rares noms qui changent de sens.
//
// Un test vérifie que tout drapeau à valeur documenté en a un.
// #nosec G101 -- des exemples de chemins et de valeurs, aucun secret
var exemplesDeValeur = map[string]string{
	"env":                   "prod",
	"repo":                  "mon-depot",
	"playbook":              "play.yml",
	"inventory":             "inventories/prod/hosts",
	"run-ref":               "01JZQ8XKPT9V2H3M4N5P6Q7R8S",
	"limit":                 "web-01",
	"e":                     "version=1.2.3",
	"extra-vars":            "version=1.2.3",
	"become-user":           "postgres",
	"u":                     "alice",
	"user":                  "alice",
	"m":                     "shell",
	"module":                "shell",
	"a":                     `"systemctl status nginx"`,
	"args":                  `"systemctl status nginx"`,
	"subject":               "alice",
	"host":                  "db-01",
	"address":               "10.20.0.42",
	"port":                  "22",
	"login":                 "ansible",
	"group":                 "web",
	"f":                     "machines.yaml",
	"remote-url":            "https://forge.example.com/equipe/mon-depot.git",
	"mainline-ref":          "refs/heads/main",
	"remote":                "origin",
	"var":                   "db_password",
	"password-file":         "~/.secrets/warden",
	"current-password-file": "~/.secrets/ancien",
	"new-password-file":     "~/.secrets/nouveau",
	"pass-file":             "~/.secrets/ssh",
	"become-pass-file":      "~/.secrets/sudo",
	"key-file":              "~/.ssh/id_ed25519",
	"vault-file":            "~/.secrets/vault",
	"value-file":            "./secret.txt",
}
