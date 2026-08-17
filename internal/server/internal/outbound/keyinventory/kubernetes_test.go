// Étage 1 de l'ADR-0010 : le clientset factice de client-go, aucun cluster.
//
// Ce paquet est le magasin de secrets du control plane. Ce que ces tests
// protègent : la partie privée ne sort pas, la partie publique est exploitable
// telle quelle, et une clé qu'`ssh-add` ne saurait pas charger est refusée AVANT
// d'atteindre un pod.

package keyinventory_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/keyinventory"
)

const ns = "warden-dev"

// catalogueFactice tient la partie publique en mémoire.
type catalogueFactice struct {
	mu  sync.Mutex
	par map[string]core.KeyDescriptor
	err error
}

func nouveauCatalogue() *catalogueFactice {
	return &catalogueFactice{par: map[string]core.KeyDescriptor{}}
}

func (c *catalogueFactice) SaveKey(_ context.Context, d core.KeyDescriptor) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.par[d.Environment] = d
	return nil
}

func (c *catalogueFactice) KeyByEnvironment(_ context.Context, env string) (core.KeyDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.par[env]
	if !ok {
		return core.KeyDescriptor{}, fmt.Errorf("environnement %s: %w", env, core.ErrNoKey)
	}
	return d, nil
}

func (c *catalogueFactice) ListKeys(context.Context) ([]core.KeyDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]core.KeyDescriptor, 0, len(c.par))
	for _, d := range c.par {
		out = append(out, d)
	}
	return out, nil
}

func monter(t *testing.T) (*keyinventory.Kubernetes, *fake.Clientset, *catalogueFactice) {
	t.Helper()
	client := fake.NewSimpleClientset()
	cat := nouveauCatalogue()
	k, err := keyinventory.New(client, ns, cat)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return k, client, cat
}

// clePrivee frappe une clé au format OpenSSH, avec ou sans phrase secrète.
func clePrivee(t *testing.T, phrase string) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("frappe : %v", err)
	}

	var bloc *pem.Block
	if phrase == "" {
		bloc, err = ssh.MarshalPrivateKey(priv, "test")
	} else {
		bloc, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(phrase))
	}
	if err != nil {
		t.Fatalf("encodage : %v", err)
	}
	return pem.EncodeToMemory(bloc)
}

// --- le chemin nominal ---

func TestUneCleFrappeeEstUtilisableDesLesDeuxCotes(t *testing.T) {
	k, client, cat := monter(t)

	d, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// La partie PUBLIQUE doit être copiable telle quelle dans un
	// `authorized_keys`. C'est sa seule raison d'être.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(d.PublicKey)); err != nil {
		t.Errorf("la partie publique n'est pas une ligne authorized_keys : %v (%q)", err, d.PublicKey)
	}
	if !strings.Contains(d.PublicKey, "warden-prod") {
		t.Errorf("la clé publique ne se nomme pas : %q — personne n'ose retirer une clé anonyme", d.PublicKey)
	}
	if !strings.HasPrefix(d.Fingerprint, "SHA256:") {
		t.Errorf("empreinte = %q, attendu la forme de ssh-keygen -l", d.Fingerprint)
	}
	if d.Origin != core.KeyGenerated {
		t.Errorf("origine = %q, attendu %q", d.Origin, core.KeyGenerated)
	}

	// La partie PRIVÉE est dans le Secret, sous le nom qu'ansible-runner
	// attend, et le catalogue le désigne.
	secret, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameFor("prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret absent : %v", err)
	}
	if d.SecretName != secret.Name {
		t.Errorf("le catalogue pointe %q, le Secret s'appelle %q", d.SecretName, secret.Name)
	}
	if _, err := ssh.ParsePrivateKey(secret.Data[keyinventory.CleSecret]); err != nil {
		t.Errorf("la partie privée n'est pas une clé lisible : %v", err)
	}
	if _, ok := cat.par["prod"]; !ok {
		t.Error("le catalogue n'a pas été alimenté")
	}
}

