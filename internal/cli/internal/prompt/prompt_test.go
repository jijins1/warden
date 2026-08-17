// Étage 1 de l'ADR-0010 : aucune E/S hors mémoire.
//
// L'extinction de l'écho, elle, ne se vérifie que sur un vrai terminal : c'est
// l'objet de `prompt_pty_test.go`, séparé parce qu'il dépend de Linux.

package prompt_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

const valeur = "hunter2-tres-secret"

// Les trois chemins d'impression par lesquels un secret arrive dans une trace
// sans que personne l'ait voulu.
func TestLaValeurEstMasqueeALImpression(t *testing.T) {
	t.Parallel()

	s := prompt.Secret(valeur)

	// Une structure englobante : c'est la forme réelle du risque, personne
	// n'imprime un secret tout seul.
	type requete struct {
		Utilisateur string
		MotDePasse  prompt.Secret
	}
	r := requete{Utilisateur: "alice", MotDePasse: s}

	cas := map[string]string{
		// nolint:staticcheck // S1025 : appeler String() testerait autre chose.
		// Ce qu'on vérifie ici est le chemin du VERBE %s, c'est-à-dire la façon
		// dont un secret arrive réellement dans une trace — personne n'écrit
		// `String()` à la main dans un log.
		"%s sur la valeur":      fmt.Sprintf("%s", s),
		"%v sur la valeur":      fmt.Sprintf("%v", s),
		"%q sur la valeur":      fmt.Sprintf("%q", s),
		"%v sur la structure":   fmt.Sprintf("%v", r),
		"%+v sur la structure":  fmt.Sprintf("%+v", r),
		"%#v sur la structure":  fmt.Sprintf("%#v", r),
		"String() explicite":    s.String(),
		"GoString() explicite":  s.GoString(),
		"print d'une tranche":   fmt.Sprint([]prompt.Secret{s}),
		"map dont c'est valeur": fmt.Sprintf("%v", map[string]prompt.Secret{"pw": s}),
	}
	for nom, rendu := range cas {
		if strings.Contains(rendu, valeur) {
			t.Errorf("%s laisse fuir la valeur : %s", nom, rendu)
		}
	}
}

// L'asymétrie délibérée : la sérialisation N'EST PAS masquée, parce que le mot
// de passe de `become` doit pouvoir voyager. La masquer enverrait le marqueur à
// la place de la valeur, en silence.
func TestLaSerialisationNEstPasMasquee(t *testing.T) {
	t.Parallel()

	brut, err := json.Marshal(prompt.Secret(valeur))
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}

	var recu prompt.Secret
	if err := json.Unmarshal(brut, &recu); err != nil {
		t.Fatalf("désérialisation : %v", err)
	}
	if string(recu.Reveal()) != valeur {
		t.Errorf("aller-retour altéré : %q", recu.Reveal())
	}
	// Et elle ne transite pas en clair dans le JSON : `[]byte` s'encode en
	// base64, ce qui n'est pas une protection mais évite qu'une valeur
	// multi-ligne casse le document.
	if strings.Contains(string(brut), valeur) {
		t.Errorf("la valeur apparaît telle quelle dans le JSON : %s", brut)
	}
}

func TestWipeEcraseLaValeur(t *testing.T) {
	t.Parallel()

	s := prompt.Secret(valeur)
	s.Wipe()

	for i, b := range s.Reveal() {
		if b != 0 {
			t.Fatalf("octet %d non écrasé", i)
		}
	}
}

// Sans terminal, Ask ÉCHOUE. Sans cette règle, un `warden … < fichier` lirait la
// première ligne du fichier comme un mot de passe, et personne ne s'en
// apercevrait.
func TestSansTerminalLaSaisieEchoue(t *testing.T) {
	t.Parallel()

	// Sous `go test`, l'entrée standard n'est pas un terminal.
	_, err := prompt.AskStdin(t.Context(), "mot de passe : ")
	if !errors.Is(err, prompt.ErrPasDeTerminal) {
		t.Fatalf("erreur = %v, attendu ErrPasDeTerminal", err)
	}

	if _, err := prompt.Ask(t.Context(), nil, "mot de passe : "); !errors.Is(err, prompt.ErrPasDeTerminal) {
		t.Errorf("erreur sur un terminal nul = %v, attendu ErrPasDeTerminal", err)
	}
}

func TestReadLine(t *testing.T) {
	t.Parallel()

	cas := map[string]struct {
		entree  string
		attendu string
		err     error
	}{
		"ligne simple":            {"hunter2\n", "hunter2", nil},
		"sans saut final":         {"hunter2", "hunter2", nil},
		"fin de ligne Windows":    {"hunter2\r\n", "hunter2", nil},
		"seule la première ligne": {"hunter2\nautre chose\n", "hunter2", nil},
		"espaces conservés":       {"  deux espaces  \n", "  deux espaces  ", nil},
		"ligne vide":              {"\n", "", prompt.ErrVide},
		"entrée vide":             {"", "", prompt.ErrVide},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			s, err := prompt.ReadLine(strings.NewReader(c.entree))
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("erreur = %v, attendu %v", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadLine: %v", err)
			}
			if string(s.Reveal()) != c.attendu {
				t.Errorf("valeur = %q, attendu %q", s.Reveal(), c.attendu)
			}
		})
	}
}

// Le multi-ligne, pour une clé privée. Une invite sans écho conviendrait mal :
// l'utilisateur ne verrait rien de ce qu'il colle.
func TestReadAllGardeLeMultiLigne(t *testing.T) {
	t.Parallel()

	const pem = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----\n"

	s, err := prompt.ReadAll(strings.NewReader(pem))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// Le saut de ligne FINAL est retiré, les intermédiaires sont gardés : une
	// clé amputée d'une ligne est une clé illisible.
	if string(s.Reveal()) != strings.TrimRight(pem, "\n") {
		t.Errorf("valeur = %q", s.Reveal())
	}
	if strings.Count(string(s.Reveal()), "\n") != 3 {
		t.Errorf("les sauts de ligne internes n'ont pas survécu : %q", s.Reveal())
	}
}

// Le plafond refuse, il ne tronque pas : une clé privée tronquée en silence
// serait acceptée à l'import et échouerait à la première connexion, très loin
// de la cause.
func TestAuDelaDuPlafondOnRefuseAuLieuDeTronquer(t *testing.T) {
	t.Parallel()

	enorme := strings.Repeat("x", 64<<10+1)

	for nom, lire := range map[string]func() (prompt.Secret, error){
		"ReadAll":  func() (prompt.Secret, error) { return prompt.ReadAll(strings.NewReader(enorme)) },
		"ReadLine": func() (prompt.Secret, error) { return prompt.ReadLine(strings.NewReader(enorme)) },
	} {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			if _, err := lire(); err == nil {
				t.Fatal("une saisie au-delà du plafond a été acceptée")
			}
		})
	}
}

func TestUneSourceNulleEstRefusee(t *testing.T) {
	t.Parallel()

	if _, err := prompt.ReadLine(nil); err == nil {
		t.Error("ReadLine(nil) accepté")
	}
	if _, err := prompt.ReadAll(nil); err == nil {
		t.Error("ReadAll(nil) accepté")
	}
}
