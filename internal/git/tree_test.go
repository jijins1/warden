// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package git

import (
	"testing"
)

func TestTreeAtListeLesFichiers(t *testing.T) {
	bare, commit := depotAvecArbre(t)

	fichiers, coupe, err := New(bare).TreeAt(t.Context(), commit, 100)
	if err != nil {
		t.Fatalf("TreeAt: %v", err)
	}
	if coupe {
		t.Error("une liste complète ne doit pas se dire coupée")
	}
	// Le répertoire `inventories` n'est pas une entrée : il se lit dans les chemins.
	veut := []TreeFile{
		{Path: ".ansibleproxy.yml", Size: int64(len("server: https://exemple\n"))},
		{Path: "inventories/hosts", Size: int64(len("[web]\nweb-01\n"))},
	}
	if len(fichiers) != len(veut) {
		t.Fatalf("= %v, veut %v", fichiers, veut)
	}
	for i := range veut {
		if fichiers[i] != veut[i] {
			t.Errorf("[%d] = %v, veut %v", i, fichiers[i], veut[i])
		}
	}
}

// Une liste coupée qui se dirait complète ferait conclure à l'absence des
// fichiers au-delà de la borne.
func TestTreeAtSignaleLaCoupe(t *testing.T) {
	bare, commit := depotAvecArbre(t)

	fichiers, coupe, err := New(bare).TreeAt(t.Context(), commit, 1)
	if err != nil {
		t.Fatalf("TreeAt: %v", err)
	}
	if !coupe || len(fichiers) != 1 {
		t.Errorf("= %v (coupe=%v), veut un fichier et la coupe signalée", fichiers, coupe)
	}
}

func TestTreeAtRefuseUnSHAInvalide(t *testing.T) {
	bare, _ := depotAvecArbre(t)
	if _, _, err := New(bare).TreeAt(t.Context(), SHA("pas-un-sha"), 10); err == nil {
		t.Fatal("un SHA invalide doit être refusé avant git")
	}
}
