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
	"path/filepath"
	"syscall"

	"github.com/jijins1/warden/internal/server"
)

func main() {
	// git invoque ce binaire par un lien symbolique nommé `pre-receive`. Le test
	// vient AVANT toute analyse d'arguments : le hook n'en reçoit aucun, il lit
	// stdin, et un usage() affiché à cet endroit refuserait tous les push avec un
	// message parfaitement incompréhensible côté client.
	if filepath.Base(os.Args[0]) == server.HookName {
		os.Exit(server.PreReceiveHook(context.Background(), os.Stdin, os.Stderr))
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wardend:", err)
		os.Exit(1)
	}
}

// Les sous-commandes qui ne sont PAS des rôles : elles s'exécutent et se
// terminent, rien n'est déployé sous ces noms (ADR-0012).
const (
	cmdMigrate = "migrate"

	// cmdUser amorce le premier compte. C'est la sortie du problème de l'œuf et
	// de la poule : créer un compte demande d'être administrateur, et il n'y a
	// aucun administrateur sur un déploiement neuf.
	//
	// Elle vit ici et non dans la CLI parce que son autorisation n'est pas un
	// mot de passe mais l'ACCÈS À LA BASE — c'est-à-dire le fait d'être
	// l'opérateur du déploiement. Le même raisonnement que `migrate`.
	cmdUser = "user"
)

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: wardend <%s|%s|%s>", joinRoles(), cmdMigrate, cmdUser)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if os.Args[1] == cmdUser {
		return server.BootstrapUser(ctx, os.Args[2:], os.Stdout)
	}

	if len(os.Args) != 2 {
		return fmt.Errorf("usage: wardend <%s|%s|%s>", joinRoles(), cmdMigrate, cmdUser)
	}

	if os.Args[1] == cmdMigrate {
		return server.Migrate(ctx)
	}

	role, err := server.ParseRole(os.Args[1])
	if err != nil {
		return fmt.Errorf("%w (attendu : %s|%s|%s)", err, joinRoles(), cmdMigrate, cmdUser)
	}

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
