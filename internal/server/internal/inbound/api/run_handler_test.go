// Étage 1 : la propriété testée ici est que le serveur ne croit PAS le client sur
// l'origine du commit d'audit. Elle ne dépend d'aucun stockage réel.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// gitFactice répond ce qu'un rôle `git` répondrait, et enregistre CE QU'ON LUI A
// DEMANDÉ — c'est cette dernière partie qui porte le test.
type gitFactice struct {
	refsDemandees []string
	parRef        map[string]string
	mainline      string
}

func (g *gitFactice) RefreshMirror(context.Context, string, string, string) (string, error) {
	return g.mainline, nil
}

func (g *gitFactice) ResolveRef(_ context.Context, _, ref string) (string, error) {
	g.refsDemandees = append(g.refsDemandees, ref)
	sha, ok := g.parRef[ref]
	if !ok {
		return "", errors.New("ref absente")
	}
	return sha, nil
}

type creatorFactice struct{ recus []core.NewRun }

func (c *creatorFactice) CreateRun(_ context.Context, n core.NewRun) (int64, error) {
	c.recus = append(c.recus, n)
	return int64(len(c.recus)), nil
}

type frappeurFactice struct{ prefixes []string }

func (f *frappeurFactice) IssuePushGrant(_ context.Context, _, refPrefix string, _ time.Time) (string, error) {
	f.prefixes = append(f.prefixes, refPrefix)
	return "jeton-" + refPrefix, nil
}

const (
	shaAlice = "1111111111111111111111111111111111111111"
	shaBob   = "2222222222222222222222222222222222222222"
	shaMain  = "3333333333333333333333333333333333333333"

	runAlice = "01JZQ000000000000000000ABC"
)

func monterEcriture(t *testing.T) (*Server, *gitFactice, *creatorFactice, *frappeurFactice) {
	t.Helper()

	g := &gitFactice{
		mainline: shaMain,
		parRef: map[string]string{
			// Le commit d'audit d'Alice, sous SON namespace (utilisateur 1).
			core.AuditRefFor(1, runAlice): shaAlice,
			// Celui de Bob, sous le sien (utilisateur 2).
			core.AuditRefFor(2, "01JZQ000000000000000000BCD"): shaBob,
		},
	}
	c := &creatorFactice{}
	f := &frappeurFactice{}

	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: c, Mirrors: g, Refs: g, Grants: f,
		// Alice (1) et Bob (2) ont tous deux droit à `prod` : ces tests portent
		// sur le commit d'audit et le démarrage, pas sur l'autorisation, qui a
		// son propre fichier. Sans ce port, ils échoueraient tous en 403 — le
		// refus par défaut faisant exactement son travail.
		Access: droitsFactices{
			"1/prod": {}, "2/prod": {},
		},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}
	return s, g, c, f
}

func posterRun(t *testing.T, s *Server, user *core.User, corps string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.HandleCreateRun(w, requete(t, http.MethodPost, "/runs", user, corps))
	return w
}

