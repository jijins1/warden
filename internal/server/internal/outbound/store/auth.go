package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// La projection de `users`, en deux formes.
//
// Deux constantes et non une transformation de chaîne : une réécriture qui
// retirerait le préfixe `u.` frapperait aussi les endroits où ces deux
// caractères apparaissent pour une autre raison. Du SQL ne se manipule pas au
// remplacement de sous-chaîne.
//
// `colonnesUser` porte l'alias, pour les jointures ; `colonnesUserNu` ne l'a pas,
// parce qu'un RETURNING porte sur la ligne écrite et n'a pas de nom de table.
const (
	colonnesUser = `u.id, u.type, u.subject, u.display_name, coalesce(u.email, ''),
       u.disabled_at IS NOT NULL, u.is_admin`

	colonnesUserNu = `id, type, subject, display_name, coalesce(email, ''),
       disabled_at IS NOT NULL, is_admin`
)

func scannerUser(row pgx.Row, u *core.User, extra ...any) error {
	dest := append([]any{
		&u.ID, &u.Type, &u.Subject, &u.DisplayName, &u.Email, &u.Disabled, &u.IsAdmin,
	}, extra...)
	return row.Scan(dest...)
}

// EmpreinteDuCompte lit un compte local et son empreinte de mot de passe.
//
// La jointure est INTERNE : un compte `users` sans ligne dans `local_credential`
// n'a pas de mot de passe — c'est le cas d'un compte OIDC, ou d'un compte créé
// sans identifiants — et le rendre avec une empreinte vide inviterait l'appelant
// à traiter la chaîne vide comme un mot de passe.
//
// Le type est contraint à `local` : un compte OIDC homonyme ne doit pas pouvoir
// se connecter par mot de passe. La contrainte d'unicité de `users` porte sur
// `(type, subject)`, donc les deux peuvent coexister.
func (s *Store) EmpreinteDuCompte(ctx context.Context, subject string) (core.User, string, error) {
	if subject == "" {
		return core.User{}, "", fmt.Errorf("sujet vide: %w", ErrNotFound)
	}

	const q = `
SELECT ` + colonnesUser + `, c.password_hash
  FROM users u
  JOIN local_credential c ON c.user_id = u.id
 WHERE u.type = 'local' AND u.subject = $1`

	var u core.User
	var empreinte string
	err := scannerUser(s.pool.QueryRow(ctx, q, subject), &u, &empreinte)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.User{}, "", fmt.Errorf("compte local %q: %w", subject, ErrNotFound)
	}
	if err != nil {
		return core.User{}, "", fmt.Errorf("lecture du compte local %q: %w", subject, err)
	}
	return u, empreinte, nil
}

