package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// --- Doublures en mémoire ---------------------------------------------------

type plStore struct {
	crons   map[int64]*core.Planification
	suivant int64
	consig  map[int64]core.Declenchement
}

func nouveauPlStore() *plStore {
	return &plStore{crons: map[int64]*core.Planification{}, consig: map[int64]core.Declenchement{}}
}

func (s *plStore) CreerPlanification(_ context.Context, n core.NouvellePlanification) (core.Planification, error) {
	for _, p := range s.crons {
		if p.ProprietaireID == n.ProprietaireID && p.Nom == n.Nom {
			return core.Planification{}, core.ErrNomDePlanificationPris
		}
	}
	s.suivant++
	p := &core.Planification{
		ID: s.suivant, Nom: n.Nom, Repo: n.Repo, Playbook: n.Playbook,
		Environment: n.Environment, Inventory: n.Inventory, Flags: n.Flags,
		Expression: n.Expression, Fuseau: n.Fuseau, ProprietaireID: n.ProprietaireID,
		Active: true, ProchainDeclenche: n.ProchainDeclenche,
	}
	s.crons[p.ID] = p
	return *p, nil
}

func (s *plStore) ListerPlanifications(_ context.Context, proprio int64) ([]core.Planification, error) {
	var out []core.Planification
	for _, p := range s.crons {
		if proprio == 0 || p.ProprietaireID == proprio {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *plStore) PlanificationParID(_ context.Context, id int64) (core.Planification, error) {
	p, ok := s.crons[id]
	if !ok {
		return core.Planification{}, core.ErrNotFound
	}
	return *p, nil
}

func (s *plStore) BasculerPlanification(_ context.Context, id int64, active bool, prochain time.Time) error {
	p := s.crons[id]
	p.Active, p.ProchainDeclenche = active, prochain
	return nil
}

func (s *plStore) SupprimerPlanification(_ context.Context, id int64) error {
	delete(s.crons, id)
	return nil
}

func (s *plStore) ReclamerEchue(_ context.Context, maintenant time.Time,
	prochain func(core.Planification) time.Time,
) (core.Planification, bool, error) {
	for _, p := range s.crons {
		if p.Active && !p.ProchainDeclenche.IsZero() && !p.ProchainDeclenche.After(maintenant) {
			avant := *p
			p.ProchainDeclenche = prochain(avant)
			p.Active = !p.ProchainDeclenche.IsZero()
			return avant, true, nil
		}
	}
	return core.Planification{}, false, nil
}

func (s *plStore) ConsignerDeclenchement(_ context.Context, id int64, d core.Declenchement) error {
	s.consig[id] = d
	return nil
}

type plComptes map[int64]core.User

func (c plComptes) UserByID(_ context.Context, id int64) (core.User, error) {
	u, ok := c[id]
	if !ok {
		return core.User{}, core.ErrNotFound
	}
	return u, nil
}

// plDroits : userID -> environnements atteignables.
type plDroits map[int64][]string

func (d plDroits) PeutAtteindre(_ context.Context, userID int64, env string) (bool, error) {
	for _, e := range d[userID] {
		if e == env {
			return true, nil
		}
	}
	return false, nil
}
func (d plDroits) EnvironnementsAtteignables(context.Context, int64) ([]string, error) {
	return nil, nil
}
func (d plDroits) ListGrants(context.Context, string) ([]core.EnvironmentGrant, error) {
	return nil, nil
}
func (d plDroits) ListGroupGrants(context.Context) ([]core.GroupGrant, error) { return nil, nil }
func (d plDroits) Grant(context.Context, string, string, int64) (core.EnvironmentGrant, error) {
	return core.EnvironmentGrant{}, nil
}
func (d plDroits) Revoke(context.Context, string, string) error                 { return nil }
func (d plDroits) AppliquerLePlan(context.Context, core.GrantPlan, int64) error { return nil }

type plGel map[string]bool // environnement -> gelé

func (g plGel) ListOptions(context.Context) ([]core.EnvironmentOptions, error)  { return nil, nil }
func (g plGel) ReplaceOptions(context.Context, []core.EnvironmentOptions) error { return nil }
func (g plGel) RunAutorise(_ context.Context, env string) (bool, error)         { return !g[env], nil }

type plDepots struct{}

func (plDepots) GetRepo(_ context.Context, name string) (core.Repo, error) {
	if name != "infra" {
		return core.Repo{}, core.ErrNotFound
	}
	return core.Repo{Name: "infra", RemoteURL: "https://forge/infra.git", MainlineRef: "refs/heads/main"}, nil
}

const plSHA = "cccccccccccccccccccccccccccccccccccccccc"

type plMiroir struct{}

func (plMiroir) RefreshMirror(context.Context, string, string, string) (string, error) {
	return plSHA, nil
}

type plAudits struct{ refs []core.AuditRef }

func (a *plAudits) RecordAuditRef(_ context.Context, r core.AuditRef) error {
	a.refs = append(a.refs, r)
	return nil
}

// plProjet sert le `.warden.yml` de la principale : il déclare le `-e` que le
// cron demande, puisque l'allowlist vient du commit (ADR-0039).
type plProjet struct{}

func (plProjet) ReadProjectConfig(context.Context, string, string) ([]byte, error) {
	return []byte("extra_vars:\n  - retention\n"), nil
}

type plCreateur struct{ runs []core.NewRun }

func (c *plCreateur) CreateRun(_ context.Context, n core.NewRun) (int64, error) {
	c.runs = append(c.runs, n)
	return int64(len(c.runs)), nil
}

// --- Montage ----------------------------------------------------------------

const (
	alice = int64(1)
	bob   = int64(2)
	admin = int64(3)
)

type montage struct {
	store    *plStore
	comptes  plComptes
	droits   plDroits
	gel      plGel
	audits   *plAudits
	createur *plCreateur
	horloge  time.Time
}

func nouveauMontage(t *testing.T) *montage {
	t.Helper()
	return &montage{
		store: nouveauPlStore(),
		comptes: plComptes{
			alice: {ID: alice, Subject: "alice"},
			bob:   {ID: bob, Subject: "bob"},
			admin: {ID: admin, Subject: "root", IsAdmin: true},
		},
		droits:   plDroits{alice: {"prod"}},
		gel:      plGel{},
		audits:   &plAudits{},
		createur: &plCreateur{},
		horloge:  time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC),
	}
}

func (m *montage) planifications() core.Planifications {
	return core.Planifications{
		Store: m.store, Repos: plDepots{}, Access: m.droits,
		Horloge: func() time.Time { return m.horloge },
	}
}

func (m *montage) planificateur() core.Planificateur {
	return core.Planificateur{
		Store: m.store, Comptes: m.comptes, Grace: time.Hour,
		Horloge: func() time.Time { return m.horloge },
		Lanceur: core.Lanceur{
			Repos: plDepots{}, Mirrors: plMiroir{}, Audits: m.audits, ProjectConfig: plProjet{},
			Access: m.droits, Protected: m.gel, Creator: m.createur,
		},
	}
}

func (m *montage) creer(t *testing.T, qui int64) core.Planification {
	t.Helper()
	u := m.comptes[qui]
	p, err := m.planifications().Creer(context.Background(), &u, core.DemandeDePlanification{
		Nom: "nightly-backup", Repo: "infra", Playbook: "backup.yml", Environment: "prod",
		Flags:      []string{"--extra-vars=retention=7"},
		Expression: "0 3 * * *", Fuseau: "Europe/Paris",
	})
	if err != nil {
		t.Fatalf("Creer: %v", err)
	}
	return p
}

// --- Création -----------------------------------------------------------------

func TestCreerPoseLeProprietaireEtLEcheance(t *testing.T) {
	m := nouveauMontage(t)
	p := m.creer(t, alice)

	if p.ProprietaireID != alice {
		t.Errorf("propriétaire %d, attendu alice", p.ProprietaireID)
	}
	// 10 h UTC le 5 novembre : la prochaine 3 h à Paris est le 6 à 2 h UTC.
	if want := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC); !p.ProchainDeclenche.Equal(want) {
		t.Errorf("échéance %s, attendu %s", p.ProchainDeclenche.UTC(), want)
	}
}