// TestLeCommitDAuditNestJamaisCeluiQueLeClientDeclare est LA raison d'être de ce
// fichier.
//
// La fuite fermée ici n'emprunte pas le protocole git mais le modèle de données :
// le diff du §6.5 compare `mainline_sha` à `audit_sha` sans se demander à qui ce
// dernier appartient, et `CanViewRun` autorise le propriétaire du run. Un
// utilisateur déclarant l'`audit_sha` d'un autre lisait donc le code de cet autre
// à travers le diff de SON PROPRE run.
//
// Le test le vérifie par ce que le serveur DEMANDE, pas seulement par ce qu'il
// enregistre : la seule ref résolue doit être celle d'Alice, composée par le
// serveur.
func TestLeCommitDAuditNestJamaisCeluiQueLeClientDeclare(t *testing.T) {
	s, g, c, _ := monterEcriture(t)

	// Le corps porte tout ce qu'un client malveillant voudrait glisser. Les champs
	// inconnus sont refusés par le décodeur, ce qui est déjà la moitié du travail.
	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"environment":"prod",
		"inventory":"inv",
		"playbook":"site.yml",
		"base_sha":"`+shaMain+`"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}

	if len(g.refsDemandees) != 1 {
		t.Fatalf("%d ref(s) résolue(s), attendu 1 : %v", len(g.refsDemandees), g.refsDemandees)
	}
	attendue := core.AuditRefFor(1, runAlice)
	if g.refsDemandees[0] != attendue {
		t.Errorf("ref résolue %q, attendue %q", g.refsDemandees[0], attendue)
	}

	if len(c.recus) != 1 {
		t.Fatalf("%d run(s) créé(s)", len(c.recus))
	}
	if c.recus[0].AuditSHA != shaAlice {
		t.Errorf("audit_sha enregistré %q, attendu celui de la ref d'alice %q",
			c.recus[0].AuditSHA, shaAlice)
	}
	if c.recus[0].AuditSHA == shaBob {
		t.Fatal("FUITE : le commit d'audit de bob a été enregistré comme celui d'alice")
	}
	// L'utilisateur vient de l'authentification, pas du corps.
	if c.recus[0].UserID != 1 {
		t.Errorf("user_id %d, attendu 1", c.recus[0].UserID)
	}
	// La principale est celle de la configuration serveur, figée à la création.
	if c.recus[0].MainlineSHA != shaMain || c.recus[0].MainlineRef != "refs/heads/main" {
		t.Errorf("principale non figée depuis la configuration : %+v", c.recus[0])
	}
}

// TestUnRunAdhocEstCreeSansPlaybook vérifie qu'un payload `kind: "adhoc"`
// traverse le handler sans jamais exiger de playbook, et enregistre sa cible.
func TestUnRunAdhocEstCreeSansPlaybook(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"environment":"prod",
		"inventory":"inv",
		"kind":"adhoc",
		"module":"shell",
		"module_args":"uptime",
		"host_pattern":"web",
		"base_sha":"`+shaMain+`"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 1 {
		t.Fatalf("%d run(s) créé(s)", len(c.recus))
	}
	recu := c.recus[0]
	if recu.Kind != core.RunAdhoc {
		t.Errorf("kind = %q, attendu %q", recu.Kind, core.RunAdhoc)
	}
	if recu.Playbook != "" {
		t.Errorf("playbook = %q, un ad-hoc n'en a pas", recu.Playbook)
	}
	if recu.AdhocTarget == nil {
		t.Fatal("cible ad-hoc absente")
	}
	if recu.AdhocTarget.Module != "shell" || recu.AdhocTarget.ModuleArgs != "uptime" ||
		recu.AdhocTarget.HostPattern != "web" {
		t.Errorf("cible ad-hoc mal reprise : %+v", recu.AdhocTarget)
	}
}

// TestUnAdhocEtUnPlaybookALaFoisSontRefuses : les deux disent quoi exécuter,
// et un client qui envoie les deux a fait une erreur qui doit être nommée
// plutôt que résolue en silence par l'un des deux champs.
func TestUnAdhocEtUnPlaybookALaFoisSontRefuses(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"kind":"adhoc",
		"module":"shell",
		"host_pattern":"web",
		"playbook":"site.yml",
		"base_sha":"`+shaMain+`"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400", w.Code)
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé malgré playbook et module à la fois")
	}
}

// TestUnAdhocSansModuleEstRefuse : la forme du module est vérifiée avant tout
// travail de dépôt, pour la même raison que les flags.
func TestUnAdhocSansModuleEstRefuse(t *testing.T) {
	s, g, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"kind":"adhoc",
		"host_pattern":"web",
		"base_sha":"`+shaMain+`"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400", w.Code)
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé sans module")
	}
	if len(g.refsDemandees) != 0 {
		t.Error("un module refusé ne doit pas coûter une résolution de ref")
	}
}

