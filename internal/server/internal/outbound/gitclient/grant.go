package gitclient

import (
	"context"
	"errors"
	"time"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// GrantIssuer frappe les jetons de push.
//
// Séparé du Client HTTP bien que les deux servent l'arête `api` → `git` : frapper
// un jeton ne fait AUCUN appel réseau. Les mêler donnerait à une opération
// purement locale l'apparence de dépendre de la disponibilité du rôle `git`, et
// ferait porter à son constructeur une URL dont elle n'a que faire.
type GrantIssuer struct {
	secret string
}

// NewGrantIssuer construit le frappeur autour du secret partagé.
//
// Le même secret que celui de l'API interne (ADR-0024, ADR-0028). Un secret vide
// produirait des jetons que n'importe qui pourrait forger, donc l'échec est au
// démarrage et non à la première frappe.
func NewGrantIssuer(secret string) (*GrantIssuer, error) {
	if secret == "" {
		return nil, errors.New("secret de signature des jetons de push absent : ils seraient forgeables")
	}
	return &GrantIssuer{secret: secret}, nil
}

// IssuePushGrant frappe un jeton pour un dépôt et un préfixe de ref.
//
// La portée arrive DÉRIVÉE de l'identité authentifiée par l'appelant. Cette
// fonction ne l'élargit jamais : elle la signe telle quelle, ce qui est
// précisément ce qui rend la vérification côté `gitd` utile.
func (g *GrantIssuer) IssuePushGrant(_ context.Context, repo, refPrefix string, expires time.Time) (string, error) {
	return gitwire.SignPushGrant(gitwire.PushGrant{
		Repo:      repo,
		RefPrefix: refPrefix,
		Expires:   expires.Unix(),
	}, g.secret)
}

// IssueFetchGrant frappe le jeton de run de l'ADR-0007.
//
// Le porteur est un pod runner, et l'ADR-0007 dit sans détour que ce jeton lui
// est lisible — donc lisible par le playbook, donc par du code arbitraire (§2).
// C'est acceptable parce que ce qu'il ouvre est minuscule : un espace de noms, en
// lecture, sur un transport dont le côté écriture refuse ce genre de jeton par
// construction.
//
// La durée doit tenir un run et pas davantage. Un jeton de lecture qui survit à
// son run est un jeton qui traîne dans l'image d'un pod terminé.
func (g *GrantIssuer) IssueFetchGrant(_ context.Context, repo, namespace string, expires time.Time) (string, error) {
	return gitwire.SignFetchGrant(gitwire.FetchGrant{
		Repo:      repo,
		Namespace: namespace,
		Expires:   expires.Unix(),
	}, g.secret)
}
