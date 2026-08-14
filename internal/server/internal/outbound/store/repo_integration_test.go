//go:build integration

// Étage 2 de l'ADR-0010 : la migration 00005 et la configuration par dépôt,
// contre un vrai PostgreSQL.
//
// Ce qui vaut d'être établi ici plutôt que raisonné : que la migration passe,
// que les contraintes de la table refusent effectivement ce que le cœur refuse
// déjà — deux barrières valent mieux qu'une seule qu'on croit avoir — et qu'un
// utilisateur ne naisse PAS administrateur.

package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func ouvrir(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open : %v", err)
	}
	t.Cleanup(s.Close)
	return s, ctx
}

// TestConfigurationDeDepotAllerRetour : écrire, relire, mettre à jour.
func TestConfigurationDeDepotAllerRetour(t *testing.T) {
	s, ctx := ouvrir(t)

	admin, err := s.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "ada", DisplayName: "Ada",
	})
	if err != nil {
		t.Fatalf("utilisateur : %v", err)
	}

	r := core.Repo{
		Name:        "depot",
		RemoteURL:   "https://forge.invalid/equipe/projet.git",
		MainlineRef: "refs/heads/main",
	}
	if err := s.UpsertRepo(ctx, r, admin.ID); err != nil {
		t.Fatalf("UpsertRepo : %v", err)
	}

	relu, err := s.GetRepo(ctx, "depot")
	if err != nil {
		t.Fatalf("GetRepo : %v", err)
	}
	if relu != r {
		t.Errorf("relu %+v, attendu %+v", relu, r)
	}

	// L'upsert plutôt qu'un couple create/update : l'administration déclare
	// l'état voulu et n'a pas à savoir si la ligne existe déjà.
	r.MainlineRef = "refs/heads/principale"
	if err := s.UpsertRepo(ctx, r, admin.ID); err != nil {
		t.Fatalf("UpsertRepo (mise à jour) : %v", err)
	}
	repos, err := s.ListRepos(ctx)
	if err != nil {
		t.Fatalf("ListRepos : %v", err)
	}
	if len(repos) != 1 || repos[0].MainlineRef != "refs/heads/principale" {
		t.Errorf("liste inattendue : %+v", repos)
	}
}

// TestUnUtilisateurNeNaitPasAdministrateur est la propriété d'amorçage.
//
// Si un utilisateur naissait administrateur, le premier à se connecter sur une
// instance neuve détiendrait la configuration de toute l'organisation — donc
// l'URL de forge depuis laquelle le code exécuté en production est tiré.
func TestUnUtilisateurNeNaitPasAdministrateur(t *testing.T) {
	s, ctx := ouvrir(t)

	u, err := s.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "premier-arrive", DisplayName: "Bob",
	})
	if err != nil {
		t.Fatalf("utilisateur : %v", err)
	}
	if u.IsAdmin {
		t.Fatal("un utilisateur fraîchement provisionné est administrateur")
	}

	relu, err := s.UserBySubject(ctx, core.UserLocal, "premier-arrive")
	if err != nil {
		t.Fatalf("UserBySubject : %v", err)
	}
	if relu.IsAdmin {
		t.Error("le drapeau d'administration remonte à vrai à la relecture")
	}
}

// TestLaBaseRefuseCeQueLeCoeurRefuse : la contrainte de table est la DERNIÈRE
// barrière, pas la première.
//
// Elle ne sert que si le cœur est court-circuité — un correctif appliqué en SQL,
// un futur chemin d'import. C'est précisément pour ces cas-là qu'elle existe, et
// c'est pourquoi elle se teste en contournant la validation du cœur.
func TestLaBaseRefuseCeQueLeCoeurRefuse(t *testing.T) {
	s, ctx := ouvrir(t)
	admin, err := s.UpsertUser(ctx, core.Identity{Type: core.UserLocal, Subject: "ada"})
	if err != nil {
		t.Fatalf("utilisateur : %v", err)
	}

	cas := []struct {
		nom  string
		repo core.Repo
	}{
		{"traversée dans le nom", core.Repo{Name: "..", RemoteURL: "https://f.invalid/x.git", MainlineRef: "refs/heads/main"}},
		{"transport exécutable", core.Repo{Name: "d", RemoteURL: "ext::sh -c evil", MainlineRef: "refs/heads/main"}},
		{"branche en nom court", core.Repo{Name: "d", RemoteURL: "https://f.invalid/x.git", MainlineRef: "main"}},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if err := s.UpsertRepo(ctx, c.repo, admin.ID); err == nil {
				t.Fatal("une configuration invalide a été écrite")
			}
		})
	}
}

// TestUneConfigurationEstImputable : l'auteur est obligatoire.
//
// La configuration d'un dépôt décide de ce qui s'exécutera en production. Une
// ligne sans auteur serait un changement que personne n'a fait.
func TestUneConfigurationEstImputable(t *testing.T) {
	s, ctx := ouvrir(t)

	err := s.UpsertRepo(ctx, core.Repo{
		Name: "depot", RemoteURL: "https://f.invalid/x.git", MainlineRef: "refs/heads/main",
	}, 0)
	if err == nil {
		t.Fatal("une configuration a été écrite sans auteur")
	}
	if !strings.Contains(err.Error(), "imputable") {
		t.Errorf("refus pour une autre raison : %v", err)
	}
}
