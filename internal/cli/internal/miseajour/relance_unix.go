//go:build !windows

package miseajour

import (
	"fmt"
	"os"
	"syscall"
)

// Relancer remplace le processus courant par le binaire neuf, avec les mêmes
// arguments et le même environnement.
//
// `exec` et non un processus enfant : le terminal, les signaux et le code de
// sortie restent ceux de la commande que l'utilisateur a tapée.
func Relancer(executable string) error {
	if err := syscall.Exec(executable, os.Args, os.Environ()); err != nil { //nolint:gosec // G204 : c'est le binaire qu'on vient de vérifier.
		return fmt.Errorf("relance de la commande : %w", err)
	}
	return nil
}
