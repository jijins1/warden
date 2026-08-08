// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// Le moteur git se teste à cet étage, sur des dépôts bare temporaires, comme
// l'ADR-0010 le prévoit explicitement.

package gitd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

const (
	jetonDeTest = "jeton-de-test-parfaitement-quelconque"
	// shaBidon est bien formé et ne désigne rien : c'est la forme qu'a un
	// artefact purgé pour l'appelant.
	shaBidon = "0123456789abcdef0123456789abcdef01234567"
)

// TestDiffRendLeDiff est le chemin nominal, bout en bout à travers le service.
func TestDiffRendLeDiff(t *testing.T) {
	srv, shas := monter(t)

	resp := appeler(t, srv, gitwire.DiffRequest{Repo: "depot", From: shas[0], To: shas[1]})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != gitwire.ContentTypeDiff {
		t.Errorf("Content-Type %q, attendu %q", ct, gitwire.ContentTypeDiff)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff absent : le diff vient du dépôt audité et n'est pas de confiance")
	}
	corps, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(corps), "deuxieme") {
		t.Errorf("le diff ne porte pas la ligne ajoutée:\n%s", corps)
	}
}

// TestDiffIdentiquesEstVide : deux fois le même commit est une réponse, pas une
// erreur — et un 200 au corps vide, pas un 204 ni un 404.
func TestDiffIdentiquesEstVide(t *testing.T) {
	srv, shas := monter(t)

	resp := appeler(t, srv, gitwire.DiffRequest{Repo: "depot", From: shas[0], To: shas[0]})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", resp.StatusCode)
	}
	corps, _ := io.ReadAll(resp.Body)
	if len(corps) != 0 {
		t.Errorf("corps attendu vide, obtenu %q", corps)
	}
}

// TestCodesDErreur couvre les trois refus que le contrat distingue.
func TestCodesDErreur(t *testing.T) {
	srv, shas := monter(t)

	cas := []struct {
		nom    string
		req    gitwire.DiffRequest
		statut int
		code   gitwire.Code
	}{
		{
			"dépôt inconnu",
			gitwire.DiffRequest{Repo: "absent", From: shas[0], To: shas[1]},
			http.StatusNotFound, gitwire.CodeUnknownRepo,
		},
		{
			// Le cas « code purgé » de l'ADR-0013, vu depuis le service.
			"objet absent",
			gitwire.DiffRequest{Repo: "depot", From: shas[0], To: shaBidon},
			http.StatusNotFound, gitwire.CodeUnknownObject,
		},
		{
			"SHA malformé",
			gitwire.DiffRequest{Repo: "depot", From: "pas-un-sha", To: shas[1]},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
		{
			"dépôt malformé",
			gitwire.DiffRequest{Repo: "../../etc", From: shas[0], To: shas[1]},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
		{
			"dépôt vide",
			gitwire.DiffRequest{Repo: "", From: shas[0], To: shas[1]},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
		{
			"dépôt composé",
			gitwire.DiffRequest{Repo: "a/b", From: shas[0], To: shas[1]},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp := appeler(t, srv, c.req)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != c.statut {
				t.Errorf("statut %d, attendu %d", resp.StatusCode, c.statut)
			}
			var e gitwire.Error
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Fatalf("corps d'erreur illisible: %v", err)
			}
			if e.Code != c.code {
				t.Errorf("code %q, attendu %q", e.Code, c.code)
			}
			// Le message est rédigé par le service : la stderr de git cite des
			// chemins du dépôt audité et n'a rien à faire dans une réponse.
			if strings.Contains(e.Message, "fatal:") || strings.Contains(e.Message, "/") {
				t.Errorf("le message d'erreur laisse fuir de la sortie git: %q", e.Message)
			}
		})
	}
}

// TestTraverseeDeChemin vérifie qu'aucun nom hostile n'atteint le système de
// fichiers, quelle que soit sa forme.
func TestTraverseeDeChemin(t *testing.T) {
	srv, shas := monter(t)

	for _, nom := range []string{"..", "../autre", "/etc/passwd", ".git", "a\\b", "dépôt"} {
		t.Run(nom, func(t *testing.T) {
			resp := appeler(t, srv, gitwire.DiffRequest{Repo: nom, From: shas[0], To: shas[1]})
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("statut %d pour le dépôt %q, attendu 400", resp.StatusCode, nom)
			}
		})
	}
}

// TestCorpsTropGrand : une DiffRequest fait quelques centaines d'octets.
func TestCorpsTropGrand(t *testing.T) {
	srv, _ := monter(t)

	enorme := bytes.NewReader([]byte(`{"repo":"` + strings.Repeat("a", 1<<20) + `"}`))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.DiffPath, enorme)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+jetonDeTest)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("statut %d, attendu 400", resp.StatusCode)
	}
}

// TestChampInconnuRefuse : un champ que le service ne connaît pas signale un
// désaccord de version entre les deux extrémités, pas une requête à honorer.
func TestChampInconnuRefuse(t *testing.T) {
	srv, _ := monter(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.DiffPath, strings.NewReader(`{"repo":"depot","inattendu":1}`))
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+jetonDeTest)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("statut %d, attendu 400", resp.StatusCode)
	}
}

// TestNewEchoueAFroid : mieux vaut refuser de démarrer que servir une API
// interne ouverte, ou répondre « dépôt inconnu » pour tous les dépôts.
func TestNewEchoueAFroid(t *testing.T) {
	racine := t.TempDir()

	if _, err := gitd.New(gitd.Config{Racine: racine}); err == nil {
		t.Error("service démarré sans jeton : l'API interne serait ouverte")
	}
	if _, err := gitd.New(gitd.Config{Jeton: jetonDeTest}); err == nil {
		t.Error("service démarré sans racine de dépôts")
	}
}

// monter construit le service devant un dépôt bare de deux commits.
func monter(t *testing.T) (*httptest.Server, []string) {
	t.Helper()

	racine := t.TempDir()
	shas := depotBare(t, filepath.Join(racine, "depot.git"))

	s, err := gitd.New(gitd.Config{Racine: racine, Jeton: jetonDeTest})
	if err != nil {
		t.Fatalf("gitd.New: %v", err)
	}

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, shas
}

// appeler envoie une requête authentifiée.
func appeler(t *testing.T, srv *httptest.Server, req gitwire.DiffRequest) *http.Response {
	t.Helper()

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.DiffPath, encoder(t, req))
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

func encoder(t *testing.T, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encodage: %v", err)
	}
	return bytes.NewReader(b)
}

// depotBare crée un dépôt bare de deux commits et rend leurs SHA.
func depotBare(t *testing.T, chemin string) []string {
	t.Helper()

	travail := t.TempDir()
	lancer(t, travail, "init", "--initial-branch=principale")
	lancer(t, travail, "config", "user.email", "test@example.com")
	lancer(t, travail, "config", "user.name", "Test")

	ecrire(t, travail, "fichier.txt", "premiere\n")
	lancer(t, travail, "add", "fichier.txt")
	lancer(t, travail, "commit", "-m", "un")
	premier := head(t, travail)

	ecrire(t, travail, "fichier.txt", "premiere\ndeuxieme\n")
	lancer(t, travail, "add", "fichier.txt")
	lancer(t, travail, "commit", "-m", "deux")
	second := head(t, travail)

	lancer(t, "", "clone", "--bare", travail, chemin)
	return []string{premier, second}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sortie, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(sortie))
}

func ecrire(t *testing.T, dir, nom, contenu string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s: %v", nom, err)
	}
}

func lancer(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if sortie, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, sortie)
	}
}
