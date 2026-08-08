// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// Le rôle `git` est doublé par un httptest.Server, sur le modèle de
// outbound/dispatch : des fausses extrémités écrites à la main, pas de
// bibliothèque de simulacres.

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
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
)

const jetonDeTest = "jeton-de-test"

// gitFake enregistre ce qu'il reçoit et rend ce qu'on lui a dit de rendre.
type gitFake struct {
	mu      sync.Mutex
	recues  []gitwire.DiffRequest
	entetes []string

	statut int
	corps  string
}

func (f *gitFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req gitwire.DiffRequest
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
	_, _ = w.Write([]byte(f.corps))
}

func (f *gitFake) appels() []gitwire.DiffRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitwire.DiffRequest(nil), f.recues...)
}

func monter(t *testing.T, fake *gitFake) *gitclient.Client {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("POST "+gitwire.DiffPath, fake)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := gitclient.New(gitclient.Config{BaseURL: srv.URL, Token: jetonDeTest})
	if err != nil {
		t.Fatalf("gitclient.New: %v", err)
	}
	return c
}

func runDeTest(ref string) core.Run {
	return core.Run{
		RunRef:      ref,
		Repo:        "warden",
		Environment: "prod",
		Inventory:   "inv",
		AuditSHA:    "1111111111111111111111111111111111111111",
		BaseSHA:     "2222222222222222222222222222222222222222",
		MainlineSHA: "3333333333333333333333333333333333333333",
	}
}

// TestDiffMainlineEnvoieLeSHAFige : la comparaison part du SHA figé dans le run,
// pas de la position de la branche aujourd'hui (ADR-0021).
func TestDiffMainlineEnvoieLeSHAFige(t *testing.T) {
	fake := &gitFake{corps: "diff attendu"}
	c := monter(t, fake)
	run := runDeTest("A")

	diff, err := c.DiffMainline(context.Background(), run)
	if err != nil {
		t.Fatalf("DiffMainline: %v", err)
	}
	if diff != "diff attendu" {
		t.Errorf("diff = %q", diff)
	}

	appels := fake.appels()
	if len(appels) != 1 {
		t.Fatalf("%d appels, attendu 1", len(appels))
	}
	if appels[0].From != run.MainlineSHA || appels[0].To != run.AuditSHA {
		t.Errorf("envoyé %s -> %s, attendu %s -> %s",
			appels[0].From, appels[0].To, run.MainlineSHA, run.AuditSHA)
	}
	if appels[0].Repo != run.Repo {
		t.Errorf("dépôt %q, attendu %q", appels[0].Repo, run.Repo)
	}
}

// TestDiffPreviousCompareLesDeuxAuditSHA est LE test de non-régression de la
// sémantique corrigée du §6.5.
//
// La version précédente comparait base_sha -> audit_sha, ce qui répondait à
// « qu'a modifié l'utilisateur en local » — déjà couvert par vs=mainline — au
// lieu de « qu'est-ce qui a changé depuis que ça marchait ».
func TestDiffPreviousCompareLesDeuxAuditSHA(t *testing.T) {
	fake := &gitFake{corps: "d"}
	c := monter(t, fake)

	run := runDeTest("B")
	precedent := runDeTest("A")
	precedent.AuditSHA = "4444444444444444444444444444444444444444"

	if _, err := c.DiffPrevious(context.Background(), run, precedent); err != nil {
		t.Fatalf("DiffPrevious: %v", err)
	}

	appels := fake.appels()
	if len(appels) != 1 {
		t.Fatalf("%d appels, attendu 1", len(appels))
	}
	if appels[0].From != precedent.AuditSHA {
		t.Errorf("from = %s, attendu l'audit_sha du run précédent %s",
			appels[0].From, precedent.AuditSHA)
	}
	if appels[0].To != run.AuditSHA {
		t.Errorf("to = %s, attendu %s", appels[0].To, run.AuditSHA)
	}
	// La borne dure : base_sha ne doit apparaître nulle part.
	if appels[0].From == run.BaseSHA || appels[0].To == run.BaseSHA {
		t.Error("base_sha utilisé : c'est la sémantique que cette tranche corrige")
	}
}

