package keyinventory

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// Vault est l'adaptateur `key-inventory` sur un montage KV v2 (ADR-0047).
//
// Même contrat que Kubernetes : la partie privée dans le magasin de secrets,
// la partie publique et les métadonnées dans PostgreSQL. La clé KV porte le
// même nom que le Secret Kubernetes (core.SecretNameFor), que le catalogue
// enregistre comme pointeur.
type Vault struct {
	kv        MagasinKV
	montage   string
	catalogue core.KeyCatalog
}

// NewVault construit l'adaptateur et échoue à froid.
func NewVault(kv MagasinKV, montage string, catalogue core.KeyCatalog) (*Vault, error) {
	if kv == nil {
		return nil, errors.New("magasin de clés : client Vault absent")
	}
	if montage == "" {
		return nil, errors.New("magasin de clés : montage Vault absent")
	}
	if catalogue == nil {
		return nil, errors.New("magasin de clés : catalogue absent")
	}
	return &Vault{kv: kv, montage: montage, catalogue: catalogue}, nil
}

// Issue rend la clé d'accès d'un environnement. `user` et `hostScope` sont
// ignorés, comme dans l'adaptateur Kubernetes : la v1 rend la clé de
// l'environnement, sans expiration.
func (v *Vault) Issue(ctx context.Context, _, environment string, _ []string) (core.AccessKey, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.AccessKey{}, err
	}

	privee, trouve, err := v.kv.Lire(ctx, v.montage, core.SecretNameFor(environment))
	if err != nil {
		return core.AccessKey{}, fmt.Errorf("lecture de la clé de %s: %w", environment, err)
	}
	if !trouve {
		return core.AccessKey{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoKey)
	}
	if len(privee) == 0 {
		// Présente mais vide : pire qu'absente, elle ferait croire
		// l'environnement configuré.
		return core.AccessKey{}, fmt.Errorf("environnement %s : la clé Vault %s est vide",
			environment, core.SecretNameFor(environment))
	}
	return core.NewAccessKey(privee, time.Time{}), nil
}

// Import pose une partie privée fournie par un opérateur.
func (v *Vault) Import(ctx context.Context, environment string, private []byte, by int64) (core.KeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.KeyDescriptor{}, err
	}
	publique, empreinte, err := publiqueDe(private)
	if err != nil {
		return core.KeyDescriptor{}, err
	}
	return v.poser(ctx, environment, private,
		avecCommentaire(publique, environment), empreinte, core.KeyImported, by)
}

// Generate frappe une paire ed25519. La partie privée ne quitte pas Vault.
func (v *Vault) Generate(ctx context.Context, environment string, by int64) (core.KeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.KeyDescriptor{}, err
	}
	privee, publique, empreinte, err := frapper(environment)
	if err != nil {
		return core.KeyDescriptor{}, err
	}
	return v.poser(ctx, environment, privee, publique, empreinte, core.KeyGenerated, by)
}

// Describe lit le catalogue, jamais Vault : le descripteur ne contient rien de
// sensible.
func (v *Vault) Describe(ctx context.Context, environment string) (core.KeyDescriptor, error) {
	return v.catalogue.KeyByEnvironment(ctx, environment)
}

// poser écrit dans Vault PUIS le catalogue, dans cet ordre et pour la même
// raison que l'adaptateur Kubernetes : un catalogue qui pointerait une clé
// absente décrirait un environnement dont aucun run ne peut se servir.
func (v *Vault) poser(ctx context.Context, environment string,
	privee []byte, publique, empreinte string, origine core.KeyOrigin, by int64,
) (core.KeyDescriptor, error) {
	nom := core.SecretNameFor(environment)
	if err := v.kv.Ecrire(ctx, v.montage, nom, privee); err != nil {
		return core.KeyDescriptor{}, fmt.Errorf("écriture de la clé de %s: %w", environment, err)
	}
	return cataloguer(ctx, v.catalogue, core.KeyDescriptor{
		Environment: environment,
		PublicKey:   publique,
		Fingerprint: empreinte,
		SecretName:  nom,
		Origin:      origine,
		CreatedBy:   by,
	})
}
