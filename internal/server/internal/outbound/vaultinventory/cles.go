package vaultinventory

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	ansiblevault "github.com/sosedoff/ansible-vault-go"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Ce qui ne dépend pas du magasin : tirage d'une clé, chiffrement, écriture
// au catalogue. Partagé par les adaptateurs Kubernetes et Vault.

// tirer tire une clé de vault.
func tirer(environment string) ([]byte, error) {
	brut := make([]byte, tailleMotDePasse)
	if _, err := rand.Read(brut); err != nil {
		return nil, fmt.Errorf("tirage de la clé de vault de %s: %w", environment, err)
	}
	// Encodé en texte : `ansible-vault` prend un mot de passe, pas des octets
	// bruts, et base64 URL-safe évite tout caractère qui poserait problème
	// dans un fichier de mot de passe collé à la main.
	return []byte(base64.RawURLEncoding.EncodeToString(brut)), nil
}

// indentationEncryptString est l'indentation que `ansible-vault
// encrypt_string` pose devant chaque ligne du bloc chiffré.
//
// MESURÉ contre la sortie de `ansible-vault encrypt_string` : elle est fixe,
// dix espaces, quelle que soit la longueur du nom de variable qui précède
// `!vault |`.
const indentationEncryptString = "          "

// chiffrer rend le bloc `!vault |` de `plaintext` sous la clé `cle`.
func chiffrer(environment string, cle core.VaultKey, plaintext []byte, varName string) (string, error) {
	// Format du corps chiffré délégué à une bibliothèque tierce plutôt que
	// réimplémenté : l'algorithme `$ANSIBLE_VAULT;1.1;AES256` (PBKDF2-HMAC-
	// SHA256, AES-256-CTR, HMAC-SHA256 en chiffrement-puis-authentification)
	// est un format à interopérabilité stricte avec Ansible — une erreur d'un
	// octet dans une implémentation maison ne se verrait qu'au déchiffrement,
	// en production.
	corps, err := ansiblevault.Encrypt(string(plaintext), string(cle.Reveal()))
	if err != nil {
		return "", fmt.Errorf("chiffrement pour %s: %w", environment, err)
	}

	var indente []byte
	for _, ligne := range splitLignes(corps) {
		indente = append(indente, indentationEncryptString...)
		indente = append(indente, ligne...)
		indente = append(indente, '\n')
	}
	return fmt.Sprintf("%s: !vault |\n%s", varName, indente), nil
}

// cataloguer écrit le descripteur PUIS le relit, pour la même raison que
// `keyinventory` : `created_at` et `rotated_at` sont posées par la base.
func cataloguer(ctx context.Context, catalogue core.VaultCatalog, d core.VaultKeyDescriptor) (core.VaultKeyDescriptor, error) {
	if err := catalogue.SaveVaultKey(ctx, d); err != nil {
		return core.VaultKeyDescriptor{}, err
	}
	if relu, err := catalogue.VaultKeyByEnvironment(ctx, d.Environment); err == nil {
		return relu, nil
	}
	return d, nil
}
