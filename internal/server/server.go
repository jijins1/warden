// Package server est le point d'entrée du control plane.
//
// Il expose les seuls symboles que `cmd/wardend` peut atteindre : tout le reste
// vit sous `internal/server/internal/`, donc hors de portée des autres domaines
// de confiance — refusé par le compilateur, pas par une revue (ADR-0011).
//
// Ce paquet doit rester réduit au type Role et à Run : il est le seul du domaine
// serveur que `internal/cli` et `internal/runner` peuvent importer.
package server

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// Role désigne le rôle sous lequel le binaire est lancé (ADR-0006).
type Role string

const (
	// RoleAPI sert l'API HTTP + WS, et l'ingestion des events sur un port
	// distinct (ADR-0008).
	RoleAPI Role = "api"
	// RoleIngest ne sert que l'ingestion. Non déployé aujourd'hui : il existe
	// pour que sa promotion en rôle déployé à part reste un changement de
	// déploiement, jamais un changement de code (ADR-0008).
	RoleIngest Role = "ingest"
	// RoleGit détient le volume des dépôts bare. Une seule instance (ADR-0006).
	RoleGit Role = "git"
	// RoleController réconcilie les pods runner. Un seul actif, leader election
	// par Lease (ADR-0006).
	RoleController Role = "controller"
)

// Roles énumère les rôles acceptés, dans l'ordre d'affichage de l'usage.
func Roles() []Role {
	return []Role{RoleAPI, RoleIngest, RoleGit, RoleController}
}

// ErrUnknownRole est retournée pour un rôle non reconnu.
var ErrUnknownRole = errors.New("rôle inconnu")

// ParseRole valide une chaîne fournie en line de commande.
func ParseRole(s string) (Role, error) {
	for _, r := range Roles() {
		if Role(s) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownRole, s)
}

// Run exécute le rôle demandé jusqu'à l'annulation du contexte.
func Run(ctx context.Context, r Role) error {
	if _, err := ParseRole(string(r)); err != nil {
		return err
	}
	return fmt.Errorf("rôle %q: pas encore implémenté", r)
}

// Variables d'environnement portant les chaînes de connexion.
//
// Deux identités de connexion, donc deux variables (ADR-0008, ADR-0012). Le
// rôle d'ingestion est restreint à INSERT sur les tables d'events ; lui donner
// le DSN applicatif annulerait la séparation sans qu'aucun test ne le voie.
const (
	EnvDatabaseURL       = "WARDEN_DATABASE_URL"
	EnvIngestDatabaseURL = "WARDEN_INGEST_DATABASE_URL"
)

// Migrate applique les migrations de schéma en attente.
//
// Sous-commande dédiée, et non un traitement au démarrage d'un pod : le rôle
// `api` tourne à N réplicas, migrer au démarrage ferait démarrer N migrateurs
// concurrents (ADR-0012). Elle est lancée par un Job Helm en pre-upgrade.
func Migrate(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	return store.Migrate(ctx, dsn)
}

// databaseURL lit une chaîne de connexion et échoue tôt si elle manque.
//
// Un secret absent doit arrêter le démarrage, pas produire une erreur de
// connexion trois couches plus bas au premier appel. La valeur n'est jamais
// journalisée ni incluse dans un message d'erreur : elle porte le mot de passe.
func databaseURL(name string) (string, error) {
	dsn := os.Getenv(name)
	if dsn == "" {
		return "", fmt.Errorf("%s n'est pas définie", name)
	}
	return dsn, nil
}