// TestUnGenreInconnuEstRefuse : l'enrôlement a son propre endpoint et ne
// passe jamais par celui-ci.
func TestUnGenreInconnuEstRefuse(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"kind":"enrollment",
		"base_sha":"`+shaMain+`"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400", w.Code)
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé avec un genre refusé par cet endpoint")
	}
}

// TestUnAuditSHAGlisseDansLeCorpsEstRefuse : le champ n'existe pas, et le
// décodeur le dit.
//
// `DisallowUnknownFields` transforme une tentative en 400 explicite plutôt qu'en
// silence. Un client qui essaie doit l'apprendre, pas croire que ça a marché.
func TestUnAuditSHAGlisseDansLeCorpsEstRefuse(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`","repo":"depot","audit_sha":"`+shaBob+`"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400", w.Code)
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé malgré le champ refusé")
	}
}

// TestUnRunSansPushEstRefuse : déclarer un run sans avoir poussé ne crée rien.
//
// C'est le corollaire direct de la résolution : si la ref n'existe pas, il n'y a
// pas d'`audit_sha` à inventer, et le produit refuse plutôt que d'enregistrer une
// ligne dont l'artefact est introuvable.
func TestUnRunSansPushEstRefuse(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"01JZQ000000000000000000ZZZ","repo":"depot"
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400", w.Code)
	}
	if len(c.recus) != 0 {
		t.Error("un run a été créé sans commit d'audit")
	}
}

// TestUnRunRefMalformeEstRefuseAvantToutUsage : le ULID compose un nom de ref et
// une URL, et il est tiré par la CLI — donc c'est de l'entrée non fiable.
func TestUnRunRefMalformeEstRefuseAvantToutUsage(t *testing.T) {
	s, g, _, _ := monterEcriture(t)

	for _, ref := range []string{
		"", "trop-court", "../../../etc/passwd",
		"01JZQ000000000000000000AB",  // 25 caractères
		"01JZQ000000000000000000ABU", // U est exclu du base32 de Crockford
	} {
		t.Run(ref, func(t *testing.T) {
			w := posterRun(t, s, administrateur(), `{"run_ref":"`+ref+`","repo":"depot"}`)
			if w.Code != http.StatusBadRequest {
				t.Errorf("statut %d, attendu 400", w.Code)
			}
		})
	}
	if len(g.refsDemandees) != 0 {
		t.Errorf("une ref a été composée à partir d'un identifiant refusé : %v", g.refsDemandees)
	}
}

// TestUnDepotNonConfigureLeDit : un 404 parlant plutôt qu'une panne.
func TestUnDepotNonConfigureLeDit(t *testing.T) {
	s, _, _, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"inconnu"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("statut %d, attendu 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "configuration") {
		t.Errorf("message peu actionnable : %s", w.Body.String())
	}
}

// TestLeJetonOuvreLeNamespaceDeSonPorteur : le préfixe est dérivé de l'identité,
// jamais demandé.
func TestLeJetonOuvreLeNamespaceDeSonPorteur(t *testing.T) {
	s, _, _, f := monterEcriture(t)

	w := httptest.NewRecorder()
	r := requete(t, http.MethodPost, "/repos/depot/push-grant", utilisateurNormal(), "")
	r.SetPathValue("repo", "depot")
	s.HandleIssuePushGrant(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("statut %d, attendu 200 : %s", w.Code, w.Body.String())
	}
	if len(f.prefixes) != 1 || f.prefixes[0] != core.AuditPrefixFor(2) {
		t.Fatalf("préfixe frappé %v, attendu %q", f.prefixes, core.AuditPrefixFor(2))
	}

	var out pushGrantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if out.ExpiresAt <= time.Now().Unix() {
		t.Error("le jeton naît expiré")
	}
	// Un jeton porteur n'a rien à faire dans un cache, ni intermédiaire ni local.
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
}

// --- démarrage du run -------------------------------------------------------

// lanceurFactice retient ce qu'on lui a demandé de démarrer.
type lanceurFactice struct {
	recus []core.Run
	err   error
}

func (l *lanceurFactice) Start(_ context.Context, run core.Run) (string, error) {
	if l.err != nil {
		return "", l.err
	}
	l.recus = append(l.recus, run)
	return "warden-run-" + strings.ToLower(run.RunRef), nil
}

func monterAvecLanceur(t *testing.T, lanceur core.RunLauncher) (*Server, *gitFactice) {
	t.Helper()
	s, g, _, _ := monterEcriture(t)
	s.deps.Starter = lanceur
	return s, g
}

// TestLeRunEstDemarreApresAvoirEteEnregistre.
//
// L'ordre porte une garantie : le run naît `interrupted` / `unknown` (§6.4), donc
// un échec au démarrage laisse une ligne qui dit la vérité. L'inverse — lancer
// puis enregistrer — laisserait un pod en vie que rien ne référence.
func TestLeRunEstDemarreApresAvoirEteEnregistre(t *testing.T) {
	l := &lanceurFactice{}
	s, _ := monterAvecLanceur(t, l)

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot","playbook":"site.yml"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}

	if len(l.recus) != 1 {
		t.Fatalf("%d run(s) démarré(s)", len(l.recus))
	}
	// Le run remis au lanceur porte l'audit_sha RÉSOLU par le serveur, pas celui
	// que le client aurait pu déclarer.
	if l.recus[0].AuditSHA != shaAlice {
		t.Errorf("commit remis au lanceur %q, attendu %q", l.recus[0].AuditSHA, shaAlice)
	}
	if l.recus[0].UserID != 1 || l.recus[0].Repo != "depot" {
		t.Errorf("run remis au lanceur inattendu : %+v", l.recus[0])
	}
	if l.recus[0].ID == 0 {
		t.Error("le run remis au lanceur n'a pas d'identifiant")
	}

	var out createRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if out.Pod == "" {
		t.Error("le nom du pod n'est pas rendu")
	}
}

// TestUnDemarrageEnEchecDitQueLeRunExisteQuandMeme.
//
// Sans cette précision, l'utilisateur croit que rien ne s'est passé et cherche un
// artefact qu'il ne sait pas nommer — alors que le run est enregistré, que son
// état dit qu'il n'a pas tourné, et que son commit d'audit est poussé.
func TestUnDemarrageEnEchecDitQueLeRunExisteQuandMeme(t *testing.T) {
	l := &lanceurFactice{err: errors.New("cluster injoignable")}
	s, _ := monterAvecLanceur(t, l)

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("statut %d, attendu 502", w.Code)
	}
	corps := w.Body.String()
	if !strings.Contains(corps, runAlice) {
		t.Errorf("le message ne nomme pas le run : %s", corps)
	}
	if !strings.Contains(corps, "recorded") {
		t.Errorf("le message ne dit pas que le run est enregistré : %s", corps)
	}
}

// TestSansLanceurLeRunEstEnregistreSansDemarrer : un déploiement sans droits
// Kubernetes reste utilisable pour tout le reste, ce qui est le mode de
// développement.
func TestSansLanceurLeRunEstEnregistreSansDemarrer(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 1 {
		t.Fatal("le run n'a pas été enregistré")
	}

	var out createRunResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Pod != "" {
		t.Errorf("un pod est annoncé sans lanceur : %q", out.Pod)
	}
}

// droitsFactices double le magasin des droits, indexé par "userID/environnement".
type droitsFactices map[string]struct{}

func (d droitsFactices) PeutAtteindre(_ context.Context, userID int64, env string) (bool, error) {
	_, ok := d[fmt.Sprintf("%d/%s", userID, env)]
	return ok, nil
}

func (d droitsFactices) ListGrants(context.Context, string) ([]core.EnvironmentGrant, error) {
	return nil, nil
}

func (d droitsFactices) Grant(context.Context, string, string, int64) (core.EnvironmentGrant, error) {
	return core.EnvironmentGrant{}, nil
}

func (d droitsFactices) Revoke(context.Context, string, string) error { return nil }

func (d droitsFactices) AppliquerLePlan(context.Context, core.GrantPlan, int64) error { return nil }

// --- l'autorisation par environnement (§10.2) ---

// LE test du contrôle. Sans droit, le run est refusé AVANT tout effet : rien
// n'est créé, et surtout la clé de l'environnement n'est jamais délivrée.
func TestUnRunSurUnEnvironnementInterditEstRefuse(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot",`+
		`"environment":"interdit","playbook":"site.yml","base_sha":"`+shaMain+`"}`)

	if w.Code != http.StatusForbidden {
		t.Fatalf("statut %d, attendu 403 : %s", w.Code, w.Body.String())
	}
	// Le message NOMME l'environnement : sans lui, l'utilisateur cherche une
	// faute de frappe au lieu de demander un accès.
	if !strings.Contains(w.Body.String(), "interdit") {
		t.Errorf("le message ne nomme pas l'environnement : %s", w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Errorf("un run a été créé malgré le refus : %+v", c.recus)
	}
}

