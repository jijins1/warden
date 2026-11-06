// Étage 1 : ce que l'adaptateur HTTP des crons doit tenir (ADR-0057) — le
// propriétaire vient de l'authentification et jamais du corps, le droit sur
// l'environnement est exigé à la création, et le cron d'autrui reste
// introuvable.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

type cronsFactices struct {
	crons map[int64]core.Planification
	recus []core.NouvellePlanification
}

func (c *cronsFactices) CreerPlanification(_ context.Context, n core.NouvellePlanification) (core.Planification, error) {
	c.recus = append(c.recus, n)
	id := int64(len(c.crons) + 1)
	p := core.Planification{
		ID: id, Nom: n.Nom, Repo: n.Repo, Playbook: n.Playbook, Environment: n.Environment,
		Expression: n.Expression, Fuseau: n.Fuseau, ProprietaireID: n.ProprietaireID,
		Active: true, ProchainDeclenche: n.ProchainDeclenche,
	}
	c.crons[id] = p
	return p, nil
}

func (c *cronsFactices) ListerPlanifications(_ context.Context, proprio int64) ([]core.Planification, error) {
	var out []core.Planification
	for _, p := range c.crons {
		if proprio == 0 || p.ProprietaireID == proprio {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *cronsFactices) PlanificationParID(_ context.Context, id int64) (core.Planification, error) {
	p, ok := c.crons[id]
	if !ok {
		return core.Planification{}, core.ErrNotFound
	}
	return p, nil
}

func (c *cronsFactices) BasculerPlanification(_ context.Context, id int64, active bool, prochain time.Time) error {
	p := c.crons[id]
	p.Active, p.ProchainDeclenche = active, prochain
	c.crons[id] = p
	return nil
}

func (c *cronsFactices) SupprimerPlanification(_ context.Context, id int64) error {
	delete(c.crons, id)
	return nil
}

func (c *cronsFactices) ReclamerEchue(context.Context, time.Time, func(core.Planification) time.Time) (core.Planification, bool, error) {
	return core.Planification{}, false, nil
}

func (c *cronsFactices) ConsignerDeclenchement(context.Context, int64, core.Declenchement) error {
	return nil
}

// monterCrons : Bob (2) a un droit sur `prod`, personne sur `staging`.
func monterCrons(t *testing.T, avecMagasin bool) (*Server, *cronsFactices) {
	t.Helper()
	c := &cronsFactices{crons: map[int64]core.Planification{}}
	deps := Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Access: droitsFactices{"2/prod": {}},
	}
	if avecMagasin {
		deps.Schedules = c
	}
	s, err := New(&Config{Addr: "127.0.0.1:0"}, deps)
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}
	return s, c
}

func posterCron(t *testing.T, s *Server, user *core.User, corps string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := requete(t, http.MethodPost, "/repos/depot/schedules", user, corps)
	r.SetPathValue("repo", "depot")
	s.HandleCreateSchedule(w, r)
	return w
}

const corpsCronValide = `{"name":"nightly","cron":"0 3 * * *","timezone":"Europe/Paris",` +
	`"environment":"prod","playbook":"backup.yml","flags":["--check"]}`

func TestCreerCronAuNomDeLAuthentifie(t *testing.T) {
	s, c := monterCrons(t, true)

	w := posterCron(t, s, utilisateurNormal(), corpsCronValide)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 1 || c.recus[0].ProprietaireID != 2 {
		t.Fatalf("cron enregistré %+v, attendu au nom de Bob (2)", c.recus)
	}
	var out scheduleJSON
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.NextFireAt == nil || out.Repo != "depot" || out.Cron != "0 3 * * *" {
		t.Errorf("réponse %+v", out)
	}
}

func TestCreerCronRefuse(t *testing.T) {
	cas := []struct {
		nom    string
		corps  string
		statut int
	}{
		// LE test de sécurité : un propriétaire désigné par le corps est refusé,
		// pas ignoré.
		{"propriétaire dans le corps", `{"name":"n","cron":"@daily","timezone":"UTC",` +
			`"environment":"prod","playbook":"x.yml","owner":"alice"}`, http.StatusBadRequest},
		{"secret dans le corps", `{"name":"n","cron":"@daily","timezone":"UTC",` +
			`"environment":"prod","playbook":"x.yml","become_password":"s"}`, http.StatusBadRequest},
		{"sans droit sur l'environnement", `{"name":"n","cron":"@daily","timezone":"UTC",` +
			`"environment":"staging","playbook":"x.yml"}`, http.StatusForbidden},
		{"expression invalide", `{"name":"n","cron":"0 3 * *","timezone":"UTC",` +
			`"environment":"prod","playbook":"x.yml"}`, http.StatusBadRequest},
		{"flags qui ne sont pas un tableau", `{"name":"n","cron":"@daily","timezone":"UTC",` +
			`"environment":"prod","playbook":"x.yml","flags":"--check"}`, http.StatusBadRequest},
	}
	for _, cc := range cas {
		t.Run(cc.nom, func(t *testing.T) {
			s, c := monterCrons(t, true)
			w := posterCron(t, s, utilisateurNormal(), cc.corps)
			if w.Code != cc.statut {
				t.Errorf("statut %d, attendu %d : %s", w.Code, cc.statut, w.Body.String())
			}
			if len(c.recus) != 0 {
				t.Error("un cron refusé a été enregistré")
			}
		})
	}
}

func TestCronsIndisponiblesSansMagasin(t *testing.T) {
	s, _ := monterCrons(t, false)
	if w := posterCron(t, s, utilisateurNormal(), corpsCronValide); w.Code != http.StatusNotImplemented {
		t.Errorf("statut %d, attendu 501", w.Code)
	}
}

func TestCronDAutruiIntrouvable(t *testing.T) {
	s, c := monterCrons(t, true)
	c.crons[7] = core.Planification{ID: 7, ProprietaireID: 99, Expression: "@daily", Fuseau: "UTC", Active: true}

	w := httptest.NewRecorder()
	r := requete(t, http.MethodDelete, "/schedules/7", utilisateurNormal(), "")
	r.SetPathValue("id", "7")
	s.HandleDeleteSchedule(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("statut %d, attendu 404", w.Code)
	}
	if _, ok := c.crons[7]; !ok {
		t.Error("le cron d'autrui a été supprimé")
	}

	// L'administrateur, lui, peut le couper.
	w = httptest.NewRecorder()
	r = requete(t, http.MethodPost, "/schedules/7/disable", administrateur(), "")
	r.SetPathValue("id", "7")
	s.HandleDisableSchedule(w, r)
	if w.Code != http.StatusOK || c.crons[7].Active {
		t.Errorf("statut %d, actif=%v : l'administrateur doit pouvoir couper le cron", w.Code, c.crons[7].Active)
	}
}
