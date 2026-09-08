package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValiderFlagsAccepteLesTrois(t *testing.T) {
	cas := [][]string{
		{"--check"},
		{"--diff"},
		{"--check", "--diff"},
		{"--limit=web-01"},
		{"--limit=web,db"},
		{"--limit=prod:!web-01"},
		{"--limit=~web.*"},
		{"--limit=web[0:5]"},
		{"--limit=tag_role_web&prod"},
		{"--check", "--diff", "--limit=web-01.exemple.net"},
		// `--extra-vars` est le seul répétable : poser trois variables s'écrit
		// `-e a=1 -e b=2 -e c=3` et pas autrement.
		{"--extra-vars=version=1.2.3"},
		{"--extra-vars=version=1.2.3", "--extra-vars=couleur=bleu"},
		// La VALEUR d'une extra-var est libre : espaces, ponctuation, URL. Seuls
		// le Jinja et les caractères de contrôle sont refusés.
		{"--extra-vars=message=bonjour le monde"},
		{"--extra-vars=url=https://exemple.net/a?b=c&d=e"},
		{"--extra-vars=vide="},
		{"--check", "--extra-vars=version=1"},
		{"--become"},
		{"--become", "--become-user=postgres"},
		{"--become", "--become-user=www-data"},
		{"--become", "--become-user=deploy.ci"},
		{"--check", "--become", "--limit=web-01"},
	}
	for _, flags := range cas {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			if err := ValiderFlags(flags); err != nil {
				t.Fatalf("refusé : %v", err)
			}
		})
	}
}

func TestValiderFlagsRefuse(t *testing.T) {
	cas := []struct {
		nom   string
		flags []string
		// dans est un fragment attendu du message : un refus qui ne dit pas
		// lequel des drapeaux pose problème envoie relire la ligne entière.
		dans string
	}{
		{"drapeau hors liste", []string{"--vault-password-file=/x"}, "--vault-password-file"},
		{"extra-vars", []string{"-e", "cible=prod"}, "-e"},
		// Le refus qui compte : `@fichier` fait lire la liste des machines dans
		// le pod au lieu du commit audité.
		{"limit depuis un fichier", []string{"--limit=@/tmp/liste"}, "@/tmp/liste"},
		{"limit vide", []string{"--limit="}, "attend une valeur"},
		{"limit sans valeur", []string{"--limit"}, "attend une valeur"},
		{"limit avec espace", []string{"--limit=web 01"}, "web 01"},
		{"limit avec saut de ligne", []string{"--limit=web\n--become"}, "motif refusé"},
		{"check avec valeur", []string{"--check=oui"}, "n'attend pas de valeur"},
		{"deux fois le même", []string{"--check", "--check"}, "deux fois"},
		{"deux limit", []string{"--limit=a", "--limit=b"}, "deux fois"},
		{"vide", []string{""}, "refusé"},
		{"trop long", []string{"--limit=" + strings.Repeat("a", longueurMaxValeur+1)}, "maximum"},

		// `-e @fichier` s'attrape faute de `=` : c'est le refus qui compte, il
		// ferait lire les variables dans un fichier du pod.
		{"extra-vars depuis un fichier", []string{"--extra-vars=@/tmp/vars.yml"}, "nom=valeur"},
		{"extra-vars en JSON", []string{`--extra-vars={"a":1}`}, "nom=valeur"},
		{"nom d'extra-var invalide", []string{"--extra-vars=mon-nom=x"}, "mon-nom"},
		{"nom d'extra-var commençant par un chiffre", []string{"--extra-vars=1a=x"}, "1a"},
		{"valeur jinja", []string{"--extra-vars=v={{ lookup('pipe','id') }}"}, "Jinja"},
		{"valeur avec saut de ligne", []string{"--extra-vars=v=a\nb"}, "contrôle"},
		// Deux fois la MÊME variable : Ansible garderait la dernière, l'audit les
		// deux. Deux variables différentes sont en revanche légitimes.
		{"deux fois la même extra-var",
			[]string{"--extra-vars=v=1", "--extra-vars=v=2"}, "deux fois"},

		// `--become-user` SEUL n'élève rien : le laisser passer produirait un run
		// sous le mauvais compte, sans qu'aucun message ne le dise.
		{"become-user sans become", []string{"--become-user=postgres"}, "sans --become"},
		{"become avec valeur", []string{"--become=oui"}, "n'attend pas de valeur"},
		{"compte avec espace", []string{"--become", "--become-user=post gres"}, "nom de compte"},
		{"compte avec métacaractère", []string{"--become", "--become-user=a;id"}, "nom de compte"},
		{"compte vide", []string{"--become", "--become-user="}, "attend une valeur"},
		{"deux become", []string{"--become", "--become"}, "deux fois"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			err := ValiderFlags(c.flags)
			if err == nil {
				t.Fatal("accepté, alors que l'allowlist devait refuser")
			}
			if !strings.Contains(err.Error(), c.dans) {
				t.Errorf("message %q ne contient pas %q", err, c.dans)
			}
		})
	}
}