// PoserMotDePasse écrit l'empreinte ET coupe toutes les sessions du compte.
//
// Les deux dans la MÊME transaction, et ce n'est pas une commodité : on change un
// mot de passe parce qu'on le croit compromis, et laisser vivre les sessions
// ouvertes avec l'ancien ne protégerait de rien. Un appelant qui devrait penser à
// faire les deux finirait par oublier.
func (s *Store) PoserMotDePasse(ctx context.Context, userID int64, empreinte string, parQui int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mot de passe du compte %d: %w", userID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
INSERT INTO local_credential (user_id, password_hash, updated_by)
VALUES ($1, $2, NULLIF($3, 0)::bigint)
ON CONFLICT (user_id) DO UPDATE
   SET password_hash = excluded.password_hash,
       updated_at    = now(),
       updated_by    = excluded.updated_by`

	if _, err := tx.Exec(ctx, q, userID, empreinte, parQui); err != nil {
		return fmt.Errorf("mot de passe du compte %d: %w", userID, err)
	}
	if err := revoquerLesSessions(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func revoquerLesSessions(ctx context.Context, tx pgx.Tx, userID int64) error {
	const q = `UPDATE session SET revoked_at = now()
                WHERE user_id = $1 AND revoked_at IS NULL`
	if _, err := tx.Exec(ctx, q, userID); err != nil {
		return fmt.Errorf("révocation des sessions du compte %d: %w", userID, err)
	}
	return nil
}

// CreateLocalUser provisionne un compte local sans mot de passe.
//
// Sans mot de passe DÉLIBÉRÉMENT : la ligne créée ici ne permet aucune connexion
// tant que `PoserMotDePasse` n'a pas été appelée. Les deux étapes sont séparées
// pour que la seule fonction capable d'écrire une empreinte soit celle qui coupe
// aussi les sessions.
//
// Idempotente sur `(type, subject)` : elle rafraîchit le nom affiché et le droit
// d'administration plutôt que d'échouer, ce qui rend l'amorçage rejouable.
func (s *Store) CreateLocalUser(ctx context.Context, subject, nom string, admin bool) (core.User, error) {
	if subject == "" {
		return core.User{}, errors.New("compte sans sujet : la clé naturelle ne peut pas être vide")
	}
	if nom == "" {
		nom = subject
	}

	const q = `
INSERT INTO users (type, subject, display_name, is_admin)
VALUES ('local', $1, $2, $3)
ON CONFLICT (type, subject) DO UPDATE
   SET display_name = excluded.display_name,
       is_admin     = excluded.is_admin
RETURNING ` + colonnesUserNu

	var u core.User
	err := scannerUser(s.pool.QueryRow(ctx, q, subject, nom, admin), &u)
	if err != nil {
		return core.User{}, fmt.Errorf("création du compte local %q: %w", subject, err)
	}
	return u, nil
}

// SetUserDisabled active ou désactive un compte, et coupe ses sessions.
//
// La désactivation sans révocation ne ferait rien avant l'expiration du jeton,
// c'est-à-dire jusqu'à trente jours après avoir cru couper l'accès.
func (s *Store) SetUserDisabled(ctx context.Context, subject string, desactive bool) (core.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return core.User{}, fmt.Errorf("désactivation du compte %q: %w", subject, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
UPDATE users SET disabled_at = CASE WHEN $2 THEN now() ELSE NULL END
 WHERE type = 'local' AND subject = $1
RETURNING ` + colonnesUserNu

	var u core.User
	err = scannerUser(tx.QueryRow(ctx, q, subject, desactive), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.User{}, fmt.Errorf("compte local %q: %w", subject, ErrNotFound)
	}
	if err != nil {
		return core.User{}, fmt.Errorf("désactivation du compte %q: %w", subject, err)
	}

	if desactive {
		if err := revoquerLesSessions(ctx, tx, u.ID); err != nil {
			return core.User{}, err
		}
	}
	return u, tx.Commit(ctx)
}

// ListLocalUsers rend les comptes locaux, avec l'état de leur mot de passe.
//
// `HasPassword` est la colonne qui compte à l'exploitation : un compte créé dont
// personne n'a jamais posé le mot de passe existe, apparaît, et ne peut pas se
// connecter. Sans cette information, il se lit comme un compte cassé.
func (s *Store) ListLocalUsers(ctx context.Context) ([]core.LocalAccount, error) {
	const q = `
SELECT ` + colonnesUser + `, c.user_id IS NOT NULL, u.last_seen_at
  FROM users u
  LEFT JOIN local_credential c ON c.user_id = u.id
 WHERE u.type = 'local'
 ORDER BY u.subject`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des comptes locaux: %w", err)
	}
	defer lignes.Close()

	var out []core.LocalAccount
	for lignes.Next() {
		var a core.LocalAccount
		if err := scannerUser(lignes, &a.User, &a.HasPassword, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("lecture d'un compte local: %w", err)
		}
		out = append(out, a)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des comptes locaux: %w", err)
	}
	return out, nil
}

// --- les sessions ---

