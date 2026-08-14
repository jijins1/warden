package gitd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// forge sert un dépôt en lecture, comme le ferait Gitea, et enregistre l'en-tête
// d'autorisation qu'elle a vu.
//
// Une vraie forge derrière `git http-backend` plutôt qu'un bouchon : ce que le
// test doit prouver, c'est qu'un `git fetch` traverse réellement le réseau et
// que le credential arrive. Un faux serveur prouverait que notre code appelle
// notre code.
func forge(t *testing.T, bare string) (string, *string) {
	t.Helper()
	vu := new(string)

	backend := &cgi.Handler{
		Path: gitBinary(t),
		Args: []string{"http-backend"},
		Dir:  filepath.Dir(bare),
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(bare),
			"GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + filepath.Dir(bare),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=/usr/bin:/bin",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			*vu = a
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + filepath.Base(bare), vu
}

// appelerMiroir frappe l'API interne comme le fera `outbound/gitclient`.
//
// Il rend le statut et le corps plutôt que la réponse : le corps est lu et
// refermé ici, ce qui évite d'avoir à discipliner chaque appelant — et rend
// visible au linter que la fermeture a bien lieu.
func appelerMiroir(t *testing.T, srv *httptest.Server, req gitwire.MirrorRequest) (int, gitwire.MirrorResponse, string) {
	t.Helper()
	corps, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.MirrorPath, bytes.NewReader(corps))
	if err != nil {
		t.Fatalf("requête : %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+jetonDeTest)

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("appel : %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	brut, _ := io.ReadAll(resp.Body)
	var out gitwire.MirrorResponse
	_ = json.Unmarshal(brut, &out)
	return resp.StatusCode, out, string(brut)
}

// monterMiroir monte un dépôt bare vide côté serveur et le service.
func monterMiroir(t *testing.T, forgeToken string) (*httptest.Server, string) {
	t.Helper()
	racine := t.TempDir()
	mustGit(t, racine, "init", "--bare", "--initial-branch=main", filepath.Join(racine, "depot.git"))

	s, err := gitd.New(gitd.Config{Racine: racine, Jeton: jetonDeTest, ForgeToken: forgeToken})
	if err != nil {
		t.Fatalf("gitd.New : %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, racine
}

// TestMiroirRafraichitDepuisLaForge est le chemin nominal, bout en bout.
func TestMiroirRafraichitDepuisLaForge(t *testing.T) {
	amont := t.TempDir()
	bareAmont := filepath.Join(amont, "projet.git")
	mustGit(t, amont, "init", "--bare", "--initial-branch=main", bareAmont)

	src := t.TempDir()
	mustGit(t, src, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(src, "site.yml"), []byte("- hosts: all"), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	mustGit(t, src, "add", ".")
	mustGit(t, src, "commit", "-m", "protégée")
	attendu := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	mustGit(t, src, "push", bareAmont, "HEAD:refs/heads/main")

	url, vu := forge(t, bareAmont)
	srv, racine := monterMiroir(t, "jeton-de-forge")

	statut, out, _ := appelerMiroir(t, srv, gitwire.MirrorRequest{
		Repo: "depot", Remote: url, Ref: "refs/heads/main",
	})
	if statut != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", statut)
	}
	if out.SHA != attendu {
		t.Errorf("miroir à %s, attendu %s", out.SHA, attendu)
	}

	// La ref existe réellement côté serveur : c'est elle qui ancrera le contrôle
	// de fraîcheur, pas la valeur rendue dans la réponse.
	local := strings.TrimSpace(mustGit(t, filepath.Join(racine, "depot.git"),
		"rev-parse", "--verify", "refs/heads/main"))
	if local != attendu {
		t.Errorf("la ref locale est à %s, attendu %s", local, attendu)
	}

	if !strings.Contains(*vu, "jeton-de-forge") {
		t.Errorf("le credential de forge n'a pas traversé le transport : %q", *vu)
	}
}

// TestMiroirRefuseUnTransportExecutable ferme la voie qui transformerait cet
// endpoint en exécution de commande.
//
// `ext::` prend une commande shell et la lance. L'URL vient de la politique
// serveur, mais elle traverse une requête, et un endpoint qui fait sortir des
// connexions du rôle `git` est le dernier endroit où faire confiance sur parole.
func TestMiroirRefuseUnTransportExecutable(t *testing.T) {
	srv, _ := monterMiroir(t, "jeton-de-forge")

	for _, remote := range []string{
		"ext::sh -c touch% /tmp/pwned",
		"file:///etc",
		"ssh://forge/projet.git",
		"",
	} {
		t.Run(remote, func(t *testing.T) {
			statut, _, _ := appelerMiroir(t, srv, gitwire.MirrorRequest{
				Repo: "depot", Remote: remote, Ref: "refs/heads/main",
			})
			if statut != http.StatusBadRequest {
				t.Errorf("statut %d, attendu 400", statut)
			}
		})
	}
}

// TestMiroirInjoignableEstUn502 : la panne est chez la forge, et le code le dit.
//
// Un 500 enverrait l'opérateur fouiller les journaux du rôle `git` alors que
// c'est la dépendance externe qui répond mal — celle que l'ADR-0027 a ajoutée au
// point de défaillance unique.
func TestMiroirInjoignableEstUn502(t *testing.T) {
	srv, _ := monterMiroir(t, "jeton-de-forge")

	statut, _, _ := appelerMiroir(t, srv, gitwire.MirrorRequest{
		Repo: "depot", Remote: "http://127.0.0.1:1/projet.git", Ref: "refs/heads/main",
	})
	if statut != http.StatusBadGateway {
		t.Errorf("statut %d, attendu 502", statut)
	}
}

// TestMiroirNeFuitPasLURL : le message d'erreur ne cite pas la forge.
//
// Une URL de forge dans un message rendu à l'appelant finit recopiée dans un
// ticket, puis dans une capture d'écran. La stderr de git la mentionne
// volontiers ; elle reste côté serveur.
func TestMiroirNeFuitPasLURL(t *testing.T) {
	srv, _ := monterMiroir(t, "jeton-de-forge")
	const url = "http://forge-interne.example.invalid:9999/equipe/projet.git"

	_, _, brut := appelerMiroir(t, srv, gitwire.MirrorRequest{
		Repo: "depot", Remote: url, Ref: "refs/heads/main",
	})
	if strings.Contains(brut, "forge-interne") {
		t.Errorf("l'URL de la forge fuit dans la réponse : %s", brut)
	}
}
