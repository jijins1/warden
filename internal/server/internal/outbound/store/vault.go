package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// SaveVaultKey écrit ou remplace les métadonnées d'une clé de vault.
//
// Un UPSERT, sur le même raisonnement que SaveKey (ssh_key) : poser une clé
// sur un environnement qui en a déjà une EST la rotation, et c'est le seul
// mécanisme de révocation dont ce magasin dispose (ADR-0045, miroir de
// l'ADR-0020).
func (s *Store) SaveVaultKey(ctx context.Context, d core.VaultKeyDescriptor) error {
	if err := core.ValidateEnvironment(d.Environment); err != nil {
		return err
	}
	if err := core.ValidateVaultOrigin(d.Origin); err != nil {
		return err
	}
	if d.Fingerprint == "" {
		return errors.New("clé de vault sans empreinte")
	}
	if d.SecretName == "" {
		return errors.New("clé de vault sans nom de Secret : sa valeur serait introuvable")
	}

	const q = `
INSERT INTO vault_key (environment, fingerprint, secret_name, origin, created_by)
VALUES ($1, $2, $3, $4, NULLIF($5, 0)::bigint)
ON CONFLICT (environment) DO UPDATE
   SET fingerprint = EXCLUDED.fingerprint,
       secret_name = EXCLUDED.secret_name,
       origin      = EXCLUDED.origin,
       created_by  = EXCLUDED.created_by,
       rotated_at  = now()`

	if _, err := s.pool.Exec(ctx, q, d.Environment, d.Fingerprint,
		d.SecretName, string(d.Origin), d.CreatedBy); err != nil {
		return fmt.Errorf("enregistrement de la clé de vault de %s: %w", d.Environment, err)
	}
	return nil
}

// VaultKeyByEnvironment rend le descripteur d'une clé de vault.
func (s *Store) VaultKeyByEnvironment(ctx context.Context, environment string) (core.VaultKeyDescriptor, error) {
	const q = `
SELECT environment, fingerprint, secret_name, origin,
       created_at, rotated_at, coalesce(created_by, 0)
  FROM vault_key
 WHERE environment = $1`

	d, err := scanVaultKey(s.pool.QueryRow(ctx, q, environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.VaultKeyDescriptor{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoVaultKey)
	}
	if err != nil {
		return core.VaultKeyDescriptor{}, fmt.Errorf("lecture de la clé de vault de %s: %w", environment, err)
	}
	return d, nil
}

// ListVaultKeys rend tous les descripteurs, triés par environnement.
func (s *Store) ListVaultKeys(ctx context.Context) ([]core.VaultKeyDescriptor, error) {
	const q = `
SELECT environment, fingerprint, secret_name, origin,
       created_at, rotated_at, coalesce(created_by, 0)
  FROM vault_key
 ORDER BY environment`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des clés de vault: %w", err)
	}
	defer lignes.Close()

	var out []core.VaultKeyDescriptor
	for lignes.Next() {
		d, err := scanVaultKey(lignes)
		if err != nil {
			return nil, fmt.Errorf("liste des clés de vault: %w", err)
		}
		out = append(out, d)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des clés de vault: %w", err)
	}
	return out, nil
}

// scanVaultKey lit une ligne de `vault_key`. Même traitement de `rotated_at`
// que scanKey : le temps NUL plutôt qu'un pointeur.
func scanVaultKey(l interface{ Scan(...any) error }) (core.VaultKeyDescriptor, error) {
	var d core.VaultKeyDescriptor
	var origine string
	var rotee *time.Time

	if err := l.Scan(&d.Environment, &d.Fingerprint, &d.SecretName,
		&origine, &d.CreatedAt, &rotee, &d.CreatedBy); err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	d.Origin = core.VaultKeyOrigin(origine)
	if rotee != nil {
		d.RotatedAt = *rotee
	}
	return d, nil
}
