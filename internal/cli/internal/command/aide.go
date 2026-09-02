package command

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Demandee dit si les arguments réclament de l'aide plutôt qu'une action.
//
// N'importe où dans la ligne, et pas seulement en tête : `warden host declare
// --env prod --help` est ce que quelqu'un tape quand il bute au milieu d'une
// commande, et lui répondre par une erreur de drapeau serait une réponse à côté.
func Demandee(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return true
		}
		// `--` arrête l'interprétation : ce qui suit appartient à la commande.
		if a == "--" {
			return false
		}
	}
	return false
}

// SansAide rend les arguments débarrassés des marqueurs d'aide, pour qu'on
// puisse retrouver DE QUOI l'aide était demandée.
func SansAide(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			continue
		}
		// Un drapeau et sa valeur ne nomment pas une commande : seul ce qui ne
		// commence pas par un tiret peut être un verbe.
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Afficher rend la page correspondant à un chemin de commande.
//
// `flagsDe` fournit le FlagSet d'un chemin donné. Une fonction plutôt qu'une
// valeur : construire tous les FlagSets pour n'en afficher qu'un exécuterait le
// câblage de commandes qu'on ne lance pas.
func Afficher(w io.Writer, chemin []string, flagsDe func([]string) *flag.FlagSet) {
	racine := Arbre()
	noeud, n := racine.Trouver(chemin)

	if noeud == racine {
		racine.Index(w)
		if len(chemin) > 0 {
			// L'aide a été demandée pour quelque chose qui n'existe pas. Le dire
			// APRÈS l'index : ce qui manque à l'utilisateur est la liste, pas le
			// constat.
			_, _ = fmt.Fprintf(w, "\n(« %s » n'est pas une commande)\n", chemin[0])
		}
		return
	}

	atteint := chemin[:n]
	var fs *flag.FlagSet
	if flagsDe != nil {
		fs = flagsDe(atteint)
	}
	noeud.Page(w, atteint, fs)

	if n < len(chemin) {
		_, _ = fmt.Fprintf(w, "\n(« %s » n'est pas une sous-commande de « warden %s »)\n",
			chemin[n], strings.Join(atteint, " "))
	}
}

// Inconnue compose l'erreur d'une commande qui n'existe pas.
//
// Elle SUGGÈRE : une faute de frappe est la cause la plus fréquente, et proposer
// le voisin le plus proche évite d'aller relire une liste pour un caractère.
func Inconnue(chemin []string, saisi string) error {
	racine := Arbre()
	noeud, _ := racine.Trouver(chemin)

	candidats := noeud.Verbes()
	if noeud == racine {
		candidats = racine.Verbes()
	}

	ou := "warden"
	if len(chemin) > 0 {
		ou = "warden " + strings.Join(chemin, " ")
	}

	if proche := plusProche(saisi, candidats); proche != "" {
		return fmt.Errorf("%q n'est pas une commande de « %s ». Vouliez-vous dire %q ?\n"+
			"« %s --help » les liste toutes.", saisi, ou, proche, ou)
	}
	sort.Strings(candidats)
	return fmt.Errorf("%q n'est pas une commande de « %s ». Il y a : %s.",
		saisi, ou, strings.Join(candidats, ", "))
}

// Manquante compose l'erreur d'un groupe invoqué sans verbe.
func Manquante(chemin []string) error {
	racine := Arbre()
	noeud, _ := racine.Trouver(chemin)
	ou := strings.Join(chemin, " ")
	return fmt.Errorf("« warden %s » attend une sous-commande : %s.\n"+
		"« warden help %s » les décrit.", ou, strings.Join(noeud.Verbes(), ", "), ou)
}

// plusProche rend le candidat à distance d'édition 1 ou 2, s'il est unique.
//
// Unique, et c'est la condition qui compte : deux suggestions ne sont pas une
// suggestion, et proposer au hasard entre `list` et `disable` ferait perdre plus
// de temps que de n'en proposer aucune.
func plusProche(saisi string, candidats []string) string {
	seuil := 2
	if len(saisi) <= 4 {
		// Sur un mot court, deux corrections en changent le sens : `add` et
		// `list` sont à distance 4, mais `add` et `all` à distance 1.
		seuil = 1
	}

	var trouve string
	for _, c := range candidats {
		if distance(saisi, c) > seuil {
			continue
		}
		if trouve != "" {
			return ""
		}
		trouve = c
	}
	return trouve
}

// distance est la distance de Damerau-Levenshtein.
//
// Damerau et non Levenshtein simple : elle compte une TRANSPOSITION pour une
// seule opération. C'est ce qui compte ici, parce que la transposition est la
// faute de frappe la plus courante — mesuré sur le cas qui a motivé ce choix,
// « hots » pour « host », que Levenshtein place à distance 2 et écarte donc du
// seuil des mots courts.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)

	// Trois lignes de travail : la transposition regarde deux rangs en arrière.
	avantPrecedent := make([]int, len(rb)+1)
	precedent := make([]int, len(rb)+1)
	courant := make([]int, len(rb)+1)
	for j := range precedent {
		precedent[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		courant[0] = i
		for j := 1; j <= len(rb); j++ {
			cout := 1
			if ra[i-1] == rb[j-1] {
				cout = 0
			}
			courant[j] = min3(courant[j-1]+1, precedent[j]+1, precedent[j-1]+cout)

			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if t := avantPrecedent[j-2] + 1; t < courant[j] {
					courant[j] = t
				}
			}
		}
		avantPrecedent, precedent, courant = precedent, courant, avantPrecedent
	}
	return precedent[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// VerifierVerbe refuse un verbe inconnu AVANT que la commande ne travaille.
//
// L'ordre porte la correction : une faute de frappe doit être signalée comme
// telle, et non comme le premier obstacle que la commande rencontre ensuite.
// Mesuré : `warden host declar` répondait « WARDEN_SERVER doit être définie »,
// parce que le client était construit avant que le verbe soit lu.
func VerifierVerbe(chemin []string, verbe string) error {
	noeud, _ := Arbre().Trouver(chemin)
	if noeud.sous(verbe) == nil {
		return Inconnue(chemin, verbe)
	}
	return nil
}
