package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRefExists est rendue par CreateRef quand la ref désigne déjà un commit.
//
// L'appelant doit pouvoir la distinguer d'une panne : elle a une suite (comparer
// le commit visé à celui déjà en place), une panne n'en a pas.
var ErrRefExists = errors.New("ref already exists")

// CreateRef pointe une ref sur un commit, EN EXIGEANT qu'elle n'existe pas.
//
// Distincte d'UpdateRef, qui écrase sans condition : `update-ref <ref> <new> ""`
// échoue atomiquement si la ref existe déjà, alors qu'un « lire puis écrire »
// équivalent laisserait une fenêtre entre les deux. C'est le contrôle qui
// compte ici, parce que la ref visée est un artefact d'audit qui s'écrit une
// fois (ADR-0027) — jamais un pointeur qu'on déplace.
func (g *Git) CreateRef(ctx context.Context, ref Ref, commit SHA) error {
	if _, err := NewRef(string(ref)); err != nil {
		return err
	}
	if !commit.Valid() {
		return fmt.Errorf("invalid commit SHA: %q", commit)
	}

	// L'ancienne valeur vide dit à git « cette ref ne doit pas exister » ; `--`
	// pour qu'une ref au nom d'option ne soit pas lue comme telle.
	if _, stderr, err := g.run(ctx, "update-ref", "--", ref.String(), commit.String(), ""); err != nil {
		// Classée par une SECONDE vérification plutôt que par le texte de stderr,
		// même raisonnement que HasObject face à « fatal: bad object » : le
		// message d'erreur de git n'est pas une interface stable.
		existe, errHasRef := g.hasRef(ctx, ref)
		if errHasRef == nil && existe {
			return ErrRefExists
		}
		return fmt.Errorf("git update-ref %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	return nil
}
