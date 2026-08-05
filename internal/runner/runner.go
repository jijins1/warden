// Package runner est le point d'entrée de l'agent qui s'exécute dans le pod.
//
// Domaine de confiance : hostile par construction (§2 — le playbook lancé ici
// est du code arbitraire). Ce binaire est lisible par un adversaire, ce qui est
// la raison d'être de sa séparation : son image ne doit contenir ni le code du
// control plane, ni ses dépendances (ADR-0011).
package runner

import (
	"context"
	"errors"
)

// Run exécute l'agent jusqu'à l'annulation du contexte.
func Run(ctx context.Context, args []string) error {
	return errors.New("pas encore implémenté")
}
