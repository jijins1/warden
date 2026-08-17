package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// SaveKey écrit ou remplace la partie publique d'une clé d'environnement.
//
// Un UPSERT et non un INSERT : poser une clé sur un environnement qui en a déjà
// une EST la rotation, et l'ADR-0020 note que c'est le seul mécanisme de
// révocation dont le produit dispose. La refuser obligerait à supprimer d'abord,
// donc à passer par un état où la production n'a plus de clé du tout.
//
// `rotated_at` n'est posé qu'au REMPLACEMENT, jamais à la première pose : une
// date de rotation sur une clé qui n'a jamais tourné ferait croire à un
// évènement qui n'a pas eu lieu.
func (s *Store) SaveKey(ctx context.Context, d core.KeyDescriptor) error {
	if err := core.ValidateEnvironment(d.Environment); err != nil {
		return err
	}
	if !d.Origin.Valid() {
		return fmt.Errorf("origine de clé %q inconnue", d.Origin)
	}
	if d.PublicKey == "" || d.Fingerprint == "" {
		return errors.New("clé sans partie publique ni empreinte")
	}
	if d.SecretName == "" {
		// Sans pointeur, la ligne décrirait une clé dont la partie privée est
		// introuvable — donc un environnement qui paraît configuré et ne l'est
		// pas.
		return errors.New("clé sans nom de Secret : sa partie privée serait introuvable")
	}

	const q = `
INSERT INTO ssh_key (environment, public_key, fingerprint, secret_name, origin, created_by)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0)::bigint)
ON CONFLICT (environment) DO UPDATE
   SET public_key  = EXCLUDED.public_key,
       fingerprint = EXCLUDED.fingerprint,
       secret_name = EXCLUDED.secret_name,
       origin      = EXCLUDED.origin,
       created_by  = EXCLUDED.created_by,
       rotated_at  = now()`

	if _, err := s.pool.Exec(ctx, q, d.Environment, d.PublicKey, d.Fingerprint,
		d.SecretName, string(d.Origin), d.CreatedBy); err != nil {
		return fmt.Errorf("enregistrement de la clé de %s: %w", d.Environment, err)
	}
	return nil
}

// KeyByEnvironment rend le descripteur d'une clé.
func (s *Store) KeyByEnvironment(ctx context.Context, environment string) (core.KeyDescriptor, error) {
	const q = `
SELECT environment, public_key, fingerprint, secret_name, origin,
       created_at, rotated_at, coalesce(created_by, 0)
  FROM ssh_key
 WHERE environment = $1`

	d, err := scanKey(s.pool.QueryRow(ctx, q, environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.KeyDescriptor{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoKey)
	}
	if err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("lecture de la clé de %s: %w", environment, err)
	}
	return d, nil
}

// ListKeys rend tous les descripteurs, triés par environnement.
func (s *Store) ListKeys(ctx context.Context) ([]core.KeyDescriptor, error) {
	const q = `
SELECT environment, public_key, fingerprint, secret_name, origin,
       created_at, rotated_at, coalesce(created_by, 0)
  FROM ssh_key
 ORDER BY environment`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des clés: %w", err)
	}
	defer lignes.Close()

	var out []core.KeyDescriptor
	for lignes.Next() {
		d, err := scanKey(lignes)
		if err != nil {
			return nil, fmt.Errorf("liste des clés: %w", err)
		}
		out = append(out, d)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des clés: %w", err)
	}
	return out, nil
}

// scanKey lit une ligne de `ssh_key`.
//
// `rotated_at` est nullable et devient le temps NUL plutôt qu'un pointeur : un
// `*time.Time` obligerait chaque appelant à déréférencer, et le premier qui
// l'oublierait planterait sur la clé qui n'a jamais tourné — c'est-à-dire le cas
// le plus courant.
func scanKey(l interface{ Scan(...any) error }) (core.KeyDescriptor, error) {
	var d core.KeyDescriptor
	var origine string
	var rotee *time.Time

	if err := l.Scan(&d.Environment, &d.PublicKey, &d.Fingerprint, &d.SecretName,
		&origine, &d.CreatedAt, &rotee, &d.CreatedBy); err != nil {
		return core.KeyDescriptor{}, err
	}
	d.Origin = core.KeyOrigin(origine)
	if rotee != nil {
		d.RotatedAt = *rotee
	}
	return d, nil
}
