// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Le parcours du code d'un run (ADR-0055) rend lisible TOUT le dépôt du commit
// audité, pas seulement ses lignes modifiées. La propriété qui compte d'abord
// est donc la même que pour le diff : qui ne voit pas le run ne voit rien, et
// ne peut même pas savoir que le run existe.
package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

// parcoureur double le rôle `git` : il rend ce qu'on lui dit et note ce qu'on
// lui demande.
type parcoureur struct {
	arbre   core.RepoTree
	contenu []byte
	err     error

	appels *[]string
}

func (p parcoureur) Tree(_ context.Context, run core.Run) (core.RepoTree, error) {
	*p.appels = append(*p.appels, "tree:"+run.AuditSHA)
	return p.arbre, p.err
}

func (p parcoureur) File(_ context.Context, run core.Run, chemin string) ([]byte, error) {
	*p.appels = append(*p.appels, "file:"+run.AuditSHA+":"+chemin)
	return p.contenu, p.err
}

const shaAudite = "1111111111111111111111111111111111111111"

// serveurParcours monte l'API sur un run d'alice (ID 7) ou d'autrui, avec un
// parcoureur ou sans (nil : rôle `git` absent).
func serveurParcours(t *testing.T, proprietaire int64, run func(*core.Run), p *parcoureur) *httptest.Server {
	t.Helper()

	m := nouveauMagasin(t)
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	r := core.Run{
		ID: 42, RunRef: "01JBQ7X4M2K9WPZ3N8VTYD6H5A", UserID: proprietaire,
		Repo: "infra", Environment: "prod", AuditSHA: shaAudite,
		State: core.RunSucceeded, StartedAt: time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC),
	}
	if run != nil {
		run(&r)
	}

	deps := api.Deps{Users: m, Auth: a, Runs: runeur{run: r}, Diffs: muet{}, Events: muet{}}
	if p != nil {
		deps.Files = *p
	}
	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0"}, deps)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h
}

func nouveauParcoureur() *parcoureur {
	return &parcoureur{appels: &[]string{}}
}

func cheminFichier(p string) string {
	return refDeTest + "/file?path=" + url.QueryEscape(p)
}

func TestLArbreDuRunListeSesFichiers(t *testing.T) {
	p := nouveauParcoureur()
	p.arbre = core.RepoTree{Files: []core.RepoFile{{Path: "roles/web/tasks/main.yml", Size: 120}}, Truncated: true}
	h := serveurParcours(t, 7, nil, p)

	resp := appel(t, h, http.MethodGet, refDeTest+"/tree", porteur(connexion(t, h)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200", resp.StatusCode)
	}
	var rep struct {
		Files []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
		Truncated bool `json:"truncated"`
	}
	if err := resp.Decoder().Decode(&rep); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if len(rep.Files) != 1 || rep.Files[0].Path != "roles/web/tasks/main.yml" || rep.Files[0].Size != 120 || !rep.Truncated {
		t.Errorf("= %+v", rep)
	}
	// Le commit lu est celui du run : le code qui a tourné.
	if (*p.appels)[0] != "tree:"+shaAudite {
		t.Errorf("appel = %v", *p.appels)
	}
}

func TestUnFichierDuRunEstServiBrut(t *testing.T) {
	p := nouveauParcoureur()
	p.contenu = []byte("<script>alert(1)</script>\n")
	h := serveurParcours(t, 7, nil, p)

	resp := appel(t, h, http.MethodGet, cheminFichier("templates/index.html.j2"), porteur(connexion(t, h)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200", resp.StatusCode)
	}
	// Du contenu de dépôt, donc pas de confiance : jamais interprétable en HTML.
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff absent")
	}
	if string(resp.Corps) != string(p.contenu) {
		t.Errorf("corps = %q", resp.Corps)
	}
	if (*p.appels)[0] != "file:"+shaAudite+":templates/index.html.j2" {
		t.Errorf("appel = %v", *p.appels)
	}
}

// LE test. Le code d'un run d'autrui est absent, pas interdit, et le rôle `git`
// n'est même pas sollicité.
func TestLeCodeDUnRunDAutruiEstIntrouvable(t *testing.T) {
	p := nouveauParcoureur()
	h := serveurParcours(t, 999, nil, p)
	jeton := connexion(t, h)

	for _, chemin := range []string{refDeTest + "/tree", cheminFichier("site.yml")} {
		resp := appel(t, h, http.MethodGet, chemin, porteur(jeton))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s : code = %d, attendu 404", chemin, resp.StatusCode)
		}
	}
	if len(*p.appels) != 0 {
		t.Errorf("le rôle git a été appelé : %v", *p.appels)
	}
}

func TestParcoursRefus(t *testing.T) {
	cas := []struct {
		nom    string
		run    func(*core.Run)
		err    error
		chemin string
		code   int
		motif  string
	}{
		{"code purgé", func(r *core.Run) { r.ArtifactPrunedAt = &time.Time{} }, nil, refDeTest + "/tree", http.StatusGone, "artifact_pruned"},
		{"dépôt inconnu", func(r *core.Run) { r.Repo = "" }, nil, refDeTest + "/tree", http.StatusGone, "repo_unknown"},
		{"objet absent", nil, core.ErrArtifactGone, refDeTest + "/tree", http.StatusGone, "object_missing"},
		{"chemin absent", nil, core.ErrPathMissing, cheminFichier("nulle-part.yml"), http.StatusNotFound, "path_missing"},
		{"trop gros", nil, core.ErrFileTooLarge, cheminFichier("gros.bin"), http.StatusRequestEntityTooLarge, "file_too_large"},
		{"délai", nil, context.DeadlineExceeded, refDeTest + "/tree", http.StatusGatewayTimeout, ""},
		{"panne", nil, core.ErrNotFound, refDeTest + "/tree", http.StatusBadGateway, ""},
		{"chemin vide", nil, nil, cheminFichier(""), http.StatusBadRequest, ""},
		{"chemin qui remonte", nil, nil, cheminFichier("../secrets"), http.StatusBadRequest, ""},
		{"chemin absolu", nil, nil, cheminFichier("/etc/passwd"), http.StatusBadRequest, ""},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			p := nouveauParcoureur()
			p.err = c.err
			h := serveurParcours(t, 7, c.run, p)

			resp := appel(t, h, http.MethodGet, c.chemin, porteur(connexion(t, h)))
			if resp.StatusCode != c.code {
				t.Fatalf("code = %d, attendu %d", resp.StatusCode, c.code)
			}
			if m := resp.Header.Get("X-Warden-Reason"); m != c.motif {
				t.Errorf("motif = %q, attendu %q", m, c.motif)
			}
		})
	}
}

// Sans rôle `git`, un 501 : une configuration valide, pas une panne.
func TestParcoursSansRoleGit(t *testing.T) {
	h := serveurParcours(t, 7, nil, nil)
	jeton := connexion(t, h)
	for _, chemin := range []string{refDeTest + "/tree", cheminFichier("site.yml")} {
		resp := appel(t, h, http.MethodGet, chemin, porteur(jeton))
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s : code = %d, attendu 501", chemin, resp.StatusCode)
		}
	}
}
