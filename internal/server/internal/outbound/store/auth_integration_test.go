//go:build integration

// Étage 2 de l'ADR-0010, pour les comptes locaux et les sessions.
//
// Trois propriétés vivent dans le SQL et pas dans le Go, donc ne se vérifient
// qu'ici :
//
//  1. changer un mot de passe RÉVOQUE les sessions, dans la même transaction ;
//  2. `SessionByToken` ne rend que les sessions VIVANTES — la condition est dans
//     le WHERE, et un appelant ne peut pas l'oublier ;
//  3. `TouchSession` n'écrit que si l'âge le justifie, sans quoi le produit
//     ferait une écriture par requête authentifiée.
package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

const motDePasseIntegration = "un mot de passe de test"

func empreinteDe(t *testing.T, mdp string) string {
	t.Helper()
	e, err := core.Empreinte([]byte(mdp))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	return e
}

// compte crée un compte local avec un mot de passe.
func compte(t *testing.T, s *store.Store, sujet string, admin bool) core.User {
	t.Helper()
	ctx := context.Background()

	u, err := s.CreateLocalUser(ctx, sujet, "", admin)
	if err != nil {
		t.Fatalf("CreateLocalUser(%s): %v", sujet, err)
	}
	if err := s.PoserMotDePasse(ctx, u.ID, empreinteDe(t, motDePasseIntegration), 0); err != nil {
		t.Fatalf("PoserMotDePasse: %v", err)
	}
	return u
}

// ouvrirSession range une session vivante et rend son jeton.
func ouvrirSession(t *testing.T, s *store.Store, userID int64, expire time.Time) core.SessionToken {
	t.Helper()

	tok, err := core.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if _, err := s.CreateSession(context.Background(), userID, tok.Hash(), expire, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return tok
}

func TestUnCompteLocalSeRelitAvecSonEmpreinte(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)

	lu, empreinte, err := s.EmpreinteDuCompte(ctx, "alice")
	if err != nil {
		t.Fatalf("EmpreinteDuCompte: %v", err)
	}
	if lu.ID != u.ID || lu.Subject != "alice" {
		t.Errorf("compte = %+v", lu)
	}

	ok, err := core.VerifierMotDePasse(empreinte, []byte(motDePasseIntegration))
	if err != nil || !ok {
		t.Errorf("l'empreinte relue ne valide pas le mot de passe (ok=%v err=%v)", ok, err)
	}
}

// Un compte SANS mot de passe existe et ne peut pas se connecter. La jointure
// est interne exprès : rendre une empreinte vide inviterait l'appelant à traiter
// la chaîne vide comme un mot de passe.
func TestUnCompteSansMotDePasseNEstPasTrouve(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	if _, err := s.CreateLocalUser(ctx, "bob", "", false); err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}

	if _, _, err := s.EmpreinteDuCompte(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, attendu ErrNotFound", err)
	}

	// Il apparaît pourtant dans la liste, avec la mention qui explique.
	comptes, err := s.ListLocalUsers(ctx)
	if err != nil {
		t.Fatalf("ListLocalUsers: %v", err)
	}
	if len(comptes) != 1 || comptes[0].HasPassword {
		t.Errorf("comptes = %+v : un compte sans mot de passe se lit comme cassé", comptes)
	}
}

// LE test de la transaction. On change un mot de passe parce qu'on le croit
// compromis ; laisser vivre les sessions ouvertes avec l'ancien ne protégerait
// de rien.
func TestChangerLeMotDePasseRevoqueLesSessions(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))

	if _, _, err := s.SessionByToken(ctx, tok.Hash()); err != nil {
		t.Fatalf("la session n'est pas vivante avant le changement : %v", err)
	}

	if err := s.PoserMotDePasse(ctx, u.ID, empreinteDe(t, "un autre mot de passe"), u.ID); err != nil {
		t.Fatalf("PoserMotDePasse: %v", err)
	}

	if _, _, err := s.SessionByToken(ctx, tok.Hash()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v : la session a survécu au changement de mot de passe", err)
	}
}

// Désactiver sans révoquer ne ferait rien avant l'expiration du jeton —
// c'est-à-dire jusqu'à trente jours après avoir cru couper l'accès.
func TestDesactiverUnCompteRevoqueSesSessions(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))

	if _, err := s.SetUserDisabled(ctx, "alice", true); err != nil {
		t.Fatalf("SetUserDisabled: %v", err)
	}

	if _, _, err := s.SessionByToken(ctx, tok.Hash()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v : la session a survécu à la désactivation", err)
	}
}

// Réactiver ne DOIT PAS rouvrir les sessions coupées : elles ont été révoquées,
// et les ressusciter rendrait la désactivation réversible pour un attaquant qui
// aurait obtenu la réactivation.
func TestReactiverNeRouvrePasLesSessions(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))

	if _, err := s.SetUserDisabled(ctx, "alice", true); err != nil {
		t.Fatalf("désactivation: %v", err)
	}
	if _, err := s.SetUserDisabled(ctx, "alice", false); err != nil {
		t.Fatalf("réactivation: %v", err)
	}

	if _, _, err := s.SessionByToken(ctx, tok.Hash()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v : la réactivation a ressuscité une session révoquée", err)
	}
}

