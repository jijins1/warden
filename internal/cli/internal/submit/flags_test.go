package submit

import (
	"flag"
	"strings"
	"testing"
)

func TestComposerRendUnOrdreStable(t *testing.T) {
	cas := []struct {
		nom  string
		f    FlagsAnsible
		veut string
	}{
		{"rien", FlagsAnsible{}, ""},
		{"check", FlagsAnsible{Check: true}, "--check"},
		{"diff", FlagsAnsible{Diff: true}, "--diff"},
		{"limit", FlagsAnsible{Limit: "web-01"}, "--limit=web-01"},
		// L'ordre ne suit PAS celui de la ligne de commande : c'est cette liste
		// qui est enregistrée dans `run.flags`, et deux runs identiques doivent
		// s'y lire identiques.
		{"les trois", FlagsAnsible{Check: true, Diff: true, Limit: "prod:!web-01"},
			"--check --diff --limit=prod:!web-01"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got, err := c.f.Composer()
			if err != nil {
				t.Fatalf("Composer : %v", err)
			}
			if strings.Join(got, " ") != c.veut {
				t.Errorf("got %q, veut %q", strings.Join(got, " "), c.veut)
			}
		})
	}
}

// Le refus a lieu sur le POSTE, avant la capture de l'arbre et le push : un
// `--limit` mal formé découvert après ce travail donnerait un message exact sur
// une cause que personne ne relierait (§5.7).
func TestComposerRefuseUnLimitMalForme(t *testing.T) {
	for _, limit := range []string{
		"@/tmp/liste", // ferait lire les machines dans un fichier du pod
		"web 01",
		"web\n--become",
		"web;rm -rf /",
		"$(hostname)",
	} {
		t.Run(limit, func(t *testing.T) {
			if _, err := (FlagsAnsible{Limit: limit}).Composer(); err == nil {
				t.Fatal("accepté")
			}
		})
	}
}

// Le champ est `omitempty` côté fil : un run sans flag ne doit pas envoyer `[]`
// et faire passer la requête par la validation des flags pour n'y rien trouver.
func TestEncoderNEnvoieRienQuandIlNYARien(t *testing.T) {
	brut, err := encoder(nil)
	if err != nil {
		t.Fatalf("encoder : %v", err)
	}
	if brut != nil {
		t.Fatalf("encoder(nil) = %s, voulu nil", brut)
	}

	brut, err = encoder([]string{"--check"})
	if err != nil {
		t.Fatalf("encoder : %v", err)
	}
	if string(brut) != `["--check"]` {
		t.Fatalf("encoder = %s", brut)
	}
}

func TestComposerLesExtraVars(t *testing.T) {
	f := FlagsAnsible{
		Check:     true,
		ExtraVars: []string{"version=1.2.3", "message=bonjour le monde", "vide="},
	}
	got, err := f.Composer()
	if err != nil {
		t.Fatalf("Composer : %v", err)
	}
	// Les extra-vars en DERNIER, et dans l'ordre de saisie : c'est celui dans
	// lequel Ansible les applique.
	veut := "--check --extra-vars=version=1.2.3 --extra-vars=message=bonjour le monde --extra-vars=vide="
	if strings.Join(got, " ") != veut {
		t.Errorf("got %q\nveut %q", strings.Join(got, " "), veut)
	}
}

func TestComposerRefuseUneExtraVarMalFormee(t *testing.T) {
	cas := []struct {
		nom  string
		vars []string
		dans string
	}{
		// Le refus qui compte : warden ne lit aucun fichier de variables, le
		// commit audité est la seule source.
		{"depuis un fichier", []string{"@vars.yml"}, "nom=valeur"},
		{"JSON", []string{`{"a":1}`}, "nom=valeur"},
		{"nom à tirets", []string{"mon-nom=x"}, "mon-nom"},
		{"nom commençant par un chiffre", []string{"1a=x"}, "1a"},
		{"jinja", []string{"v={{ lookup('pipe','id') }}"}, "Jinja"},
		{"saut de ligne", []string{"v=a\nb"}, "contrôle"},
		{"deux fois la même", []string{"v=1", "v=2"}, "deux fois"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			_, err := (FlagsAnsible{ExtraVars: c.vars}).Composer()
			if err == nil {
				t.Fatal("accepté")
			}
			if !strings.Contains(err.Error(), c.dans) {
				t.Errorf("message %q ne contient pas %q", err, c.dans)
			}
		})
	}
}

// Deux variables DIFFÉRENTES sont légitimes : c'est tout l'intérêt d'un drapeau
// répétable.
func TestDeuxExtraVarsDifferentesPassent(t *testing.T) {
	got, err := (FlagsAnsible{ExtraVars: []string{"a=1", "b=2"}}).Composer()
	if err != nil {
		t.Fatalf("Composer : %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %v", got)
	}
}

// Repetable est ce qui manque à `flag` : sans elle, `-e a=1 -e b=2` ne
// laisserait que la seconde.
func TestRepetableCollecteToutesLesOccurrences(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var vars Repetable
	fs.Var(&vars, "e", "")
	fs.Var(&vars, "extra-vars", "")

	if err := fs.Parse([]string{"-e", "a=1", "--extra-vars", "b=2", "-e", "c=3"}); err != nil {
		t.Fatalf("Parse : %v", err)
	}
	if strings.Join(vars, " ") != "a=1 b=2 c=3" {
		t.Errorf("got %v", vars)
	}
}
