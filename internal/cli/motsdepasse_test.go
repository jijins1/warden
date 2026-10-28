package cli

import (
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// Les deux mots de passe ne peuvent pas venir de la même entrée standard : le
// second lirait du vide sans que rien ne le dise.
func TestDeuxMotsDePasseSurStdinSontRefuses(t *testing.T) {
	become := &prompt.Source{Quoi: "élévation", Stdin: true}
	connexion := &prompt.Source{Quoi: "connexion", Stdin: true}
	if _, _, err := lireLesMotsDePasse(t.Context(), become, connexion); err == nil {
		t.Error("deux lectures de l'entrée standard acceptées")
	}
}

// Rien de demandé, rien de lu.
func TestSansDemandeAucunMotDePasse(t *testing.T) {
	b, c, err := lireLesMotsDePasse(t.Context(), &prompt.Source{}, &prompt.Source{})
	if err != nil || b != nil || c != nil {
		t.Errorf("b=%v c=%v err=%v", b != nil, c != nil, err)
	}
}
