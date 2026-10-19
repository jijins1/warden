package vault

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

func consigne(t *testing.T) (*Consigne, *fauxVault) {
	t.Helper()
	f, srv := nouveauFauxVault(t)
	c, err := NewConsigne(client(t, srv), montageTest, time.Minute)
	if err != nil {
		t.Fatalf("NewConsigne : %v", err)
	}
	return c, f
}

// La propriété qui justifie le mécanisme : une valeur lue ne se relit pas.
func TestLaLectureConsommeLaValeur(t *testing.T) {
	c, f := consigne(t)
	ctx := t.Context()

	if err := c.Stash(ctx, "01J9RUNREF-become", []byte("motdepasse")); err != nil {
		t.Fatalf("Stash : %v", err)
	}
	v, err := c.Take(ctx, "01J9RUNREF-become")
	if err != nil || string(v) != "motdepasse" {
		t.Fatalf("Take : %q, %v", v, err)
	}
	if _, err := c.Take(ctx, "01J9RUNREF-become"); !errors.Is(err, core.ErrNoStash) {
		t.Errorf("seconde lecture : %v, ErrNoStash attendu", err)
	}
	if len(f.kv) != 0 {
		t.Error("la valeur reste dans Vault après lecture")
	}
	if f.effacesLogiquement != 0 {
		t.Error("la destruction est passée par data/ : la valeur resterait récupérable")
	}
}

// Deux répliques, deux clients authentifiés séparément : ce que l'une dépose,
// l'autre le livre. C'est ce que le cubbyhole ne permettait pas (ADR-0047).
func TestUneValeurDeposeeParUneRepliqueEstLueParLAutre(t *testing.T) {
	f, srv := nouveauFauxVault(t)
	a, _ := NewConsigne(client(t, srv), montageTest, time.Minute)
	b, _ := NewConsigne(client(t, srv), montageTest, time.Minute)

	if err := a.Stash(t.Context(), "01J9RUNREF", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Take(t.Context(), "01J9RUNREF"); err != nil || string(v) != "pw" {
		t.Errorf("Take par l'autre réplique : %q, %v", v, err)
	}
	if f.logins != 2 {
		t.Errorf("%d authentifications, 2 attendues", f.logins)
	}
}

func TestReposerUneValeurLaRemplace(t *testing.T) {
	c, _ := consigne(t)
	ctx := t.Context()
	_ = c.Stash(ctx, "k", []byte("ancien"))
	_ = c.Stash(ctx, "k", []byte("nouveau"))
	if v, err := c.Take(ctx, "k"); err != nil || string(v) != "nouveau" {
		t.Errorf("Take : %q, %v", v, err)
	}
}

// Une valeur jamais reprise ne reste pas utilisable : passé le TTL, elle est
// détruite et traitée comme absente.
func TestUneValeurPerimeeEstDetruiteEtAbsente(t *testing.T) {
	c, f := consigne(t)
	maintenant := time.Now()
	c.now = func() time.Time { return maintenant }

	if err := c.Stash(t.Context(), "k", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	maintenant = maintenant.Add(2 * time.Minute)
	if _, err := c.Take(t.Context(), "k"); !errors.Is(err, core.ErrNoStash) {
		t.Errorf("valeur périmée : %v, ErrNoStash attendu", err)
	}
	if len(f.kv) != 0 {
		t.Error("la valeur périmée n'a pas été détruite")
	}
}

func TestUneValeurAbsenteEstDistinguee(t *testing.T) {
	c, _ := consigne(t)
	_, err := c.Take(t.Context(), "jamais-posee")
	if !errors.Is(err, core.ErrNoStash) {
		t.Errorf("%v, ErrNoStash attendu", err)
	}
}

func TestUneConsigneVideOuSansCleEstRefusee(t *testing.T) {
	c, _ := consigne(t)
	if err := c.Stash(t.Context(), "", []byte("x")); err == nil {
		t.Error("clé vide acceptée")
	}
	if err := c.Stash(t.Context(), "k", nil); err == nil {
		t.Error("valeur vide acceptée")
	}
	if _, err := c.Take(t.Context(), ""); err == nil {
		t.Error("Take sans clé accepté")
	}
}

// Vault injoignable : l'erreur nomme la clé, jamais la valeur.
func TestUneErreurNeCiteJamaisLaValeur(t *testing.T) {
	_, srv := nouveauFauxVault(t)
	kv := client(t, srv)
	srv.Close()
	c, _ := NewConsigne(kv, montageTest, time.Minute)
	err := c.Stash(t.Context(), "k", []byte("valeur-secrete"))
	if err == nil {
		t.Fatal("Stash vers un Vault fermé a réussi")
	}
	if strings.Contains(err.Error(), "valeur-secrete") {
		t.Errorf("la valeur fuite dans l'erreur : %v", err)
	}
}
