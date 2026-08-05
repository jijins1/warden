// Command wardend est le control plane de warden : un binaire, plusieurs rôles
// déployés séparément (ADR-0006).
//
// Ce main est volontairement mince. Il ne peut de toute façon rien faire de
// plus : les paquets internes du control plane sont hors de sa portée, seul
// `internal/server` lui est accessible (ADR-0011).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jijins1/warden/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: wardend <%s>", joinRoles())
	}

	role, err := server.ParseRole(os.Args[1])
	if err != nil {
		return fmt.Errorf("%w (attendu : %s)", err, joinRoles())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, role); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func joinRoles() string {
	out := ""
	for i, r := range server.Roles() {
		if i > 0 {
			out += "|"
		}
		out += string(r)
	}
	return out
}
