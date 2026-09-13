package runref

import (
	"regexp"
	"strings"
	"testing"
)

// serverShape is the SAME expression the server validates with, copied rather
// than imported.
//
// ADR-0011 forbids the CLI from importing the control plane: they are distinct
// trust domains, and the binary on the user's machine is readable by an
// adversary. The duplication is therefore deliberate — and this test is what
// keeps the two copies from drifting, since a drift would surface as every run
// being refused with "run_ref must be a 26-character ULID".
var serverShape = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)

func TestUnRunRefEstAccepteParLeServeur(t *testing.T) {
	for range 200 {
		ref, err := New()
		if err != nil {
			t.Fatalf("New : %v", err)
		}
		if !serverShape.MatchString(ref) {
			t.Fatalf("le serveur refuserait %q", ref)
		}
	}
}

// TestLesRunRefsSontDistincts : 80 bits d'aléa, donc une collision serait un
// défaut de génération et non de la malchance.
func TestLesRunRefsSontDistincts(t *testing.T) {
	vus := make(map[string]bool, 1000)
	for range 1000 {
		ref, err := New()
		if err != nil {
			t.Fatalf("New : %v", err)
		}
		if vus[ref] {
			t.Fatalf("identifiant produit deux fois : %s", ref)
		}
		vus[ref] = true
	}
}

// TestLesRunRefsSeTrientParDate épingle la propriété qui rend une liste de refs
// lisible sans rien joindre : le préfixe temporel.
func TestLesRunRefsSeTrientParDate(t *testing.T) {
	premier, err := New()
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	// Le composant temporel est en millisecondes ; sans attente les deux
	// identifiants partagent le même horodatage et l'ordre n'est plus garanti.
	for {
		second, err := New()
		if err != nil {
			t.Fatalf("New : %v", err)
		}
		if second[:10] == premier[:10] {
			continue
		}
		if strings.Compare(second, premier) <= 0 {
			t.Fatalf("%q ne trie pas après %q", second, premier)
		}
		return
	}
}

// TestLAlphabetExclutLesCaracteresAmbigus : I, L, O et U sont exclus pour qu'un
// identifiant recopié à la main ne se confonde pas avec 1 et 0.
func TestLAlphabetExclutLesCaracteresAmbigus(t *testing.T) {
	for range 500 {
		ref, err := New()
		if err != nil {
			t.Fatalf("New : %v", err)
		}
		if strings.ContainsAny(ref, "ILOU") {
			t.Fatalf("caractère ambigu dans %q", ref)
		}
	}
}

// TestTimeRondTripe : Time doit rendre l'instant qui a produit le ULID, à la
// milliseconde près (48 bits de résolution, pas plus).
func TestTimeRondTripe(t *testing.T) {
	ref, err := New()
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	quand, err := Time(ref)
	if err != nil {
		t.Fatalf("Time : %v", err)
	}
	if quand.IsZero() {
		t.Fatal("horodatage nul")
	}
}

func TestTimeRefuseLaMauvaiseLongueur(t *testing.T) {
	if _, err := Time("trop-court"); err == nil {
		t.Fatal("longueur invalide acceptée")
	}
}

func TestTimeRefuseLAlphabetInvalide(t *testing.T) {
	if _, err := Time(strings.Repeat("I", 26)); err == nil {
		t.Fatal("caractère hors alphabet accepté")
	}
}