// CreateSession enregistre une session ouverte, avec ses groupes.
//
// Session et groupes dans la MÊME transaction : une session sans les groupes
// qu'elle devait porter serait une session qui marche, mais sans les droits
// attendus, et rien ne dirait pourquoi.
func (s *Store) CreateSession(ctx context.Context, userID int64, hash []byte, expire time.Time, groupes []string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("ouverture de session du compte %d: %w", userID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `INSERT INTO session (user_id, token_hash, expires_at)
               VALUES ($1, $2, $3) RETURNING id`

	var id int64
	if err := tx.QueryRow(ctx, q, userID, hash, expire).Scan(&id); err != nil {
		return 0, fmt.Errorf("ouverture de session du compte %d: %w", userID, err)
	}

	if len(groupes) > 0 {
		const qg = `INSERT INTO session_group (session_id, group_name)
                    SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`
		if _, err := tx.Exec(ctx, qg, id, groupes); err != nil {
			return 0, fmt.Errorf("groupes de la session du compte %d: %w", userID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("ouverture de session du compte %d: %w", userID, err)
	}
	return id, nil
}

// SessionByToken résout un jeton VIVANT en session et utilisateur.
//
// Les trois conditions de vie sont dans le WHERE et non dans le Go : une session
// révoquée, expirée ou inconnue est indistinctement ErrNotFound. Les rendre puis
// les filtrer plus haut inviterait un appelant à décider lui-même de ce qui est
// encore valable, et c'est exactement la décision qu'on ne veut pas voir se
// dupliquer.
//
// La session rendue porte ses groupes (ADR-0053). `is_admin` est le drapeau du
// compte, et lui seul : l'administration par groupe est une règle du cœur.
func (s *Store) SessionByToken(ctx context.Context, hash []byte) (core.Session, core.User, error) {
	const q = `
SELECT ` + colonnesUser + `, s.id, s.expires_at, s.last_seen_at,
       coalesce((SELECT array_agg(sg.group_name ORDER BY sg.group_name)
                   FROM session_group sg WHERE sg.session_id = s.id), '{}')
  FROM session s
  JOIN users u ON u.id = s.user_id
 WHERE s.token_hash = $1
   AND s.revoked_at IS NULL
   AND s.expires_at > now()`

	var u core.User
	var sess core.Session
	err := scannerUser(s.pool.QueryRow(ctx, q, hash), &u,
		&sess.ID, &sess.ExpiresAt, &sess.LastSeenAt, &sess.Groupes)
	if errors.Is(err, pgx.ErrNoRows) {
		// Le message ne dit PAS laquelle des trois raisons : il finit dans un log
		// serveur, mais rien ne garantit qu'il ne finira pas aussi ailleurs.
		return core.Session{}, core.User{}, fmt.Errorf("session: %w", ErrNotFound)
	}
	if err != nil {
		return core.Session{}, core.User{}, fmt.Errorf("lecture de la session: %w", err)
	}
	sess.UserID = u.ID
	return sess, u, nil
}

// RevokeSession coupe une session. Idempotent : une session déjà révoquée ou
// inconnue n'est pas une erreur, `warden logout` étant souvent lancé deux fois.
func (s *Store) RevokeSession(ctx context.Context, id int64) error {
	const q = `UPDATE session SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("révocation de la session %d: %w", id, err)
	}
	return nil
}

// TouchSession rafraîchit `last_seen_at`, PARESSEUSEMENT.
//
// La condition d'âge est dans le WHERE : l'écriture n'a lieu que si elle apporte
// quelque chose. Sans elle, le produit ferait une écriture par requête
// authentifiée — donc une écriture sur son chemin le plus chaud, pour une
// statistique.
func (s *Store) TouchSession(ctx context.Context, id int64, age time.Duration) error {
	const q = `UPDATE session SET last_seen_at = now()
                WHERE id = $1 AND last_seen_at < now() - $2::interval`
	if _, err := s.pool.Exec(ctx, q, id, age); err != nil {
		return fmt.Errorf("rafraîchissement de la session %d: %w", id, err)
	}
	return nil
}
