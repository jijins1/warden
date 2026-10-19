// Étage 1 de l'ADR-0010 : un magasin KV en mémoire, aucun Vault.
//
// Le client Vault lui-même est testé dans `outbound/vault` ; ici, c'est le
// contrat de l'inventaire qui l'est — le même que celui de l'adaptateur
// Kubernetes.

package keyinventory_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/keyinventory"
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

func monterVault(t *testing.T) (*keyinventory.Vault, *kvFactice, *catalogueFactice) {
	t.Helper()
	kv, cat := nouveauKV(), nouveauCatalogue()
	v, err := keyinventory.NewVault(kv, montage, cat)
	if err != nil {
		t.Fatalf("NewVault : %v", err)
	}
	return v, kv, cat
}

// Les deux moitiés forment une PAIRE, et Issue rend celle que Vault garde.
func TestVaultUneCleFrappeeFormeUnePaire(t *testing.T) {
	v, kv, cat := monterVault(t)

	d, err := v.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate : %v", err)
	}
	if d.SecretName != core.SecretNameFor("prod") || d.Origin != core.KeyGenerated {
		t.Errorf("descripteur : %+v", d)
	}
	if _, ok := cat.par["prod"]; !ok {
		t.Error("le catalogue n'a pas été alimenté")
	}

	cle, err := v.Issue(t.Context(), "alice", "prod", nil)
	if err != nil {
		t.Fatalf("Issue : %v", err)
	}
	if string(cle.Reveal()) != string(kv.valeurs[montage+"/"+core.SecretNameFor("prod")]) {
		t.Error("Issue ne rend pas ce que Vault contient")
	}
	signataire, err := ssh.ParsePrivateKey(cle.Reveal())
	if err != nil {
		t.Fatalf("partie privée illisible : %v", err)
	}
	if got := ssh.FingerprintSHA256(signataire.PublicKey()); got != d.Fingerprint {
		t.Errorf("empreintes divergentes : %s / %s", got, d.Fingerprint)
	}
	if !cle.Expiry.IsZero() {
		t.Error("la v1 ne pose aucune expiration")
	}
}

// L'octet pour octet compte : une clé importée n'est pas réencodée.
func TestVaultUneCleImporteeEstConserveeTelleQuelle(t *testing.T) {
	v, kv, _ := monterVault(t)
	privee := clePrivee(t, "")

	d, err := v.Import(t.Context(), "staging", privee, 7)
	if err != nil {
		t.Fatalf("Import : %v", err)
	}
	if d.Origin != core.KeyImported || !strings.Contains(d.PublicKey, "warden-staging") {
		t.Errorf("descripteur : %+v", d)
	}
	if string(kv.valeurs[montage+"/"+core.SecretNameFor("staging")]) != string(privee) {
		t.Error("la clé importée a été réécrite")
	}
}

func TestVaultUneCleAPhraseSecreteEstRefusee(t *testing.T) {
	v, kv, _ := monterVault(t)
	if _, err := v.Import(t.Context(), "prod", clePrivee(t, "phrase"), 7); !errors.Is(err, keyinventory.ErrPassphrase) {
		t.Errorf("%v, ErrPassphrase attendu", err)
	}
	if len(kv.valeurs) != 0 {
		t.Error("une clé refusée a été écrite")
	}
}

func TestVaultUnEnvironnementSansCleLeDitClairement(t *testing.T) {
	v, _, _ := monterVault(t)
	if _, err := v.Issue(t.Context(), "alice", "prod", nil); !errors.Is(err, core.ErrNoKey) {
		t.Errorf("%v, ErrNoKey attendu", err)
	}
}

func TestVaultUneCleVideNestPasPrisePourUneCle(t *testing.T) {
	v, kv, _ := monterVault(t)
	kv.valeurs[montage+"/"+core.SecretNameFor("prod")] = []byte{}
	_, err := v.Issue(t.Context(), "alice", "prod", nil)
	if err == nil || errors.Is(err, core.ErrNoKey) {
		t.Errorf("%v : une clé vide doit être une erreur distincte de l'absence", err)
	}
}

// L'ORDRE : Vault d'abord. Un échec d'écriture ne laisse pas le catalogue
// pointer une clé absente.
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
	if _, err := keyinventory.NewVault(nil, montage, nouveauCatalogue()); err == nil {
		t.Error("client absent accepté")
	}
	if _, err := keyinventory.NewVault(nouveauKV(), "", nouveauCatalogue()); err == nil {
		t.Error("montage absent accepté")
	}
	if _, err := keyinventory.NewVault(nouveauKV(), montage, nil); err == nil {
		t.Error("catalogue absent accepté")
	}
}
