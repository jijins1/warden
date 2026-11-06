package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrCronInvalide est rendue pour une expression cron ou un fuseau refusés.
var ErrCronInvalide = errors.New("expression cron invalide")

// ExpressionCron est une expression cron à cinq champs, interprétée dans un
// fuseau nommé (ADR-0057).
//
// Écrite ici plutôt qu'importée : le besoin est borné — cinq champs, aucune
// seconde, aucune extension — et une centaine de lignes testées coûtent moins
// qu'une dépendance de plus dans le binaire du control plane.
type ExpressionCron struct {
	source string
	lieu   *time.Location

	minutes, heures, joursDuMois, mois, joursDeSemaine uint64

	// Le jour du mois et le jour de semaine se combinent par OU quand les deux
	// sont restreints — c'est la règle de cron, contre-intuitive mais attendue
	// par quiconque en a déjà écrit un. Un `*` sur l'un des deux laisse l'autre
	// seul juge.
	domEtoile, dowEtoile bool
}

// Source rend l'expression telle qu'elle a été donnée.
func (e ExpressionCron) Source() string { return e.source }

// Fuseau rend le fuseau d'interprétation.
func (e ExpressionCron) Fuseau() *time.Location { return e.lieu }

var raccourcisCron = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// horizonCron borne la recherche de l'occurrence suivante. Au-delà, une
// expression est considérée comme ne se réalisant jamais : le 29 février tombe
// au pire tous les huit ans (1900, 2100), cinq ans n'y suffiraient pas.
const horizonCron = 9 * 366 * 24 * time.Hour

// AnalyserCron lit une expression cron et la lie à un fuseau.
//
// Le fuseau est OBLIGATOIRE et nommé : « 3 h » ne veut rien dire sans lui, et
// le déduire du serveur ferait changer l'heure d'un cron le jour où l'image
// change de base.
func AnalyserCron(expression, fuseau string) (ExpressionCron, error) {
	if fuseau == "" {
		return ExpressionCron{}, fmt.Errorf("%w : le fuseau est requis (par exemple Europe/Paris)", ErrCronInvalide)
	}
	lieu, err := time.LoadLocation(fuseau)
	if err != nil {
		return ExpressionCron{}, fmt.Errorf("%w : fuseau %q inconnu", ErrCronInvalide, fuseau)
	}

	brut := strings.TrimSpace(expression)
	if r, ok := raccourcisCron[brut]; ok {
		brut = r
	}
	champs := strings.Fields(brut)
	if len(champs) != 5 {
		return ExpressionCron{}, fmt.Errorf(
			"%w : %q — cinq champs attendus (minute heure jour mois jour-de-semaine)",
			ErrCronInvalide, expression)
	}

	e := ExpressionCron{source: strings.TrimSpace(expression), lieu: lieu}
	bornes := []struct {
		nom      string
		min, max int
		dest     *uint64
		etoile   *bool
	}{
		{"minute", 0, 59, &e.minutes, nil},
		{"heure", 0, 23, &e.heures, nil},
		{"jour du mois", 1, 31, &e.joursDuMois, &e.domEtoile},
		{"mois", 1, 12, &e.mois, nil},
		{"jour de semaine", 0, 7, &e.joursDeSemaine, &e.dowEtoile},
	}
	for i, b := range bornes {
		masque, etoile, err := analyserChamp(champs[i], b.min, b.max)
		if err != nil {
			return ExpressionCron{}, fmt.Errorf("%w : %s : %w", ErrCronInvalide, b.nom, err)
		}
		*b.dest = masque
		if b.etoile != nil {
			*b.etoile = etoile
		}
	}
	// 7 et 0 désignent tous deux dimanche.
	if e.joursDeSemaine&(1<<7) != 0 {
		e.joursDeSemaine |= 1
		e.joursDeSemaine &^= 1 << 7
	}

	// Une expression qui ne se réalise jamais (`0 0 30 2 *`) est refusée ICI :
	// acceptée, elle produirait un cron qui ne tire jamais, sans un mot.
	if e.Suivante(time.Now()).IsZero() {
		return ExpressionCron{}, fmt.Errorf("%w : %q ne se réalise jamais", ErrCronInvalide, expression)
	}
	return e, nil
}

