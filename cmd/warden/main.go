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

	if err := cli.Run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "warden:", err)
		os.Exit(1)
	}
}
