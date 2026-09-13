package git

import (
	"context"
	"errors"
	"testing"
)

// TestCreateRefEcritUneFois épingle le contrat que le rôle `git` s'apprête à
// exposer pour les commits d'audit lancés depuis le navigateur (ADR-0042) : la
// première écriture réussit, toute écriture suivante sur la MÊME ref échoue et
// la ref reste où elle était.
func TestCreateRefEcritUneFois(t *testing.T) {
	bare, shas := depotBare(t)
	g := New(bare)
	ctx := context.Background()

	ref, err := NewRef("refs/audit/1/01JZQ0000000000000000P0001")
	if err != nil {
		t.Fatalf("NewRef: %v", err)
	}

	if err := g.CreateRef(ctx, ref, shas[0]); err != nil {
		t.Fatalf("première écriture: %v", err)
	}

	resolu, err := g.Resolve(ctx, ref.String())
	if err != nil {
		t.Fatalf("résolution après écriture: %v", err)
	}
	if resolu != shas[0] {
		t.Fatalf("ref résolue à %s, attendu %s", resolu, shas[0])
	}

	// Rejeu avec le MÊME commit : toujours refusé au niveau de cette primitive.
	// C'est à l'appelant (gitd) de traiter ErrRefExists + comparaison comme un
	// succès idempotent ; CreateRef lui-même ne devine jamais l'intention.
	if err := g.CreateRef(ctx, ref, shas[0]); !errors.Is(err, ErrRefExists) {
		t.Fatalf("rejeu identique: attendu ErrRefExists, obtenu %v", err)
	}

	// Tentative avec un AUTRE commit : refusée aussi, et la ref ne bouge pas.
	if err := g.CreateRef(ctx, ref, shas[1]); !errors.Is(err, ErrRefExists) {
		t.Fatalf("écriture divergente: attendu ErrRefExists, obtenu %v", err)
	}
	resolu, err = g.Resolve(ctx, ref.String())
	if err != nil {
		t.Fatalf("résolution après tentative divergente: %v", err)
	}
	if resolu != shas[0] {
		t.Fatalf("la ref a bougé vers %s après une écriture refusée", resolu)
	}
}

// TestCreateRefRefuseUnCommitAbsent : une ref ne doit jamais pointer sur rien.
func TestCreateRefRefuseUnCommitAbsent(t *testing.T) {
	bare, _ := depotBare(t)
	g := New(bare)
	ctx := context.Background()

	ref, err := NewRef("refs/audit/1/01JZQ0000000000000000P0002")
	if err != nil {
		t.Fatalf("NewRef: %v", err)
	}

	err = g.CreateRef(ctx, ref, SHA("0123456789abcdef0123456789abcdef01234567"))
	if err == nil {
		t.Fatal("commit absent accepté")
	}
	if errors.Is(err, ErrRefExists) {
		t.Fatal("un commit absent ne doit pas se rapporter à ErrRefExists")
	}
}

// TestCreateRefRefuseUnSHAMalforme : validé avant d'atteindre git, comme
// UpdateRef.
func TestCreateRefRefuseUnSHAMalforme(t *testing.T) {
	bare, _ := depotBare(t)
	g := New(bare)

	ref, err := NewRef("refs/audit/1/01JZQ0000000000000000P0003")
	if err != nil {
		t.Fatalf("NewRef: %v", err)
	}

	if err := g.CreateRef(context.Background(), ref, SHA("pas-un-sha")); err == nil {
		t.Fatal("SHA malformé accepté")
	}
}
