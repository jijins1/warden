// Étage 1 : la propriété testée ici est que lancer un run depuis le navigateur
// tient les mêmes garanties d'ordre que POST /runs (ADR-0042) — en particulier
// qu'AUCUN travail n'a lieu avant que l'environnement nommé soit autorisé, et
// que la ref d'audit composée reste celle du serveur, jamais du client.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// auditFactice enregistre ce qu'on lui a demandé de pointer, et répond ce
// qu'on lui a dit de répondre.
type auditFactice struct {
	recus []core.AuditRef
	err   error
}

func (a *auditFactice) RecordAuditRef(_ context.Context, ar core.AuditRef) error {
	a.recus = append(a.recus, ar)
	return a.err
}

// monterLancement construit un serveur pour POST /repos/{repo}/runs, avec
// Alice (1) et Bob (2) autorisés sur `prod` seulement.
func monterLancement(t *testing.T) (*Server, *gitFactice, *creatorFactice, *auditFactice) {
	t.Helper()

	g := &gitFactice{mainline: shaMain}
	c := &creatorFactice{}
	au := &auditFactice{}

	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: c, Mirrors: g, Audits: au,
		Access: droitsFactices{"1/prod": {}, "2/prod": {}},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}
	return s, g, c, au
}

func posterLancement(t *testing.T, s *Server, user *core.User, repo, corps string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := requete(t, http.MethodPost, "/repos/"+repo+"/runs", user, corps)
	r.SetPathValue("repo", repo)
	s.HandleLaunchRun(w, r)
	return w
}

const corpsLancementValide = `{"environment":"prod","playbook":"site.yml"}`

// TestLancementChemainNominal : la principale devient audit_sha, base_sha ET
// mainline_sha à la fois — c'est le fait que documente HandleLaunchRun, pas un
// oubli.
func TestLancementCheminNominal(t *testing.T) {
	s, g, c, au := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot", corpsLancementValide)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}

	var out createRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if !core.ValidRunRef(out.RunRef) {
		t.Fatalf("run_ref %q n'est pas un ULID valide", out.RunRef)
	}
	if out.AuditSHA != g.mainline || out.MainlineSHA != g.mainline {
		t.Errorf("audit_sha=%q mainline_sha=%q, attendu %q pour les deux", out.AuditSHA, out.MainlineSHA, g.mainline)
	}

	if len(c.recus) != 1 {
		t.Fatalf("%d run(s) créé(s), attendu 1", len(c.recus))
	}
	n := c.recus[0]
	if n.Kind != core.RunPlaybook {
		t.Errorf("Kind = %q, attendu playbook", n.Kind)
	}
	if n.AuditSHA != g.mainline || n.BaseSHA != g.mainline || n.MainlineSHA != g.mainline {
		t.Errorf("les trois SHA ne coïncident pas : audit=%q base=%q mainline=%q",
			n.AuditSHA, n.BaseSHA, n.MainlineSHA)
	}

	// LE test de sécurité de cette fonctionnalité : la ref demandée au rôle
	// `git` est composée par le serveur à partir de l'identité authentifiée et
	// du run_ref RENDU, jamais d'une valeur du client — qui n'en a fourni
	// aucune.
	if len(au.recus) != 1 {
		t.Fatalf("%d ref(s) d'audit frappée(s), attendu 1", len(au.recus))
	}
	attendu := core.AuditRefFor(2, out.RunRef)
	if au.recus[0].Ref != attendu {
		t.Errorf("ref frappée %q, attendu %q", au.recus[0].Ref, attendu)
	}
	if au.recus[0].CommitSHA != g.mainline {
		t.Errorf("commit frappé %q, attendu %q", au.recus[0].CommitSHA, g.mainline)
	}
}

// TestLancementSansDroitNeRemueRien : le refus d'autorisation doit précéder
// TOUT effet — ni rafraîchissement de miroir, ni frappe de ref d'audit.
func TestLancementSansDroitNeRemueRien(t *testing.T) {
	s, g, c, au := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot", `{"environment":"interdit","playbook":"site.yml"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("statut %d, attendu 403 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé malgré le refus")
	}
	if len(au.recus) != 0 {
		t.Error("une ref d'audit a été frappée malgré le refus")
	}
	_ = g
}