// Le refus précède les EFFETS. Un run refusé ne doit rien avoir remué — ni
// rafraîchi le miroir, ni consigné de mot de passe d'élévation.
func TestUnRunRefuseNaRienRemue(t *testing.T) {
	s, g, _, _ := monterEcriture(t)

	posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot",`+
		`"environment":"interdit","playbook":"site.yml","base_sha":"`+shaMain+`"}`)

	// La ref d'audit n'a même pas été RÉSOLUE : le refus précède toute lecture
	// du dépôt, donc un run refusé n'a rien remué.
	if len(g.refsDemandees) != 0 {
		t.Errorf("le dépôt a été interrogé pour un run refusé : %v", g.refsDemandees)
	}
}

// Le droit d'Alice ne vaut pas pour Bob : le contrôle lit bien l'utilisateur
// AUTHENTIFIÉ, pas quelque chose du corps de la requête.
func TestLeDroitEstLuSurLAppelantAuthentifie(t *testing.T) {
	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: &creatorFactice{}, Mirrors: &gitFactice{mainline: shaMain},
		Refs: &gitFactice{parRef: map[string]string{
			core.AuditRefFor(2, "01JZQ000000000000000000BCD"): shaBob,
		}},
		Grants: &frappeurFactice{},
		Access: droitsFactices{"1/prod": {}}, // Alice seule
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	w := posterRun(t, s, utilisateurNormal(), `{"run_ref":"01JZQ000000000000000000BCD","repo":"depot",`+
		`"environment":"prod","playbook":"site.yml","base_sha":"`+shaMain+`"}`)

	if w.Code != http.StatusForbidden {
		t.Errorf("statut %d, attendu 403 : Bob a utilisé le droit d'Alice", w.Code)
	}
}

