package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// PeutAtteindre dit si l'utilisateur a un droit VIVANT sur l'environnement.
//
// La condition de vie est dans le WHERE, comme pour les sessions : un appelant ne
// peut pas oublier d'interpréter `revoked_at`, et il n'a aucune décision à
// prendre. C'est la requête posée à chaque création de run ET à chaque livraison
// d'ordre, donc l'index partiel `environment_grant_vivants_idx` la porte.
//
// Deux chemins : un droit NOMINATIF, ou un droit d'un GROUPE porté par une
// session vivante de l'utilisateur (ADR-0053). Le second ne regarde pas la
// session de la requête mais TOUTES les sessions vivantes du compte : à la
// livraison d'un ordre, il n'y a pas de requête, et c'est ce qui fait qu'un run
// dont l'auteur s'est déconnecté ne reçoit plus la clé que son groupe donnait.
func (s *Store) PeutAtteindre(ctx context.Context, userID int64, environment string) (bool, error) {
	const q = `
SELECT EXISTS (
    SELECT 1 FROM environment_grant
     WHERE user_id = $1 AND environment = $2 AND revoked_at IS NULL
) OR EXISTS (
    SELECT 1 FROM ` + sessionsVivantesEtGroupes + `
       AND gg.environment = $2
)`

	var ok bool
	if err := s.pool.QueryRow(ctx, q, userID, environment).Scan(&ok); err != nil {
		return false, fmt.Errorf("lecture du droit sur %q: %w", environment, err)
	}
	return ok, nil
}

// EnvironnementsAtteignables rend les environnements sur lesquels
// l'utilisateur a un droit VIVANT, triés.
//
// La condition de vie est dans le WHERE, pour la même raison que dans
// PeutAtteindre. DISTINCT par prudence seulement : l'index partiel interdit
// déjà deux droits vivants sur le même couple.
func (s *Store) EnvironnementsAtteignables(ctx context.Context, userID int64) ([]string, error) {
	const q = `
SELECT environment FROM environment_grant
 WHERE user_id = $1 AND revoked_at IS NULL
UNION
SELECT gg.environment FROM ` + sessionsVivantesEtGroupes + `
 ORDER BY 1`

	lignes, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("environnements atteignables: %w", err)
	}
	defer lignes.Close()

	out := []string{}
	for lignes.Next() {
		var env string
		if err := lignes.Scan(&env); err != nil {
			return nil, fmt.Errorf("lecture d'un environnement atteignable: %w", err)
		}
		out = append(out, env)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("environnements atteignables: %w", err)
	}
	return out, nil
}

// sessionsVivantesEtGroupes joint les sessions vivantes de l'utilisateur `$1`
// aux droits vivants de leurs groupes, sous l'alias `gg`.
//
// Une seule écriture de la condition de vie d'une session — non révoquée, non
// expirée — pour les deux lectures des droits : la dupliquer, c'est risquer
// qu'un jour l'une oublie l'expiration. Le fragment FINIT par son WHERE :
// l'appelant le prolonge par `AND`.
const sessionsVivantesEtGroupes = `session s
      JOIN session_group sg ON sg.session_id = s.id
      JOIN group_grant gg ON gg.group_name = sg.group_name AND gg.revoked_at IS NULL
     WHERE s.user_id = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`

// colonnesGrant projette un droit avec les sujets des deux comptes.
//
// Les SUJETS et non les identifiants : ces lignes s'affichent, et un tableau
// d'entiers obligerait l'appelant à faire N lectures pour le rendre lisible.
const colonnesGrant = `g.id, g.user_id, u.subject, g.environment,
       g.granted_at, coalesce(g.granted_by, 0), coalesce(p.subject, ''),
       g.revoked_at`

func scannerGrant(row pgx.Row) (core.EnvironmentGrant, error) {
	var g core.EnvironmentGrant
	var revoque *time.Time
	err := row.Scan(&g.ID, &g.UserID, &g.Subject, &g.Environment,
		&g.GrantedAt, &g.GrantedBy, &g.GrantedBySubject, &revoque)
	if err != nil {
		return core.EnvironmentGrant{}, err
	}
	if revoque != nil {
		g.RevokedAt = *revoque
	}
	return g, nil
}

// ListGrants rend les droits, tous ou ceux d'un compte.
//
// Les RÉVOQUÉS sont inclus : c'est une vue d'audit et non une liste de ce qui est
// permis. « Qui avait accès à prod le 3 mars » ne se répond pas sans eux.
func (s *Store) ListGrants(ctx context.Context, subject string) ([]core.EnvironmentGrant, error) {
	const q = `
SELECT ` + colonnesGrant + `
  FROM environment_grant g
  JOIN users u ON u.id = g.user_id
  LEFT JOIN users p ON p.id = g.granted_by
 WHERE ($1 = '' OR u.subject = $1)
 ORDER BY u.subject, g.environment, g.granted_at DESC`

	lignes, err := s.pool.Query(ctx, q, subject)
	if err != nil {
		return nil, fmt.Errorf("liste des droits: %w", err)
	}
	defer lignes.Close()

	var out []core.EnvironmentGrant
	for lignes.Next() {
		g, err := scannerGrant(lignes)
		if err != nil {
			return nil, fmt.Errorf("lecture d'un droit: %w", err)
		}
		out = append(out, g)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des droits: %w", err)
	}
	return out, nil
}

