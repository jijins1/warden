// Command warden est la CLI utilisateur.
//
// Elle s'exécute sur la machine de l'utilisateur, domaine de confiance distinct
// du control plane et du pod runner (ADR-0011). Rien de ce qu'elle calcule ne
// fait autorité : la configuration effective d'un run est résolue par le serveur
// depuis le commit (ADR-0009).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jijins1/warden/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err := cli.Run(ctx, os.Args[1:])
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		// Un Ctrl-C n'est pas un échec du produit : le run est interrompu, et
		// c'est ce que l'utilisateur a demandé.
	case errors.Is(err, cli.ErrDejaDit):
		// Le message est déjà sur stderr. Le préfixer de « warden: » ici mettrait
		// une ligne d'erreur à la SUITE d'un index d'aide de cinquante lignes,
		// c'est-à-dire là où personne ne la lira.
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "warden:", err)
		os.Exit(1)
	}
}
