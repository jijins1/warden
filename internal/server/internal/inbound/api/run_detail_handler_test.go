// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// `GET /runs/{run_ref}` existe pour qu'un lien profond survive à un
// rechargement. La propriété qui compte ici n'est pas qu'il rende un run —
// c'est ce qu'il rend à qui n'a pas le droit de le voir.
package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

// runeur rend UN run et rien d'autre.
type runeur struct {
	muet
	run core.Run
}

func (r runeur) RunByRef(_ context.Context, ref string) (core.Run, error) {
	if ref != r.run.RunRef {
		return core.Run{}, core.ErrNotFound
	}
	return r.run, nil
}

// serveurAvecRun monte l'API sur un run dont on choisit le propriétaire.
func serveurAvecRun(t *testing.T, proprietaire int64) *httptest.Server {
	t.Helper()

	m := nouveauMagasin(t)
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	fin := time.Date(2026, 8, 27, 14, 42, 0, 0, time.UTC)
	r := runeur{run: core.Run{
		ID:               42, // séquentiel : il ne doit PAS ressortir
		RunRef:           "01JBQ7X4M2K9WPZ3N8VTYD6H5A",
		UserID:           proprietaire,
		Repo:             "infra",
		Environment:      "prod",
		Playbook:         "playbooks/site.yml",
		State:            core.RunInterrupted,
		TerminationCause: core.CauseUserInterrupt,
		ChangedCount:     3,
		StartedAt:        fin.Add(-time.Minute),
		EndedAt:          &fin,
	}}

	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0"}, api.Deps{
		Users: m, Auth: a,
		Runs: r, Diffs: muet{}, Events: muet{},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h
}

const refDeTest = "/runs/01JBQ7X4M2K9WPZ3N8VTYD6H5A"

// Le propriétaire du run — `magasin` donne l'ID 7 à alice.
func TestUnRunSeLitParSaReference(t *testing.T) {
	h := serveurAvecRun(t, 7)
	jeton := connexion(t, h)

	resp := appel(t, h, http.MethodGet, refDeTest, porteur(jeton))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200", resp.StatusCode)
	}

	var brut map[string]any
	if err := resp.Decoder().Decode(&brut); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}

	if brut["run_ref"] != "01JBQ7X4M2K9WPZ3N8VTYD6H5A" {
		t.Errorf("run_ref = %v", brut["run_ref"])
	}
	// La cause voyage AVEC l'état : un client ne doit jamais avoir à afficher
	// « interrompu » tout seul.
	if brut["termination_cause"] != "user_interrupt" {
		t.Errorf("termination_cause = %v", brut["termination_cause"])
	}
	// `kind` est normalisé plutôt que rendu vide.
	if brut["kind"] != "playbook" {
		t.Errorf("kind = %v, attendu playbook", brut["kind"])
	}
	// L'entier séquentiel ne sort pas : l'ADR-0021 a retiré l'énumération en
	// exposant des ULID, la republier ici la rendrait.
	for _, interdit := range []string{"ID", "id", "UserID", "user_id"} {
		if _, présent := brut[interdit]; présent {
			t.Errorf("le champ %q est exposé", interdit)
		}
	}
}

// LE test. Un run qu'on n'a pas le droit de voir est ABSENT, pas interdit :
// distinguer les deux ferait de l'endpoint un oracle des références qui
// existent.
func TestUnRunDAutruiEstIntrouvableEtNonInterdit(t *testing.T) {
	h := serveurAvecRun(t, 999) // appartient à quelqu'un d'autre
	jeton := connexion(t, h)

	resp := appel(t, h, http.MethodGet, refDeTest, porteur(jeton))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("code = %d, attendu 404 : l'endpoint distingue interdit d'absent",
			resp.StatusCode)
	}
}

func TestUnRunInconnuRepond404(t *testing.T) {
	h := serveurAvecRun(t, 7)
	jeton := connexion(t, h)

	resp := appel(t, h, http.MethodGet, "/runs/01JAAAAAAAAAAAAAAAAAAAAAAA", porteur(jeton))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("code = %d, attendu 404", resp.StatusCode)
	}
}
