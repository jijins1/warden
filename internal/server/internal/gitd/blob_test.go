// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package gitd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

const contenuConfig = "server: https://exemple\ndefault_env: staging\n"

// monterAvecConfig sert un dépôt portant un fichier de configuration à la racine
// et un répertoire — de quoi exercer les trois issues du handler.
func monterAvecConfig(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	racine := t.TempDir()
	travail := t.TempDir()
	lancer(t, travail, "init", "--initial-branch=principale")
	lancer(t, travail, "config", "user.email", "test@example.com")
	lancer(t, travail, "config", "user.name", "Test")

	ecrire(t, travail, ".ansibleproxy.yml", contenuConfig)
	if err := mkdirTest(filepath.Join(travail, "inventories")); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ecrire(t, travail, filepath.Join("inventories", "hosts"), "[web]\n")
	lancer(t, travail, "add", ".")
	lancer(t, travail, "commit", "-m", "un")
	commit := head(t, travail)

	lancer(t, "", "clone", "--bare", travail, filepath.Join(racine, "depot.git"))

	s, err := gitd.New(gitd.Config{Racine: racine, Jeton: jetonDeTest})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, commit
}

func appelerBlob(t *testing.T, srv *httptest.Server, req gitwire.BlobRequest) *http.Response {
	t.Helper()

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.BlobPath, encoder(t, req))
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+jetonDeTest)
	r.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	return resp
}

func TestBlobRendLeContenu(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	resp := appelerBlob(t, srv, gitwire.BlobRequest{
		Repo: "depot", SHA: commit, Path: ".ansibleproxy.yml",
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d", resp.StatusCode)
	}
	corps, _ := io.ReadAll(resp.Body)
	if string(corps) != contenuConfig {
		t.Errorf("= %q, veut %q", corps, contenuConfig)
	}
	// Le contenu vient d'un dépôt audité : il ne doit jamais pouvoir être
	// interprété comme du HTML par un navigateur qui l'atteindrait.
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("l'en-tête nosniff manque")
	}
}

// LE test du lot. Un commit absent et un chemin absent partagent le 404, et
// l'appelant TOLÈRE le second tout en refusant le premier : les confondre
// ferait passer une incohérence pour un état ordinaire.
func TestBlobDistingueCommitEtCheminAbsents(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	cas := []struct {
		nom  string
		req  gitwire.BlobRequest
		veut gitwire.Code
	}{
		{
			nom:  "chemin absent",
			req:  gitwire.BlobRequest{Repo: "depot", SHA: commit, Path: "absent.yml"},
			veut: gitwire.CodeUnknownPath,
		},
		{
			nom:  "commit absent",
			req:  gitwire.BlobRequest{Repo: "depot", SHA: shaBidon, Path: ".ansibleproxy.yml"},
			veut: gitwire.CodeUnknownObject,
		},
		{
			nom:  "dépôt inconnu",
			req:  gitwire.BlobRequest{Repo: "inexistant", SHA: commit, Path: ".ansibleproxy.yml"},
			veut: gitwire.CodeUnknownRepo,
		},
		{
			nom:  "SHA malformé",
			req:  gitwire.BlobRequest{Repo: "depot", SHA: "pas-un-sha", Path: ".ansibleproxy.yml"},
			veut: gitwire.CodeBadRequest,
		},
		{
			// Un répertoire existe et n'est pas un blob : ce n'est ni un chemin
			// absent, ni une panne.
			nom:  "chemin qui est un répertoire",
			req:  gitwire.BlobRequest{Repo: "depot", SHA: commit, Path: "inventories"},
			veut: gitwire.CodeBadRequest,
		},
		{
			nom:  "traversée de chemin",
			req:  gitwire.BlobRequest{Repo: "depot", SHA: commit, Path: "../evasion"},
			veut: gitwire.CodeBadRequest,
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp := appelerBlob(t, srv, c.req)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusOK {
				t.Fatalf("%s devrait échouer", c.nom)
			}
			var e gitwire.Error
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Fatalf("corps d'erreur illisible: %v", err)
			}
			if e.Code != c.veut {
				t.Errorf("code = %q, veut %q (statut %d)", e.Code, c.veut, resp.StatusCode)
			}
		})
	}
}

func TestBlobRefuseUnChampInconnu(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+gitwire.BlobPath,
		encoder(t, map[string]string{"repo": "depot", "sha": commit, "path": "x", "bidon": "y"}))
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+jetonDeTest)
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("un champ inconnu doit être refusé, statut %d", resp.StatusCode)
	}
}

// mkdirTest crée un répertoire de fixture. `os.MkdirAll` est appelé à travers
// cette indirection pour garder l'import d'`os` hors du fichier principal, qui
// n'en a pas d'autre usage.
func mkdirTest(chemin string) error { return os.MkdirAll(chemin, 0o750) }