// Les deux moitiés doivent former une PAIRE. Un test qui vérifie chacune de son
// côté passerait aussi si l'on écrivait la clé publique de quelqu'un d'autre.
func TestLesDeuxMoitiesFormentUnePaire(t *testing.T) {
	k, client, _ := monter(t)

	d, err := k.Generate(t.Context(), "prod", 7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	secret, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameFor("prod"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret absent : %v", err)
	}

	signataire, err := ssh.ParsePrivateKey(secret.Data[keyinventory.CleSecret])
	if err != nil {
		t.Fatalf("partie privée illisible : %v", err)
	}
	if got := ssh.FingerprintSHA256(signataire.PublicKey()); got != d.Fingerprint {
		t.Errorf("empreintes divergentes :\n  privée  %s\n  annoncée %s", got, d.Fingerprint)
	}
}

func TestUneCleImporteeEstRelueEtNommee(t *testing.T) {
	k, client, _ := monter(t)

	privee := clePrivee(t, "")
	d, err := k.Import(t.Context(), "staging", privee, 7)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if d.Origin != core.KeyImported {
		t.Errorf("origine = %q, attendu %q", d.Origin, core.KeyImported)
	}
	if !strings.Contains(d.PublicKey, "warden-staging") {
		t.Errorf("clé publique non nommée : %q", d.PublicKey)
	}

	// L'octet pour octet compte : réencoder la clé de quelqu'un d'autre est un
	// bon moyen de la rendre inutilisable sans que rien ne le dise.
	secret, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameFor("staging"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Secret absent : %v", err)
	}
	if string(secret.Data[keyinventory.CleSecret]) != string(privee) {
		t.Error("la clé importée a été réécrite au lieu d'être conservée telle quelle")
	}
}

func TestIssueRendLaPartiePriveeDeLEnvironnement(t *testing.T) {
	k, client, _ := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 7); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// `user` et `hostScope` sont passés et ignorés : c'est la décision de
	// l'ADR-0020 pour la v1, et le port les transmet quand même.
	cle, err := k.Issue(t.Context(), "alice", "prod", []string{"db-prod-02"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if cle.Zero() {
		t.Fatal("clé nulle")
	}
	if !cle.Expiry.IsZero() {
		t.Errorf("expiration = %v, la v1 n'en pose aucune", cle.Expiry)
	}

	secret, _ := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameFor("prod"), metav1.GetOptions{})
	if string(cle.Reveal()) != string(secret.Data[keyinventory.CleSecret]) {
		t.Error("Issue ne rend pas ce que le Secret contient")
	}
}

// --- ce qui doit être refusé ---

// LE refus qui vient d'une mesure. `ansible-runner` charge la clé par `ssh-add`
// avant de lancer le playbook : une clé protégée y attendrait une saisie qui ne
// viendra jamais dans un pod, et le run resterait bloqué sans message.
func TestUneCleAPhraseSecreteEstRefuseeALImport(t *testing.T) {
	k, client, cat := monter(t)

	_, err := k.Import(t.Context(), "prod", clePrivee(t, "hunter2"), 7)
	if !errors.Is(err, keyinventory.ErrPassphrase) {
		t.Fatalf("erreur = %v, attendu ErrPassphrase", err)
	}

	// Et rien n'a été écrit : un refus ne doit pas laisser d'état.
	if _, err := client.CoreV1().Secrets(ns).Get(t.Context(), core.SecretNameFor("prod"), metav1.GetOptions{}); err == nil {
		t.Error("un Secret a été écrit malgré le refus")
	}
	if len(cat.par) != 0 {
		t.Error("le catalogue a été alimenté malgré le refus")
	}
}

func TestUneCleIllisibleEstRefusee(t *testing.T) {
	k, _, _ := monter(t)

	cas := map[string][]byte{
		"texte quelconque": []byte("ceci n'est pas une clé\n"),
		"vide":             {},
		"clé PUBLIQUE":     []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA warden\n"),
	}
	for nom, brut := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := k.Import(t.Context(), "prod", brut, 7); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// L'environnement devient un composant de nom de Secret Kubernetes. Une
// violation découverte par l'API se présenterait à la LIVRAISON de l'ordre,
// très loin d'ici.
func TestUnEnvironnementInutilisableCommeNomEstRefuse(t *testing.T) {
	k, _, _ := monter(t)

	for _, env := range []string{"", "PROD", "prod.eu", "prod/eu", "-prod", "prod_eu", strings.Repeat("p", 56)} {
		t.Run(fmt.Sprintf("%q", env), func(t *testing.T) {
			if _, err := k.Generate(t.Context(), env, 7); err == nil {
				t.Fatal("accepté à tort")
			}
			if _, err := k.Issue(t.Context(), "alice", env, nil); err == nil {
				t.Fatal("Issue accepté à tort")
			}
		})
	}
}

func TestUnEnvironnementSansCleLeDitClairement(t *testing.T) {
	k, _, _ := monter(t)

	_, err := k.Issue(t.Context(), "alice", "jamais-configure", nil)
	if !errors.Is(err, core.ErrNoKey) {
		t.Fatalf("erreur = %v, attendu core.ErrNoKey", err)
	}
}

// Un Secret présent mais vide est PIRE qu'absent : il fait croire
// l'environnement configuré, et le run échouerait sur une erreur SSH.
func TestUnSecretVideNestPasPrisPourUneCle(t *testing.T) {
	k, client, _ := monter(t)

	_, err := client.CoreV1().Secrets(ns).Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: core.SecretNameFor("prod"), Namespace: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("préparation : %v", err)
	}

	if _, err := k.Issue(t.Context(), "alice", "prod", nil); err == nil {
		t.Fatal("un Secret vide a été pris pour une clé")
	}
}

// --- rotation ---

// Remplacer EST la rotation, et l'ADR-0020 note que c'est le seul mécanisme de
// révocation disponible. La refuser obligerait à supprimer d'abord, donc à
// passer par un état où l'environnement n'a plus de clé du tout.
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

	cle, err := k.Issue(t.Context(), "alice", "prod", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	s, err := ssh.ParsePrivateKey(cle.Reveal())
	if err != nil {
		t.Fatalf("clé illisible : %v", err)
	}
	if ssh.FingerprintSHA256(s.PublicKey()) != apres.Fingerprint {
		t.Error("Issue rend encore l'ancienne clé après rotation")
	}
}

// --- masquage ---

// La clé privée traverse le cœur, des structures englobantes et des traces. Les
// trois chemins d'impression par lesquels elle fuirait doivent être fermés.
func TestLaClePriveeEstMasqueeALImpression(t *testing.T) {
	k, _, _ := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 7); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cle, err := k.Issue(t.Context(), "alice", "prod", nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	debut := string(cle.Reveal())[:40]
	rendus := []string{
		fmt.Sprintf("%v", cle),
		fmt.Sprintf("%+v", cle),
		fmt.Sprintf("%#v", cle),
		// nolint:staticcheck // S1025 : c'est le chemin du VERBE %s qu'on teste,
		// pas String(). Personne n'écrit String() à la main dans une trace.
		fmt.Sprintf("%s", cle),
		fmt.Sprintf("%v", struct{ K core.AccessKey }{cle}),
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
			_, err := keyinventory.New(nil, ns, nouveauCatalogue())
			return err
		},
		"sans namespace": func() error {
			_, err := keyinventory.New(fake.NewSimpleClientset(), "", nouveauCatalogue())
			return err
		},
		"sans catalogue": func() error {
			_, err := keyinventory.New(fake.NewSimpleClientset(), ns, nil)
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

// Le Secret est écrit AVANT le catalogue. Si le catalogue échoue, l'appel
// échoue — mais la clé privée reste, orpheline et inerte, et le prochain essai
// la remplace. L'inverse laisserait un catalogue désignant un Secret absent,
// donc un environnement qui paraît configuré et ne l'est pas.
func TestUnCatalogueEnEchecFaitEchouerLaPose(t *testing.T) {
	client := fake.NewSimpleClientset()
	cat := nouveauCatalogue()
	cat.err = errors.New("base indisponible")

	k, err := keyinventory.New(client, ns, cat)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := k.Generate(t.Context(), "prod", 7); err == nil {
		t.Fatal("la pose a réussi malgré un catalogue en échec")
	}
}

// L'auteur est retenu : la clé d'un environnement décide de ce qui peut joindre
// la production, donc savoir qui l'a posée est du même ordre que savoir qui a
// lancé un run.
func TestLAuteurDeLaPoseEstRetenu(t *testing.T) {
	k, _, cat := monter(t)

	if _, err := k.Generate(t.Context(), "prod", 42); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if cat.par["prod"].CreatedBy != 42 {
		t.Errorf("créée par = %d, attendu 42", cat.par["prod"].CreatedBy)
	}

	if _, err := k.Import(t.Context(), "prod", clePrivee(t, ""), 7); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if cat.par["prod"].CreatedBy != 7 {
		t.Errorf("après rotation, créée par = %d, attendu 7", cat.par["prod"].CreatedBy)
	}
}
