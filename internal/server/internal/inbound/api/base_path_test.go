// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLePrefixeMonteLApiSousUnChemin.
//
// MÊME ORIGINE (ADR-0035, amendement) : l'interface prend la racine de l'hôte et
// l'API vit sous un préfixe. Le retrait du préfixe est fait ICI et non par un
// middleware du contrôleur d'ingress : le serveur sait où il est monté, et la
// chart cesse d'être liée à Traefik.
func TestLePrefixeMonteLApiSousUnChemin(t *testing.T) {
	atteint := false
	interne := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		atteint = true
		// Le handler doit voir son chemin NU : c'est ce qui permet aux routes
		// d'être écrites sans savoir sous quel préfixe elles sont servies.
		if r.URL.Path != "/runs" {
			t.Errorf("le handler voit %q, attendu %q", r.URL.Path, "/runs")
		}
	})

	h := souslePrefixe("/api", interne)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/runs", nil)
	h.ServeHTTP(rec, req)

	if !atteint {
		t.Fatalf("le handler n'a pas été atteint (code %d)", rec.Code)
	}
}

// Hors du préfixe, rien ne répond. Sans quoi l'API resterait jointe à la racine
// et masquerait l'interface, qui y vit désormais.
func TestHorsDuPrefixeRienNEstServi(t *testing.T) {
	interne := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("le handler a été atteint sur %q, il ne devait pas l'être", r.URL.Path)
	})

	h := souslePrefixe("/api", interne)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/runs", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code %d, attendu 404", rec.Code)
	}
}

// Un préfixe VIDE laisse le handler à la racine : c'est le déploiement à deux
// origines, qui doit continuer de fonctionner sans changer une valeur.
func TestSansPrefixeLApiResteALaRacine(t *testing.T) {
	for _, prefixe := range []string{"", "/"} {
		atteint := false
		interne := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { atteint = true })

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/runs", nil)
		souslePrefixe(prefixe, interne).ServeHTTP(httptest.NewRecorder(), req)

		if !atteint {
			t.Errorf("préfixe %q : le handler n'a pas été atteint", prefixe)
		}
	}
}
