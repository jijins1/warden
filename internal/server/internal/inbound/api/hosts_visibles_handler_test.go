// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// `GET /hosts` (ADR-0052) rend les machines que l'appelant peut voir. La
// propriété qui compte n'est pas qu'il rende des machines — c'est ce qu'il NE
// rend PAS : rien hors des droits vivants, administrateur ou pas, panne ou pas.
// Les tests passent par le routeur complet, middleware compris : la route
// cohabite avec `GET /hosts/{host}/runs`, et c'est le routeur qui en décide.

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

// droitsDeTest double le magasin des droits : les environnements atteignables
// de chaque compte, ou une panne.
type droitsDeTest struct {
	parCompte map[int64][]string
	err       error
}

func (d droitsDeTest) PeutAtteindre(_ context.Context, userID int64, env string) (bool, error) {
	return slices.Contains(d.parCompte[userID], env), d.err
}

func (d droitsDeTest) EnvironnementsAtteignables(_ context.Context, userID int64) ([]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.parCompte[userID], nil
}

func (droitsDeTest) ListGrants(context.Context, string) ([]core.EnvironmentGrant, error) {
	return nil, nil
}

func (droitsDeTest) ListGroupGrants(context.Context) ([]core.GroupGrant, error) {
	return nil, nil
}

func (droitsDeTest) Grant(context.Context, string, string, int64) (core.EnvironmentGrant, error) {
	return core.EnvironmentGrant{}, nil
}

func (droitsDeTest) Revoke(context.Context, string, string) error { return nil }

func (droitsDeTest) AppliquerLePlan(context.Context, core.GrantPlan, int64) error { return nil }

// inventaireDeTest tient des machines en mémoire, et retient le filtre que
// le cœur lui remet — c'est lui qui borne ce que la liste montre.
type inventaireDeTest struct {
	machines []core.ManagedHost
	filtres  []core.FiltreDeMachines
}

func (i *inventaireDeTest) SaveHost(context.Context, core.ManagedHost) error { return nil }
func (i *inventaireDeTest) HostByName(context.Context, string, string) (core.ManagedHost, error) {
	return core.ManagedHost{}, core.ErrNoHost
}
func (i *inventaireDeTest) ListHosts(context.Context, string) ([]core.ManagedHost, error) {
	return i.machines, nil
}
func (i *inventaireDeTest) ForgetHost(context.Context, string, string) error { return nil }

// ListerMachines applique la portée comme le ferait le magasin. La pagination
// elle-même est vérifiée sur PostgreSQL (étage 2) ; ici, un curseur se reconnaît
// seulement comme lisible ou non.
func (i *inventaireDeTest) ListerMachines(_ context.Context, f core.FiltreDeMachines) (core.PageDeMachines, error) {
	i.filtres = append(i.filtres, f)
	if f.Cursor == "illisible" {
		return core.PageDeMachines{}, fmt.Errorf("%w %q", core.ErrCurseurIllisible, f.Cursor)
	}
	page := core.PageDeMachines{Hosts: []core.ManagedHost{}}
	for _, h := range i.machines {
		if slices.Contains(f.Environnements, h.Environment) &&
			strings.Contains(strings.ToLower(h.Name), strings.ToLower(f.Nom)) {
			page.Hosts = append(page.Hosts, h)
		}
	}
	return page, nil
}

// serveurDeMachines monte l'API sur un inventaire de deux environnements.
// Alice — l'ID 7 du `magasin` — a un droit vivant sur `staging` seulement.
func serveurDeMachines(t *testing.T, admin bool, droits droitsDeTest) (func(string) reponse, *inventaireDeTest) {
	t.Helper()

	m := nouveauMagasin(t)
	m.user.IsAdmin = admin
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	enrole := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	inv := &inventaireDeTest{machines: []core.ManagedHost{
		{Name: "db-01", Environment: "prod", Address: "10.0.0.1", Port: 22, Login: "ansible", Mode: core.HostDeclared, EnrolledAt: enrole},
		{Name: "web-01", Environment: "staging", Address: "web-01.lan", Port: 2222, Login: "deploy",
			HostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple", Mode: core.HostAssisted, EnrolledAt: enrole},
	}}

	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0"}, api.Deps{
		Users: m, Auth: a,
		Runs: muet{}, Diffs: muet{}, Events: muet{},
		Hosts: inv, Access: droits,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	h := newServeur(t, srv)
	jeton := connexion(t, h)
	return func(chemin string) reponse {
		return appel(t, h, http.MethodGet, chemin, porteur(jeton))
	}, inv
}

func newServeur(t *testing.T, srv *api.Server) *httptest.Server {
	t.Helper()
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h
}

func droitsDAlice() droitsDeTest {
	return droitsDeTest{parCompte: map[int64][]string{7: {"staging"}, 8: {"prod"}}}
}

// machinesRendues lit une réponse de `GET /hosts`, en vérifiant sa forme.
func machinesRendues(t *testing.T, r reponse) ([]map[string]any, string) {
	t.Helper()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200 : %s", r.StatusCode, r.Corps)
	}
	var brut map[string]any
	if err := r.Decoder().Decode(&brut); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	// `hosts` est TOUJOURS un tableau : un `null` obligerait chaque client à
	// distinguer « rien » de « absent ».
	liste, ok := brut["hosts"].([]any)
	if !ok {
		t.Fatalf("hosts = %#v, attendu un tableau", brut["hosts"])
	}
	suite, ok := brut["next_cursor"].(string)
	if !ok {
		t.Fatalf("next_cursor = %#v, attendu une chaîne", brut["next_cursor"])
	}
	out := make([]map[string]any, 0, len(liste))
	for _, e := range liste {
		out = append(out, e.(map[string]any))
	}
	return out, suite
}

