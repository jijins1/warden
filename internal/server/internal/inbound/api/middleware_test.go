// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Le middleware d'authentification est l'endroit du produit où une erreur ne se
// voit pas et ouvre tout. Ces tests portent sur ce qu'il REFUSE, pas sur ce qu'il
// accepte : un jeton inconnu, un jeton d'un compte désactivé, un en-tête de
// développement quand le mode n'est pas posé, et surtout — un appel non
// authentifié qui atteindrait autre chose que la connexion.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

const motDePasseDeTest = "un mot de passe de test"

// magasin double À LA FOIS les comptes, les sessions et l'annuaire : les trois
// ports sont satisfaits par le même `store` en production, et les séparer ici
// n'apporterait que du câblage.
type magasin struct {
	empreinte string
	user      core.User

	sessions map[string]core.Session
	prochain int64
	revoquee map[int64]bool
}

func nouveauMagasin(t *testing.T) *magasin {
	t.Helper()
	e, err := core.Empreinte([]byte(motDePasseDeTest))
	if err != nil {
		t.Fatalf("Empreinte: %v", err)
	}
	return &magasin{
		empreinte: e,
		user: core.User{
			ID: 7, Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
		},
		sessions: map[string]core.Session{},
		revoquee: map[int64]bool{},
	}
}

func (m *magasin) EmpreinteDuCompte(_ context.Context, sujet string) (core.User, string, error) {
	if sujet != m.user.Subject {
		return core.User{}, "", core.ErrNotFound
	}
	return m.user, m.empreinte, nil
}

func (m *magasin) PoserMotDePasse(_ context.Context, _ int64, e string, _ int64) error {
	m.empreinte = e
	for id := range m.sessions {
		_ = id
	}
	m.sessions = map[string]core.Session{}
	return nil
}

func (m *magasin) CreateSession(_ context.Context, userID int64, hash []byte, expire time.Time) (int64, error) {
	m.prochain++
	m.sessions[string(hash)] = core.Session{ID: m.prochain, UserID: userID, ExpiresAt: expire}
	return m.prochain, nil
}

func (m *magasin) SessionByToken(_ context.Context, hash []byte) (core.Session, core.User, error) {
	s, ok := m.sessions[string(hash)]
	if !ok || m.revoquee[s.ID] || time.Now().After(s.ExpiresAt) {
		return core.Session{}, core.User{}, core.ErrNotFound
	}
	return s, m.user, nil
}

func (m *magasin) RevokeSession(_ context.Context, id int64) error {
	m.revoquee[id] = true
	return nil
}

func (m *magasin) TouchSession(context.Context, int64, time.Duration) error { return nil }

func (m *magasin) UserBySubject(_ context.Context, _ core.UserType, sujet string) (core.User, error) {
	if sujet != m.user.Subject {
		return core.User{}, core.ErrNotFound
	}
	return m.user, nil
}

// muet satisfait les ports que `api.New` exige sans qu'aucun test d'ici ne les
// atteigne : ces requêtes sont refusées AVANT le routeur, ce qui est justement
// ce qu'on vérifie. Un double qui panique dirait la même chose ; rendre
// ErrNotFound évite seulement qu'un test mal écrit passe pour une réussite.
type muet struct{}

func (muet) RunByRef(context.Context, string) (core.Run, error) {
	return core.Run{}, core.ErrNotFound
}

func (muet) ListRuns(context.Context, core.RunFilter) (core.RunListResult, error) {
	return core.RunListResult{}, core.ErrNotFound
}

func (muet) RunsByHost(context.Context, string, time.Time, time.Time) ([]core.RunListItem, error) {
	return nil, core.ErrNotFound
}

func (muet) PreviousRun(context.Context, core.Run) (core.Run, error) {
	return core.Run{}, core.ErrNotFound
}

func (muet) DiffForRun(context.Context, core.Run, int64) (json.RawMessage, error) {
	return nil, core.ErrNotFound
}

func (muet) Follow(context.Context, int64, int64) (core.EventStream, error) {
	return nil, core.ErrNotFound
}

// serveur monte l'API sur un `httptest`, avec ou sans le mode de développement.
func serveur(t *testing.T, modeDev bool) (*httptest.Server, *magasin) {
	t.Helper()

	m := nouveauMagasin(t)
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0", InsecureDevAuth: modeDev},
		api.Deps{
			Users: m, Auth: a,
			Runs: muet{}, Diffs: muet{}, Events: muet{},
		})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h, m
}

