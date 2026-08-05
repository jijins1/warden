// Command warden-runner est l'agent qui s'exécute dans le pod runner.
//
// Ce binaire vit dans un environnement hostile par construction : le playbook
// qu'il lance est du code arbitraire (§2 de la spec). Il est donc lisible par un
// attaquant, et c'est la raison d'être de sa séparation — son image ne doit
// contenir ni le code du control plane, ni ses dépendances (ADR-0011). Le
// compilateur garantit ce point : `internal/runner` ne peut pas atteindre
// `internal/server/internal/…`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jijins1/warden/internal/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "warden-runner:", err)
		os.Exit(1)
	}
}
