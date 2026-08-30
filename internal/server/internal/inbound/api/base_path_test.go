// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// L'API répond sous le préfixe, et le handler y voit son chemin NU.
//
// C'est ce qui permet aux routes d'être écrites sans savoir sous quel préfixe
// elles sont servies — et donc de l'être une seule fois pour les deux montages.
func TestLApiRepondSousLePrefixe(t *testing.T) {
	var vu string
	interne := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { vu = r.URL.Path })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/runs", nil)
	monterAussiSous("/api", interne).ServeHTTP(httptest.NewRecorder(), req)

	if vu != "/runs" {
		t.Fatalf("le handler voit %q, attendu %q", vu, "/runs")
	}
}

// ET À LA RACINE, en même temps. C'est toute la raison d'être de ce montage.
//
// La CLI garde l'hôte de l'API à sa racine — donc aucun client à migrer — tandis
// que le navigateur joint la MÊME API sous `/api` de l'hôte de l'interface, ce
// qui la met en même origine que la SPA. Servir l'un sans l'autre casse
// exactement la moitié des appelants, et le test le dirait trop tard.
func TestLApiRepondAussiALaRacine(t *testing.T) {
	var vu string
	interne := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { vu = r.URL.Path })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/runs", nil)
	monterAussiSous("/api", interne).ServeHTTP(httptest.NewRecorder(), req)

	if vu != "/runs" {
		t.Fatalf("le handler voit %q, attendu %q", vu, "/runs")
	}
}

// Un préfixe VIDE laisse le handler seul à la racine : c'est le déploiement
// d'avant, qui doit continuer de fonctionner sans changer une valeur.
func TestSansPrefixeLApiResteALaRacine(t *testing.T) {
	for _, prefixe := range []string{"", "/"} {
		atteint := false
		interne := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { atteint = true })

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/runs", nil)
		monterAussiSous(prefixe, interne).ServeHTTP(httptest.NewRecorder(), req)

		if !atteint {
			t.Errorf("préfixe %q : le handler n'a pas été atteint", prefixe)
		}
	}
}
