//go:build integration

// Étage 2 de l'ADR-0010 : PostgreSQL réel, testcontainers.
//
// Miroir de `store_integration_test.go` pour `vault_key` (ADR-0045,
// migration 00015) : mêmes propriétés à vérifier — idempotence de la pose,
// contrainte de forme du schéma, et séparation des rôles.
package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func descripteurVault(env, empreinte string) core.VaultKeyDescriptor {
	return core.VaultKeyDescriptor{
		Environment: env,
		Fingerprint: empreinte,
		SecretName:  core.SecretNameForVault(env),
		Origin:      core.VaultKeyGenerated,
	}
}

func TestUneCleDeVaultEstEnregistreeEtRelue(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	empreinte := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.SaveVaultKey(ctx, descripteurVault("prod", empreinte)); err != nil {
		t.Fatalf("SaveVaultKey: %v", err)
	}

	d, err := s.VaultKeyByEnvironment(ctx, "prod")
	if err != nil {
		t.Fatalf("VaultKeyByEnvironment: %v", err)
	}
	if d.Fingerprint != empreinte || d.SecretName != core.SecretNameForVault("prod") {
		t.Errorf("descripteur relu = %+v", d)
	}
	if !d.RotatedAt.IsZero() {
		t.Errorf("rotated_at = %v sur une clé jamais remplacée", d.RotatedAt)
	}

	if _, err := s.VaultKeyByEnvironment(ctx, "jamais-configure"); !errors.Is(err, core.ErrNoVaultKey) {
		t.Errorf("erreur = %v, attendu core.ErrNoVaultKey", err)
	}
}

func TestReposerUneCleDeVaultEstUneRotation(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	empreinteA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	empreinteB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := s.SaveVaultKey(ctx, descripteurVault("prod", empreinteA)); err != nil {
		t.Fatalf("SaveVaultKey: %v", err)
	}
	nouvelle := descripteurVault("prod", empreinteB)
	nouvelle.Origin = core.VaultKeyImported
	if err := s.SaveVaultKey(ctx, nouvelle); err != nil {
		t.Fatalf("SaveVaultKey (rotation): %v", err)
	}

	d, err := s.VaultKeyByEnvironment(ctx, "prod")
	if err != nil {
		t.Fatalf("VaultKeyByEnvironment: %v", err)
	}
	if d.Fingerprint != empreinteB || d.Origin != core.VaultKeyImported {
		t.Errorf("la rotation n'a pas remplacé : %+v", d)
	}
	if d.RotatedAt.IsZero() {
		t.Error("rotated_at non posé : impossible de dire depuis quand la clé est en place")
	}

	toutes, err := s.ListVaultKeys(ctx)
	if err != nil {
		t.Fatalf("ListVaultKeys: %v", err)
	}
	if len(toutes) != 1 {
		t.Errorf("%d clés de vault pour un environnement", len(toutes))
	}
}

func TestUnEnvironnementMalFormeEstRefuseParLeSchemaDeVault(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	for _, env := range []string{"PROD", "prod.eu", "prod/eu", "-prod", ""} {
		_, err := conn.Exec(ctx, `
INSERT INTO vault_key (environment, fingerprint, secret_name, origin)
VALUES ($1, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'warden-vault-x', 'generated')`, env)
		if err == nil {
			t.Errorf("l'environnement %q a été accepté par le schéma", env)
		}
	}
}

// L'ingestion écrit des events. Elle n'a AUCUNE raison de connaître les clés
// de vault de la flotte, et le GRANT ne lui en donne pas.
func TestLIngestionNeVoitPasLesClesDeVault(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)
	for _, q := range []string{
		`CREATE ROLE ingest_login_vault LOGIN PASSWORD 'ingest'`,
		`GRANT warden_ingest TO ingest_login_vault`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("préparation du rôle: %v", err)
		}
	}

	restreint := connect(t, swapUser(dsn, "ingest_login_vault", "ingest"))
	defer restreint.Close(ctx)

	interdits := map[string]string{
		"lire les clés de vault":     `SELECT count(*) FROM vault_key`,
		"poser une clé de vault":     `INSERT INTO vault_key (environment, fingerprint, secret_name, origin) VALUES ('x', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'w', 'generated')`,
		"effacer une clé de vault":   `DELETE FROM vault_key`,
		"détourner une clé de vault": `UPDATE vault_key SET secret_name = 'ailleurs'`,
	}
	for nom, q := range interdits {
		if _, err := restreint.Exec(ctx, q); err == nil {
			t.Errorf("le rôle d'ingestion peut %s — le GRANT est trop large", nom)
		}
	}
}