// Grant accorde un droit sur un environnement.
//
// Idempotente : réaccorder un droit vivant le rend tel quel, sans réécrire sa
// date. Un opérateur qui rejoue une commande ne doit pas avoir à se demander si
// elle a pris — et surtout, `granted_at` ne doit pas glisser, puisqu'elle répond
// à « depuis quand ».
func (s *Store) Grant(ctx context.Context, subject, environment string, parQui int64) (core.EnvironmentGrant, error) {
	u, err := s.UserBySubject(ctx, core.UserLocal, subject)
	if err != nil {
		return core.EnvironmentGrant{}, err
	}

	// `ON CONFLICT` ne peut pas viser un index PARTIEL sans répéter sa
	// condition ; la répéter ici la dupliquerait. Une lecture préalable est plus
	// lisible, et la course qu'elle laisse est arrêtée par l'index lui-même — le
	// second insert violerait l'unicité, ce que l'appelant verra comme une
	// erreur plutôt que comme un droit accordé en double.
	vivant, err := s.PeutAtteindre(ctx, u.ID, environment)
	if err != nil {
		return core.EnvironmentGrant{}, err
	}
	if vivant {
		return s.grantVivant(ctx, u.ID, environment)
	}

	const q = `
INSERT INTO environment_grant (user_id, environment, granted_by)
VALUES ($1, $2, NULLIF($3, 0)::bigint)
RETURNING id`

	var id int64
	if err := s.pool.QueryRow(ctx, q, u.ID, environment, parQui).Scan(&id); err != nil {
		return core.EnvironmentGrant{}, fmt.Errorf("droit de %s sur %q: %w", subject, environment, err)
	}
	return s.grantVivant(ctx, u.ID, environment)
}

// grantVivant relit le droit vivant, pour que les dates rendues soient celles de
// la base et non celles que le Go aurait calculées.
func (s *Store) grantVivant(ctx context.Context, userID int64, environment string) (core.EnvironmentGrant, error) {
	const q = `
SELECT ` + colonnesGrant + `
  FROM environment_grant g
  JOIN users u ON u.id = g.user_id
  LEFT JOIN users p ON p.id = g.granted_by
 WHERE g.user_id = $1 AND g.environment = $2 AND g.revoked_at IS NULL`

	g, err := scannerGrant(s.pool.QueryRow(ctx, q, userID, environment))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.EnvironmentGrant{}, fmt.Errorf("droit sur %q: %w", environment, ErrNotFound)
	}
	if err != nil {
		return core.EnvironmentGrant{}, fmt.Errorf("lecture du droit sur %q: %w", environment, err)
	}
	return g, nil
}

// Revoke éteint un droit.
//
// Idempotente, et SANS erreur quand il n'y en avait pas : révoquer un droit
// absent laisse l'état voulu, et échouer ferait douter d'un résultat pourtant
// atteint.
//
// Un UPDATE et jamais un DELETE : la ligne révoquée reste lisible, parce que
// « qui avait accès à prod le 3 mars » est une question d'audit.
func (s *Store) Revoke(ctx context.Context, subject, environment string) error {
	const q = `
UPDATE environment_grant g
   SET revoked_at = now()
  FROM users u
 WHERE u.id = g.user_id
   AND u.subject = $1 AND u.type = 'local'
   AND g.environment = $2
   AND g.revoked_at IS NULL`

	if _, err := s.pool.Exec(ctx, q, subject, environment); err != nil {
		return fmt.Errorf("révocation du droit de %s sur %q: %w", subject, environment, err)
	}
	return nil
}

