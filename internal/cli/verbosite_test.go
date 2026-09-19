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
