package vaultinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// MagasinKV range une valeur sous un nom, dans un montage. C'est le client
// Vault de `outbound/vault`, reçu par cette interface parce que deux
// adaptateurs sortants ne s'importent pas (ADR-0019).
type MagasinKV interface {
	// Lire rend `trouve` faux quand la clé n'existe pas : ce n'est pas une
	// erreur.
	Lire(ctx context.Context, montage, chemin string) (valeur []byte, trouve bool, err error)
	Ecrire(ctx context.Context, montage, chemin string, valeur []byte) error
}

// Vault est l'adaptateur `vault-inventory` sur un montage KV v2 (ADR-0047).
//
// Même contrat que Kubernetes : la clé en clair dans le magasin de secrets,
// ses métadonnées dans PostgreSQL. La clé KV porte le même nom que le Secret
// Kubernetes (core.SecretNameForVault), que le catalogue enregistre comme
// pointeur.
type Vault struct {
	kv        MagasinKV
	montage   string
	catalogue core.VaultCatalog
}

// NewVault construit l'adaptateur et échoue à froid.
func NewVault(kv MagasinKV, montage string, catalogue core.VaultCatalog) (*Vault, error) {
	if kv == nil {
		return nil, errors.New("magasin de clés de vault : client Vault absent")
	}
	if montage == "" {
		return nil, errors.New("magasin de clés de vault : montage Vault absent")
	}
	if catalogue == nil {
		return nil, errors.New("magasin de clés de vault : catalogue absent")
	}
	return &Vault{kv: kv, montage: montage, catalogue: catalogue}, nil
}

// Issue rend la clé de vault d'un environnement.
func (v *Vault) Issue(ctx context.Context, environment string) (core.VaultKey, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKey{}, err
	}

	plaintext, trouve, err := v.kv.Lire(ctx, v.montage, core.SecretNameForVault(environment))
	if err != nil {
		return core.VaultKey{}, fmt.Errorf("lecture de la clé de vault de %s: %w", environment, err)
	}
	if !trouve {
		return core.VaultKey{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoVaultKey)
	}
	if len(plaintext) == 0 {
		return core.VaultKey{}, fmt.Errorf("environnement %s : la clé Vault %s est vide",
			environment, core.SecretNameForVault(environment))
	}
	return core.NewVaultKey(plaintext), nil
}

// Import pose une clé fournie par un opérateur.
func (v *Vault) Import(ctx context.Context, environment string, plaintext []byte, by int64) (core.VaultKeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	if len(plaintext) == 0 {
		return core.VaultKeyDescriptor{}, errors.New("clé de vault vide")
	}
	return v.poser(ctx, environment, plaintext, core.VaultKeyImported, by)
}

// Generate tire une clé. Elle ne quitte pas Vault.
func (v *Vault) Generate(ctx context.Context, environment string, by int64) (core.VaultKeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	plaintext, err := tirer(environment)
	if err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	return v.poser(ctx, environment, plaintext, core.VaultKeyGenerated, by)
}

// Describe lit le catalogue, jamais Vault.
func (v *Vault) Describe(ctx context.Context, environment string) (core.VaultKeyDescriptor, error) {
	return v.catalogue.VaultKeyByEnvironment(ctx, environment)
}

// EncryptString chiffre une valeur avec la clé de l'environnement. La clé ne
// fait jamais l'aller-retour vers l'appelant.
func (v *Vault) EncryptString(ctx context.Context, environment string, plaintext []byte, varName string) (string, error) {
	cle, err := v.Issue(ctx, environment)
	if err != nil {
		return "", err
	}
	return chiffrer(environment, cle, plaintext, varName)
}

// poser écrit dans Vault PUIS le catalogue : un catalogue qui pointerait une
// clé absente décrirait un environnement dont aucun run ne peut se servir.
func (v *Vault) poser(ctx context.Context, environment string,
	plaintext []byte, origine core.VaultKeyOrigin, by int64,
) (core.VaultKeyDescriptor, error) {
	nom := core.SecretNameForVault(environment)
	somme := sha256.Sum256(plaintext)

	if err := v.kv.Ecrire(ctx, v.montage, nom, plaintext); err != nil {
		return core.VaultKeyDescriptor{}, fmt.Errorf("écriture de la clé de vault de %s: %w", environment, err)
	}
	return cataloguer(ctx, v.catalogue, core.VaultKeyDescriptor{
		Environment: environment,
		Fingerprint: hex.EncodeToString(somme[:]),
		SecretName:  nom,
		Origin:      origine,
		CreatedBy:   by,
	})
}