// connexion ouvre une session et rend le jeton.
func connexion(t *testing.T, h *httptest.Server) string {
	t.Helper()

	corps := strings.NewReader(`{"subject":"alice","password":"` + motDePasseDeTest + `"}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.URL+"/auth/login", corps)
	if err != nil {
		t.Fatalf("requête de connexion: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /auth/login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connexion refusée : %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if out.Token == "" {
		t.Fatal("aucun jeton rendu")
	}
	return out.Token
}

// appel exécute une requête avec les en-têtes donnés.
func appel(t *testing.T, h *httptest.Server, methode, chemin string, entetes map[string]string) reponse {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), methode, h.URL+chemin, nil)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	for k, v := range entetes {
		req.Header.Set(k, v)
	}
	// Pas de redirection suivie : `ServeMux` répond 301 sur un chemin à nettoyer,
	// et la suivre masquerait ce que le premier saut a réellement décidé.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", methode, chemin, err)
	}
	brut, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("%s %s: lecture du corps: %v", methode, chemin, err)
	}
	return reponse{StatusCode: resp.StatusCode, Header: resp.Header, Corps: brut}
}

// reponse est ce qu'un appel de test observe.
//
// Une VALEUR et non un `*http.Response`, et ce n'est pas cosmétique : le corps
// est déjà lu et la connexion déjà libérée quand elle arrive à l'appelant, donc
// il ne reste AUCUNE ressource à fermer. Rendre un `*http.Response` obligeait
// chaque appelant à s'en souvenir — ou à s'en remettre à un `t.Cleanup` dont
// l'ordre n'était visible nulle part, et qui gardait les connexions ouvertes
// jusqu'à la fin du test.
type reponse struct {
	StatusCode int
	Header     http.Header
	Corps      []byte
}

// Decoder lit le corps déjà en mémoire.
func (r reponse) Decoder() *json.Decoder { return json.NewDecoder(bytes.NewReader(r.Corps)) }

func porteur(jeton string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + jeton}
}

// --- le chemin nominal ---

func TestUnJetonOuvreLAcces(t *testing.T) {
	h, _ := serveur(t, false)
	jeton := connexion(t, h)

	resp := appel(t, h, http.MethodGet, "/auth/whoami", porteur(jeton))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami = %d, attendu 200", resp.StatusCode)
	}

	var id struct {
		Subject   string `json:"subject"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := resp.Decoder().Decode(&id); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if id.Subject != "alice" {
		t.Errorf("sujet = %q", id.Subject)
	}
	if id.ExpiresAt == "" {
		t.Error("aucune échéance rendue : l'appelant ne peut pas savoir quand se reconnecter")
	}
}

// --- ce que le middleware refuse ---

// SANS le mode de développement, aucun en-tête ne vaut plus rien. C'est la
// posture qui compte : un produit d'audit qui accepte « je suis alice » en
// silence est pire qu'un produit qui refuse de répondre.
func TestSansJetonNiModeDevToutEstRefuse(t *testing.T) {
	h, _ := serveur(t, false)

	cas := map[string]map[string]string{
		"aucun en-tête":         nil,
		"en-tête de dév seul":   {"X-User-Subject": "alice"},
		"porteur vide":          {"Authorization": "Bearer "},
		"porteur malformé":      {"Authorization": "Bearer pas-un-jeton"},
		"schéma inattendu":      {"Authorization": "Basic YWxpY2U6eA=="},
		"jeton inconnu mais OK": {"Authorization": "Bearer " + jetonBienForme()},
	}
	for nom, entetes := range cas {
		t.Run(nom, func(t *testing.T) {
			resp := appel(t, h, http.MethodGet, "/auth/whoami", entetes)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("code = %d, attendu 401", resp.StatusCode)
			}
			// La RFC 6750 veut que le serveur dise ce qu'il attend : sans cet
			// en-tête, un client automatisé ne peut que deviner.
			if resp.Header.Get("WWW-Authenticate") == "" {
				t.Error("WWW-Authenticate absent d'un 401")
			}
		})
	}
}

// LE test qui protège l'exception de routage : `/auth/login` est monté hors du
// middleware, et RIEN d'autre ne doit l'être.
func TestSeuleLaConnexionEchappeAuMiddleware(t *testing.T) {
	h, _ := serveur(t, false)

	for _, chemin := range []string{
		"/runs",
		"/admin/users",
		"/admin/hosts",
		"/auth/whoami",
		"/auth/logout",
		"/auth/password",
	} {
		t.Run(chemin, func(t *testing.T) {
			resp := appel(t, h, http.MethodGet, chemin, nil)
			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s a répondu 200 sans authentification", chemin)
			}
		})
	}
}

// Un chemin déguisé ne doit pas contourner le middleware. `ServeMux` nettoie et
// redirige plutôt que de servir — c'est précisément pourquoi l'exception est un
// ROUTAGE et non une comparaison de chaîne dans le middleware.
func TestUnCheminDeguiseNAtteintPasLesRoutesProtegees(t *testing.T) {
	h, _ := serveur(t, false)

	for _, chemin := range []string{
		"/auth/login/../runs",
		"/auth/login/../../admin/users",
		"//auth/login",
	} {
		t.Run(chemin, func(t *testing.T) {
			resp := appel(t, h, http.MethodPost, chemin, nil)
			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s a répondu 200 : le contournement a marché", chemin)
			}
		})
	}
}