func TestCreerRefuse(t *testing.T) {
	base := core.DemandeDePlanification{
		Nom: "n", Repo: "infra", Playbook: "site.yml", Environment: "prod",
		Expression: "0 3 * * *", Fuseau: "UTC",
	}
	cas := []struct {
		nom   string
		qui   int64
		modif func(*core.DemandeDePlanification)
		cause core.CauseDeRefus
	}{
		{"sans droit sur l'environnement", bob, func(*core.DemandeDePlanification) {}, core.RefusInterdit},
		{"expression invalide", alice, func(d *core.DemandeDePlanification) { d.Expression = "0 25 * * *" }, core.RefusForme},
		{"sans fuseau", alice, func(d *core.DemandeDePlanification) { d.Fuseau = "" }, core.RefusForme},
		{"sans environnement", alice, func(d *core.DemandeDePlanification) { d.Environment = "" }, core.RefusForme},
		{"nom en majuscules", alice, func(d *core.DemandeDePlanification) { d.Nom = "Nightly" }, core.RefusForme},
		{"flag hors allowlist", alice, func(d *core.DemandeDePlanification) { d.Flags = []string{"--become-user"} }, core.RefusForme},
		{"dépôt inconnu", alice, func(d *core.DemandeDePlanification) { d.Repo = "inconnu" }, core.RefusIntrouvable},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			m := nouveauMontage(t)
			d := base
			c.modif(&d)
			u := m.comptes[c.qui]
			_, err := m.planifications().Creer(context.Background(), &u, d)
			var r *core.RefusDeLancement
			if !errors.As(err, &r) || r.Cause != c.cause {
				t.Errorf("erreur %v, attendu la cause %d", err, c.cause)
			}
		})
	}
}