// AppliquerLePlan exécute un plan de droits, TOUT OU RIEN.
//
// Une seule transaction, et ce n'est pas du confort : un plan à moitié appliqué
// laisserait un état que ni le fichier ni la base ne décrivent — des révocations
// faites, des attributions non — c'est-à-dire précisément l'incohérence que le
// versionnement existe pour supprimer. Le prochain passage la réparerait, mais
// entre-temps quelqu'un a perdu un accès qu'il devait garder.
//
// Les RÉVOCATIONS d'abord. L'ordre compte quand un même compte apparaît des deux
// côtés — ce qui n'arrive pas dans un plan bien formé, mais l'ordre inverse
// rendrait ce cas destructeur au lieu d'être neutre.
//
// Un compte inconnu fait ÉCHOUER le plan entier plutôt que d'être ignoré : une
// faute de frappe dans un fichier de droits doit se voir en revue, pas produire
// silencieusement un accès manquant.
func (s *Store) AppliquerLePlan(ctx context.Context, plan core.GrantPlan, parQui int64) error {
	if plan.Vide() {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("application du plan de droits: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, c := range plan.Revoquer {
		const q = `
UPDATE environment_grant g
   SET revoked_at = now()
  FROM users u
 WHERE u.id = g.user_id AND u.subject = $1 AND u.type = 'local'
   AND g.environment = $2 AND g.revoked_at IS NULL`

		if _, err := tx.Exec(ctx, q, c.Subject, c.Environment); err != nil {
			return fmt.Errorf("révocation de %s sur %q: %w", c.Subject, c.Environment, err)
		}
	}

	for _, c := range plan.Accorder {
		const q = `
INSERT INTO environment_grant (user_id, environment, granted_by)
SELECT u.id, $2, NULLIF($3, 0)::bigint
  FROM users u
 WHERE u.subject = $1 AND u.type = 'local'`

		tag, err := tx.Exec(ctx, q, c.Subject, c.Environment, parQui)
		if err != nil {
			return fmt.Errorf("droit de %s sur %q: %w", c.Subject, c.Environment, err)
		}
		if tag.RowsAffected() == 0 {
			// Le SELECT n'a rien rendu : le compte n'existe pas. Le plan entier
			// échoue — voir l'en-tête.
			return fmt.Errorf("compte %q: %w", c.Subject, ErrNotFound)
		}
	}

	if err := appliquerLesGroupes(ctx, tx, plan.Groupes); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// appliquerLesGroupes exécute la moitié du plan qui porte sur les groupes, dans
// la transaction des droits nominatifs. Révocations d'abord, pour la même
// raison que plus haut.
//
// Aucun « groupe inconnu » ici : un groupe n'est pas provisionné côté warden.
func appliquerLesGroupes(ctx context.Context, tx pgx.Tx, plan core.GroupPlan) error {
	for _, c := range plan.Revoquer {
		const q = `UPDATE group_grant SET revoked_at = now()
                    WHERE group_name = $1 AND environment = $2 AND revoked_at IS NULL`
		if _, err := tx.Exec(ctx, q, c.Group, c.Environment); err != nil {
			return fmt.Errorf("révocation du groupe %s sur %q: %w", c.Group, c.Environment, err)
		}
	}
	for _, g := range plan.RevoquerAdmin {
		const q = `UPDATE admin_group SET revoked_at = now()
                    WHERE group_name = $1 AND revoked_at IS NULL`
		if _, err := tx.Exec(ctx, q, g); err != nil {
			return fmt.Errorf("révocation de l'administration du groupe %s: %w", g, err)
		}
	}
	for _, c := range plan.Accorder {
		const q = `INSERT INTO group_grant (group_name, environment) VALUES ($1, $2)`
		if _, err := tx.Exec(ctx, q, c.Group, c.Environment); err != nil {
			return fmt.Errorf("droit du groupe %s sur %q: %w", c.Group, c.Environment, err)
		}
	}
	for _, g := range plan.AccorderAdmin {
		const q = `INSERT INTO admin_group (group_name) VALUES ($1)`
		if _, err := tx.Exec(ctx, q, g); err != nil {
			return fmt.Errorf("administration du groupe %s: %w", g, err)
		}
	}
	return nil
}

// ListGroupGrants rend les droits des groupes, RÉVOQUÉS compris : c'est, comme
// ListGrants, une vue d'audit.
func (s *Store) ListGroupGrants(ctx context.Context) ([]core.GroupGrant, error) {
	const q = `
SELECT group_name, environment, granted_at, revoked_at
  FROM group_grant
 ORDER BY group_name, environment, granted_at DESC`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des droits de groupe: %w", err)
	}
	defer lignes.Close()

	var out []core.GroupGrant
	for lignes.Next() {
		var g core.GroupGrant
		var revoque *time.Time
		if err := lignes.Scan(&g.Group, &g.Environment, &g.GrantedAt, &revoque); err != nil {
			return nil, fmt.Errorf("lecture d'un droit de groupe: %w", err)
		}
		if revoque != nil {
			g.RevokedAt = *revoque
		}
		out = append(out, g)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des droits de groupe: %w", err)
	}
	return out, nil
}

// GroupesAdmin rend, triés, les groupes qui ouvrent VIVEMENT l'administration.
func (s *Store) GroupesAdmin(ctx context.Context) ([]string, error) {
	const q = `SELECT group_name FROM admin_group WHERE revoked_at IS NULL ORDER BY group_name`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("groupes d'administration: %w", err)
	}
	defer lignes.Close()

	out := []string{}
	for lignes.Next() {
		var g string
		if err := lignes.Scan(&g); err != nil {
			return nil, fmt.Errorf("lecture d'un groupe d'administration: %w", err)
		}
		out = append(out, g)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("groupes d'administration: %w", err)
	}
	return out, nil
}
