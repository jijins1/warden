// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.

package gitd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// appelerAudit envoie une requête authentifiée à AuditRefPath.
func appelerAudit(t *testing.T, srv *httptest.Server, req gitwire.AuditRefRequest) *http.Response {
	t.Helper()

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.AuditRefPath, encoder(t, req))
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

// TestAuditRefEcritUneFois est le chemin nominal, bout en bout à travers le
// service : la première écriture crée la ref sur le commit demandé.
func TestAuditRefEcritUneFois(t *testing.T) {
	srv, shas := monter(t)

	resp := appelerAudit(t, srv, gitwire.AuditRefRequest{
		Repo: "depot", Ref: "refs/audit/1/01JZQ0000000000000000P0001", CommitSHA: shas[0],
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		corps, _ := io.ReadAll(resp.Body)
		t.Fatalf("statut %d, attendu 200 (%s)", resp.StatusCode, corps)
	}
	var out gitwire.AuditRefResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if !out.Created {
		t.Error("Created = false sur la première écriture")
	}
}

// TestAuditRefRejeuIdempotent : la même requête, rejouée, réussit sans recréer
// ni déplacer la ref — c'est ce qui rend une déclaration de run interrompue
// rejouable sans risque (ADR-0025 appliqué côté serveur cette fois).
func TestAuditRefRejeuIdempotent(t *testing.T) {
	srv, shas := monter(t)
	req := gitwire.AuditRefRequest{
		Repo: "depot", Ref: "refs/audit/1/01JZQ0000000000000000P0002", CommitSHA: shas[0],
	}

	premier := appelerAudit(t, srv, req)
	defer func() { _ = premier.Body.Close() }()
	if premier.StatusCode != http.StatusOK {
		t.Fatalf("première écriture: statut %d", premier.StatusCode)
	}

	second := appelerAudit(t, srv, req)
	defer func() { _ = second.Body.Close() }()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("rejeu: statut %d, attendu 200", second.StatusCode)
	}
	var out gitwire.AuditRefResponse
	if err := json.NewDecoder(second.Body).Decode(&out); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if out.Created {
		t.Error("Created = true sur un rejeu : ce n'est pas une nouvelle écriture")
	}
}

// TestAuditRefConflitEstRefuse : le même identifiant de run réutilisé sur un
// AUTRE commit est un conflit, et la ref déjà écrite ne bouge pas.
func TestAuditRefConflitEstRefuse(t *testing.T) {
	srv, shas := monter(t)
	const ref = "refs/audit/1/01JZQ0000000000000000P0003"

	premier := appelerAudit(t, srv, gitwire.AuditRefRequest{Repo: "depot", Ref: ref, CommitSHA: shas[0]})
	defer func() { _ = premier.Body.Close() }()
	if premier.StatusCode != http.StatusOK {
		t.Fatalf("première écriture: statut %d", premier.StatusCode)
	}

	conflit := appelerAudit(t, srv, gitwire.AuditRefRequest{Repo: "depot", Ref: ref, CommitSHA: shas[1]})
	defer func() { _ = conflit.Body.Close() }()
	if conflit.StatusCode != http.StatusConflict {
		t.Fatalf("statut %d, attendu 409", conflit.StatusCode)
	}
	var e gitwire.Error
	if err := json.NewDecoder(conflit.Body).Decode(&e); err != nil {
		t.Fatalf("corps d'erreur illisible: %v", err)
	}
	if e.Code != gitwire.CodeRefExists {
		t.Errorf("code %q, attendu %q", e.Code, gitwire.CodeRefExists)
	}

	// La ref n'a pas bougé : rejouer la requête d'origine doit toujours réussir
	// en rejeu, jamais en conflit avec elle-même.
	rejeu := appelerAudit(t, srv, gitwire.AuditRefRequest{Repo: "depot", Ref: ref, CommitSHA: shas[0]})
	defer func() { _ = rejeu.Body.Close() }()
	if rejeu.StatusCode != http.StatusOK {
		t.Fatalf("la ref a bougé : statut %d au lieu de 200", rejeu.StatusCode)
	}
}

// TestAuditRefRefuseHorsPrefixe : ce chemin n'écrit QUE sous refs/audit/,
// jamais le pointeur de principale ni un espace de noms.
func TestAuditRefRefuseHorsPrefixe(t *testing.T) {
	srv, shas := monter(t)

	for _, ref := range []string{
		"refs/warden/mainline",
		"refs/namespaces/runs/refs/heads/run",
		"refs/heads/principale",
	} {
		t.Run(ref, func(t *testing.T) {
			resp := appelerAudit(t, srv, gitwire.AuditRefRequest{Repo: "depot", Ref: ref, CommitSHA: shas[0]})
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("statut %d pour %q, attendu 400", resp.StatusCode, ref)
			}
		})
	}
}

// TestAuditRefCodesDErreur couvre les refus qui ne tiennent pas au préfixe.
func TestAuditRefCodesDErreur(t *testing.T) {
	srv, shas := monter(t)

	cas := []struct {
		nom    string
		req    gitwire.AuditRefRequest
		statut int
		code   gitwire.Code
	}{
		{
			"dépôt inconnu",
			gitwire.AuditRefRequest{Repo: "absent", Ref: "refs/audit/1/01JZQ0000000000000000P0004", CommitSHA: shas[0]},
			http.StatusNotFound, gitwire.CodeUnknownRepo,
		},
		{
			"commit absent",
			gitwire.AuditRefRequest{Repo: "depot", Ref: "refs/audit/1/01JZQ0000000000000000P0005", CommitSHA: shaBidon},
			http.StatusNotFound, gitwire.CodeUnknownObject,
		},
		{
			"SHA malformé",
			gitwire.AuditRefRequest{Repo: "depot", Ref: "refs/audit/1/01JZQ0000000000000000P0006", CommitSHA: "pas-un-sha"},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
		{
			"ref malformée",
			gitwire.AuditRefRequest{Repo: "depot", Ref: "refs/audit/a b", CommitSHA: shas[0]},
			http.StatusBadRequest, gitwire.CodeBadRequest,
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			resp := appelerAudit(t, srv, c.req)
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
		})
	}
}

// TestAuditRefSansJetonRefuse : l'authentification du listener interne
// s'applique aussi à ce chemin.
func TestAuditRefSansJetonRefuse(t *testing.T) {
	srv, shas := monter(t)

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+gitwire.AuditRefPath, encoder(t, gitwire.AuditRefRequest{
			Repo: "depot", Ref: "refs/audit/1/01JZQ0000000000000000P0007", CommitSHA: shas[0],
		}))
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	r.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("statut %d, attendu 401", resp.StatusCode)
	}
}
