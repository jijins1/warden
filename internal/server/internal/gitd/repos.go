package gitd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// nomDeDepot borne ce qu'un identifiant de dépôt peut contenir.
//
// La même expression que la contrainte `run_repo_forme` de la migration 00004.
// Les deux existent : la base empêche d'écrire un nom hostile, celle-ci empêche
// de l'utiliser si jamais il entrait par un autre chemin.
var nomDeDepot = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// depots résout un identifiant de dépôt en répertoire de dépôt bare.
type depots struct{ racine string }

// chemin résout un identifiant de dépôt en chemin, SANS exiger qu'il existe.
//
// Séparé de `dir` parce que la création d'un dépôt a besoin du chemin d'un dépôt
// qui n'existe pas encore — et que relâcher le contrôle d'existence dans `dir`
// pour la servir supprimerait la distinction entre « dépôt inconnu » et « ce
// répertoire n'est pas un dépôt », deux pannes qu'un opérateur ne diagnostique
// pas de la même façon.
//
// La validation précède TOUTE manipulation de chemin. Un filepath.Clean appliqué
// après coup sur une entrée non validée est un contrôle qu'on croit avoir : il
// normalise, il ne refuse pas.
func (d depots) chemin(repo string) (string, *gitwire.Error) {
	if !nomDeDepot.MatchString(repo) {
		return "", &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "nom de dépôt invalide",
		}
	}

	// Ceinture et bretelles. L'expression ci-dessus interdit déjà le point
	// initial et la barre oblique, donc `..` et tout chemin composé ; ce refus
	// explicite survit à un assouplissement futur de l'expression.
	if repo == ".." || strings.Contains(repo, "/") || strings.Contains(repo, `\`) {
		return "", &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "nom de dépôt invalide",
		}
	}

	dir := filepath.Join(d.racine, repo+".git")

	// Confirmation que la jonction n'est pas sortie de la racine. Redondant avec
	// ce qui précède, et gardé : c'est l'invariant qui compte vraiment, et il se
	// vérifie ici plutôt que de se déduire de la forme du nom.
	rel, err := filepath.Rel(d.racine, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &gitwire.Error{
			Code:    gitwire.CodeBadRequest,
			Message: "nom de dépôt invalide",
		}
	}

	return dir, nil
}

// dir rend le répertoire d'un dépôt bare EXISTANT.
func (d depots) dir(repo string) (string, *gitwire.Error) {
	dir, errChemin := d.chemin(repo)
	if errChemin != nil {
		return "", errChemin
	}

	// Un dépôt bare a un répertoire `objects`. Le tester plutôt que le seul
	// répertoire distingue « ce dépôt n'existe pas » de « ce répertoire existe
	// mais n'est pas un dépôt », deux choses qu'un opérateur ne diagnostique pas
	// de la même façon.
	if _, err := os.Stat(filepath.Join(dir, "objects")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", &gitwire.Error{
				Code:    gitwire.CodeUnknownRepo,
				Message: "dépôt inconnu",
			}
		}
		return "", &gitwire.Error{
			Code:    gitwire.CodeInternal,
			Message: "dépôt illisible",
		}
	}

	return dir, nil
}
