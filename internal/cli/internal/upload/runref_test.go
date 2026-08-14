package upload

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
		ref, err := NewRunRef()
		if err != nil {
			t.Fatalf("NewRunRef : %v", err)
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
		ref, err := NewRunRef()
		if err != nil {
			t.Fatalf("NewRunRef : %v", err)
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
	premier, err := NewRunRef()
	if err != nil {
		t.Fatalf("NewRunRef : %v", err)
	}
	// Le composant temporel est en millisecondes ; sans attente les deux
	// identifiants partagent le même horodatage et l'ordre n'est plus garanti.
	for {
		second, err := NewRunRef()
		if err != nil {
			t.Fatalf("NewRunRef : %v", err)
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
		ref, err := NewRunRef()
		if err != nil {
			t.Fatalf("NewRunRef : %v", err)
		}
		if strings.ContainsAny(ref, "ILOU") {
			t.Fatalf("caractère ambigu dans %q", ref)
		}
	}
}