// gelFactice double le magasin des options par environnement (ADR-0044),
// indexé par nom : présent = gelé (`allowRun: false`).
type gelFactice map[string]struct{}

func (g gelFactice) ListOptions(context.Context) ([]core.EnvironmentOptions, error)  { return nil, nil }
func (g gelFactice) ReplaceOptions(context.Context, []core.EnvironmentOptions) error { return nil }
func (g gelFactice) RunAutorise(_ context.Context, env string) (bool, error) {
	_, gele := g[env]
	return !gele, nil
}

// LE test de l'ADR-0044 : le gel refuse un run MÊME quand les droits
// l'autorisent, parce que les deux contrôles sont volontairement
// indépendants — c'est ce qui permet de geler `prod` sans toucher à qui y a
// droit.
func TestUnRunSurUnEnvironnementGeleEstRefuseMalgreLeDroit(t *testing.T) {
	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: &creatorFactice{}, Mirrors: &gitFactice{mainline: shaMain},
		Refs: &gitFactice{parRef: map[string]string{
			core.AuditRefFor(1, runAlice): shaAlice,
		}},
		Grants:    &frappeurFactice{},
		Access:    droitsFactices{"1/prod": {}}, // Alice a le droit...
		Protected: gelFactice{"prod": {}},       // ...mais prod est gelé.
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}

	w := posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot",`+
		`"environment":"prod","playbook":"site.yml","base_sha":"`+shaMain+`"}`)

	if w.Code != http.StatusForbidden {
		t.Fatalf("statut %d, attendu 403 : prod est gelé : %s", w.Code, w.Body.String())
	}
}

