// Étage 1 de l'ADR-0010 : sans annuaire. Ce qui se vérifie ici est ce que
// l'adaptateur REFUSE avant de joindre quoi que ce soit.
//
// CE QUI N'EST PAS TESTÉ, et c'est assumé (ADR-0053) : le dialogue avec un AD.
// Le filtre échappé, la distinction refus (code 49) / panne, le refus quand
// plusieurs comptes répondent, les groupes imbriqués et StartTLS ne sont prouvés
// par aucun test. Les groupes imbriqués reposent sur une règle propre à AD
// (LDAP_MATCHING_RULE_IN_CHAIN) qu'un OpenLDAP de test ne reproduit pas : un test
// contre lui donnerait une confiance qu'il n'a pas. Ces cas se vérifient sur
// l'AD réel, à la mise en service.

package activedirectory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/activedirectory"
)

func configValide() activedirectory.Config {
	return activedirectory.Config{
		URL:          "ldaps://dc.exemple.local:636",
		BaseDN:       "DC=exemple,DC=local",
		BindDN:       "CN=svc-warden,OU=Services,DC=exemple,DC=local",
		BindPassword: "secret",
	}
}

func TestUneURLEnClairEstRefuseeAuDemarrage(t *testing.T) {
	t.Parallel()

	cfg := configValide()
	cfg.URL = "ldap://dc.exemple.local:389"
	if _, err := activedirectory.New(cfg); err == nil {
		t.Fatal("ldap:// sans StartTLS accepté : les mots de passe passeraient en clair")
	}

	cfg.StartTLS = true
	if _, err := activedirectory.New(cfg); err != nil {
		t.Fatalf("ldap:// avec StartTLS refusé : %v", err)
	}
}

func TestLeCompteDeServiceEstRequis(t *testing.T) {
	t.Parallel()

	cfg := configValide()
	cfg.BindPassword = ""
	if _, err := activedirectory.New(cfg); err == nil {
		t.Fatal("configuration sans mot de passe du compte de service acceptée")
	}
}

// Un bind avec un mot de passe vide est un bind NON AUTHENTIFIÉ, que l'AD
// accepte. L'adresse ne résout pas : si le refus n'avait pas lieu AVANT toute
// connexion, l'erreur serait une panne de réseau et non un refus.
func TestUnMotDePasseVideEstRefuseSansJoindreLAnnuaire(t *testing.T) {
	t.Parallel()

	cfg := configValide()
	cfg.URL = "ldaps://annuaire.invalid:636"
	a, err := activedirectory.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Un délai court : si le garde-fou sautait, le test échouerait vite au lieu
	// d'attendre une résolution DNS.
	ctx, annuler := context.WithTimeout(context.Background(), 2*time.Second)
	defer annuler()

	for _, cas := range []struct{ login, mdp string }{
		{"alice", ""},
		{"", "un mot de passe"},
		{"   ", "un mot de passe"},
	} {
		_, err := a.Authentifier(ctx, cas.login, []byte(cas.mdp))
		if !errors.Is(err, core.ErrIdentifiantsRefuses) {
			t.Errorf("login %q, mot de passe %q : %v, refus attendu", cas.login, cas.mdp, err)
		}
	}
}

// Le GUID doit être celui que montre `Get-ADUser`, sans quoi on ne peut pas
// rapprocher un compte warden d'un compte AD à la main.
func TestLeGUIDEstAuFormatMicrosoft(t *testing.T) {
	t.Parallel()

	// Get-ADUser : ObjectGUID = 3f2504e0-4f89-11d3-9a0c-0305e82c3301
	brut := []byte{
		0xe0, 0x04, 0x25, 0x3f, 0x89, 0x4f, 0xd3, 0x11,
		0x9a, 0x0c, 0x03, 0x05, 0xe8, 0x2c, 0x33, 0x01,
	}
	got, err := activedirectory.FormaterGUID(brut)
	if err != nil {
		t.Fatalf("FormaterGUID: %v", err)
	}
	if want := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"; got != want {
		t.Fatalf("GUID = %s, attendu %s", got, want)
	}

	if _, err := activedirectory.FormaterGUID(brut[:8]); err == nil {
		t.Fatal("GUID tronqué accepté")
	}
}
