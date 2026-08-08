// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.

package gitd_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// TestRefusSansJeton est le TEST NÉGATIF exigé par le doc.go de `gitd` et par
// l'ADR-0007 : le rôle `git` est joignable depuis les pods runner, donc depuis
// du code arbitraire, et son autorisation doit être couverte par un test qui
// vérifie le refus — pas seulement l'acceptation.
func TestRefusSansJeton(t *testing.T) {
	srv, _ := monter(t)

	cas := []struct {
		nom      string
		entete   string
		presente bool
	}{
		{"aucun en-tête", "", false},
		{"Bearer sans valeur", "Bearer ", true},
		{"Bearer vide", "Bearer", true},
		{"jeton faux, même longueur", "Bearer " + strings.Repeat("x", len(jetonDeTest)), true},
		{"jeton faux, longueur différente", "Bearer court", true},
		{"schéma Basic", "Basic dXNlcjpwYXNz", true},
		{"jeton correct mais en préfixe", "Bearer " + jetonDeTest + "suffixe", true},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			req := requeteDiff(t, srv.URL, gitwire.DiffRequest{Repo: "depot", From: shaBidon, To: shaBidon})
			if c.presente {
				req.Header.Set("Authorization", c.entete)
			}

			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("appel: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("statut %d, attendu 401", resp.StatusCode)
			}

			// Le refus n'apprend rien : ni corps, ni indication du schéma
			// attendu. Chacune de ces différences est une information offerte à
			// qui sonde le service.
			corps, _ := io.ReadAll(resp.Body)
			if len(corps) != 0 {
				t.Errorf("corps non vide sur un refus: %q", corps)
			}
			if v := resp.Header.Get("WWW-Authenticate"); v != "" {
				t.Errorf("WWW-Authenticate rendu sur un refus: %q", v)
			}
		})
	}
}

// TestJetonDansLURLRefuse : le jeton se présente en en-tête et nulle part
// ailleurs. Une URL se retrouve dans les journaux d'accès de tous les
// intermédiaires — c'est l'argument que sse.go tient déjà pour le dispatch.
func TestJetonDansLURLRefuse(t *testing.T) {
	srv, _ := monter(t)

	req := requeteDiff(t, srv.URL+"?token="+jetonDeTest,
		gitwire.DiffRequest{Repo: "depot", From: shaBidon, To: shaBidon})

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("statut %d, attendu 401 : le jeton en paramètre d'URL ne vaut rien", resp.StatusCode)
	}
}

// TestAuthentifieNAutorisePasTout : un jeton valide ouvre l'endpoint de diff, et
// rien d'autre. S'authentifier n'est pas être autorisé à tout faire.
func TestAuthentifieNAutorisePasTout(t *testing.T) {
	srv, _ := monter(t)

	cas := []struct{ methode, chemin string }{
		{http.MethodGet, gitwire.DiffPath},
		{http.MethodDelete, gitwire.DiffPath},
		{http.MethodPost, "/internal/v1/inconnu"},
		{http.MethodGet, "/"},
	}

	for _, c := range cas {
		t.Run(c.methode+" "+c.chemin, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), c.methode, srv.URL+c.chemin, nil)
			if err != nil {
				t.Fatalf("requête: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+jetonDeTest)

			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("appel: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s %s a réussi alors que seul POST %s existe",
					c.methode, c.chemin, gitwire.DiffPath)
			}
		})
	}
}

// requeteDiff construit une requête de diff sans jeton ; l'appelant l'ajoute.
func requeteDiff(t *testing.T, url string, req gitwire.DiffRequest) *http.Request {
	t.Helper()
	corps := encoder(t, req)
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, urlDiff(url), corps)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func urlDiff(base string) string {
	if strings.Contains(base, "?") {
		i := strings.Index(base, "?")
		return base[:i] + gitwire.DiffPath + base[i:]
	}
	return base + gitwire.DiffPath
}
