package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEcrireLireDetruire(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	c := client(t, srv)
	ctx := t.Context()

	// Des octets qui ne sont pas de l'UTF-8 : une clé privée n'en est pas
	// forcément.
	valeur := []byte{0x00, 0xff, 'k', 'e', 'y'}
	if err := c.Ecrire(ctx, "warden", "warden-key-prod", valeur); err != nil {
		t.Fatalf("Ecrire : %v", err)
	}
	lu, trouve, err := c.Lire(ctx, "warden", "warden-key-prod")
	if err != nil || !trouve {
		t.Fatalf("Lire : trouve=%v err=%v", trouve, err)
	}
	if !bytes.Equal(lu, valeur) {
		t.Errorf("valeur relue : %v", lu)
	}

	if err := c.Detruire(ctx, "warden", "warden-key-prod"); err != nil {
		t.Fatalf("Detruire : %v", err)
	}
	if _, trouve, _ := c.Lire(ctx, "warden", "warden-key-prod"); trouve {
		t.Error("la clé détruite est encore lisible")
	}
	if f.effacesLogiquement != 0 {
		t.Error("la destruction est passée par data/ : la valeur resterait récupérable")
	}
}

func TestUneCleAbsenteNEstPasUneErreur(t *testing.T) {
	_, srv := nouveauFauxVault(t)
	c := client(t, srv)

	_, trouve, err := c.Lire(t.Context(), "warden", "absente")
	if err != nil || trouve {
		t.Errorf("trouve=%v err=%v", trouve, err)
	}
	if err := c.Detruire(t.Context(), "warden", "absente"); err != nil {
		t.Errorf("détruire une clé absente : %v", err)
	}
}

// Un seul login pour plusieurs appels : le jeton est gardé.
func TestLeJetonEstReutilise(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	c := client(t, srv)
	for range 3 {
		if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if f.logins != 1 {
		t.Errorf("%d authentifications, 1 attendue", f.logins)
	}
}

// Un jeton révoqué côté Vault se rattrape par une nouvelle authentification,
// sans faire échouer l'appel.
func TestUnJetonRevoqueEstRemplace(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	c := client(t, srv)
	if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
		t.Fatal(err)
	}
	f.revoquerTout()
	if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
		t.Fatalf("appel après révocation : %v", err)
	}
	if f.logins != 2 {
		t.Errorf("%d authentifications, 2 attendues", f.logins)
	}
}

// Aux deux tiers du bail, un jeton renouvelable est renouvelé, pas remplacé.
func TestUnJetonRenouvelableEstRenouvele(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	f.bail, f.renouvelable = 60, true
	c := client(t, srv)
	maintenant := time.Now()
	c.now = func() time.Time { return maintenant }

	if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
		t.Fatal(err)
	}
	maintenant = maintenant.Add(45 * time.Second)
	if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 || f.renews != 1 {
		t.Errorf("logins=%d renews=%d, attendu 1 et 1", f.logins, f.renews)
	}
}

// Un jeton non renouvelable est remplacé par une nouvelle authentification,
// qui relit le secret_id : sa rotation ne demande pas de redémarrage.
func TestUnJetonNonRenouvelableEstRemplaceEnRelisantLeSecretID(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	f.bail = 60
	c := client(t, srv)
	maintenant := time.Now()
	c.now = func() time.Time { return maintenant }

	if _, _, err := c.Lire(t.Context(), "warden", "x"); err != nil {
		t.Fatal(err)
	}
	// Le fichier est remplacé par un secret_id que le faux Vault refuse : la
	// nouvelle authentification doit le lire, donc échouer.
	if err := os.WriteFile(c.cfg.SecretIDFile, []byte("tourne"), 0o600); err != nil {
		t.Fatal(err)
	}
	maintenant = maintenant.Add(45 * time.Second)
	if _, _, err := c.Lire(t.Context(), "warden", "x"); err == nil {
		t.Fatal("le secret_id n'a pas été relu")
	}
	if f.renews != 0 {
		t.Error("un jeton non renouvelable a été renouvelé")
	}
}

// Le secret_id ne fuite pas dans une erreur.
func TestUnLoginRefuseNeCiteRien(t *testing.T) {
	_, srv := nouveauFauxVault(t)
	fichier := filepath.Join(t.TempDir(), "secret_id")
	if err := os.WriteFile(fichier, []byte("mauvais-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Addr: srv.URL, RoleID: roleTest, SecretIDFile: fichier})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Lire(t.Context(), "warden", "x")
	if err == nil {
		t.Fatal("un login refusé a été accepté")
	}
	if strings.Contains(err.Error(), "mauvais-secret") {
		t.Errorf("le secret_id fuite dans l'erreur : %v", err)
	}
}

func TestUnCheminQuiSortDuMontageEstRefuse(t *testing.T) {
	_, srv := nouveauFauxVault(t)
	c := client(t, srv)
	for _, chemin := range []string{"", "../sys/policy", "a//b", "a/./b", "a?b", "a%2fb"} {
		if _, _, err := c.Lire(t.Context(), "warden", chemin); err == nil {
			t.Errorf("chemin %q accepté", chemin)
		}
	}
}

func TestNewEchoueAFroid(t *testing.T) {
	cas := []Config{
		{RoleID: "r", SecretIDFile: "f"},
		{Addr: "http://v", SecretIDFile: "f"},
		{Addr: "http://v", RoleID: "r"},
	}
	for _, cfg := range cas {
		if _, err := New(cfg); err == nil {
			t.Errorf("configuration incomplète acceptée : %+v", cfg)
		}
	}
}
