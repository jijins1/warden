package vaultinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// CleSecret est la clé de données du Secret portant le mot de passe en clair.
const CleSecret = "vault_password"

// Etiquettes posées sur les Secrets de clés de vault.
//
// Même rôle que celles de `keyinventory` : rendre les Secrets listables et
// supprimables sans deviner leur nom. `ValeurRole` diffère pour distinguer un
// nettoyage de namespace qui viserait l'une des deux familles sans l'autre.
const (
	LabelComposant   = "app.kubernetes.io/name"
	LabelRole        = "app.kubernetes.io/component"
	LabelEnvironment = "warden.io/environment"

	ValeurComposant = "warden"
	ValeurRole      = "vault-key"
)

// tailleMotDePasse est la taille, en octets, du mot de passe tiré par
// Generate.
//
// 32 octets d'aléa avant encodage : largement au-delà de ce qu'une attaque
// par force brute peut envisager, et cohérent avec la taille de clé que
// `ansible-vault` dérive lui-même (AES-256).
const tailleMotDePasse = 32

// Kubernetes est l'adaptateur `vault-inventory` de l'ADR-0045.
//
// Il tient la valeur SENSIBLE — le mot de passe en clair — dans un Secret du
// namespace des pods. Les métadonnées vivent dans PostgreSQL. Même séparation
// que `keyinventory.Kubernetes`, pour la même raison (ADR-0013).
type Kubernetes struct {
	client    kubernetes.Interface
	namespace string
	catalogue core.VaultCatalog
}

// New construit l'adaptateur et échoue à froid.
func New(client kubernetes.Interface, namespace string, catalogue core.VaultCatalog) (*Kubernetes, error) {
	if client == nil {
		return nil, errors.New("magasin de clés de vault : client Kubernetes absent")
	}
	if namespace == "" {
		return nil, errors.New("magasin de clés de vault : namespace absent")
	}
	if catalogue == nil {
		return nil, errors.New("magasin de clés de vault : catalogue absent")
	}
	return &Kubernetes{client: client, namespace: namespace, catalogue: catalogue}, nil
}

// Issue rend la clé de vault d'un environnement (ADR-0045).
//
// Lue par le control plane au moment de la LIVRAISON de l'ordre, jamais
// montée, jamais présente avant le claim — même chemin que la clé SSH de
// l'ADR-0020.
func (k *Kubernetes) Issue(ctx context.Context, environment string) (core.VaultKey, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKey{}, err
	}

	secret, err := k.client.CoreV1().Secrets(k.namespace).
		Get(ctx, core.SecretNameForVault(environment), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return core.VaultKey{}, fmt.Errorf("environnement %s: %w", environment, core.ErrNoVaultKey)
	}
	if err != nil {
		return core.VaultKey{}, fmt.Errorf("lecture de la clé de vault de %s: %w", environment, err)
	}

	plaintext := secret.Data[CleSecret]
	if len(plaintext) == 0 {
		return core.VaultKey{}, fmt.Errorf("environnement %s : le Secret %s ne porte pas de clé sous %q",
			environment, secret.Name, CleSecret)
	}
	return core.NewVaultKey(plaintext), nil
}

// Import pose un mot de passe fourni par un opérateur.
func (k *Kubernetes) Import(ctx context.Context, environment string, plaintext []byte, by int64) (core.VaultKeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	if len(plaintext) == 0 {
		return core.VaultKeyDescriptor{}, errors.New("clé de vault vide")
	}
	return k.poser(ctx, environment, plaintext, core.VaultKeyImported, by)
}

// Generate tire un mot de passe aléatoire.
//
// La valeur ne quitte JAMAIS le cluster : elle est tirée ici, écrite dans le
// Secret, et rendue à personne.
func (k *Kubernetes) Generate(ctx context.Context, environment string, by int64) (core.VaultKeyDescriptor, error) {
	if err := core.ValidateEnvironment(environment); err != nil {
		return core.VaultKeyDescriptor{}, err
	}

	plaintext, err := tirer(environment)
	if err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	return k.poser(ctx, environment, plaintext, core.VaultKeyGenerated, by)
}

// Describe rend ce qu'on peut dire d'une clé sans la révéler.
//
// La lecture passe par le CATALOGUE et non par le Secret, même raisonnement
// que `keyinventory.Kubernetes.Describe`.
func (k *Kubernetes) Describe(ctx context.Context, environment string) (core.VaultKeyDescriptor, error) {
	return k.catalogue.VaultKeyByEnvironment(ctx, environment)
}

// EncryptString chiffre une valeur avec la clé de l'environnement et rend le
// bloc `!vault |` prêt à coller dans un fichier YAML.
//
// C'est le SEUL chemin par lequel un appelant humain interagit avec cette
// clé : il donne une valeur en clair, il reçoit un texte chiffré. La valeur en
// clair de la clé de vault ne fait jamais l'aller-retour vers lui.
func (k *Kubernetes) EncryptString(ctx context.Context, environment string, plaintext []byte, varName string) (string, error) {
	cle, err := k.Issue(ctx, environment)
	if err != nil {
		return "", err
	}
	return chiffrer(environment, cle, plaintext, varName)
}

// splitLignes découpe sur `\n`, sans dépendre de `strings.Split` pour un
// détail : la dernière ligne d'`Encrypt` ne porte pas de saut de ligne final,
// et un split naïf laisserait une ligne vide qui s'indenterait à tort.
func splitLignes(s string) []string {
	var lignes []string
	debut := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lignes = append(lignes, s[debut:i])
			debut = i + 1
		}
	}
	if debut < len(s) {
		lignes = append(lignes, s[debut:])
	}
	return lignes
}

// poser écrit le Secret PUIS le catalogue, dans cet ordre — même
// raisonnement que `keyinventory.Kubernetes.poser` (ADR-0023).
func (k *Kubernetes) poser(ctx context.Context, environment string,
	plaintext []byte, origine core.VaultKeyOrigin, by int64,
) (core.VaultKeyDescriptor, error) {
	nom := core.SecretNameForVault(environment)
	somme := sha256.Sum256(plaintext)
	empreinte := hex.EncodeToString(somme[:])

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nom,
			Namespace: k.namespace,
			Labels: map[string]string{
				LabelComposant:   ValeurComposant,
				LabelRole:        ValeurRole,
				LabelEnvironment: environment,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{CleSecret: plaintext},
	}

	api := k.client.CoreV1().Secrets(k.namespace)
	_, err := api.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Remplacer EST la rotation — voir la note de dette de l'ADR-0045.
		_, err = api.Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return core.VaultKeyDescriptor{}, fmt.Errorf("écriture de la clé de vault de %s: %w", environment, err)
	}

	return cataloguer(ctx, k.catalogue, core.VaultKeyDescriptor{
		Environment: environment,
		Fingerprint: empreinte,
		SecretName:  nom,
		Origin:      origine,
		CreatedBy:   by,
	})
}