// Le message d'un drapeau inconnu NOMME ce qui est accepté : une liste fermée
// dont on ne publie pas le contenu envoie chercher dans un code source.
func TestUnRefusNommeLaListeAcceptee(t *testing.T) {
	err := ValiderFlags([]string{"--tags=deploy"})
	if err == nil {
		t.Fatal("--tags accepté")
	}
	for _, attendu := range []string{"--check", "--diff", "--limit=<valeur>"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("le refus ne nomme pas %s : %v", attendu, err)
		}
	}
}

func TestFlagsDemandes(t *testing.T) {
	cas := []struct {
		nom    string
		brut   string
		veut   []string
		refuse bool
	}{
		{nom: "absent", brut: "", veut: nil},
		{nom: "tableau vide", brut: `[]`, veut: nil},
		{nom: "les trois", brut: `["--check","--diff","--limit=web"]`,
			veut: []string{"--check", "--diff", "--limit=web"}},
		// Un OBJET est refusé À L'ENTRÉE, alors que la relecture le tolère : la
		// tolérance ne vaut que pour les lignes écrites avant ce contrôle.
		{nom: "objet", brut: `{}`, refuse: true},
		{nom: "nombre", brut: `3`, refuse: true},
		{nom: "tableau de nombres", brut: `[1,2]`, refuse: true},
		{nom: "tableau imbriqué", brut: `[["--check"]]`, refuse: true},
		{nom: "drapeau hors liste", brut: `["--vault-password-file=/x"]`, refuse: true},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got, err := FlagsDemandes(json.RawMessage(c.brut))
			if c.refuse {
				if err == nil {
					t.Fatalf("accepté : %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("refusé : %v", err)
			}
			if strings.Join(got, " ") != strings.Join(c.veut, " ") {
				t.Errorf("got %v, veut %v", got, c.veut)
			}
		})
	}
}

// La relecture doit survivre au défaut du schéma initial, qui est un OBJET.
// Sans cette tolérance, chaque run déjà en base deviendrait indélivrable.
func TestFlagsEnregistresTolereLesFormesHistoriques(t *testing.T) {
	for _, brut := range []string{"", "{}", "null", `{"a":1}`} {
		got, err := FlagsEnregistres([]byte(brut))
		if err != nil {
			t.Errorf("%s : %v", brut, err)
		}
		if got != nil {
			t.Errorf("%s : %v, voulu nil", brut, got)
		}
	}
}

// Elle REVALIDE en revanche le contenu d'un tableau : c'est le dernier point
// avant que ces chaînes deviennent l'argv d'un processus du domaine hostile.
func TestFlagsEnregistresRevalideLeContenu(t *testing.T) {
	if _, err := FlagsEnregistres([]byte(`["--check"]`)); err != nil {
		t.Fatalf("--check refusé à la relecture : %v", err)
	}
	if _, err := FlagsEnregistres([]byte(`["--extra-vars=@/etc/passwd"]`)); err == nil {
		t.Fatal("une ligne écrite avant l'allowlist est repartie telle quelle")
	}
}
