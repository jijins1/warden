// Package cli est le point d'entrée de la CLI utilisateur.
//
// Domaine de confiance : la machine de l'utilisateur (ADR-0011). Rien de ce que
// ce domaine calcule ne fait autorité — la configuration effective d'un run est
// résolue par le serveur depuis le commit (ADR-0009). La seule exception est le
// champ `server:` du fichier de projet : on ne peut pas demander au serveur où
// est le serveur.
package cli

import (
	"context"
	"errors"
)

// Run exécute la commande demandée jusqu'à l'annulation du contexte.
func Run(ctx context.Context, args []string) error {
	return errors.New("pas encore implémenté")
}
