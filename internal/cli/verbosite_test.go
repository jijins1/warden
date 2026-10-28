package cli

import "testing"

// `flag` ne sait pas répéter : `-v -v` réaffecterait le même booléen au lieu
// d'en compter deux. Les quatre noms sont donc déclarés séparément, comme chez
// `ansible-playbook`, et c'est `niveau` qui les réduit à un seul réglage.
func TestVerbositeRendLeNiveauLePlusEleve(t *testing.T) {
	cas := []struct {
		nom  string
		args []string
		veut int
	}{
		{"rien", nil, 0},
		{"un v", []string{"-v"}, 1},
		{"deux v", []string{"-vv"}, 2},
		{"quatre v", []string{"-vvvv"}, 4},
		// Tapés ensemble, le plus élevé l'emporte — une main qui hésite, pas une
		// contradiction. Ce qui part vers le serveur reste UN drapeau, et c'est
		// là-bas que deux niveaux simultanés sont refusés.
		{"v puis vvv", []string{"-v", "-vvv"}, 3},
		{"vvv puis v", []string{"-vvv", "-v"}, 3},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			fs, f := fsSubmit()
			if err := fs.Parse(append([]string{"--playbook", "p.yml"}, c.args...)); err != nil {
				t.Fatalf("Parse : %v", err)
			}
			if got := f.verbeux.niveau(); got != c.veut {
				t.Errorf("niveau = %d, veut %d", got, c.veut)
			}
		})
	}
}

// `warden run` porte les mêmes drapeaux que `warden submit` : un ad-hoc est un
// run de playbook auquel il ne manque que le playbook.
func TestVerbositeEstAussiSurRun(t *testing.T) {
	fs, f := fsRun()
	if err := fs.Parse([]string{"-m", "ping", "-vv"}); err != nil {
		t.Fatalf("Parse : %v", err)
	}
	if got := f.verbeux.niveau(); got != 2 {
		t.Errorf("niveau = %d, veut 2", got)
	}
}

// Le motif se donne EN TÊTE, comme chez `ansible` : les drapeaux qui le
// suivent doivent être lus, et non pris pour d'autres positionnels.
func TestRunAccepteLeMotifAvantLesDrapeaux(t *testing.T) {
	cas := map[string][]string{
		"motif en tête":   {"all", "-m", "ping", "-vv"},
		"motif au milieu": {"-m", "ping", "all", "-vv"},
		"motif en queue":  {"-m", "ping", "-vv", "all"},
	}
	for nom, args := range cas {
		t.Run(nom, func(t *testing.T) {
			fs, f := fsRun()
			positionnels, err := parserEntremele(fs, args)
			if err != nil {
				t.Fatalf("parse : %v", err)
			}
			if len(positionnels) != 1 || positionnels[0] != "all" {
				t.Errorf("positionnels = %v, veut [all]", positionnels)
			}
			if *f.module != "ping" || f.verbeux.niveau() != 2 {
				t.Errorf("module = %q, niveau = %d", *f.module, f.verbeux.niveau())
			}
		})
	}
}

// Après `--`, plus rien n'est un drapeau.
func TestParserEntremeleRespecteLeSeparateur(t *testing.T) {
	fs, f := fsRun()
	positionnels, err := parserEntremele(fs, []string{"-m", "ping", "--", "all", "-vv"})
	if err != nil {
		t.Fatalf("parse : %v", err)
	}
	if len(positionnels) != 2 || positionnels[1] != "-vv" || f.verbeux.niveau() != 0 {
		t.Errorf("positionnels = %v, niveau = %d", positionnels, f.verbeux.niveau())
	}
}