func TestLesMachinesVisiblesSontCellesDesDroitsVivants(t *testing.T) {
	get, _ := serveurDeMachines(t, false, droitsDAlice())

	machines, suite := machinesRendues(t, get("/hosts"))
	if len(machines) != 1 || machines[0]["host"] != "web-01" {
		t.Fatalf("machines = %v, attendu web-01 seule", machines)
	}
	if suite != "" {
		t.Errorf("next_cursor = %q sur la dernière page", suite)
	}

	// Le formalisme est celui de `GET /admin/hosts` : mêmes champs, snake_case.
	m := machines[0]
	attendu := map[string]any{
		"host":        "web-01",
		"environment": "staging",
		"address":     "web-01.lan",
		"port":        float64(2222),
		"login":       "deploy",
		"host_key":    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple",
		"mode":        "assisted",
		"enrolled_at": "2026-10-01T09:30:00Z",
	}
	for k, v := range attendu {
		if m[k] != v {
			t.Errorf("%s = %#v, attendu %#v", k, m[k], v)
		}
	}
}

// L'administration n'élargit pas l'accès : un administrateur sans droit ne
// voit rien ici. L'inventaire complet reste sous `/admin/hosts`.
func TestUnAdministrateurSansDroitNeVoitAucuneMachine(t *testing.T) {
	get, _ := serveurDeMachines(t, true, droitsDeTest{parCompte: map[int64][]string{8: {"prod", "staging"}}})

	if machines, _ := machinesRendues(t, get("/hosts")); len(machines) != 0 {
		t.Errorf("machines = %v, attendu aucune", machines)
	}
}

// Nommer un environnement hors de la portée rend une liste vide, et non un 403
// qui confirmerait qu'il existe.
func TestUnEnvironnementHorsPorteeRendUneListeVide(t *testing.T) {
	get, _ := serveurDeMachines(t, false, droitsDAlice())

	if machines, _ := machinesRendues(t, get("/hosts?env=prod")); len(machines) != 0 {
		t.Errorf("machines = %v, attendu aucune", machines)
	}
	if machines, _ := machinesRendues(t, get("/hosts?env=staging")); len(machines) != 1 {
		t.Errorf("machines = %v, attendu web-01", machines)
	}
}

func TestUnePanneDesDroitsNOuvreAucuneMachine(t *testing.T) {
	droits := droitsDAlice()
	droits.err = errors.New("base injoignable")
	get, _ := serveurDeMachines(t, true, droits)

	if machines, _ := machinesRendues(t, get("/hosts")); len(machines) != 0 {
		t.Errorf("machines = %v, attendu aucune", machines)
	}
}

// Les critères de l'appelant atteignent le magasin, et rien d'autre de la
// requête : la portée ne se lit pas dans l'URL.
func TestLesCriteresDeLaRequeteRetrecissentSeulement(t *testing.T) {
	get, inv := serveurDeMachines(t, false, droitsDAlice())

	machinesRendues(t, get("/hosts?q=WEB&limit=20&cursor=abc&environments=prod&environnements=prod"))
	if len(inv.filtres) != 1 {
		t.Fatalf("%d lectures, attendu 1", len(inv.filtres))
	}
	f := inv.filtres[0]
	if f.Nom != "WEB" || f.Limit != 20 || f.Cursor != "abc" || !slices.Equal(f.Environnements, []string{"staging"}) {
		t.Errorf("filtre = %+v", f)
	}
}

// Un curseur illisible est la faute de l'appelant, et le message le nomme —
// comme sous `GET /runs`.
func TestUnCurseurIllisibleEstUn400(t *testing.T) {
	get, _ := serveurDeMachines(t, false, droitsDAlice())

	r := get("/hosts?cursor=illisible")
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("code = %d, attendu 400", r.StatusCode)
	}
	if !strings.Contains(string(r.Corps), "cursor") {
		t.Errorf("le message ne nomme pas le curseur : %s", r.Corps)
	}
}

func TestSansSessionLaListeDesMachinesEstRefusee(t *testing.T) {
	m := nouveauMagasin(t)
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	inv := &inventaireDeTest{}
	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0"}, api.Deps{
		Users: m, Auth: a, Runs: muet{}, Diffs: muet{}, Events: muet{},
		Hosts: inv, Access: droitsDAlice(),
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	h := newServeur(t, srv)

	if r := appel(t, h, http.MethodGet, "/hosts", nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401", r.StatusCode)
	}
	if len(inv.filtres) != 0 {
		t.Error("l'inventaire a été lu sans appelant")
	}
}

// `GET /hosts` et `GET /hosts/{host}/runs` cohabitent : la vue d'audit par
// machine n'est pas avalée par la liste, ni l'inverse.
func TestLaListeNAvalePasLaVueParMachine(t *testing.T) {
	get, inv := serveurDeMachines(t, false, droitsDAlice())

	r := get("/hosts/web-01/runs")
	if len(inv.filtres) != 0 {
		t.Errorf("`/hosts/web-01/runs` a atteint la liste des machines")
	}
	if strings.Contains(string(r.Corps), `"hosts"`) {
		t.Errorf("`/hosts/web-01/runs` a rendu la liste : %s", r.Corps)
	}
}