// Les trois conditions de vie sont dans le WHERE. Un appelant ne peut donc pas
// oublier d'en vérifier une — et il n'a aucune décision à prendre.
//
// Le schéma en ajoute une quatrième, en amont : `expires_at > created_at`
// interdit d'écrire une session née expirée.
func TestSeulesLesSessionsVivantesSontRendues(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)

	t.Run("expirée", func(t *testing.T) {
		// Une session est créée COURTE puis attendue, plutôt qu'antidatée : le
		// schéma refuse `expires_at <= created_at`, et c'est une bonne chose —
		// une session née expirée n'a aucun sens, et pouvoir en écrire une
		// permettrait de fabriquer des traces incohérentes.
		tok := ouvrirSession(t, s, u.ID, time.Now().Add(300*time.Millisecond))
		if _, _, err := s.SessionByToken(ctx, tok.Hash()); err != nil {
			t.Fatalf("la session n'était pas vivante à sa création : %v", err)
		}

		time.Sleep(400 * time.Millisecond)

		if _, _, err := s.SessionByToken(ctx, tok.Hash()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v : une session expirée a été rendue", err)
		}
	})

	t.Run("révoquée", func(t *testing.T) {
		tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))
		sess, _, err := s.SessionByToken(ctx, tok.Hash())
		if err != nil {
			t.Fatalf("SessionByToken: %v", err)
		}
		if err := s.RevokeSession(ctx, sess.ID); err != nil {
			t.Fatalf("RevokeSession: %v", err)
		}
		if _, _, err := s.SessionByToken(ctx, tok.Hash()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v : une session révoquée a été rendue", err)
		}
	})

	t.Run("inconnue", func(t *testing.T) {
		inconnu := sha256.Sum256([]byte("jamais vu"))
		if _, _, err := s.SessionByToken(ctx, inconnu[:]); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}

// `warden logout` est souvent lancé deux fois, et la seconde ne doit pas
// ressembler à une panne.
func TestRevoquerEstIdempotent(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))
	sess, _, _ := s.SessionByToken(ctx, tok.Hash())

	for i := range 3 {
		if err := s.RevokeSession(ctx, sess.ID); err != nil {
			t.Errorf("révocation %d : %v", i, err)
		}
	}
	// Et sur une session qui n'existe pas : le réconciliateur et la CLI
	// travaillent tous deux sur des états lus un instant plus tôt.
	if err := s.RevokeSession(ctx, 999999); err != nil {
		t.Errorf("révocation d'une session absente : %v", err)
	}
}

// La paresse du rafraîchissement est ce qui évite une écriture par requête
// authentifiée — donc une écriture sur le chemin le plus chaud du produit, pour
// une statistique.
func TestLeRafraichissementEstParesseux(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	tok := ouvrirSession(t, s, u.ID, time.Now().Add(time.Hour))
	sess, _, err := s.SessionByToken(ctx, tok.Hash())
	if err != nil {
		t.Fatalf("SessionByToken: %v", err)
	}
	avant := sess.LastSeenAt

	// Seuil d'une heure : la session vient d'être créée, rien ne doit bouger.
	if err := s.TouchSession(ctx, sess.ID, time.Hour); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	apres, _, _ := s.SessionByToken(ctx, tok.Hash())
	if !apres.LastSeenAt.Equal(avant) {
		t.Errorf("last_seen_at a bougé (%v -> %v) alors que le seuil n'était pas atteint",
			avant, apres.LastSeenAt)
	}

	// Seuil nul : l'écriture doit avoir lieu.
	time.Sleep(10 * time.Millisecond)
	if err := s.TouchSession(ctx, sess.ID, 0); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	apres, _, _ = s.SessionByToken(ctx, tok.Hash())
	if !apres.LastSeenAt.After(avant) {
		t.Errorf("last_seen_at n'a pas bougé (%v) alors que le seuil était atteint", apres.LastSeenAt)
	}
}

// Rejouer l'amorçage doit être sûr : c'est une commande qu'un opérateur relance
// après une erreur de frappe, et échouer l'obligerait à passer par le SQL.
func TestCreerUnCompteEstIdempotent(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	premier, err := s.CreateLocalUser(ctx, "alice", "Alice", false)
	if err != nil {
		t.Fatalf("première création: %v", err)
	}
	second, err := s.CreateLocalUser(ctx, "alice", "Alice Martin", true)
	if err != nil {
		t.Fatalf("seconde création: %v", err)
	}

	if second.ID != premier.ID {
		t.Errorf("un second compte a été créé (%d puis %d)", premier.ID, second.ID)
	}
	if second.DisplayName != "Alice Martin" || !second.IsAdmin {
		t.Errorf("compte = %+v : le rejeu n'a pas rafraîchi le descripteur", second)
	}
}

// Le schéma refuse une empreinte qui n'est pas de l'argon2id. La contrainte est
// une ceinture : elle attrape le jour où un chemin d'écriture non prévu
// enregistrerait un mot de passe en clair.
func TestLeSchemaRefuseUneEmpreinteQuiNEnEstPasUne(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u, err := s.CreateLocalUser(ctx, "alice", "", false)
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}

	if err := s.PoserMotDePasse(ctx, u.ID, "un mot de passe en clair", 0); err == nil {
		t.Error("le schéma a accepté une empreinte qui n'est pas de l'argon2id")
	}
}

// L'amorçage n'a AUCUN utilisateur pour porter `updated_by`. Zéro devient NULL
// plutôt qu'une clé étrangère invalide : la trace dit alors « on ne sait pas
// qui », ce qui est vrai, au lieu de désigner quelqu'un.
func TestLAmorcageNAttribueLeMotDePasseAPersonne(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u, err := s.CreateLocalUser(ctx, "alice", "", true)
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	if err := s.PoserMotDePasse(ctx, u.ID, empreinteDe(t, motDePasseIntegration), 0); err != nil {
		t.Fatalf("PoserMotDePasse avec parQui=0 : %v", err)
	}
}