// Un magasin de gel ABSENT n'empêche rien : c'est l'inverse d'Access, voir
// core.VerifierRunAutorise.
func TestSansMagasinDeGelLeRunPasseSiLeDroitLAutorise(t *testing.T) {
	s, _, c, _ := monterEcriture(t) // Deps.Protected n'est pas monté.

	posterRun(t, s, administrateur(), `{"run_ref":"`+runAlice+`","repo":"depot",`+
		`"environment":"prod","playbook":"site.yml","base_sha":"`+shaMain+`"}`)

	if len(c.recus) != 1 {
		t.Errorf("run non créé alors qu'aucun gel n'est monté : %+v", c.recus)
	}
}

// Les flags acceptés sont enregistrés TELS QUELS : l'allowlist refuse, elle
// n'ampute pas. Sans cette propriété, `run.flags` cesserait de dire ce que
// l'utilisateur a demandé, et l'audit mentirait de la manière la plus discrète
// qui soit.
func TestLesFlagsAcceptesSontEnregistres(t *testing.T) {
	s, _, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"environment":"prod",
		"playbook":"site.yml",
		"base_sha":"`+shaMain+`",
		"flags":["--check","--diff","--limit=web-01"]
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, attendu 201 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 1 {
		t.Fatalf("%d run(s) créé(s)", len(c.recus))
	}
	if got := strings.Join(c.recus[0].Flags, " "); got != "--check --diff --limit=web-01" {
		t.Errorf("flags enregistrés %q", got)
	}
}

// Un drapeau hors allowlist est refusé en 400, et le refus a lieu AVANT que le
// rôle `git` ne travaille : une erreur de frappe ne doit pas coûter un
// rafraîchissement de miroir.
func TestUnFlagHorsAllowlistEstRefuseAvantLeRoleGit(t *testing.T) {
	s, g, c, _ := monterEcriture(t)

	w := posterRun(t, s, administrateur(), `{
		"run_ref":"`+runAlice+`",
		"repo":"depot",
		"environment":"prod",
		"playbook":"site.yml",
		"base_sha":"`+shaMain+`",
		"flags":["--vault-password-file=/tmp/x"]
	}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	// Le message NOMME ce qui est accepté : sans ça, on va chercher la liste
	// dans un code source.
	if !strings.Contains(w.Body.String(), "--check") {
		t.Errorf("le refus ne nomme pas la liste acceptée : %s", w.Body.String())
	}
	if len(g.refsDemandees) != 0 {
		t.Errorf("le rôle git a travaillé pour un run refusé : %v", g.refsDemandees)
	}
	if len(c.recus) != 0 {
		t.Errorf("%d run(s) créé(s) malgré le refus", len(c.recus))
	}
}

// `flags` doit être un TABLEAU de chaînes. Le champ était un `json.RawMessage`
// recopié tel quel jusque sur l'argv : un objet, un nombre ou une liste de
// listes y passaient sans que rien ne regarde leur forme.
func TestUnFlagsQuiNestPasUnTableauEstRefuse(t *testing.T) {
	for _, brut := range []string{`{}`, `3`, `"--check"`, `[1]`, `[["--check"]]`} {
		t.Run(brut, func(t *testing.T) {
			s, _, _, _ := monterEcriture(t)
			w := posterRun(t, s, administrateur(), `{
				"run_ref":"`+runAlice+`",
				"repo":"depot",
				"environment":"prod",
				"playbook":"site.yml",
				"base_sha":"`+shaMain+`",
				"flags":`+brut+`
			}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
			}
		})
	}
}
