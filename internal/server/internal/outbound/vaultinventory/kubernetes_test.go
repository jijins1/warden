// Étage 1 de l'ADR-0010 : le clientset factice de client-go, aucun cluster.
//
// Ce paquet est le magasin de la clé Ansible Vault (ADR-0045). Ce que ces
// tests protègent : la valeur en clair ne sort jamais vers un appelant humain
// — seul un bloc CHIFFRÉ le fait — et elle reste retrouvable à l'identique par
// le run qui doit déchiffrer.

package vaultinventory_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	ansiblevault "github.com/sosedoff/ansible-vault-go"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/vaultinventory"
)

const ns = "warden-dev"

// catalogueFactice tient les métadonnées en mémoire, sur le même patron que
// celui de `keyinventory_test`.
type catalogueFactice struct {
	mu       sync.Mutex
	horodate bool
	par      map[string]core.VaultKeyDescriptor
	err      error
}

func nouveauCatalogue() *catalogueFactice {
	return &catalogueFactice{par: map[string]core.VaultKeyDescriptor{}}
}

func (c *catalogueFactice) SaveVaultKey(_ context.Context, d core.VaultKeyDescriptor) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.horodate {
		d.CreatedAt = time.Now().UTC()
	}
	c.par[d.Environment] = d
	return nil
}

func (c *catalogueFactice) VaultKeyByEnvironment(_ context.Context, env string) (core.VaultKeyDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.par[env]
	if !ok {
		return core.VaultKeyDescriptor{}, fmt.Errorf("environnement %s: %w", env, core.ErrNoVaultKey)
	}
	return d, nil
}

func (c *catalogueFactice) ListVaultKeys(context.Context) ([]core.VaultKeyDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]core.VaultKeyDescriptor, 0, len(c.par))
	for _, d := range c.par {
		out = append(out, d)
	}
	return out, nil
}

func monter(t *testing.T) (*vaultinventory.Kubernetes, *fake.Clientset, *catalogueFactice) {
	t.Helper()
	client := fake.NewSimpleClientset()
	cat := nouveauCatalogue()
	k, err := vaultinventory.New(client, ns, cat)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return k, client, cat
}

// --- le chemin nominal ---

func TestUneCleTireeEstUtilisableDesLesDeuxCotes(t *testing.T) {
	k, client, cat := monter(t)

	d, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(d.Fingerprint) != 64 {
		t.Errorf("empreinte = %q, attendu 64 caractères hex (SHA-256)", d.Fingerprint)
	}
	if d.Origin != core.VaultKeyGenerated {
		t.Errorf("origine = %q, attendu %q", d.Origin, core.VaultKeyGenerated)
	}

	secret, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameForVault("prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret absent : %v", err)
	}
	if d.SecretName != secret.Name {
		t.Errorf("le catalogue pointe %q, le Secret s'appelle %q", d.SecretName, secret.Name)
	}
	if len(secret.Data[vaultinventory.CleSecret]) == 0 {
		t.Error("le Secret ne porte aucune valeur")
	}
	if _, ok := cat.par["prod"]; !ok {
		t.Error("le catalogue n'a pas été alimenté")
	}
}

func TestUneCleImporteeEstConserveeTelleQuelle(t *testing.T) {
	k, client, _ := monter(t)

	d, err := k.Import(t.Context(), "staging", []byte("mon-mot-de-passe-existant"), 7)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if d.Origin != core.VaultKeyImported {
		t.Errorf("origine = %q, attendu %q", d.Origin, core.VaultKeyImported)
	}

	secret, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameForVault("staging"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret absent : %v", err)
	}
	if string(secret.Data[vaultinventory.CleSecret]) != "mon-mot-de-passe-existant" {
		t.Error("la clé importée a été réécrite au lieu d'être conservée telle quelle")
	}
}

func TestImportRefuseUneCleVide(t *testing.T) {
	k, _, _ := monter(t)
	if _, err := k.Import(t.Context(), "prod", nil, 7); err == nil {
		t.Fatal("accepté à tort")
	}
}

func TestIssueRendLaValeurDeLEnvironnement(t *testing.T) {
	k, client, _ := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 7); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cle, err := k.Issue(t.Context(), "prod")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if cle.Zero() {
		t.Fatal("clé nulle")
	}

	secret, _ := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameForVault("prod"), metav1.GetOptions{})
	if string(cle.Reveal()) != string(secret.Data[vaultinventory.CleSecret]) {
		t.Error("Issue ne rend pas ce que le Secret contient")
	}
}

// EncryptString est le SEUL chemin humain : la valeur chiffrée doit se
// déchiffrer avec la MÊME clé qu'Issue rend au runner, et à personne d'autre.
func TestEncryptStringSeDechiffreAvecLaCleDeLEnvironnement(t *testing.T) {
	k, _, _ := monter(t)

	d, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	bloc, err := k.EncryptString(t.Context(), "prod", []byte("s3cr3t"), "db_password")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	if !strings.HasPrefix(bloc, "db_password: !vault |\n") {
		t.Errorf("bloc rendu inattendu :\n%s", bloc)
	}

	// Extraire le corps chiffré (tout après la première ligne, désindenté) et
	// le déchiffrer avec la clé qu'Issue rendrait au runner.
	corps := desindenter(t, bloc)
	cle, err := k.Issue(t.Context(), "prod")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clair, err := ansiblevault.Decrypt(corps, string(cle.Reveal()))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if clair != "s3cr3t" {
		t.Errorf("déchiffré = %q, attendu %q", clair, "s3cr3t")
	}

	// L'empreinte du descripteur n'est PAS celle du texte chiffré : elle
	// identifie la clé, produite au Generate, indépendante de chaque appel de
	// chiffrement.
	if d.Fingerprint == "" {
		t.Error("empreinte absente")
	}
}

