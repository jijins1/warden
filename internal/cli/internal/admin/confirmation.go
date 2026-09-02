package admin

import (
	"io"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

// lireOui lit une réponse de confirmation au terminal.
//
// Le refus est le DÉFAUT quand la réponse n'est pas un oui franc — y compris
// quand la lecture échoue, ce qui est le cas d'une entrée fermée ou redirigée.
// Accepter par distraction, ou pire par absence de terminal, est exactement ce
// que ces questions existent pour empêcher.
//
// Partagée entre l'empreinte d'une machine et le descripteur d'un dépôt : deux
// implémentations finiraient par ne plus interpréter « y » de la même façon.
func lireOui(entree io.Reader) bool {
	reponse, err := prompt.ReadLine(entree)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(reponse.Reveal()))) {
	case "o", "oui", "y", "yes":
		return true
	default:
		return false
	}
}
