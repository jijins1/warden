package gitclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
)

// auditFake enregistre ce qu'il reçoit et rend ce qu'on lui a dit de rendre.
type auditFake struct {
	mu      sync.Mutex
	recues  []gitwire.AuditRefRequest
	entetes []string

	statut int
	corps  string
}

func (f *auditFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req gitwire.AuditRefRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	f.recues = append(f.recues, req)
	f.entetes = append(f.entetes, r.Header.Get("Authorization"))
	f.mu.Unlock()

	if f.statut != 0 && f.statut != http.StatusOK {
		w.WriteHeader(f.statut)
		_, _ = w.Write([]byte(f.corps))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(f.corps))
}

func (f *auditFake) appels() []gitwire.AuditRefRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitwire.AuditRefRequest(nil), f.recues...)
}

func monterAudit(t *testing.T, fake *auditFake) *gitclient.Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("POST "+gitwire.AuditRefPath, fake)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := gitclient.New(gitclient.Config{BaseURL: srv.URL, Token: jetonDeTest})
	if err != nil {
		t.Fatalf("gitclient.New: %v", err)
	}
	return c
}

// TestRecordAuditRefEnvoieLaRefLitterale : le client transmet ce que
// l'appelant a déjà décidé, rien de plus.
func TestRecordAuditRefEnvoieLaRefLitterale(t *testing.T) {
	corps, _ := json.Marshal(gitwire.AuditRefResponse{Created: true})
	fake := &auditFake{corps: string(corps)}
	c := monterAudit(t, fake)

	err := c.RecordAuditRef(context.Background(), core.AuditRef{
		Repo: "warden", Ref: "refs/audit/42/01JZQ0000000000000000P0001",
		CommitSHA: "1111111111111111111111111111111111111111",
	})
	if err != nil {
		t.Fatalf("RecordAuditRef: %v", err)
	}

	appels := fake.appels()
	if len(appels) != 1 {
		t.Fatalf("%d appel(s), attendu 1", len(appels))
	}
	got := appels[0]
	if got.Repo != "warden" || got.Ref != "refs/audit/42/01JZQ0000000000000000P0001" ||
		got.CommitSHA != "1111111111111111111111111111111111111111" {
		t.Errorf("requête transmise = %+v", got)
	}
	if fake.entetes[0] != "Bearer "+jetonDeTest {
		t.Errorf("en-tête d'autorisation = %q", fake.entetes[0])
	}
}

// TestRecordAuditRefConflitTraduit : un conflit se présente à l'appelant comme
// core.ErrAuditRefConflict, distinct de core.ErrArtifactGone et d'une erreur
// interne — c'est ce qui lui permet de répondre 409 plutôt que 500.
func TestRecordAuditRefConflitTraduit(t *testing.T) {
	corps, _ := json.Marshal(gitwire.Error{Code: gitwire.CodeRefExists, Message: "conflit"})
	fake := &auditFake{statut: http.StatusConflict, corps: string(corps)}
	c := monterAudit(t, fake)

	err := c.RecordAuditRef(context.Background(), core.AuditRef{
		Repo: "warden", Ref: "refs/audit/42/01JZQ0000000000000000P0002",
		CommitSHA: "1111111111111111111111111111111111111111",
	})
	if !errors.Is(err, core.ErrAuditRefConflict) {
		t.Fatalf("erreur = %v, attendu core.ErrAuditRefConflict", err)
	}
}

// TestRecordAuditRefDepotInconnuTraduit : même classement que les autres
// méthodes du client — dépôt inconnu se présente comme un artefact disparu.
func TestRecordAuditRefDepotInconnuTraduit(t *testing.T) {
	corps, _ := json.Marshal(gitwire.Error{Code: gitwire.CodeUnknownRepo, Message: "dépôt inconnu"})
	fake := &auditFake{statut: http.StatusNotFound, corps: string(corps)}
	c := monterAudit(t, fake)

	err := c.RecordAuditRef(context.Background(), core.AuditRef{
		Repo: "absent", Ref: "refs/audit/42/01JZQ0000000000000000P0003",
		CommitSHA: "1111111111111111111111111111111111111111",
	})
	if !errors.Is(err, core.ErrArtifactGone) {
		t.Fatalf("erreur = %v, attendu core.ErrArtifactGone", err)
	}
}

// TestRecordAuditRefChampsObligatoires : refusé avant tout appel réseau.
func TestRecordAuditRefChampsObligatoires(t *testing.T) {
	fake := &auditFake{corps: "{}"}
	c := monterAudit(t, fake)

	cas := []core.AuditRef{
		{Ref: "refs/audit/42/x", CommitSHA: "1111111111111111111111111111111111111111"},
		{Repo: "warden", CommitSHA: "1111111111111111111111111111111111111111"},
		{Repo: "warden", Ref: "refs/audit/42/x"},
	}
	for _, a := range cas {
		if err := c.RecordAuditRef(context.Background(), a); err == nil {
			t.Errorf("%+v accepté sans appel réseau", a)
		}
	}
	if len(fake.appels()) != 0 {
		t.Error("un appel réseau a eu lieu malgré des champs manquants")
	}
}

// TestRecordAuditRefJetonAbsentDeLErreur : un 401 ne doit jamais recopier le
// jeton, comme pour les autres méthodes du client.
func TestRecordAuditRefJetonAbsentDeLErreur(t *testing.T) {
	fake := &auditFake{statut: http.StatusUnauthorized, corps: ""}
	c := monterAudit(t, fake)

	err := c.RecordAuditRef(context.Background(), core.AuditRef{
		Repo: "warden", Ref: "refs/audit/42/01JZQ0000000000000000P0004",
		CommitSHA: "1111111111111111111111111111111111111111",
	})
	if err == nil {
		t.Fatal("erreur attendue")
	}
	if strings.Contains(err.Error(), jetonDeTest) {
		t.Errorf("le jeton apparaît dans l'erreur: %v", err)
	}
}
