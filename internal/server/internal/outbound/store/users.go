package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// Identity est ce qu'un fournisseur d'identité affirme d'un utilisateur qui
// arrive.
type Identity struct {
	Type core.UserType

	// Subject est l'identifiant IMMUABLE du fournisseur : le `sub` OIDC,
	// l'entryUUID ou l'objectGUID LDAP. Jamais le DN, qui change dès qu'on
	// déplace quelqu'un d'unité organisationnelle ; jamais l'email ni le login,
	// qui changent aussi. L'ADR-0013 ne purgeant rien, un run de 2026 doit
	// encore se résoudre à la bonne personne en 2032.
	Subject string

	// DisplayName et Email sont un INSTANTANÉ rafraîchi à chaque connexion. Le
	// run pointant l'id, l'audit affiche l'identité actuelle d'un acteur passé.
	// C'est le bon comportement pour un produit d'audit, mais c'est un choix.
	DisplayName string
	Email       string
}

// User est un utilisateur tel que stocké.
type User struct {
	ID          int64
	Type        core.UserType
	Subject     string
	DisplayName string
	Email       string
	// Disabled est rendu à l'appelant plutôt qu'appliqué ici : refuser ou non
	// est une décision d'autorisation, elle appartient au cœur.
	Disabled bool
}

// UpsertUser provisionne l'utilisateur à sa première arrivée et rafraîchit
// l'instantané aux suivantes (ADR-0021).
//
// Deux fournisseurs pour un même humain font deux lignes, et elles ne sont pas
// fusionnées : un rapprochement erroné dans un journal d'audit est pire qu'une
// absence de rapprochement.
//
// Il n'existe volontairement aucune méthode de suppression. Les runs vivent
// indéfiniment ; supprimer un utilisateur ferait échouer la contrainte de clé
// étrangère ou détruirait de l'audit en cascade. `disabled_at` est là pour ça.
func (s *Store) UpsertUser(ctx context.Context, id Identity) (User, error) {
	if !id.Type.Valid() {
		return User{}, fmt.Errorf("type d'utilisateur %q inconnu", id.Type)
	}
	if id.Subject == "" {
		return User{}, fmt.Errorf("identité %s sans sujet: la clé naturelle ne peut pas être vide", id.Type)
	}

	const q = `
INSERT INTO users (type, subject, display_name, email)
VALUES ($1, $2, $3, NULLIF($4, ''))
ON CONFLICT (type, subject) DO UPDATE
   SET display_name = excluded.display_name,
       email        = excluded.email,
       last_seen_at = now()
RETURNING id, type, subject, display_name, coalesce(email, ''), disabled_at IS NOT NULL`

	var u User
	err := s.pool.QueryRow(ctx, q, string(id.Type), id.Subject, id.DisplayName, id.Email).
		Scan(&u.ID, &u.Type, &u.Subject, &u.DisplayName, &u.Email, &u.Disabled)
	if err != nil {
		return User{}, fmt.Errorf("provisionnement de l'utilisateur %s/%s: %w", id.Type, id.Subject, err)
	}
	return u, nil
}

// UserBySubject lit un utilisateur déjà provisionné.
//
// Distinct de UpsertUser : provisionner appartient au flux de connexion, qui a
// vérifié l'identité. Une lecture de requête ne doit pas créer de ligne, sinon
// un appelant non authentifié fait grossir la table `users` à volonté.
func (s *Store) UserBySubject(ctx context.Context, typ core.UserType, subject string) (User, error) {
	if subject == "" {
		return User{}, fmt.Errorf("sujet vide: %w", ErrNotFound)
	}

	const q = `
SELECT id, type, subject, display_name, coalesce(email, ''), disabled_at IS NOT NULL
  FROM users
 WHERE type = $1 AND subject = $2`

	var u User
	err := s.pool.QueryRow(ctx, q, string(typ), subject).
		Scan(&u.ID, &u.Type, &u.Subject, &u.DisplayName, &u.Email, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("utilisateur %s/%s: %w", typ, subject, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("lecture de l'utilisateur %s/%s: %w", typ, subject, err)
	}
	return u, nil
}
