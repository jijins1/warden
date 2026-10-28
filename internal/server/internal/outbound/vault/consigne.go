package vault

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// La consigne : un magasin de valeurs qui ne font que PASSER, sur un montage
// KV v2 dédié (ADR-0047).
//
// Même contrat que la consigne Kubernetes de `outbound/orchestrator` : une
// valeur déposée par une réplique est lisible par toutes, et détruite à la
// lecture. Le montage est réglé à `max_versions = 1` par le déploiement ;
// la destruction passe de toute façon par `metadata/`, qui emporte toutes les
// versions.

// Champs de la clé KV v2.
const (
	champDepot   = "stashed_at"
	formatDepot  = time.RFC3339Nano
	ttlParDefaut = 10 * time.Minute
)

// Consigne implémente core.SecretStash sur Vault.
type Consigne struct {
	kv      *Client
	montage string
	ttl     time.Duration
	now     func() time.Time
}

// NewConsigne construit la consigne. `ttl` borne l'âge d'une valeur lisible :
// KV n'expire rien, et une valeur jamais reprise ne doit pas rester utilisable
// indéfiniment. Zéro vaut la valeur par défaut.
func NewConsigne(kv *Client, montage string, ttl time.Duration) (*Consigne, error) {
	if kv == nil {
		return nil, errors.New("consigne vault : client absent")
	}
	if err := cheminSur(montage); err != nil {
		return nil, fmt.Errorf("consigne vault : montage : %w", err)
	}
	if ttl < 0 {
		return nil, errors.New("consigne vault : durée de vie négative")
	}
	if ttl == 0 {
		ttl = ttlParDefaut
	}
	return &Consigne{kv: kv, montage: montage, ttl: ttl, now: time.Now}, nil
}

// Stash range une valeur pour un seul usage. Écrase ce qui existait, comme la
// consigne Kubernetes : reprendre un enrôlement interrompu doit remplacer le
// mot de passe précédent.
func (c *Consigne) Stash(ctx context.Context, cle string, valeur []byte) error {
	if cle == "" {
		return errors.New("consigne : clé absente")
	}
	if len(valeur) == 0 {
		return errors.New("consigne : valeur vide")
	}
	if err := c.kv.EcrireChamps(ctx, c.montage, cle, map[string]string{
		cleValeur:  base64.StdEncoding.EncodeToString(valeur),
		champDepot: c.now().UTC().Format(formatDepot),
	}); err != nil {
		// Le message ne cite jamais la valeur.
		return fmt.Errorf("consigne %s : %w", cle, err)
	}
	return nil
}

// Take lit la valeur ET la détruit.
//
// L'échec de la destruction n'annule PAS la lecture, comme dans la consigne
// Kubernetes : la valeur est rendue avec l'erreur, pour que le reste soit
// nettoyable. Une valeur plus vieille que le TTL est détruite et traitée comme
// absente.
func (c *Consigne) Take(ctx context.Context, cle string) ([]byte, error) {
	if cle == "" {
		return nil, errors.New("consigne : clé absente")
	}

	champs, trouve, err := c.kv.LireChamps(ctx, c.montage, cle)
	if err != nil {
		return nil, fmt.Errorf("consigne %s : %w", cle, err)
	}
	if !trouve {
		return nil, fmt.Errorf("consigne %s : %w", cle, core.ErrNoStash)
	}

	// Destruction immédiate, avant même d'examiner la valeur : une valeur
	// périmée ou malformée n'a pas plus de raison de rester qu'une bonne.
	errDestruction := c.kv.Detruire(ctx, c.montage, cle)

	valeur, err := c.decoder(champs)
	if err != nil {
		return nil, fmt.Errorf("consigne %s : %w", cle, err)
	}
	if errDestruction != nil {
		return valeur, fmt.Errorf("consigne %s : lue mais non détruite : %w", cle, errDestruction)
	}
	return valeur, nil
}

// decoder extrait la valeur et vérifie son âge.
func (c *Consigne) decoder(champs map[string]any) ([]byte, error) {
	brut, _ := champs[cleValeur].(string)
	valeur, err := base64.StdEncoding.DecodeString(brut)
	if err != nil || len(valeur) == 0 {
		return nil, core.ErrNoStash
	}

	depotBrut, _ := champs[champDepot].(string)
	depot, err := time.Parse(formatDepot, depotBrut)
	if err != nil {
		// Sans date de dépôt, l'âge est inconnu : la valeur est refusée plutôt
		// que supposée fraîche.
		return nil, core.ErrNoStash
	}
	if c.now().Sub(depot) > c.ttl {
		return nil, core.ErrNoStash
	}
	return valeur, nil
}
