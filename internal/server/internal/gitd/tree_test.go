// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package gitd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

func appelerInterne(t *testing.T, srv *httptest.Server, chemin string, req any) *http.Response {
	t.Helper()

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+chemin, encoder(t, req))
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

func lireCode(t *testing.T, resp *http.Response) gitwire.Code {
	t.Helper()
	var e gitwire.Error
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("corps d'erreur illisible: %v", err)
	}
	return e.Code
}

func TestTreeListeLesFichiers(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	resp := appelerInterne(t, srv, gitwire.TreePath, gitwire.TreeRequest{Repo: "depot", SHA: commit})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d", resp.StatusCode)
	}

	var rep gitwire.TreeResponse
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if rep.Truncated {
		t.Error("une liste complète ne doit pas se dire coupée")
	}
	veut := []gitwire.TreeFile{
		{Path: ".ansibleproxy.yml", Size: int64(len(contenuConfig))},
		{Path: "inventories/hosts", Size: int64(len("[web]\n"))},
	}
	if len(rep.Files) != len(veut) || rep.Files[0] != veut[0] || rep.Files[1] != veut[1] {
		t.Errorf("= %+v, veut %+v", rep.Files, veut)
	}
}

// Un commit purgé se dit `unknown_object`, comme pour le diff : l'interface en
// fait « le code n'est plus là », pas une panne.
func TestTreeCommitAbsent(t *testing.T) {
	srv, _ := monterAvecConfig(t)

	resp := appelerInterne(t, srv, gitwire.TreePath, gitwire.TreeRequest{
		Repo: "depot", SHA: strings.Repeat("a", 40),
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound || lireCode(t, resp) != gitwire.CodeUnknownObject {
		t.Errorf("statut %d, veut 404 unknown_object", resp.StatusCode)
	}
}

func TestFileRendLeContenu(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	resp := appelerInterne(t, srv, gitwire.FilePath, gitwire.FileRequest{
		Repo: "depot", SHA: commit, Path: "inventories/hosts",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("le contenu d'un dépôt audité doit être servi avec nosniff")
	}
	corps, _ := io.ReadAll(resp.Body)
	if string(corps) != "[web]\n" {
		t.Errorf("= %q", corps)
	}
}

func TestFileCodesDErreur(t *testing.T) {
	srv, commit := monterAvecConfig(t)

	cas := []struct {
		nom    string
		chemin string
		statut int
		code   gitwire.Code
	}{
		{"chemin absent", "nulle-part.yml", http.StatusNotFound, gitwire.CodeUnknownPath},
		{"répertoire", "inventories", http.StatusBadRequest, gitwire.CodeBadRequest},
		{"sortie de l'arbre", "../x", http.StatusBadRequest, gitwire.CodeBadRequest},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp := appelerInterne(t, srv, gitwire.FilePath, gitwire.FileRequest{
				Repo: "depot", SHA: commit, Path: c.chemin,
			})
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != c.statut {
				t.Fatalf("statut %d, veut %d", resp.StatusCode, c.statut)
			}
			if code := lireCode(t, resp); code != c.code {
				t.Errorf("code %q, veut %q", code, c.code)
			}
		})
	}
}

// Les deux routes lisent un blob de la même manière, sous deux bornes : un
// fichier de plus de 64 Ko est servi par FilePath et refusé par BlobPath, et
// FilePath dit un dépassement comme tel.
func TestFileEtBlobGardentLeursBornes(t *testing.T) {
	racine := t.TempDir()
	travail := t.TempDir()
	lancer(t, travail, "init", "--initial-branch=principale")
	lancer(t, travail, "config", "user.email", "test@example.com")
	lancer(t, travail, "config", "user.name", "Test")
	ecrire(t, travail, "moyen.txt", strings.Repeat("x", gitwire.MaxBlobBytes+1))
	ecrire(t, travail, "gros.txt", strings.Repeat("x", gitwire.MaxFileBytes+1))
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

	statut := func(chemin string, req any) (int, *http.Response) {
		resp := appelerInterne(t, srv, chemin, req)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp.StatusCode, resp
	}

	if st, _ := statut(gitwire.FilePath, gitwire.FileRequest{Repo: "depot", SHA: commit, Path: "moyen.txt"}); st != http.StatusOK {
		t.Errorf("FilePath sur 64 Ko + 1 : statut %d, veut 200", st)
	}
	if st, _ := statut(gitwire.BlobPath, gitwire.BlobRequest{Repo: "depot", SHA: commit, Path: "moyen.txt"}); st != http.StatusBadRequest {
		t.Errorf("BlobPath sur 64 Ko + 1 : statut %d, veut 400 comme avant l'ADR-0055", st)
	}
	st, resp := statut(gitwire.FilePath, gitwire.FileRequest{Repo: "depot", SHA: commit, Path: "gros.txt"})
	if st != http.StatusRequestEntityTooLarge || lireCode(t, resp) != gitwire.CodeTooLarge {
		t.Errorf("FilePath sur 1 Mo + 1 : statut %d, veut 413 too_large", st)
	}
}
