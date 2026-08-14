package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// GetRepo lit la politique serveur d'un dépôt.
func (s *Store) GetRepo(ctx context.Context, name string) (core.Repo, error) {
	if name == "" {
		return core.Repo{}, fmt.Errorf("nom de dépôt vide: %w", ErrNotFound)
	}

	const q = `
SELECT name, remote_url, mainline_ref
  FROM repo
 WHERE name = $1`

	var r core.Repo
	err := s.pool.QueryRow(ctx, q, name).Scan(&r.Name, &r.RemoteURL, &r.MainlineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		// Un dépôt inconnu n'est pas une panne : c'est un dépôt qu'aucun
		// administrateur n'a encore configuré, et l'appelant doit pouvoir le dire
		// à l'utilisateur plutôt que de rendre une erreur interne.
		return core.Repo{}, fmt.Errorf("dépôt %q non configuré: %w", name, ErrNotFound)
	}
	if err != nil {
		return core.Repo{}, fmt.Errorf("lecture du dépôt %q: %w", name, err)
	}
	return r, nil
}

// ListRepos rend toute la configuration, triée par nom.
//
// Sans pagination, délibérément. Le nombre de dépôts d'une organisation se
// compte en dizaines et il est borné par le travail humain de les déclarer — à
// la différence des runs, qui grossissent tout seuls et que ListRuns pagine pour
// cette raison.
func (s *Store) ListRepos(ctx context.Context) ([]core.Repo, error) {
	const q = `
SELECT name, remote_url, mainline_ref
  FROM repo
 ORDER BY name`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des dépôts: %w", err)
	}
	defer rows.Close()

	var out []core.Repo
	for rows.Next() {
		var r core.Repo
		if err := rows.Scan(&r.Name, &r.RemoteURL, &r.MainlineRef); err != nil {
			return nil, fmt.Errorf("lecture d'une ligne de dépôt: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("parcours des dépôts: %w", err)
	}
	return out, nil
}

// UpsertRepo écrit la configuration d'un dépôt et retient son auteur.
//
// Un upsert plutôt qu'un couple create/update : l'administration se fait par
// déclaration de l'état voulu, et distinguer les deux obligerait l'appelant à
// savoir si la ligne existe — une question dont la réponse peut changer entre le
// moment où il la pose et celui où il écrit.
func (s *Store) UpsertRepo(ctx context.Context, r core.Repo, by int64) error {
	// La validation vit dans le cœur et elle est appliquée ici aussi. Les
	// contraintes de la table sont la dernière barrière, pas la première : leurs
	// messages nomment la contrainte violée et non ce qu'il fallait écrire.
	if err := r.Valid(); err != nil {
		return err
	}
	if by <= 0 {
		return errors.New("configuration de dépôt écrite sans auteur : elle doit être imputable")
	}

	const q = `
INSERT INTO repo (name, remote_url, mainline_ref, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (name) DO UPDATE
   SET remote_url   = excluded.remote_url,
       mainline_ref = excluded.mainline_ref,
       updated_at   = now(),
       updated_by   = excluded.updated_by`

	if _, err := s.pool.Exec(ctx, q, r.Name, r.RemoteURL, r.MainlineRef, by); err != nil {
		return fmt.Errorf("écriture du dépôt %q: %w", r.Name, err)
	}
	return nil
}
