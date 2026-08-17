package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// SaveHost écrit ou remplace une machine de l'inventaire.
//
// Un UPSERT sur `(environment, name)` : redéclarer une machine est le geste
// normal — on corrige une adresse, on change de compte, on réenrôle après une
// réinstallation. Le refuser obligerait à supprimer d'abord, donc à passer par
// un état où la machine n'est plus connue.
//
// `enrolled_at` N'EST PAS redaté au remplacement : la question posée en incident
// est « depuis quand cette machine est-elle dans le périmètre », pas « quand
// a-t-on corrigé sa fiche ».
func (s *Store) SaveHost(ctx context.Context, h core.ManagedHost) error {
	if err := h.Validate(); err != nil {
		return err
	}

	const q = `
INSERT INTO managed_host (name, environment, address, port, login, host_key,
                          enrollment_mode, enrolled_by)
VALUES ($1, $2, $3::inet, $4, $5, NULLIF($6, ''), $7, NULLIF($8, 0)::bigint)
ON CONFLICT (environment, name) DO UPDATE
   SET address         = EXCLUDED.address,
       port            = EXCLUDED.port,
       login           = EXCLUDED.login,
       host_key        = EXCLUDED.host_key,
       enrollment_mode = EXCLUDED.enrollment_mode,
       enrolled_by     = EXCLUDED.enrolled_by`

	_, err := s.pool.Exec(ctx, q, h.Name, h.Environment, h.Address.String(),
		h.Port, h.Login, h.HostKey, string(h.Mode), h.EnrolledBy)
	if err != nil {
		return fmt.Errorf("enregistrement de la machine %s/%s: %w", h.Environment, h.Name, err)
	}
	return nil
}

// HostByName rend une machine de l'inventaire.
func (s *Store) HostByName(ctx context.Context, environment, name string) (core.ManagedHost, error) {
	const q = `
SELECT name, environment, host(address), port, login, coalesce(host_key, ''),
       enrollment_mode, enrolled_at, coalesce(enrolled_by, 0)
  FROM managed_host
 WHERE environment = $1 AND name = $2`

	h, err := scanHost(s.pool.QueryRow(ctx, q, environment, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ManagedHost{}, fmt.Errorf("%s/%s: %w", environment, name, core.ErrNoHost)
	}
	if err != nil {
		return core.ManagedHost{}, fmt.Errorf("lecture de la machine %s/%s: %w", environment, name, err)
	}
	return h, nil
}

// ListHosts rend les machines d'un environnement, ou toutes.
//
// Un seul chemin SQL pour les deux cas plutôt que deux requêtes : la clause
// `$1 = ”` est lue une fois et deux requêtes jumelles finissent par diverger
// sur la liste des colonnes.
func (s *Store) ListHosts(ctx context.Context, environment string) ([]core.ManagedHost, error) {
	const q = `
SELECT name, environment, host(address), port, login, coalesce(host_key, ''),
       enrollment_mode, enrolled_at, coalesce(enrolled_by, 0)
  FROM managed_host
 WHERE $1 = '' OR environment = $1
 ORDER BY environment, name`

	lignes, err := s.pool.Query(ctx, q, environment)
	if err != nil {
		return nil, fmt.Errorf("liste des machines: %w", err)
	}
	defer lignes.Close()

	var out []core.ManagedHost
	for lignes.Next() {
		h, err := scanHost(lignes)
		if err != nil {
			return nil, fmt.Errorf("liste des machines: %w", err)
		}
		out = append(out, h)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des machines: %w", err)
	}
	return out, nil
}

// ForgetHost retire une machine de l'inventaire.
//
// Elle ne retire RIEN de la machine : la clé publique reste dans son
// `authorized_keys`. L'appelant doit le dire à l'utilisateur, faute de quoi le
// produit laisserait croire à une révocation qui n'a pas eu lieu.
func (s *Store) ForgetHost(ctx context.Context, environment, name string) error {
	const q = `DELETE FROM managed_host WHERE environment = $1 AND name = $2`

	tag, err := s.pool.Exec(ctx, q, environment, name)
	if err != nil {
		return fmt.Errorf("retrait de la machine %s/%s: %w", environment, name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s/%s: %w", environment, name, core.ErrNoHost)
	}
	return nil
}

// scanHost lit une ligne de `managed_host`.
//
// `host(address)` et non `address` : le type `inet` de PostgreSQL rend
// `10.0.0.1/32` là où `netip.ParseAddr` attend `10.0.0.1`. La conversion est
// faite par la base, qui connaît son type, plutôt que par un découpage de chaîne
// ici — lequel casserait sur la première adresse IPv6.
func scanHost(l interface{ Scan(...any) error }) (core.ManagedHost, error) {
	var h core.ManagedHost
	var adresse, mode string

	if err := l.Scan(&h.Name, &h.Environment, &adresse, &h.Port, &h.Login,
		&h.HostKey, &mode, &h.EnrolledAt, &h.EnrolledBy); err != nil {
		return core.ManagedHost{}, err
	}

	addr, err := netip.ParseAddr(adresse)
	if err != nil {
		// La colonne est un `inet` : ce cas ne devrait pas exister. S'il
		// existe, le dire vaut mieux que rendre une machine sans adresse, que
		// l'enrôlement essaierait de joindre nulle part.
		return core.ManagedHost{}, fmt.Errorf("adresse %q illisible pour %s/%s: %w",
			adresse, h.Environment, h.Name, err)
	}
	h.Address = addr
	h.Mode = core.EnrollmentMode(mode)
	return h, nil
}
