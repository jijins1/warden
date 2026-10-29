// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.

package gitclient_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
)

// monterRoute sert une seule route interne par un handler écrit à la main.
func monterRoute(t *testing.T, chemin string, h http.HandlerFunc) *gitclient.Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("POST "+chemin, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := gitclient.New(gitclient.Config{BaseURL: srv.URL, Token: jetonDeTest})
	if err != nil {
		t.Fatalf("gitclient.New: %v", err)
	}
	return c
}

func echec(code gitwire.Code) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code.HTTPStatus())
		_ = json.NewEncoder(w).Encode(gitwire.Error{Code: code, Message: "x"})
	}
}

// Le commit lu est l'audit_sha du run : le code qui a tourné.
func TestTreeLitLeCommitAudite(t *testing.T) {
	var recue gitwire.TreeRequest
	c := monterRoute(t, gitwire.TreePath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recue)
		_ = json.NewEncoder(w).Encode(gitwire.TreeResponse{
			Files:     []gitwire.TreeFile{{Path: "site.yml", Size: 12}},
			Truncated: true,
		})
	})

	run := runDeTest("01J")
	arbre, err := c.Tree(t.Context(), run)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if recue.Repo != run.Repo || recue.SHA != run.AuditSHA {
		t.Errorf("requête %+v, veut le dépôt et l'audit_sha du run", recue)
	}
	if len(arbre.Files) != 1 || arbre.Files[0] != (core.RepoFile{Path: "site.yml", Size: 12}) || !arbre.Truncated {
		t.Errorf("= %+v", arbre)
	}
}

func TestFileTraduitLesCodes(t *testing.T) {
	cas := []struct {
		code gitwire.Code
		veut error
	}{
		// Le même code que pour la configuration, lu par un autre appelant :
		// ici c'est un chemin absent, pas un projet sans configuration.
		{gitwire.CodeUnknownPath, core.ErrPathMissing},
		{gitwire.CodeTooLarge, core.ErrFileTooLarge},
		{gitwire.CodeUnknownObject, core.ErrArtifactGone},
	}
	for _, c := range cas {
		t.Run(string(c.code), func(t *testing.T) {
			client := monterRoute(t, gitwire.FilePath, echec(c.code))
			_, err := client.File(t.Context(), runDeTest("01J"), "site.yml")
			if !errors.Is(err, c.veut) {
				t.Errorf("= %v, veut %v", err, c.veut)
			}
			if errors.Is(err, core.ErrNoProjectConfig) {
				t.Error("un chemin absent du parcours ne doit pas se lire comme une configuration absente")
			}
		})
	}
}

func TestFileRendLeContenu(t *testing.T) {
	var recue gitwire.FileRequest
	c := monterRoute(t, gitwire.FilePath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recue)
		_, _ = w.Write([]byte("- hosts: all\n"))
	})

	run := runDeTest("01J")
	contenu, err := c.File(t.Context(), run, "site.yml")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if string(contenu) != "- hosts: all\n" {
		t.Errorf("= %q", contenu)
	}
	if recue != (gitwire.FileRequest{Repo: run.Repo, SHA: run.AuditSHA, Path: "site.yml"}) {
		t.Errorf("requête %+v", recue)
	}
}

// Sans dépôt connu, rien ne part vers le rôle `git`.
func TestParcoursSansDepot(t *testing.T) {
	c := monterRoute(t, gitwire.TreePath, func(http.ResponseWriter, *http.Request) {
		t.Error("le rôle git ne doit pas être appelé")
	})
	run := runDeTest("01J")
	run.Repo = ""
	if _, err := c.Tree(t.Context(), run); !errors.Is(err, core.ErrArtifactGone) {
		t.Errorf("Tree = %v", err)
	}
	if _, err := c.File(t.Context(), run, "x"); !errors.Is(err, core.ErrArtifactGone) {
		t.Errorf("File = %v", err)
	}
}

// `.warden.yml` d'abord, `.ansibleproxy.yml` seulement s'il manque (ADR-0056).
func TestReadProjectConfigReplieSurLAncienNom(t *testing.T) {
	cas := []struct {
		nom        string
		presents   map[string]string
		attendu    string
		demandes   []string
		sansConfig bool
	}{
		{"nouveau seul", map[string]string{".warden.yml": "n"}, "n", []string{".warden.yml"}, false},
		{"ancien seul", map[string]string{".ansibleproxy.yml": "a"}, "a",
			[]string{".warden.yml", ".ansibleproxy.yml"}, false},
		{"les deux", map[string]string{".warden.yml": "n", ".ansibleproxy.yml": "a"}, "n",
			[]string{".warden.yml"}, false},
		{"aucun", nil, "", []string{".warden.yml", ".ansibleproxy.yml"}, true},
	}

	for _, k := range cas {
		t.Run(k.nom, func(t *testing.T) {
			var demandes []string
			c := monterRoute(t, gitwire.BlobPath, func(w http.ResponseWriter, r *http.Request) {
				var req gitwire.BlobRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				demandes = append(demandes, req.Path)
				contenu, ok := k.presents[req.Path]
				if !ok {
					echec(gitwire.CodeUnknownPath)(w, r)
					return
				}
				_, _ = w.Write([]byte(contenu))
			})

			brut, err := c.ReadProjectConfig(t.Context(), "depot", "1111111111111111111111111111111111111111")
			if got := errors.Is(err, core.ErrNoProjectConfig); got != k.sansConfig {
				t.Fatalf("errors.Is(ErrNoProjectConfig) = %v (%v)", got, err)
			}
			if string(brut) != k.attendu {
				t.Errorf("contenu = %q, attendu %q", brut, k.attendu)
			}
			if fmt.Sprint(demandes) != fmt.Sprint(k.demandes) {
				t.Errorf("chemins demandés = %v, attendu %v", demandes, k.demandes)
			}
		})
	}
}