// TestJetonEnEntete : le jeton voyage en en-tête et nulle part ailleurs.
func TestJetonEnEntete(t *testing.T) {
	fake := &gitFake{corps: "d"}
	c := monter(t, fake)

	if _, err := c.DiffMainline(context.Background(), runDeTest("A")); err != nil {
		t.Fatalf("DiffMainline: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.entetes[0] != "Bearer "+jetonDeTest {
		t.Errorf("Authorization = %q", fake.entetes[0])
	}
}

// TestDepotVideNAppellePas : un run antérieur à la migration 00004 n'a pas de
// dépôt. Inutile d'aller le demander au rôle `git`.
func TestDepotVideNAppellePas(t *testing.T) {
	fake := &gitFake{corps: "d"}
	c := monter(t, fake)

	run := runDeTest("A")
	run.Repo = ""

	_, err := c.DiffMainline(context.Background(), run)
	if !errors.Is(err, core.ErrArtifactGone) {
		t.Errorf("erreur = %v, attendu ErrArtifactGone", err)
	}
	if n := len(fake.appels()); n != 0 {
		t.Errorf("%d appels alors qu'aucun n'était nécessaire", n)
	}
}

// TestDepotsDifferentsRefuses : deux dépôts n'ont aucun ancêtre commun.
func TestDepotsDifferentsRefuses(t *testing.T) {
	fake := &gitFake{corps: "d"}
	c := monter(t, fake)

	run := runDeTest("B")
	precedent := runDeTest("A")
	precedent.Repo = "autre"

	_, err := c.DiffPrevious(context.Background(), run, precedent)
	if !errors.Is(err, core.ErrArtifactGone) {
		t.Errorf("erreur = %v, attendu ErrArtifactGone", err)
	}
	if n := len(fake.appels()); n != 0 {
		t.Errorf("%d appels alors que l'incohérence est locale", n)
	}
}

// TestCodesTraduits : les deux absences se présentent à l'audit comme « le code
// n'est plus là », et sont donc reconnaissables par errors.Is.
func TestCodesTraduits(t *testing.T) {
	cas := []struct {
		nom     string
		statut  int
		code    gitwire.Code
		artefat bool
	}{
		{"dépôt inconnu", http.StatusNotFound, gitwire.CodeUnknownRepo, true},
		{"objet absent", http.StatusNotFound, gitwire.CodeUnknownObject, true},
		{"requête refusée", http.StatusBadRequest, gitwire.CodeBadRequest, false},
		{"panne du moteur", http.StatusInternalServerError, gitwire.CodeInternal, false},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			corps, _ := json.Marshal(gitwire.Error{Code: c.code, Message: "msg"})
			fake := &gitFake{statut: c.statut, corps: string(corps)}
			cl := monter(t, fake)

			_, err := cl.DiffMainline(context.Background(), runDeTest("A"))
			if err == nil {
				t.Fatal("erreur attendue")
			}
			if got := errors.Is(err, core.ErrArtifactGone); got != c.artefat {
				t.Errorf("errors.Is(ErrArtifactGone) = %v, attendu %v (%v)", got, c.artefat, err)
			}
		})
	}
}

// TestCorpsIllisible : un rôle `git` qui répond du HTML ne doit pas faire
// paniquer le client, ni passer pour un succès.
func TestCorpsIllisible(t *testing.T) {
	fake := &gitFake{statut: http.StatusBadGateway, corps: "<html>proxy</html>"}
	c := monter(t, fake)

	if _, err := c.DiffMainline(context.Background(), runDeTest("A")); err == nil {
		t.Error("réponse non-JSON acceptée")
	}
}

// TestJetonAbsentDeLErreur : un 401 ne doit jamais recopier le jeton.
func TestJetonAbsentDeLErreur(t *testing.T) {
	fake := &gitFake{statut: http.StatusUnauthorized, corps: ""}
	c := monter(t, fake)

	_, err := c.DiffMainline(context.Background(), runDeTest("A"))
	if err == nil {
		t.Fatal("erreur attendue")
	}
	if strings.Contains(err.Error(), jetonDeTest) {
		t.Errorf("le jeton apparaît dans l'erreur: %v", err)
	}
}

// TestContexteAnnule : l'annulation remonte telle quelle, pour que l'appelant
// puisse répondre 504 plutôt que 502.
func TestContexteAnnule(t *testing.T) {
	lent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		// Repli court : le client doit rendre la main sur SON délai (50 ms), et
		// l'étage 1 doit rester rapide (ADR-0010).
		case <-time.After(300 * time.Millisecond):
		}
	})
	srv := httptest.NewServer(lent)
	t.Cleanup(srv.Close)

	c, err := gitclient.New(gitclient.Config{BaseURL: srv.URL, Token: jetonDeTest})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, annuler := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer annuler()

	_, err = c.DiffMainline(ctx, runDeTest("A"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("erreur = %v, attendu DeadlineExceeded", err)
	}
}

// TestNewRefuseUneConfigurationInutilisable.
func TestNewRefuseUneConfigurationInutilisable(t *testing.T) {
	if _, err := gitclient.New(gitclient.Config{BaseURL: "http://x"}); err == nil {
		t.Error("client construit sans jeton")
	}
	if _, err := gitclient.New(gitclient.Config{BaseURL: "pas-une-url", Token: "t"}); err == nil {
		t.Error("client construit sur une adresse sans schéma ni hôte")
	}
}