// analyserChamp rend le masque de bits d'un champ, et s'il s'agit de `*` seul
// (avec ou sans pas).
func analyserChamp(champ string, min, max int) (uint64, bool, error) {
	var masque uint64
	etoile := false
	for _, morceau := range strings.Split(champ, ",") {
		if morceau == "" {
			return 0, false, fmt.Errorf("élément vide dans %q", champ)
		}
		plage, pasTexte, aUnPas := strings.Cut(morceau, "/")
		pas := 1
		if aUnPas {
			p, err := strconv.Atoi(pasTexte)
			if err != nil || p <= 0 {
				return 0, false, fmt.Errorf("pas %q invalide", pasTexte)
			}
			pas = p
		}

		var debut, fin int
		switch {
		case plage == "*":
			debut, fin = min, max
			if len(strings.Split(champ, ",")) == 1 && !aUnPas {
				etoile = true
			}
		case strings.Contains(plage, "-"):
			a, b, _ := strings.Cut(plage, "-")
			var err error
			if debut, err = valeurCron(a, min, max); err != nil {
				return 0, false, err
			}
			if fin, err = valeurCron(b, min, max); err != nil {
				return 0, false, err
			}
			if debut > fin {
				return 0, false, fmt.Errorf("plage %q à l'envers", plage)
			}
		default:
			v, err := valeurCron(plage, min, max)
			if err != nil {
				return 0, false, err
			}
			debut, fin = v, v
			if aUnPas {
				// `5/15` : de 5 jusqu'au bout, comme les crons usuels.
				fin = max
			}
		}
		for v := debut; v <= fin; v += pas {
			masque |= 1 << uint(v)
		}
	}
	return masque, etoile, nil
}

func valeurCron(texte string, min, max int) (int, error) {
	v, err := strconv.Atoi(texte)
	if err != nil {
		return 0, fmt.Errorf("%q n'est pas un nombre", texte)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%d hors de [%d, %d]", v, min, max)
	}
	return v, nil
}

func (e ExpressionCron) jourConvient(t time.Time) bool {
	dom := e.joursDuMois&(1<<uint(t.Day())) != 0
	dow := e.joursDeSemaine&(1<<uint(t.Weekday())) != 0
	switch {
	case e.domEtoile && e.dowEtoile:
		return true
	case e.domEtoile:
		return dow
	case e.dowEtoile:
		return dom
	default:
		return dom || dow
	}
}

// Suivante rend la première occurrence STRICTEMENT postérieure à `apres`, ou
// l'instant zéro si aucune n'existe dans l'horizon de recherche.
//
// La recherche avance sur l'HEURE MURALE du fuseau, par sauts de mois, de
// jour, d'heure puis de minute. Deux conséquences sur les changements d'heure,
// voulues :
//
//   - une occurrence tombée dans le trou de mars (2 h 30 à Paris n'existe pas)
//     glisse à la première minute valide qui suit ;
//   - une occurrence dans l'heure doublée d'octobre ne tire qu'UNE fois : la
//     condition « strictement après », en temps absolu, écarte la seconde
//     lecture de la même heure murale.
func (e ExpressionCron) Suivante(apres time.Time) time.Time {
	if e.lieu == nil {
		return time.Time{}
	}
	limite := apres.Add(horizonCron)
	t := apres.In(e.lieu).Truncate(time.Minute)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, e.lieu)

	for t.Before(limite) {
		if e.mois&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, e.lieu)
			continue
		}
		if !e.jourConvient(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, e.lieu)
			continue
		}
		if e.heures&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, e.lieu)
			continue
		}
		if e.minutes&(1<<uint(t.Minute())) == 0 || !t.After(apres) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, e.lieu)
			continue
		}
		return t
	}
	return time.Time{}
}
