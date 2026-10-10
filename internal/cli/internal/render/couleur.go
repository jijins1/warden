package render

import (
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// Les codes qu'Ansible emploie par défaut (`ansible/utils/color.py` et les
// `COLOR_*` de sa configuration). Ils ne servent qu'aux lignes que la CLI doit
// RECONSTITUER : tout le reste arrive déjà coloré par Ansible lui-même.
const (
	couleurOK          = "0;32" // vert
	couleurChanged     = "0;33" // jaune
	couleurErreur      = "0;31" // rouge
	couleurSkip        = "0;36" // cyan
	couleurUnreachable = "1;31" // rouge vif
	couleurWarn        = "1;35" // violet vif
)

// stringc colore un texte comme le `stringc` d'Ansible.
func stringc(texte, code string) string {
	return "\x1b[" + code + "m" + texte + "\x1b[0m"
}

// sequenceSGR reconnaît les seules séquences qu'Ansible écrit : la sélection de
// couleur, `ESC [ … m`.
var sequenceSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// assainir ne laisse atteindre le terminal que les couleurs.
//
// Le texte vient du pod runner, domaine hostile (ADR-0011) : un playbook y
// écrit ce qu'il veut, par un avertissement ou une sortie de module. Le passer
// tel quel donnerait à un dépôt le pouvoir d'effacer l'écran, de réécrire des
// lignes déjà affichées, ou de remplir le presse-papiers (OSC 52). Les
// séquences de couleur sont gardées ; tout autre caractère de contrôle — C0
// comme C1, dont l'ESC des autres séquences — disparaît, sauf le saut de ligne
// et la tabulation, qui sont de la mise en page.
func assainir(texte string) string {
	var b strings.Builder
	b.Grow(len(texte))
	debut := 0
	for _, m := range sequenceSGR.FindAllStringIndex(texte, -1) {
		sansControle(&b, texte[debut:m[0]])
		b.WriteString(texte[m[0]:m[1]])
		debut = m[1]
	}
	sansControle(&b, texte[debut:])
	return b.String()
}

func sansControle(b *strings.Builder, s string) {
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
}

// sansCouleur retire les couleurs d'un texte rendu par Ansible.
func sansCouleur(texte string) string {
	return sequenceSGR.ReplaceAllString(texte, "")
}

// Couleur dit si la sortie doit être colorée, selon les règles d'Ansible.
func Couleur(sortie io.Writer) bool {
	f, ok := sortie.(interface{ Fd() uintptr })
	return couleurPour(os.Getenv, ok && term.IsTerminal(int(f.Fd())))
}

// couleurPour reprend `ansible/utils/color.py` : `ANSIBLE_NOCOLOR` (ou
// `NO_COLOR`, son alias) coupe, une sortie qui n'est pas un terminal coupe, et
// `ANSIBLE_FORCE_COLOR` l'emporte sur les deux.
//
// Les MÊMES variables et non des drapeaux à nous : qui a réglé Ansible dans son
// shell n'a pas à le régler une seconde fois pour warden.
func couleurPour(getenv func(string) string, terminal bool) bool {
	if booleen(getenv("ANSIBLE_FORCE_COLOR")) {
		return true
	}
	if booleen(getenv("ANSIBLE_NOCOLOR")) || booleen(getenv("NO_COLOR")) {
		return false
	}
	return terminal
}

// booleen lit une valeur comme Ansible lit ses booléens de configuration
// (`BOOLEANS_TRUE`).
func booleen(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "y", "yes", "on", "1", "true", "t":
		return true
	}
	return false
}
