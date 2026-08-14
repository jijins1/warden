package gitwire

import (
	"errors"
	"strings"
	"time"
)

// FetchGrant est la portée de LECTURE qu'un jeton ouvre à un pod runner.
//
// Symétrique de PushGrant, et pour la même raison : le rôle `git` ne parle pas à
// PostgreSQL, donc la portée doit voyager dans le jeton plutôt que dans un état
// répliqué (ADR-0028). Le genre de la signature les rend non interchangeables —
// un pod runner ne doit pas pouvoir présenter son jeton de lecture comme une
// autorisation d'écriture.
//
// C'est le « jeton de run » que l'ADR-0007 décrit et que rien n'avait encore
// implémenté : la lecture du smart HTTP était livrée depuis l'ADR-0026 contre un
// port `Authorizer` sans implémentation, donc jamais servie.
type FetchGrant struct {
	// Repo est l'identifiant de dépôt.
	Repo string `json:"r"`

	// Namespace confine la vue des refs à un seul run (ADR-0007).
	//
	// ATTENTION au chemin qu'il désigne : git IMBRIQUE chaque composant séparé
	// par une barre. `runs/<id>` résout
	// `refs/namespaces/runs/refs/namespaces/<id>/`, et non
	// `refs/namespaces/runs/<id>/` comme l'ADR-0007 l'écrivait — l'ADR-0026 a
	// mesuré l'écart et l'a corrigé. Quiconque crée ces refs compose le chemin
	// en insérant `refs/namespaces/` devant CHAQUE composant.
	Namespace string `json:"n"`

	// Expires est une date Unix en secondes.
	//
	// La révocation est ici plus importante que pour l'écriture : le porteur est
	// un pod dont l'ADR-0007 dit que le jeton lui est lisible, donc au playbook,
	// donc à du code arbitraire. La durée doit tenir un run, pas une journée.
	Expires int64 `json:"x"`
}

// SignFetchGrant frappe un jeton de lecture.
func SignFetchGrant(g FetchGrant, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("secret de signature absent : le jeton serait forgeable")
	}
	if g.Repo == "" || g.Namespace == "" {
		// Un espace de noms vide donnerait à git la vue COMPLÈTE des refs, soit
		// exactement la fuite que l'ADR-0007 existe pour empêcher. Le refus est
		// ici, à la frappe, plutôt qu'à la vérification.
		return "", errors.New("portée incomplète : dépôt et espace de noms sont obligatoires")
	}
	if strings.HasPrefix(g.Namespace, "/") || strings.HasSuffix(g.Namespace, "/") ||
		strings.Contains(g.Namespace, "..") {
		return "", errors.New("espace de noms mal formé")
	}
	if g.Expires <= 0 {
		return "", errors.New("portée sans expiration : le jeton serait éternel")
	}
	return signer(genreFetch, g, secret)
}

// ParseFetchGrant vérifie un jeton de lecture et rend la portée qu'il déclare.
func ParseFetchGrant(token, secret string) (FetchGrant, error) {
	var g FetchGrant
	if err := verifier(genreFetch, token, secret, &g); err != nil {
		return FetchGrant{}, err
	}
	if g.Repo == "" || g.Namespace == "" {
		return FetchGrant{}, ErrGrantInvalid
	}
	if time.Now().Unix() >= g.Expires {
		return FetchGrant{}, ErrGrantInvalid
	}
	return g, nil
}
