// Étage 1 de l'ADR-0010 : la livraison d'un mot de passe de connexion
// (ADR-0048), avec les empreintes qui seules l'autorisent à partir.
package core_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// machinesFactices est un catalogue de machines en mémoire.
type machinesFactices []core.ManagedHost

func (m machinesFactices) SaveHost(context.Context, core.ManagedHost) error { return nil }
func (m machinesFactices) HostByName(context.Context, string, string) (core.ManagedHost, error) {
	return core.ManagedHost{}, core.ErrNoHost
}
func (m machinesFactices) ForgetHost(context.Context, string, string) error { return nil }
func (m machinesFactices) ListHosts(_ context.Context, env string) ([]core.ManagedHost, error) {
	var out []core.ManagedHost
	for _, h := range m {
		if env == "" || h.Environment == env {
			out = append(out, h)
		}
	}
	return out, nil
}

const cleHote = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple"

func TestLeMotDePasseDeConnexionPartAvecLesEmpreintesDeclarees(t *testing.T) {
	run := runValide()
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleConnexion(run.RunRef), []byte("mdp-ldap"))
	machines := machinesFactices{
		{Name: "ldap-01", Environment: run.Environment, Address: "10.0.0.5", Port: 22, HostKey: cleHote},
		{Name: "ldap-02", Environment: run.Environment, Address: "ldap-02", Port: 2222, HostKey: cleHote},
		// Déclarée sans clé d'hôte : absente des empreintes, donc refusée par
		// le runner.
		{Name: "sans-cle", Environment: run.Environment, Address: "10.0.0.7", Port: 22},
		// Un autre environnement : jamais.
		{Name: "autre", Environment: "ailleurs", Address: "10.9.9.9", Port: 22, HostKey: cleHote},
	}

	b, err := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	if err != nil {
		t.Fatal(err)
	}
	b.AvecEnrolement(machines, nil, consigne)

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatalf("OrderFor : %v", err)
	}
	if string(ordre.ConnectionPassword.Reveal()) != "mdp-ldap" {
		t.Error("le mot de passe de connexion n'est pas dans l'ordre")
	}
	attendu := []string{
		"ldap-01,10.0.0.5 " + cleHote,
		"[ldap-02]:2222 " + cleHote,
	}
	if !slices.Equal(ordre.KnownHosts, attendu) {
		t.Errorf("empreintes :\n%q\nattendu\n%q", ordre.KnownHosts, attendu)
	}

	// Consommé par la livraison, comme celui d'élévation.
	ordre2, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordre2.ConnectionPassword) != 0 {
		t.Error("le mot de passe doit être consommé par sa lecture")
	}
}

// Sans catalogue de machines, le mot de passe ne part pas : il partirait
// sans empreintes.
func TestSansCatalogueLeMotDePasseNePartPas(t *testing.T) {
	run := runValide()
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleConnexion(run.RunRef), []byte("mdp-ldap"))

	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecEnrolement(nil, nil, consigne)

	if _, err := b.OrderFor(t.Context(), 1); err == nil {
		t.Error("un mot de passe est parti sans empreintes vérifiables")
	}
}

// Un run à clé vérifie AUSSI les empreintes : c'est le défaut (ADR-0048).
func TestUnRunACleVerifieLesEmpreintes(t *testing.T) {
	run := runValide()
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecMachines(machinesFactices{{Name: "m", Environment: run.Environment, Address: "m", HostKey: cleHote}})

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ordre.HostKeyChecking || !slices.Equal(ordre.KnownHosts, []string{"m " + cleHote}) {
		t.Errorf("vérification = %v, empreintes = %q", ordre.HostKeyChecking, ordre.KnownHosts)
	}
}

// Sans catalogue, la vérification reste stricte, sur une liste vide : fermé
// par défaut.
func TestSansCatalogueLaVerificationResteStricte(t *testing.T) {
	run := runValide()
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ordre.HostKeyChecking || len(ordre.KnownHosts) != 0 {
		t.Errorf("vérification = %v, empreintes = %q", ordre.HostKeyChecking, ordre.KnownHosts)
	}
}

// Un run sans environnement n'a passé aucun contrôle de droit : il ne reçoit
// les empreintes d'aucun environnement, et la vérification reste stricte.
func TestSansEnvironnementAucuneEmpreinte(t *testing.T) {
	run := runValide()
	run.Environment = ""
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecMachines(machinesFactices{{Name: "prod-01", Environment: "prod", Address: "10.0.0.1", HostKey: cleHote}})

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ordre.HostKeyChecking || len(ordre.KnownHosts) != 0 {
		t.Errorf("vérification = %v, empreintes = %q", ordre.HostKeyChecking, ordre.KnownHosts)
	}
}

// Sans environnement, le mot de passe ne part pas : il n'aurait aucune
// empreinte contre laquelle se vérifier.
func TestSansEnvironnementLeMotDePasseNePartPas(t *testing.T) {
	run := runValide()
	run.Environment = ""
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleConnexion(run.RunRef), []byte("mdp-ldap"))
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecEnrolement(machinesFactices{{Name: "prod-01", Environment: "prod", Address: "10.0.0.1", HostKey: cleHote}}, nil, consigne)

	if _, err := b.OrderFor(t.Context(), 1); err == nil {
		t.Error("un mot de passe est parti pour un run sans environnement")
	}
}

// L'interrupteur de transition coupe la vérification des runs à clé…
func TestLInterrupteurCoupeLaVerificationDesRunsACle(t *testing.T) {
	run := runValide()
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecMachines(machinesFactices{{Name: "m", Environment: run.Environment, HostKey: cleHote}})
	b.SansVerifierLesEmpreintes()

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if ordre.HostKeyChecking || len(ordre.KnownHosts) != 0 {
		t.Errorf("vérification = %v, empreintes = %q", ordre.HostKeyChecking, ordre.KnownHosts)
	}
}

// … mais jamais celle d'un run à mot de passe.
func TestLInterrupteurNeTouchePasAuxRunsAMotDePasse(t *testing.T) {
	run := runValide()
	consigne := &consigneOrdre{}
	_ = consigne.Stash(t.Context(), core.CleConnexion(run.RunRef), []byte("mdp-ldap"))
	b, _ := core.NewOrderBuilder(runsFactices{1: run}, &frappeurFactice{}, nil, "https://git.invalid")
	b.AvecEnrolement(machinesFactices{{Name: "m", Environment: run.Environment, Address: "m", HostKey: cleHote}}, nil, consigne)
	b.SansVerifierLesEmpreintes()

	ordre, err := b.OrderFor(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ordre.HostKeyChecking || len(ordre.KnownHosts) != 1 {
		t.Errorf("un run à mot de passe est parti sans vérification : %v", ordre.HostKeyChecking)
	}
}

func TestCleConnexionEstDistincteDesAutres(t *testing.T) {
	r := "01J9RUNREF"
	cles := []string{core.CleConnexion(r), core.CleBecome(r), core.CleForwardEnv(r), r}
	for i := range cles {
		for j := i + 1; j < len(cles); j++ {
			if cles[i] == cles[j] {
				t.Errorf("deux consignes homonymes : %q", cles[i])
			}
		}
	}
}

func TestLeCompteDistantPasseLAllowlist(t *testing.T) {
	if err := core.ValiderFlags([]string{"--user=alice"}); err != nil {
		t.Errorf("--user refusé : %v", err)
	}
	if err := core.ValiderFlags([]string{"--user=alice;rm"}); err == nil {
		t.Error("un compte malformé a été accepté")
	}
}
