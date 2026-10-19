// Étage 1 de l'ADR-0010 : un magasin KV en mémoire, aucun Vault.
//
// Le client Vault lui-même est testé dans `outbound/vault` ; ici, c'est le
// contrat de l'inventaire qui l'est — le même que celui de l'adaptateur
// Kubernetes.

package vaultinventory_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	ansiblevault "github.com/sosedoff/ansible-vault-go"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/vaultinventory"
)

const montage = "warden"

// kvFactice tient les valeurs en mémoire, indexées par « montage/chemin ».
type kvFactice struct {
	mu       sync.Mutex
	valeurs  map[string][]byte
	errEcrit error
}

func nouveauKV() *kvFactice { return &kvFactice{valeurs: map[string][]byte{}} }

func (k *kvFactice) Lire(_ context.Context, m, chemin string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.valeurs[m+"/"+chemin]
	return v, ok, nil
}

func (k *kvFactice) Ecrire(_ context.Context, m, chemin string, valeur []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.errEcrit != nil {
		return k.errEcrit
	}
	k.valeurs[m+"/"+chemin] = append([]byte(nil), valeur...)
	return nil
}

func monterVault(t *testing.T) (*vaultinventory.Vault, *kvFactice, *catalogueFactice) {
	t.Helper()
	kv, cat := nouveauKV(), nouveauCatalogue()
	v, err := vaultinventory.NewVault(kv, montage, cat)
	if err != nil {
		t.Fatalf("NewVault : %v", err)
	}
	return v, kv, cat
}

// Le bloc chiffré se déchiffre avec la clé que le runner recevra.
func TestVaultEncryptStringSeDechiffreAvecLaCleDeLEnvironnement(t *testing.T) {
	v, _, cat := monterVault(t)

	d, err := v.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate : %v", err)
	}
	if d.SecretName != core.SecretNameForVault("prod") || d.Fingerprint == "" {
		t.Errorf("descripteur : %+v", d)
	}
	if _, ok := cat.par["prod"]; !ok {
		t.Error("le catalogue n'a pas été alimenté")
	}

	bloc, err := v.EncryptString(t.Context(), "prod", []byte("s3cr3t"), "db_password")
	if err != nil {
		t.Fatalf("EncryptString : %v", err)
	}
	if !strings.HasPrefix(bloc, "db_password: !vault |\n") {
		t.Errorf("bloc rendu inattendu :\n%s", bloc)
	}
	cle, err := v.Issue(t.Context(), "prod")
	if err != nil {
		t.Fatalf("Issue : %v", err)
	}
	clair, err := ansiblevault.Decrypt(desindenter(t, bloc), string(cle.Reveal()))
	if err != nil || clair != "s3cr3t" {
		t.Errorf("déchiffré = %q, %v", clair, err)
	}
}

func TestVaultUneCleImporteeEstConserveeTelleQuelle(t *testing.T) {
	v, kv, _ := monterVault(t)
	if _, err := v.Import(t.Context(), "staging", []byte("mot-de-passe"), 7); err != nil {
		t.Fatalf("Import : %v", err)
	}
	if string(kv.valeurs[montage+"/"+core.SecretNameForVault("staging")]) != "mot-de-passe" {
		t.Error("la clé importée a été réécrite")
	}
	if _, err := v.Import(t.Context(), "staging", nil, 7); err == nil {
		t.Error("une clé vide a été acceptée")
	}
}

func TestVaultUnEnvironnementSansCleLeDitClairement(t *testing.T) {
	v, _, _ := monterVault(t)
	if _, err := v.Issue(t.Context(), "prod"); !errors.Is(err, core.ErrNoVaultKey) {
		t.Errorf("%v, ErrNoVaultKey attendu", err)
	}
	if _, err := v.EncryptString(t.Context(), "prod", []byte("x"), "v"); !errors.Is(err, core.ErrNoVaultKey) {
		t.Errorf("EncryptString sans clé : %v", err)
	}
}

func TestVaultUnEchecDEcritureNeTouchePasLeCatalogue(t *testing.T) {
	v, kv, cat := monterVault(t)
	kv.errEcrit = errors.New("vault scellé")
	if _, err := v.Generate(t.Context(), "prod", 7); err == nil {
		t.Fatal("la pose a réussi malgré Vault en échec")
	}
	if len(cat.par) != 0 {
		t.Error("le catalogue pointe une clé que Vault n'a pas")
	}
}

func TestVaultNewRefuseUnMontageIncomplet(t *testing.T) {
	if _, err := vaultinventory.NewVault(nil, montage, nouveauCatalogue()); err == nil {
		t.Error("client absent accepté")
	}
	if _, err := vaultinventory.NewVault(nouveauKV(), "", nouveauCatalogue()); err == nil {
		t.Error("montage absent accepté")
	}
	if _, err := vaultinventory.NewVault(nouveauKV(), montage, nil); err == nil {
		t.Error("catalogue absent accepté")
	}
}
