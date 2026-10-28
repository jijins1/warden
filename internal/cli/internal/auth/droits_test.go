package auth_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/auth"
	"github.com/jijins1/warden/internal/cli/internal/client"
)

// La colonne ACCORDÉ PAR distingue trois origines : une personne, la reprise de
// l'historique, et le document pour un groupe de l'annuaire (ADR-0053). Un
// groupe affiché « (historique) » laisserait croire qu'il existait avant les
// droits.
func TestLaListeDesDroitsNommeLOrigineDeChaqueDroit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/grants" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"subject":"alice","environment":"prod","granted_at":"2026-10-01T09:00:00Z","granted_by":"bob"},
			{"subject":"carol","environment":"prod","granted_at":"2026-08-16T09:00:00Z"},
			{"subject":"group:gg-warden-prod","environment":"prod","granted_at":"2026-10-01T09:00:00Z"}
		]`))
	}))
	t.Cleanup(srv.Close)

	api, err := client.New(client.Config{BaseURL: srv.URL, Token: "x"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	var sortie bytes.Buffer
	if err := auth.ListerDroits(context.Background(), api, "", &sortie); err != nil {
		t.Fatalf("ListerDroits: %v", err)
	}

	lignes := strings.Split(strings.TrimSpace(sortie.String()), "\n")
	if len(lignes) != 4 {
		t.Fatalf("sortie = %q, attendu un en-tête et trois droits", sortie.String())
	}
	for i, attendu := range []struct{ sujet, origine string }{
		{"alice", "bob"},
		{"carol", "(historique)"},
		{"group:gg-warden-prod", "(document)"},
	} {
		l := lignes[i+1]
		if !strings.HasPrefix(l, attendu.sujet) || !strings.HasSuffix(l, attendu.origine) {
			t.Errorf("ligne %q : attendu %s accordé par %s", l, attendu.sujet, attendu.origine)
		}
	}
}
