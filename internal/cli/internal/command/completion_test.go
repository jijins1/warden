package command

import (
	"flag"
	"strings"
	"testing"
)

// flagsDeTest reconstitue, pour les tests de ce paquet, un tout petit
// équivalent de `flagsPour` du paquet `cli` — juste assez pour exercer
// `Localiser` sans dépendre d'un import cyclique vers `cli`.
func flagsDeTest(chemin []string) *flag.FlagSet {
	if len(chemin) == 0 {
		return nil
	}
	switch chemin[0] {
	case "submit":
		fs := flag.NewFlagSet("submit", flag.ContinueOnError)
		fs.String("env", "", "")
		fs.String("repo", "", "")
		fs.Bool("check", false, "")
		return fs
	case "host":
		fs := flag.NewFlagSet("host", flag.ContinueOnError)
		fs.String("env", "", "")
		fs.String("host", "", "")
		fs.String("f", "", "")
		fs.Bool("from-inventory", false, "")
		return fs
	default:
		return nil
	}
}

func TestLocaliserDescendLArbreEnSautantLesDrapeaux(t *testing.T) {
	racine := &Commande{
		Nom: "warden",
		Sous: []Commande{
			{Nom: "submit", Flags: []string{"env", "repo", "check"}},
			{
				Nom: "host",
				Sous: []Commande{
					{Nom: "declare", Flags: []string{"f", "env", "from-inventory"}},
					{Nom: "forget", Flags: []string{"env", "host"}},
				},
			},
		},
	}

	cas := []struct {
		nom          string
		avant        []string
		cheminAttend string
	}{
		{"racine, rien avant", nil, ""},
		{"une feuille directe", []string{"submit"}, "submit"},
		{"un drapeau à valeur saute son argument", []string{"submit", "--env", "prod"}, "submit"},
		{"un drapeau booléen ne saute rien", []string{"submit", "--check"}, "submit"},
		{"un groupe puis un verbe", []string{"host", "declare"}, "host declare"},
		{"un verbe puis un drapeau à valeur", []string{"host", "declare", "--env", "prod"}, "host declare"},
		{"verbe inconnu : on reste au groupe", []string{"host", "truc"}, "host"},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			noeud, chemin, _ := racine.Localiser(c.avant, flagsDeTest)
			if got := strings.Join(chemin, " "); got != c.cheminAttend {
				t.Errorf("chemin = %q, attendu %q", got, c.cheminAttend)
			}
			if noeud == nil {
				t.Fatal("nœud nil")
			}
		})
	}
}

func TestLocaliserRendLeFlagSetDuCheminAtteint(t *testing.T) {
	racine := &Commande{
		Nom: "warden",
		Sous: []Commande{
			{
				Nom: "host",
				Sous: []Commande{
					{Nom: "declare", Flags: []string{"f", "env", "from-inventory"}},
					{Nom: "forget", Flags: []string{"env", "host"}},
				},
			},
		},
	}

	_, _, fs := racine.Localiser([]string{"host", "forget"}, flagsDeTest)
	if fs == nil {
		t.Fatal("aucun FlagSet rendu")
	}
	if fs.Lookup("env") == nil {
		t.Error("--env absent du FlagSet de host")
	}
}

// Le troisième tiroir de la complétion : un verbe ne propose QUE SES
// drapeaux, pas ceux de ses frères — même exigence que
// `TestLaPageDUnVerbeNeMontreQueSesDrapeaux` dans `cli`.
func TestCandidatsDrapeauxNeMontreQueLesSiensPropres(t *testing.T) {
	declare := Commande{Nom: "declare", Flags: []string{"f", "env", "from-inventory"}}
	forget := Commande{Nom: "forget", Flags: []string{"env", "host"}}

	fs := flagsDeTest([]string{"host"})

	candidats := forget.CandidatsDrapeaux(fs)
	attendus := map[string]bool{"--env": true, "--host": true}
	if len(candidats) != len(attendus) {
		t.Fatalf("candidats = %v, attendu %v", candidats, attendus)
	}
	for _, c := range candidats {
		if !attendus[c] {
			t.Errorf("%q ne devrait pas figurer parmi les drapeaux de forget", c)
		}
	}

	for _, c := range candidats {
		if c == "--from-inventory" || c == "-f" {
			t.Errorf("%q appartient à declare, pas à forget : %v", c, candidats)
		}
	}
	_ = declare
}

func TestCandidatsDrapeauxSansFlagSetNeCraquePas(t *testing.T) {
	c := Commande{Nom: "x", Flags: []string{"env"}}
	if got := c.CandidatsDrapeaux(nil); got != nil {
		t.Errorf("attendu nil, obtenu %v", got)
	}
}

func TestCandidatsDrapeauxIgnoreUnDrapeauNonDeclareDansLeFlagSet(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.String("env", "", "")

	c := Commande{Nom: "x", Flags: []string{"env", "fantome"}}
	candidats := c.CandidatsDrapeaux(fs)
	if len(candidats) != 1 || candidats[0] != "--env" {
		t.Errorf("candidats = %v, attendu [--env]", candidats)
	}
}
