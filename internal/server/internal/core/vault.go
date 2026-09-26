package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// VaultKey est la clé Ansible Vault d'un environnement, telle que le control
// plane la tient en mémoire (ADR-0045).
//
// Enveloppée dans une structure à champ non exporté, sur le même patron que
// AccessKey (ADR-0020) : elle ne peut être construite que par ce paquet, et
// les trois chemins d'impression usuels la remplacent par un marqueur.
//
// Contrairement à AccessKey, elle ne descend JAMAIS vers un humain — c'est la
// décision de cadrage de l'ADR-0045. Elle sert uniquement à (a) déchiffrer au
// moment du run, livrée au pod par le canal de dispatch comme la clé SSH, et
// (b) chiffrer côté serveur pour `EncryptString`, sans jamais quitter ce
// paquet en clair.
type VaultKey struct {
	v []byte
}

// nolint:gosec
const vaultKeyMask = "[clé de vault supprimée]"

// NewVaultKey enveloppe un mot de passe de vault en clair.
//
// Exportée pour la même raison que NewAccessKey : l'adaptateur qui lit le
// Secret vit dans `outbound`, et l'ADR-0019 lui interdit d'être dans le cœur.
func NewVaultKey(plaintext []byte) VaultKey { return VaultKey{v: plaintext} }

// Reveal rend la valeur en clair. Nom délibérément inconfortable, comme celui
// d'AccessKey : il doit se voir en revue, et il n'a que deux appelants
// légitimes — l'écriture du fichier `<pdd>/env/vault_password` côté runner, et
// la routine de chiffrement de l'adaptateur `vaultinventory`.
func (k VaultKey) Reveal() []byte { return k.v }

// Zero dit si la clé est la clé nulle.
func (k VaultKey) Zero() bool { return len(k.v) == 0 }

// String masque la valeur.
func (k VaultKey) String() string { return vaultKeyMask }

// GoString masque la valeur sous `%#v`, que String ne couvre pas.
func (k VaultKey) GoString() string { return vaultKeyMask }

// MarshalJSON masque la valeur, pour la même raison qu'AccessKey : rien ne
// sérialise légitimement ce type, mais une structure englobante qui finirait
// par en contenir un ne doit pas fuiter au premier `json.Marshal`.
func (k VaultKey) MarshalJSON() ([]byte, error) {
	return []byte(`"` + vaultKeyMask + `"`), nil
}

// VaultKeyDescriptor est ce qu'on peut dire d'une clé de vault sans la
// révéler.
//
// C'est ce que l'API d'environnement rend et ce que `warden vault show`
// affiche : une empreinte à comparer, rien de plus. Il n'y a pas de partie
// « publique » comme pour une clé SSH — un mot de passe symétrique n'en a pas.
type VaultKeyDescriptor struct {
	Environment string
	Fingerprint string

	// SecretName pointe le Secret Kubernetes portant la valeur en clair. Un
	// POINTEUR, jamais la valeur — même raisonnement que KeyDescriptor.SecretName.
	SecretName string

	Origin    VaultKeyOrigin
	CreatedAt time.Time
	// RotatedAt est le temps nul tant que la clé n'a jamais été remplacée.
	RotatedAt time.Time
	CreatedBy int64
}

// SecretNameForVault compose le nom du Secret portant le mot de passe en
// clair.
//
// Distinct de SecretNameFor (clés SSH) : les deux familles de secrets ne
// doivent jamais partager un nom de Secret, sous peine qu'une rotation de
// l'une écrase l'autre.
func SecretNameForVault(environment string) string {
	return "warden-vault-" + environment
}

// VaultKeyOrigin dit d'où vient le mot de passe.
type VaultKeyOrigin string

const (
	// VaultKeyGenerated : warden a tiré le mot de passe, il n'a jamais quitté
	// le cluster.
	VaultKeyGenerated VaultKeyOrigin = "generated"
	// VaultKeyImported : il vient d'un opérateur — migration d'un dépôt déjà
	// chiffré avec un mot de passe existant.
	VaultKeyImported VaultKeyOrigin = "imported"
)

// Valid dit si l'origine fait partie de celles que le schéma accepte.
func (o VaultKeyOrigin) Valid() bool {
	return o == VaultKeyGenerated || o == VaultKeyImported
}

// ErrNoVaultKey signale qu'aucune clé de vault n'est posée pour cet
// environnement.
//
// Comme ErrNoKey, ce n'est PAS une erreur : un environnement dont aucun
// playbook n'utilise `!vault |` n'a besoin d'aucune clé, et c'est le cas par
// défaut. L'appelant décide.
var ErrNoVaultKey = errors.New("aucune clé de vault pour cet environnement")

// VaultIssuer répond à « avec quelle clé déchiffrer les secrets versionnés de
// cet environnement » au moment de la livraison d'un ordre.
//
// Séparé de VaultStore pour la même raison que KeyIssuer l'est de KeyStore :
// l'un est sur le chemin chaud de chaque run, l'autre sert l'administration.
type VaultIssuer interface {
	Issue(ctx context.Context, environment string) (VaultKey, error)
}

// VaultStore pose et lit les clés de vault, et chiffre pour le compte d'un
// utilisateur qui n'a jamais accès à la valeur en clair.
type VaultStore interface {
	// Import pose un mot de passe fourni par un opérateur — migration d'un
	// dépôt déjà chiffré ailleurs.
	Import(ctx context.Context, environment string, plaintext []byte, by int64) (VaultKeyDescriptor, error)

	// Generate tire un mot de passe aléatoire. Il ne quitte jamais le cluster.
	Generate(ctx context.Context, environment string, by int64) (VaultKeyDescriptor, error)

	// Describe rend ce qu'on peut dire d'une clé sans la révéler. Erreur
	// enveloppant ErrNoVaultKey quand l'environnement n'en a pas.
	Describe(ctx context.Context, environment string) (VaultKeyDescriptor, error)

	// EncryptString chiffre une valeur avec la clé de l'environnement et rend
	// le bloc `!vault |` prêt à coller dans un fichier YAML, nommé pour
	// `varName`.
	//
	// C'est le SEUL chemin par lequel un humain interagit avec cette clé : il
	// donne une valeur en clair, il reçoit un texte chiffré. La clé elle-même
	// ne fait jamais l'aller-retour.
	EncryptString(ctx context.Context, environment string, plaintext []byte, varName string) (string, error)
}

// VaultCatalog conserve les métadonnées d'une clé de vault.
//
// Deux magasins comme pour KeyCatalog : PostgreSQL ne porte JAMAIS la valeur
// en clair (migration 00015), le Secret Kubernetes ne porte que celle-là.
type VaultCatalog interface {
	// SaveVaultKey écrit ou remplace les métadonnées.
	SaveVaultKey(ctx context.Context, d VaultKeyDescriptor) error
	// VaultKeyByEnvironment rend le descripteur, ou une erreur enveloppant
	// ErrNoVaultKey.
	VaultKeyByEnvironment(ctx context.Context, environment string) (VaultKeyDescriptor, error)
	// ListVaultKeys rend tous les descripteurs, triés par environnement.
	ListVaultKeys(ctx context.Context) ([]VaultKeyDescriptor, error)
}

// ValidateVaultOrigin est un raccourci de validation partagé par les
// adaptateurs, sur le même principe que ValidateEnvironment : au bord, pas au
// fond.
func ValidateVaultOrigin(o VaultKeyOrigin) error {
	if !o.Valid() {
		return fmt.Errorf("origine de clé de vault %q inconnue", o)
	}
	return nil
}