// TestLancementEnvironnementObligatoire : refusé avant tout port, y compris la
// lecture du dépôt.
func TestLancementEnvironnementObligatoire(t *testing.T) {
	s, _, c, au := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot", `{"playbook":"site.yml"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 0 || len(au.recus) != 0 {
		t.Error("un effet a eu lieu malgré l'absence d'environnement")
	}
}

// TestLancementPlaybookObligatoire.
func TestLancementPlaybookObligatoire(t *testing.T) {
	s, _, _, _ := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot", `{"environment":"prod"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
}

// TestLancementDepotInconnu.
func TestLancementDepotInconnu(t *testing.T) {
	s, g, _, _ := monterLancement(t)
	_ = g

	w := posterLancement(t, s, utilisateurNormal(), "absent", corpsLancementValide)
	if w.Code != http.StatusNotFound {
		t.Fatalf("statut %d, attendu 404 : %s", w.Code, w.Body.String())
	}
}

// TestLancementRefuseUnKindDansLeCorps épingle le cut de portée (ADR-0042) : un
// run lancé depuis le navigateur est TOUJOURS un playbook. `DisallowUnknownFields`
// refuse le champ plutôt que de l'ignorer, ce qui fait de ce refus un contrat et
// non une habitude.
func TestLancementRefuseUnKindDansLeCorps(t *testing.T) {
	s, _, _, _ := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot",
		`{"environment":"prod","playbook":"site.yml","kind":"adhoc"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
}

// TestLancementFlagRefuseAvantLeRoleGit.
func TestLancementFlagRefuseAvantLeRoleGit(t *testing.T) {
	s, g, c, au := monterLancement(t)

	w := posterLancement(t, s, utilisateurNormal(), "depot",
		`{"environment":"prod","playbook":"site.yml","flags":["--inventaire-hostile"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 0 || len(au.recus) != 0 {
		t.Error("un effet a eu lieu malgré le flag refusé")
	}
	_ = g
}

// TestLancementConflitDeRefEstUnConflit : un identifiant réutilisé sur un autre
// commit est un 409, pas une panne.
func TestLancementConflitDeRefEstUnConflit(t *testing.T) {
	s, _, _, au := monterLancement(t)
	au.err = core.ErrAuditRefConflict

	w := posterLancement(t, s, utilisateurNormal(), "depot", corpsLancementValide)
	if w.Code != http.StatusConflict {
		t.Fatalf("statut %d, attendu 409 : %s", w.Code, w.Body.String())
	}
}

// TestLancementMiroirEnEchec.
func TestLancementMiroirEnEchec(t *testing.T) {
	s, _, c, au := monterLancement(t)
	// gitFactice ne sait pas échouer sur RefreshMirror directement ; on
	// substitue un faux dédié qui échoue toujours.
	echec := &gitEchecFactice{}
	s2, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: c, Mirrors: echec, Audits: au,
		Access: droitsFactices{"2/prod": {}},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	w := posterLancement(t, s2, utilisateurNormal(), "depot", corpsLancementValide)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("statut %d, attendu 502 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 0 || len(au.recus) != 0 {
		t.Error("un effet a eu lieu malgré l'échec du miroir")
	}
	_ = s
}

type gitEchecFactice struct{}

func (gitEchecFactice) RefreshMirror(context.Context, string, string, string) (string, error) {
	return "", errors.New("forge injoignable")
}

// TestLancementSansAudits : sans le port, l'endpoint répond 501 plutôt que de
// paniquer — le mode d'un déploiement CLI seule.
func TestLancementSansAudits(t *testing.T) {
	g := &gitFactice{mainline: shaMain}
	c := &creatorFactice{}
	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: c, Mirrors: g,
		Access: droitsFactices{"2/prod": {}},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	w := posterLancement(t, s, utilisateurNormal(), "depot", corpsLancementValide)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("statut %d, attendu 501 : %s", w.Code, w.Body.String())
	}
}

// TestLancementSansAuthentificationEstRefuse.
func TestLancementSansAuthentificationEstRefuse(t *testing.T) {
	s, _, _, _ := monterLancement(t)

	w := posterLancement(t, s, nil, "depot", corpsLancementValide)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("statut %d, attendu 401 : %s", w.Code, w.Body.String())
	}
}