func desindenter(t *testing.T, bloc string) string {
	t.Helper()
	var lignes []string
	for _, ligne := range strings.Split(bloc, "\n")[1:] {
		lignes = append(lignes, strings.TrimPrefix(ligne, "          "))
	}
	return strings.TrimRight(strings.Join(lignes, "\n"), "\n")
}

func TestEncryptStringSansCleEstRefuse(t *testing.T) {
	k, _, _ := monter(t)
	if _, err := k.EncryptString(t.Context(), "jamais-configure", []byte("x"), "v"); !errors.Is(err, core.ErrNoVaultKey) {
		t.Fatalf("erreur = %v, attendu core.ErrNoVaultKey", err)
	}
}

// --- ce qui doit être refusé ---

func TestUnEnvironnementInutilisableCommeNomEstRefuse(t *testing.T) {
	k, _, _ := monter(t)

	for _, env := range []string{"", "PROD", "prod.eu", "prod/eu", "-prod", "prod_eu", strings.Repeat("p", 56)} {
		t.Run(fmt.Sprintf("%q", env), func(t *testing.T) {
			if _, err := k.Generate(t.Context(), env, 7); err == nil {
				t.Fatal("accepté à tort")
			}
			if _, err := k.Issue(t.Context(), env); err == nil {
				t.Fatal("Issue accepté à tort")
			}
		})
	}
}

func TestUnEnvironnementSansCleLeDitClairement(t *testing.T) {
	k, _, _ := monter(t)

	_, err := k.Issue(t.Context(), "jamais-configure")
	if !errors.Is(err, core.ErrNoVaultKey) {
		t.Fatalf("erreur = %v, attendu core.ErrNoVaultKey", err)
	}
}

func TestUnSecretVideNestPasPrisPourUneCle(t *testing.T) {
	k, client, _ := monter(t)

	_, err := client.CoreV1().Secrets(ns).Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: core.SecretNameForVault("prod"), Namespace: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("préparation : %v", err)
	}

	if _, err := k.Issue(t.Context(), "prod"); err == nil {
		t.Fatal("un Secret vide a été pris pour une clé")
	}
}

// --- rotation ---

func TestReposerUneCleRemplaceLAncienne(t *testing.T) {
	k, client, _ := monter(t)

	avant, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	apres, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate (rotation): %v", err)
	}
	if avant.Fingerprint == apres.Fingerprint {
		t.Fatal("la rotation a rendu la même clé")
	}

	secrets, err := client.CoreV1().Secrets(ns).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(secrets.Items) != 1 {
		t.Errorf("%d Secrets après rotation, attendu 1 — l'ancienne clé traîne", len(secrets.Items))
	}
}

// --- masquage ---

func TestLaCleDeVaultEstMasqueeALImpression(t *testing.T) {
	k, _, _ := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 7); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cle, err := k.Issue(t.Context(), "prod")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	debut := string(cle.Reveal())[:10]
	rendus := []string{
		fmt.Sprintf("%v", cle),
		fmt.Sprintf("%+v", cle),
		fmt.Sprintf("%#v", cle),
		// nolint:staticcheck // S1025 : c'est le chemin du VERBE %s qu'on teste.
		fmt.Sprintf("%s", cle),
		fmt.Sprintf("%v", struct{ K core.VaultKey }{cle}),
	}
	for i, r := range rendus {
		if strings.Contains(r, debut) {
			t.Errorf("impression %d laisse fuir la clé : %s", i, r)
		}
	}
}

// --- montage ---

func TestNewRefuseUnMontageIncomplet(t *testing.T) {
	cas := map[string]func() error{
		"sans client": func() error {
			_, err := vaultinventory.New(nil, ns, nouveauCatalogue())
			return err
		},
		"sans namespace": func() error {
			_, err := vaultinventory.New(fake.NewSimpleClientset(), "", nouveauCatalogue())
			return err
		},
		"sans catalogue": func() error {
			_, err := vaultinventory.New(fake.NewSimpleClientset(), ns, nil)
			return err
		},
	}
	for nom, construire := range cas {
		t.Run(nom, func(t *testing.T) {
			if err := construire(); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

func TestUnCatalogueEnEchecFaitEchouerLaPose(t *testing.T) {
	client := fake.NewSimpleClientset()
	cat := nouveauCatalogue()
	cat.err = errors.New("base indisponible")

	k, err := vaultinventory.New(client, ns, cat)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := k.Generate(t.Context(), "prod", 7); err == nil {
		t.Fatal("la pose a réussi malgré un catalogue en échec")
	}
}

func TestLAuteurDeLaPoseEstRetenu(t *testing.T) {
	k, _, cat := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 42); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if cat.par["prod"].CreatedBy != 42 {
		t.Errorf("créée par = %d, attendu 42", cat.par["prod"].CreatedBy)
	}
}

func TestLeDescripteurRenduPorteLesDatesDeLaBase(t *testing.T) {
	client := fake.NewSimpleClientset()
	cat := nouveauCatalogue()
	cat.horodate = true

	k, err := vaultinventory.New(client, ns, cat)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	d, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if d.CreatedAt.IsZero() {
		t.Error("le descripteur rendu ne porte pas sa date de création")
	}
}
