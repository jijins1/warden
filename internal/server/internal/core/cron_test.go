package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

func paris(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSuivante(t *testing.T) {
	p := paris(t)
	cas := []struct {
		nom, expr string
		apres     time.Time
		attendu   time.Time
	}{
		{"tous les jours à 3 h, avant l'heure", "0 3 * * *",
			time.Date(2026, 11, 5, 1, 0, 0, 0, p), time.Date(2026, 11, 5, 3, 0, 0, 0, p)},
		{"tous les jours à 3 h, juste après", "0 3 * * *",
			time.Date(2026, 11, 5, 3, 0, 0, 0, p), time.Date(2026, 11, 6, 3, 0, 0, 0, p)},
		{"raccourci @daily", "@daily",
			time.Date(2026, 11, 5, 12, 0, 0, 0, p), time.Date(2026, 11, 6, 0, 0, 0, 0, p)},
		{"pas de 15 minutes", "*/15 * * * *",
			time.Date(2026, 11, 5, 12, 7, 30, 0, p), time.Date(2026, 11, 5, 12, 15, 0, 0, p)},
		{"jours ouvrés, un vendredi soir", "30 8 * * 1-5",
			time.Date(2026, 11, 6, 20, 0, 0, 0, p), time.Date(2026, 11, 9, 8, 30, 0, 0, p)},
		{"dimanche écrit 7", "0 0 * * 7",
			time.Date(2026, 11, 5, 0, 0, 0, 0, p), time.Date(2026, 11, 8, 0, 0, 0, 0, p)},
		{"jour du mois OU jour de semaine", "0 0 1 * 1",
			time.Date(2026, 11, 5, 0, 0, 0, 0, p), time.Date(2026, 11, 9, 0, 0, 0, 0, p)},
		{"fin d'année", "0 0 1 1 *",
			time.Date(2026, 12, 31, 23, 59, 0, 0, p), time.Date(2027, 1, 1, 0, 0, 0, 0, p)},
		{"29 février", "0 0 29 2 *",
			time.Date(2026, 3, 1, 0, 0, 0, 0, p), time.Date(2028, 2, 29, 0, 0, 0, 0, p)},
		{"liste", "0 6,18 * * *",
			time.Date(2026, 11, 5, 7, 0, 0, 0, p), time.Date(2026, 11, 5, 18, 0, 0, 0, p)},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			e, err := core.AnalyserCron(c.expr, "Europe/Paris")
			if err != nil {
				t.Fatal(err)
			}
			if got := e.Suivante(c.apres); !got.Equal(c.attendu) {
				t.Errorf("Suivante(%s) = %s, attendu %s", c.apres, got, c.attendu)
			}
		})
	}
}

// Le fuseau décide de l'instant : 3 h à Paris n'est pas 3 h UTC.
func TestSuivanteRespecteLeFuseau(t *testing.T) {
	e, err := core.AnalyserCron("0 3 * * *", "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	got := e.Suivante(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %s, attendu %s", got.UTC(), want)
	}
}

// L'heure doublée d'octobre ne fait tirer qu'une fois.
func TestHeureDoubleeNeTireQuUneFois(t *testing.T) {
	p := paris(t)
	e, err := core.AnalyserCron("30 2 * * *", "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	// 25 octobre 2026 : à 3 h CEST, on revient à 2 h CET.
	premiere := e.Suivante(time.Date(2026, 10, 25, 0, 0, 0, 0, p))
	if premiere.Day() != 25 || premiere.Hour() != 2 || premiere.Minute() != 30 {
		t.Fatalf("première occurrence : %s", premiere)
	}
	seconde := e.Suivante(premiere)
	if seconde.Day() != 26 {
		t.Errorf("l'heure doublée a tiré deux fois : %s puis %s", premiere, seconde)
	}
}

// Le trou de mars saute l'occurrence de ce jour-là, sans boucler.
func TestTrouDeMarsSauteLOccurrence(t *testing.T) {
	p := paris(t)
	e, err := core.AnalyserCron("30 2 * * *", "Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	// 29 mars 2026 : 2 h CET devient 3 h CEST, 2 h 30 n'existe pas.
	got := e.Suivante(time.Date(2026, 3, 29, 0, 0, 0, 0, p))
	if want := time.Date(2026, 3, 30, 2, 30, 0, 0, p); !got.Equal(want) {
		t.Errorf("got %s, attendu %s", got, want)
	}
}

func TestAnalyserCronRefuse(t *testing.T) {
	cas := []struct{ nom, expr, fuseau string }{
		{"fuseau absent", "0 3 * * *", ""},
		{"fuseau inconnu", "0 3 * * *", "Mars/Olympus"},
		{"quatre champs", "0 3 * *", "UTC"},
		{"six champs", "0 0 3 * * *", "UTC"},
		{"minute hors borne", "60 3 * * *", "UTC"},
		{"heure hors borne", "0 24 * * *", "UTC"},
		{"jour zéro", "0 0 0 * *", "UTC"},
		{"plage à l'envers", "0 5-3 * * *", "UTC"},
		{"pas nul", "*/0 * * * *", "UTC"},
		{"texte", "zéro 3 * * *", "UTC"},
		{"élément vide", "0 3,,4 * * *", "UTC"},
		{"ne se réalise jamais", "0 0 30 2 *", "UTC"},
		{"raccourci inconnu", "@reboot", "UTC"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if _, err := core.AnalyserCron(c.expr, c.fuseau); !errors.Is(err, core.ErrCronInvalide) {
				t.Errorf("AnalyserCron(%q, %q) = %v, attendu ErrCronInvalide", c.expr, c.fuseau, err)
			}
		})
	}
}