func TestCreerDeuxFoisLeMemeNomEstUnConflit(t *testing.T) {
	m := nouveauMontage(t)
	m.creer(t, alice)
	u := m.comptes[alice]
	_, err := m.planifications().Creer(context.Background(), &u, core.DemandeDePlanification{
		Nom: "nightly-backup", Repo: "infra", Playbook: "x.yml", Environment: "prod",
		Expression: "@daily", Fuseau: "UTC",
	})
	var r *core.RefusDeLancement
	if !errors.As(err, &r) || r.Cause != core.RefusConflit {
		t.Errorf("erreur %v, attendu un conflit", err)
	}
}

// Le cron d'autrui est introuvable, sauf pour un administrateur.
func TestGestionDuCronDAutrui(t *testing.T) {
	m := nouveauMontage(t)
	p := m.creer(t, alice)

	u := m.comptes[bob]
	if _, err := m.planifications().Basculer(context.Background(), &u, p.ID, false); err == nil {
		t.Fatal("bob a désactivé le cron d'alice")
	}
	if err := m.planifications().Supprimer(context.Background(), &u, p.ID); err == nil {
		t.Fatal("bob a supprimé le cron d'alice")
	}
	if l, _ := m.planifications().Lister(context.Background(), &u); len(l) != 0 {
		t.Errorf("bob voit %d cron(s) d'autrui", len(l))
	}

	a := m.comptes[admin]
	if _, err := m.planifications().Basculer(context.Background(), &a, p.ID, false); err != nil {
		t.Fatalf("l'administrateur n'a pas pu couper le cron : %v", err)
	}
	if m.store.crons[p.ID].Active {
		t.Error("le cron est resté actif")
	}
}

// --- Déclenchement ------------------------------------------------------------