// Un compte désactivé APRÈS l'ouverture de session : la désactivation coupe les
// sessions, mais la course existe et le compte doit primer.
func TestUnCompteDesactivePerdSonAcces(t *testing.T) {
	h, m := serveur(t, false)
	jeton := connexion(t, h)

	m.user.Disabled = true

	resp := appel(t, h, http.MethodGet, "/auth/whoami", porteur(jeton))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401 : un compte désactivé garde l'accès", resp.StatusCode)
	}
}

// Après `logout`, le jeton ne vaut plus rien. Sans cela, se déconnecter ne
// ferait qu'effacer un fichier local.
func TestUnJetonRevoqueNeVautPlusRien(t *testing.T) {
	h, _ := serveur(t, false)
	jeton := connexion(t, h)

	resp := appel(t, h, http.MethodPost, "/auth/logout", porteur(jeton))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, attendu 204", resp.StatusCode)
	}

	resp = appel(t, h, http.MethodGet, "/auth/whoami", porteur(jeton))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code = %d : le jeton révoqué fonctionne encore", resp.StatusCode)
	}
}

// --- le mode de développement ---

// L'en-tête reste accepté derrière l'opt-in, parce que c'est ce qui fait tourner
// les tests de bout en bout. Mais il ne doit pas ouvrir de SESSION : `whoami`
// doit dire que l'identité est affirmée et non vérifiée.
func TestLEnTeteDeDevNOuvrePasDeSession(t *testing.T) {
	h, _ := serveur(t, true)

	resp := appel(t, h, http.MethodGet, "/auth/whoami",
		map[string]string{"X-User-Subject": "alice"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200 en mode de développement", resp.StatusCode)
	}

	var id struct {
		Subject   string `json:"subject"`
		ExpiresAt string `json:"expires_at"`
	}
	_ = resp.Decoder().Decode(&id)
	if id.ExpiresAt != "" {
		t.Errorf("échéance = %q : l'en-tête de développement a ouvert une session", id.ExpiresAt)
	}
}

// Le jeton l'emporte, et un jeton INVALIDE ne retombe PAS sur l'en-tête. Sans
// cela, un client dont la session a expiré continuerait de travailler contre un
// serveur de développement sans jamais s'en apercevoir.
func TestUnJetonInvalideNeRetombePasSurLEnTeteDeDev(t *testing.T) {
	h, _ := serveur(t, true)

	resp := appel(t, h, http.MethodGet, "/auth/whoami", map[string]string{
		"Authorization":  "Bearer " + jetonBienForme(),
		"X-User-Subject": "alice",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401 : le jeton invalide est retombé sur l'en-tête",
			resp.StatusCode)
	}
}

// Même en mode de développement, un sujet inconnu ne doit RIEN provisionner : un
// upsert piloté par un en-tête non authentifié laisserait n'importe qui faire
// grossir la table des comptes.
func TestUnSujetInconnuEstRefuseMemeEnModeDev(t *testing.T) {
	h, _ := serveur(t, true)

	resp := appel(t, h, http.MethodGet, "/auth/whoami",
		map[string]string{"X-User-Subject": "personne"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401", resp.StatusCode)
	}
}

// --- la connexion elle-même ---

func TestUneConnexionRefuseeNeDitPasPourquoi(t *testing.T) {
	h, _ := serveur(t, false)

	for nom, corps := range map[string]string{
		"compte inconnu":       `{"subject":"personne","password":"` + motDePasseDeTest + `"}`,
		"mauvais mot de passe": `{"subject":"alice","password":"un autre mot de passe"}`,
	} {
		t.Run(nom, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				h.URL+"/auth/login", strings.NewReader(corps))
			if err != nil {
				t.Fatalf("requête: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.Client().Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("code = %d, attendu 401", resp.StatusCode)
			}
			// Le corps doit être le MÊME dans les deux cas : un message qui
			// distinguerait confirmerait l'existence d'un compte.
			brut, _ := io.ReadAll(resp.Body)
			if got := strings.TrimSpace(string(brut)); got != "invalid credentials" {
				t.Errorf("corps = %q : il renseigne sur la cause", got)
			}
		})
	}
}

// jetonBienForme rend un jeton de la bonne forme qui ne correspond à rien.
//
// La FORME est valide exprès : un jeton malformé serait refusé par
// `ParseSessionToken` avant toute recherche, et ne testerait donc pas le refus
// qui compte.
func jetonBienForme() string {
	t, err := core.NewSessionToken()
	if err != nil {
		panic(err)
	}
	return t.Reveal()
}
