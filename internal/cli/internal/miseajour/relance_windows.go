//go:build windows

package miseajour

import "errors"

// Relancer n'est pas disponible sous Windows : `exec` n'y remplace pas le
// processus, et un enfant laisserait deux consoles se disputer l'entrée.
func Relancer(string) error {
	return errors.New("CLI mise à jour : relancez la commande")
}