func TestTourLanceAuNomDuProprietaire(t *testing.T) {
	m := nouveauMontage(t)
	p := m.creer(t, alice)
	m.horloge = p.ProchainDeclenche.Add(20 * time.Second)

	n, err := m.planificateur().Tour(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Tour = %d, %v", n, err)
	}
	if len(m.createur.runs) != 1 {
		t.Fatalf("%d run(s) créé(s), attendu 1", len(m.createur.runs))
	}
	run := m.createur.runs[0]
	if run.UserID != alice {
		t.Errorf("run au nom de %d, attendu alice", run.UserID)
	}
	if run.ScheduleID == nil || *run.ScheduleID != p.ID {
		t.Errorf("schedule_id %v, attendu %d", run.ScheduleID, p.ID)
	}
	if run.AuditSHA != plSHA || run.BaseSHA != plSHA || run.MainlineSHA != plSHA {
		t.Error("un run planifié doit exécuter la principale telle quelle (ADR-0042)")
	}
	if len(run.Flags) != 1 || run.Flags[0] != "--extra-vars=retention=7" {
		t.Errorf("flags %v", run.Flags)
	}
	if ref := m.audits.refs[0].Ref; !strings.HasPrefix(ref, core.AuditPrefixFor(alice)) {
		t.Errorf("ref d'audit %q hors du namespace d'alice", ref)
	}

	c := m.store.consig[p.ID]
	if c.RunRef != run.RunRef || c.Erreur != "" {
		t.Errorf("consignation %+v", c)
	}
	// L'échéance a avancé d'un jour.
	if got := m.store.crons[p.ID].ProchainDeclenche; !got.Equal(p.ProchainDeclenche.Add(24 * time.Hour)) {
		t.Errorf("échéance suivante %s", got)
	}

	// Un second tour à la même heure ne relance rien.
	if n, _ := m.planificateur().Tour(context.Background()); n != 0 || len(m.createur.runs) != 1 {
		t.Errorf("second tour : %d réclamé(s), %d run(s)", n, len(m.createur.runs))
	}
}

func TestTourAvantLEcheanceNeFaitRien(t *testing.T) {
	m := nouveauMontage(t)
	p := m.creer(t, alice)
	m.horloge = p.ProchainDeclenche.Add(-time.Minute)

	if n, _ := m.planificateur().Tour(context.Background()); n != 0 {
		t.Errorf("%d cron(s) réclamé(s) avant l'échéance", n)
	}
}

func TestTourRefuse(t *testing.T) {
	cas := []struct {
		nom      string
		preparer func(*montage)
		retard   time.Duration
		message  string
	}{
		{"retard au-delà de la grâce", func(*montage) {}, 3 * time.Hour, "missed"},
		{"propriétaire désactivé", func(m *montage) {
			u := m.comptes[alice]
			u.Disabled = true
			m.comptes[alice] = u
		}, 0, "disabled"},
		{"droit retiré depuis la création", func(m *montage) { m.droits[alice] = nil }, 0, "no access to environment prod"},
		{"environnement gelé", func(m *montage) { m.gel["prod"] = true }, 0, "frozen"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			m := nouveauMontage(t)
			p := m.creer(t, alice)
			c.preparer(m)
			m.horloge = p.ProchainDeclenche.Add(c.retard)

			if n, err := m.planificateur().Tour(context.Background()); n != 1 || err != nil {
				t.Fatalf("Tour = %d, %v", n, err)
			}
			if len(m.createur.runs) != 0 {
				t.Errorf("un run est parti malgré le refus")
			}
			if got := m.store.consig[p.ID].Erreur; !strings.Contains(got, c.message) {
				t.Errorf("last_error = %q, attendu %q", got, c.message)
			}
			// Le refus n'arrête pas le cron : l'échéance a avancé.
			if !m.store.crons[p.ID].ProchainDeclenche.After(m.horloge) {
				t.Error("l'échéance n'a pas avancé")
			}
		})
	}
}

// Réactiver repart de maintenant : rien n'est rattrapé.
func TestReactiverNeRattrapePas(t *testing.T) {
	m := nouveauMontage(t)
	p := m.creer(t, alice)
	u := m.comptes[alice]
	if _, err := m.planifications().Basculer(context.Background(), &u, p.ID, false); err != nil {
		t.Fatal(err)
	}

	m.horloge = m.horloge.Add(72 * time.Hour)
	r, err := m.planifications().Basculer(context.Background(), &u, p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ProchainDeclenche.After(m.horloge) {
		t.Errorf("échéance %s antérieure à maintenant %s", r.ProchainDeclenche, m.horloge)
	}
}
