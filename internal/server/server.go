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
	"time"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
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

	switch r {
	case RoleAPI:
		return runAPI(ctx)
	case RoleIngest:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	case RoleGit:
		return runGit(ctx)
	case RoleController:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	default:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	}
}

func runAPI(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}

	cfg := &api.Config{
		Addr:        os.Getenv("WARDEN_API_ADDR"),
		DatabaseURL: dsn,
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}

	// Assemblage : c'est le SEUL endroit qui connaît à la fois les ports du
	// cœur et les adaptateurs qui les satisfont (ADR-0019).
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	// Le lecteur de diffs a sa propre identité de connexion : les payloads
	// --diff sont sous contrôle d'accès distinct (ADR-0008). À défaut de DSN
	// dédié, on retombe sur le DSN applicatif — acceptable en développement,
	// pas en production.
	diffDSN := os.Getenv(EnvDiffDatabaseURL)
	if diffDSN == "" {
		diffDSN = dsn
	}
	dr, err := store.OpenDiffReader(ctx, diffDSN)
	if err != nil {
		return fmt.Errorf("opening diff reader: %w", err)
	}
	defer dr.Close()

	f, err := flux.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening flux: %w", err)
	}
	defer f.Close()

	// LISTEN/NOTIFY appartient à l'adaptateur, pas au serveur HTTP.
	go func() { _ = f.Listen(context.WithoutCancel(ctx)) }()

	// Le client vers le rôle `git` est FACULTATIF : sans lui, les diffs git
	// répondent 501 et le reste de l'API fonctionne. C'est ce qui permet de
	// développer sans volume de dépôts.
	//
	// Le type est celui du PORT et non *gitclient.Client : une valeur nulle de
	// type concret rangée dans un champ d'interface n'est pas nil, et la branche
	// qui répond 501 se transformerait en déréférencement de pointeur nul.
	var repo core.RepoDiffer
	if adresse := os.Getenv(EnvGitInternalURL); adresse != "" {
		jeton := os.Getenv(EnvGitInternalToken)
		if jeton == "" {
			// Une URL sans jeton n'est pas une dégradation, c'est une erreur de
			// configuration : elle produirait des 401 en silence à la première
			// consultation d'audit.
			return fmt.Errorf("%s est définie sans %s", EnvGitInternalURL, EnvGitInternalToken)
		}
		gc, err := gitclient.New(gitclient.Config{BaseURL: adresse, Token: jeton})
		if err != nil {
			return fmt.Errorf("client du rôle git: %w", err)
		}
		repo = gc
	}

	server, err := api.New(cfg, api.Deps{
		Runs:   st,
		Users:  st,
		Diffs:  dr,
		Events: f,
		Repo:   repo,
	})
	if err != nil {
		return fmt.Errorf("creating API server: %w", err)
	}
	defer func() { _ = server.Close() }()

	return server.Run(ctx)
}

// Variables d'environnement portant les chaînes de connexion.
//
// Deux identités de connexion, donc deux variables (ADR-0008, ADR-0012). Le
// rôle d'ingestion est restreint à INSERT sur les tables d'events ; lui donner
// le DSN applicatif annulerait la séparation sans qu'aucun test ne le voie.
const (
	EnvDatabaseURL       = "WARDEN_DATABASE_URL"
	EnvIngestDatabaseURL = "WARDEN_INGEST_DATABASE_URL"
	EnvDiffDatabaseURL   = "WARDEN_DIFF_DATABASE_URL"
)

// Configuration du rôle `git` et de l'arête qui y mène (ADR-0024).
//
// Deux adresses et non une : le smart HTTP est joignable depuis les pods runner,
// donc depuis du code arbitraire (ADR-0007), là où l'API interne ne l'est que
// par le rôle `api` (ADR-0006). Les séparer dès maintenant rend la NetworkPolicy
// exprimable par port, sans changement de code le jour où le smart HTTP existera.
const (
	EnvGitAddr          = "WARDEN_GIT_ADDR"           // smart HTTP — pas encore servi
	EnvGitInternalAddr  = "WARDEN_GIT_INTERNAL_ADDR"  // API interne, servie par gitd
	EnvGitRoot          = "WARDEN_GIT_ROOT"           // racine des dépôts bare
	EnvGitInternalURL   = "WARDEN_GIT_INTERNAL_URL"   // côté `api` : où joindre gitd
	EnvGitInternalToken = "WARDEN_GIT_INTERNAL_TOKEN" // partagé par les deux
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

// runGit sert l'API interne du rôle `git` (ADR-0024).
//
// Le smart HTTP n'est pas encore servi : ce rôle se réduit pour l'instant aux
// diffs d'audit du §6.5.
func runGit(ctx context.Context) error {
	// ADR-0005 : le moteur est le binaire git, et sa version est une condition
	// de correction, pas une préférence. Le contrôle est ici, au démarrage du
	// processus, plutôt que dans gitd.New — c'est le processus qui a une version
	// de git, pas l'objet.
	if err := git.CheckVersion(ctx); err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}

	srv, err := gitd.New(gitd.Config{
		Addr:             os.Getenv(EnvGitInternalAddr),
		Racine:           os.Getenv(EnvGitRoot),
		Jeton:            os.Getenv(EnvGitInternalToken),
		GracefulShutdown: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}
	return srv.Run(ctx)
}
