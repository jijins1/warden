package cli

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/command"
)

// chemins énumère tous les chemins de commande de l'arbre.
func chemins(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	var descendre func(prefixe []string, c *command.Commande)
	descendre = func(prefixe []string, c *command.Commande) {
		for i := range c.Sous {
			s := &c.Sous[i]
			chemin := append(append([]string{}, prefixe...), s.Nom)
			out = append(out, chemin)
			descendre(chemin, s)
		}
	}
	descendre(nil, command.Arbre())
	return out
}

// LE test de l'aide à tiroir : chaque nœud a de quoi remplir sa page.
//
// Sans lui, une commande ajoutée sans résumé apparaîtrait dans l'index comme une
// ligne vide — ce qui est pire que son absence, parce que ça ressemble à un bug
// d'affichage plutôt qu'à un oubli.
func TestChaqueCommandeADeQuoiSeDecrire(t *testing.T) {
	racine := command.Arbre()
	for _, chemin := range chemins(t) {
		t.Run(strings.Join(chemin, " "), func(t *testing.T) {
			noeud, n := racine.Trouver(chemin)
			if n != len(chemin) {
				t.Fatalf("chemin non résolu : %v", chemin)
			}
			if noeud.Resume == "" {
				t.Error("aucun résumé : la commande apparaîtrait vide dans l'index")
			}
			// Un GROUPE n'a pas d'usage propre : ses verbes en ont un. Une
			// feuille doit en avoir un.
			if len(noeud.Sous) == 0 && len(noeud.Usage) == 0 {
				t.Error("aucun usage : la page ne dirait pas comment l'invoquer")
			}
		})
	}
}

// Les drapeaux nommés par l'arbre existent dans le FlagSet réel.
//
// C'est la moitié qui empêche l'aide de MENTIR : documenter `--group` sur une
// commande qui ne l'accepte pas enverrait quelqu'un taper une invocation
// refusée.
func TestChaqueDrapeauDocumenteExiste(t *testing.T) {
	racine := command.Arbre()
	for _, chemin := range chemins(t) {
		noeud, _ := racine.Trouver(chemin)
		fs := flagsPour(chemin)
		for _, nom := range noeud.Flags {
			if fs == nil {
				t.Errorf("%s documente %q mais n'a aucun FlagSet",
					strings.Join(chemin, " "), nom)
				continue
			}
			if fs.Lookup(nom) == nil {
				t.Errorf("%s documente %q, que le FlagSet ne déclare pas",
					strings.Join(chemin, " "), nom)
			}
		}
	}
}

// Les drapeaux du FlagSet réel sont nommés par au moins un verbe.
//
// L'autre moitié, et la plus utile des deux : elle attrape le drapeau AJOUTÉ au
// code sans être documenté, qui est le cas fréquent — on ajoute une option, on
// oublie l'aide, et personne ne découvre jamais l'option.
func TestChaqueDrapeauReelEstDocumente(t *testing.T) {
	racine := command.Arbre()

	// Pour chaque groupe, l'union des drapeaux nommés par ses verbes.
	for _, sommet := range racine.Verbes() {
		fs := flagsPour([]string{sommet})
		if fs == nil {
			continue
		}

		noeud, _ := racine.Trouver([]string{sommet})
		documentes := map[string]bool{}
		for _, nom := range noeud.Flags {
			documentes[nom] = true
		}
		for i := range noeud.Sous {
			for _, nom := range noeud.Sous[i].Flags {
				documentes[nom] = true
			}
		}

		fs.VisitAll(func(f *flag.Flag) {
			if !documentes[f.Name] {
				t.Errorf("warden %s accepte --%s, qu'aucune page d'aide ne nomme",
					sommet, f.Name)
			}
		})
	}
}

// L'aide demandée est une commande qui a RÉUSSI : elle sort sur stdout et rend
// nil. C'est le changement délibéré par rapport à l'ancienne implémentation, qui
// rendait l'usage sous forme d'erreur dans tous les cas.
func TestUneAideDemandeeNEstPasUneErreur(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"},
		{"help", "host"}, {"host", "--help"},
		{"help", "host", "declare"}, {"host", "declare", "--help"},
		{"host", "declare", "--env", "prod", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			if err := aide(&out, args); err != nil {
				t.Fatalf("aide: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("aucune sortie")
			}
		})
	}
}

// Le troisième tiroir montre les drapeaux DU VERBE, et pas ceux de ses frères.
//
// C'est tout l'intérêt du tiroir : `warden host forget` n'accepte pas de fichier
// de déclaration, et lui en montrer un enverrait quelqu'un l'essayer.
func TestLaPageDUnVerbeNeMontreQueSesDrapeaux(t *testing.T) {
	var out bytes.Buffer
	if err := aide(&out, []string{"host", "forget", "--help"}); err != nil {
		t.Fatalf("aide: %v", err)
	}
	rendu := out.String()

	for _, attendu := range []string{"--env", "--host"} {
		if !strings.Contains(rendu, attendu) {
			t.Errorf("%s manque :\n%s", attendu, rendu)
		}
	}
	for _, absent := range []string{"--from-inventory", "-f "} {
		if strings.Contains(rendu, absent) {
			t.Errorf("%s figure sur la page de forget :\n%s", absent, rendu)
		}
	}
}

// L'index ne montre PAS les drapeaux : c'est ce qui le rend lisible, et ce que
// l'ancienne constante de cent lignes ne faisait pas.
func TestLIndexResteCourt(t *testing.T) {
	var out bytes.Buffer
	command.Arbre().Index(&out)

	if n := strings.Count(out.String(), "\n"); n > 40 {
		t.Errorf("l'index fait %d lignes : il redevient un mur", n)
	}
	if strings.Contains(out.String(), "--env") {
		t.Errorf("l'index montre des drapeaux :\n%s", out.String())
	}
}

// Une faute de frappe est signalée comme telle, avec la suggestion.
func TestUneFauteDeFrappeEstSuggeree(t *testing.T) {
	cas := map[string]struct {
		chemin []string
		saisi  string
		attend string
	}{
		"transposition":     {nil, "hots", "host"},
		"lettre manquante":  {[]string{"host"}, "declar", "declare"},
		"abréviation":       {[]string{"key"}, "lst", "list"},
		"verbe d'un groupe": {[]string{"user"}, "ad", "add"},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			err := command.Inconnue(c.chemin, c.saisi)
			if err == nil {
				t.Fatal("aucune erreur")
			}
			if !strings.Contains(err.Error(), c.attend) {
				t.Errorf("%q n'est pas suggéré : %v", c.attend, err)
			}
		})
	}
}

// Sans voisin proche, on liste plutôt que de suggérer au hasard.
func TestSansVoisinOnListe(t *testing.T) {
	err := command.Inconnue(nil, "quelquechosedecompletementautre")
	if err == nil {
		t.Fatal("aucune erreur")
	}
	if strings.Contains(err.Error(), "Vouliez-vous dire") {
		t.Errorf("une suggestion a été inventée : %v", err)
	}
	if !strings.Contains(err.Error(), "submit") {
		t.Errorf("les commandes ne sont pas listées : %v", err)
	}
}
