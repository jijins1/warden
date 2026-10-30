package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/version"
)

func TestSeulUnEcartEntreDeuxVersionsPublieesDeclencheLaMiseAJour(t *testing.T) {
	cas := []struct {
		locale, serveur string
		attendu         bool
	}{
		{"0.0.41", "0.0.42", true},
		// Symétrique : le but est l'accord, pas la nouveauté (ADR-0049).
		{"0.0.43", "0.0.42", true},
		{"0.0.42", "0.0.42", false},
		{"dev", "0.0.42", false},
		{"0.0.42", "dev", false},
		// Un serveur antérieur à l'ADR-0049 n'annonce rien.
		{"0.0.42", "", false},
	}
	for _, c := range cas {
		if got := ecart(c.locale, c.serveur); got != c.attendu {
			t.Errorf("ecart(%q, %q) = %v, attendu %v", c.locale, c.serveur, got, c.attendu)
		}
	}
}

func TestLaReponseVideAccepteLaMiseAJour(t *testing.T) {
	for reponse, attendu := range map[string]bool{
		"\n": true, "o\n": true, "Oui\n": true, "y\n": true,
		"n\n": false, "non\n": false, "plus tard\n": false,
	} {
		if got := accepte(strings.NewReader(reponse)); got != attendu {
			t.Errorf("accepte(%q) = %v, attendu %v", reponse, got, attendu)
		}
	}
}

// serveurEn monte un serveur qui annonce `v` et ne sert rien d'autre : un test
// qui atteindrait le téléchargement échouerait sur un 404, et non en silence.
func serveurEn(t *testing.T, v string) string {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"version":%q}`, v)
	}))
	t.Cleanup(h.Close)
	return h.URL
}

// enVersion donne à la CLI de ce test la version `v`.
func enVersion(t *testing.T, v string) {
	t.Helper()
	avant := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = avant })
}

// pasUnTerminal rend une entrée qui n'est pas un terminal, comme celle d'un
// script ou d'une CI.
func pasUnTerminal(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func alignee(t *testing.T, serveur string) (string, error) {
	t.Helper()
	api, err := client.New(client.Config{BaseURL: serveur})
	if err != nil {
		t.Fatal(err)
	}
	var sortie bytes.Buffer
	err = aligneeSurLeServeur(t.Context(), api, pasUnTerminal(t), &sortie)
	return sortie.String(), err
}

// Sans terminal, personne ne peut répondre : la soumission échoue en nommant la
// commande qui en sort, sans rien demander ni rien installer.
func TestSansTerminalUnEcartBloqueLaSoumission(t *testing.T) {
	enVersion(t, "0.0.41")

	sortie, err := alignee(t, serveurEn(t, "0.0.42"))
	if err == nil || !strings.Contains(err.Error(), "warden upgrade") {
		t.Errorf("err = %v, attendu un refus qui nomme « warden upgrade »", err)
	}
	if sortie != "" {
		t.Errorf("une question a été posée sans terminal : %q", sortie)
	}
}

func TestSansEcartLaSoumissionPasse(t *testing.T) {
	enVersion(t, "0.0.42")

	if _, err := alignee(t, serveurEn(t, "0.0.42")); err != nil {
		t.Errorf("err = %v, attendu aucun blocage", err)
	}
}

// LE garde-fou contre une relance sans fin : un binaire tout juste installé qui
// annonce encore une autre version que le serveur s'arrête, il ne redemande pas.
func TestUneCLIToutJusteMiseAJourNeSeRelancePasEnBoucle(t *testing.T) {
	enVersion(t, "0.0.41")
	t.Setenv(envRelance, "1")

	_, err := alignee(t, serveurEn(t, "0.0.42"))
	if err == nil || !strings.Contains(err.Error(), "incohérente") {
		t.Errorf("err = %v, attendu le refus d'une release incohérente", err)
	}
}

func upgrade(t *testing.T, serveur string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvServeur, serveur)
	var sortie bytes.Buffer
	err := commandeUpgrade(t.Context(), args, &sortie)
	return sortie.String(), err
}

func TestUpgradeAJourNeTelechargeRien(t *testing.T) {
	enVersion(t, "0.0.42")

	sortie, err := upgrade(t, serveurEn(t, "0.0.42"))
	if err != nil || !strings.Contains(sortie, "à jour (v0.0.42)") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
}

func TestUpgradeContreUnServeurDeDevNeTelechargeRien(t *testing.T) {
	enVersion(t, "0.0.42")

	sortie, err := upgrade(t, serveurEn(t, "dev"))
	if err != nil || !strings.Contains(sortie, "n'annonce pas de version publiée") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
}

func TestUpgradeRefuseUnArgument(t *testing.T) {
	if _, err := upgrade(t, serveurEn(t, "0.0.42"), "0.0.7"); err == nil {
		t.Error("un argument a été accepté : la version visée est celle du serveur, pas un choix")
	}
}

func versions(t *testing.T, serveur string, args ...string) (string, error) {
	t.Helper()
	t.Setenv(EnvServeur, serveur)
	var sortie bytes.Buffer
	err := commandeVersion(t.Context(), args, &sortie)
	return sortie.String(), err
}

func TestVersionAfficheLaCLIEtLeServeur(t *testing.T) {
	enVersion(t, "0.0.42")

	sortie, err := versions(t, serveurEn(t, "0.0.42"))
	if err != nil || !strings.Contains(sortie, "CLI      v0.0.42") || !strings.Contains(sortie, "serveur  v0.0.42") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
	if strings.Contains(sortie, "warden upgrade") {
		t.Errorf("un écart a été signalé entre deux versions égales : %q", sortie)
	}
}

func TestVersionSignaleUnEcartEtNommeUpgrade(t *testing.T) {
	enVersion(t, "0.0.41")

	sortie, err := versions(t, serveurEn(t, "0.0.42"))
	if err != nil || !strings.Contains(sortie, "warden upgrade") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
}

func TestVersionDUnServeurQuiNAnnonceRien(t *testing.T) {
	enVersion(t, "0.0.42")
	h := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(h.Close)

	sortie, err := versions(t, h.URL)
	if err != nil || !strings.Contains(sortie, "serveur  non annoncée") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
}

// Le serveur injoignable n'efface pas la version de la CLI, et le code de sortie
// le dit.
func TestVersionDeLaCLISurvitAUnServeurInjoignable(t *testing.T) {
	enVersion(t, "0.0.42")
	h := httptest.NewServer(http.NotFoundHandler())
	adresse := h.URL
	h.Close()

	sortie, err := versions(t, adresse)
	if err == nil || !strings.Contains(sortie, "CLI      v0.0.42") {
		t.Errorf("sortie = %q, err = %v", sortie, err)
	}
}
