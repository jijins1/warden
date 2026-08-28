// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// La surface navigateur (ADR-0035). Trois propriétés y sont load-bearing, et
// aucune ne se voit à l'œil nu quand elle casse :
//
//  1. l'allowlist d'origines est UNE seule liste, consommée par le CORS ET par
//     le WebSocket. Durcir l'un en oubliant l'autre laisse un CSWSH ouvert ;
//  2. le cookie n'ouvre l'écriture qu'accompagné d'un en-tête que seul du JS
//     de même origine peut poser ;
//  3. le porteur continue de l'emporter, pour que la CLI ne change pas de
//     comportement parce qu'un navigateur est passé par là.
package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

const origineUI = "https://warden-ui.example.com"

// reponse est ce qu'on garde d'une réponse APRÈS l'avoir refermée.
//
// Une valeur, et non un `*http.Response` : rendre le pointeur oblige chaque
// appelant à fermer le corps, et l'oubli ne se voit pas. Fermer ici et ne rendre
// que ce qui se lit supprime la question plutôt que de la déplacer.
type reponse struct {
	code    int
	entetes http.Header
	cookies []*http.Cookie
	corps   string
}

func (r reponse) cookie(nom string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == nom {
			return c
		}
	}
	return nil
}

// appelUI exécute une requête et rend la réponse déjà refermée.
func appelUI(t *testing.T, c *http.Client, methode, url string, entetes map[string]string, corps string) reponse {
	t.Helper()

	var lecteur io.Reader
	if corps != "" {
		lecteur = strings.NewReader(corps)
	}
	req, err := http.NewRequestWithContext(context.Background(), methode, url, lecteur)
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	for k, v := range entetes {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", methode, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	brut, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lecture du corps: %v", err)
	}
	return reponse{
		code:    resp.StatusCode,
		entetes: resp.Header,
		cookies: resp.Cookies(),
		corps:   string(brut),
	}
}

// clientSimple ne garde PAS les cookies : il tient le rôle de la CLI.
func clientSimple() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// clientNavigateur garde les cookies, donc se comporte comme un navigateur —
// c'est tout l'objet de ces tests.
func clientNavigateur(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	c := clientSimple()
	c.Jar = jar
	return c
}

// serveurNavigateur monte l'API avec une allowlist d'origines.
//
// `UICookieInsecure` est posé parce que `httptest` sert en clair : un cookie
// `Secure` ne serait jamais RENVOYÉ par un `cookiejar`, et le test passerait
// pour de mauvaises raisons — il vérifierait l'absence de cookie, pas son refus.
func serveurNavigateur(t *testing.T, origines ...string) *httptest.Server {
	t.Helper()

	m := nouveauMagasin(t)
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	srv, err := api.New(&api.Config{
		Addr:             "127.0.0.1:0",
		UIOrigins:        origines,
		UICookieInsecure: true,
	}, api.Deps{
		Users: m, Auth: a,
		Runs: muet{}, Diffs: muet{}, Events: muet{},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h
}

const corpsDeConnexion = `{"subject":"alice","password":"` + motDePasseDeTest + `"}`

// ouvrirSession se connecte AVEC le client donné, de sorte qu'un client à jar
// conserve le cookie pour la suite.
func ouvrirSession(t *testing.T, h *httptest.Server, c *http.Client) reponse {
	t.Helper()
	r := appelUI(t, c, http.MethodPost, h.URL+"/auth/login",
		map[string]string{"Content-Type": "application/json"}, corpsDeConnexion)
	if r.code != http.StatusOK {
		t.Fatalf("connexion refusée : %d", r.code)
	}
	return r
}

// --- CORS ---

// L'origine autorisée est renvoyée TELLE QUELLE, jamais `*` : avec
// `Allow-Credentials`, l'étoile est refusée par les navigateurs, et un serveur
// qui la renvoie quand même a un CORS qui ne marche que sans cookie — donc pas
// du tout.
func TestUneOrigineAutoriseeObtientSesEnTetes(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	r := appelUI(t, clientSimple(), http.MethodGet, h.URL+"/auth/whoami",
		map[string]string{"Origin": origineUI}, "")

	if got := r.entetes.Get("Access-Control-Allow-Origin"); got != origineUI {
		t.Errorf("Allow-Origin = %q, attendu %q", got, origineUI)
	}
	if got := r.entetes.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q : le cookie ne partira pas", got)
	}
	// Sans `Vary`, un cache partagé sert à une origine la réponse taillée pour
	// une autre.
	if got := r.entetes.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, doit contenir Origin", got)
	}
}

func TestUneOrigineInconnueNObtientRien(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	r := appelUI(t, clientSimple(), http.MethodGet, h.URL+"/auth/whoami",
		map[string]string{"Origin": "https://ailleurs.example.net"}, "")

	if got := r.entetes.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q pour une origine inconnue", got)
	}
}

// Le défaut REFUSE. Un déploiement qui ne pose pas la variable n'ouvre pas
// l'API à tout le monde par accident.
func TestSansAllowlistAucuneOrigineNestAcceptee(t *testing.T) {
	h := serveurNavigateur(t)

	r := appelUI(t, clientSimple(), http.MethodGet, h.URL+"/auth/whoami",
		map[string]string{"Origin": origineUI}, "")

	if got := r.entetes.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q sans allowlist configurée", got)
	}
}

// Une requête SANS `Origin` est celle de la CLI et du runner. Elle doit
// traverser exactement comme avant : le CORS n'est pas une autorisation, et le
// transformer en filtre casserait tous les clients non-navigateur.
func TestSansOrigineLaRequeteTraverseIntacte(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	c := clientSimple()
	connexion := ouvrirSession(t, h, c)
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(connexion.corps), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}

	r := appelUI(t, c, http.MethodGet, h.URL+"/auth/whoami",
		map[string]string{"Authorization": "Bearer " + out.Token}, "")
	if r.code != http.StatusOK {
		t.Fatalf("code = %d : la CLI a été gênée par le CORS", r.code)
	}
	if got := r.entetes.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q sur une requête sans Origin", got)
	}
}

func TestLaPrevolAutoriseeAnnonceCeQuelleAccepte(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	r := appelUI(t, clientSimple(), http.MethodOptions, h.URL+"/runs", map[string]string{
		"Origin":                         origineUI,
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "content-type,x-warden-ui",
	}, "")

	if r.code != http.StatusNoContent {
		t.Fatalf("prévol = %d, attendu 204", r.code)
	}
	if got := r.entetes.Get("Access-Control-Allow-Origin"); got != origineUI {
		t.Errorf("Allow-Origin = %q", got)
	}
	// L'en-tête anti-CSRF doit être annonçable, sinon la prévol échoue et
	// AUCUNE écriture n'est possible depuis le navigateur.
	entetes := strings.ToLower(r.entetes.Get("Access-Control-Allow-Headers"))
	if !strings.Contains(entetes, "x-warden-ui") {
		t.Errorf("Allow-Headers = %q : l'en-tête anti-CSRF n'est pas annoncé", entetes)
	}
}

// Une prévol d'origine inconnue est REFUSÉE explicitement plutôt que servie
// sans en-têtes : le navigateur bloquerait de toute façon, mais un 403 dit à
// l'exploitant que quelqu'un frappe depuis une origine non déclarée.
func TestUnePrevolInconnueEstRefusee(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	r := appelUI(t, clientSimple(), http.MethodOptions, h.URL+"/runs", map[string]string{
		"Origin":                        "https://ailleurs.example.net",
		"Access-Control-Request-Method": "GET",
	}, "")

	if r.code != http.StatusForbidden {
		t.Errorf("prévol inconnue = %d, attendu 403", r.code)
	}
}

// --- le cookie ---

func TestLaConnexionPoseUnCookieDeSession(t *testing.T) {
	h := serveurNavigateur(t, origineUI)

	r := ouvrirSession(t, h, clientSimple())

	c := r.cookie("warden_session")
	if c == nil {
		t.Fatal("aucun cookie de session posé")
	}
	if !c.HttpOnly {
		t.Error("cookie lisible par JS : une XSS emporte la session")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, attendu Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q : le cookie ne couvrirait pas toute l'API", c.Path)
	}

	// Le corps JSON reste INCHANGÉ : la CLI lit `token` et ne doit rien voir
	// de ce changement.
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(r.corps), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if out.Token == "" {
		t.Error("le corps ne porte plus de jeton : la CLI est cassée")
	}
}

// Le cookie authentifie une LECTURE sans rien de plus.
func TestLeCookieOuvreLaLecture(t *testing.T) {
	h := serveurNavigateur(t, origineUI)
	c := clientNavigateur(t)
	ouvrirSession(t, h, c)

	r := appelUI(t, c, http.MethodGet, h.URL+"/auth/whoami", nil, "")
	if r.code != http.StatusOK {
		t.Fatalf("whoami par cookie = %d, attendu 200", r.code)
	}
}

// LE test anti-CSRF. Un site tiers peut faire partir une requête avec le cookie
// — les formulaires ne sont pas soumis au CORS — mais il ne peut pas poser un
// en-tête arbitraire sans prévol.
func TestLeCookieSeulNOuvrePasLEcriture(t *testing.T) {
	h := serveurNavigateur(t, origineUI)
	c := clientNavigateur(t)
	ouvrirSession(t, h, c)

	r := appelUI(t, c, http.MethodPost, h.URL+"/auth/logout", nil, "")
	if r.code != http.StatusForbidden {
		t.Errorf("POST par cookie sans en-tête UI = %d, attendu 403", r.code)
	}
}

func TestLeCookieAvecLEnTeteUIOuvreLEcriture(t *testing.T) {
	h := serveurNavigateur(t, origineUI)
	c := clientNavigateur(t)
	ouvrirSession(t, h, c)

	r := appelUI(t, c, http.MethodPost, h.URL+"/auth/logout",
		map[string]string{"X-Warden-UI": "1"}, "")
	if r.code != http.StatusNoContent {
		t.Errorf("logout = %d, attendu 204", r.code)
	}
}

// Le logout doit EXPIRER le cookie en plus de révoquer la session. Révoquer
// sans effacer laisserait le navigateur renvoyer un cookie mort à chaque
// requête, et l'utilisateur verrait des 401 sans comprendre pourquoi.
func TestLeLogoutEffaceLeCookie(t *testing.T) {
	h := serveurNavigateur(t, origineUI)
	c := clientNavigateur(t)
	ouvrirSession(t, h, c)

	r := appelUI(t, c, http.MethodPost, h.URL+"/auth/logout",
		map[string]string{"X-Warden-UI": "1"}, "")
	if r.code != http.StatusNoContent {
		t.Fatalf("logout = %d", r.code)
	}

	ck := r.cookie("warden_session")
	if ck == nil {
		t.Fatal("le logout n'a pas réémis le cookie pour l'effacer")
	}
	if ck.MaxAge >= 0 && !ck.Expires.Before(time.Now()) {
		t.Errorf("cookie non expiré : MaxAge=%d Expires=%v", ck.MaxAge, ck.Expires)
	}
}

// Le porteur reste PREMIER. Un navigateur qui présenterait les deux ne doit pas
// faire basculer la requête sur le chemin cookie, sans quoi la CLI verrait son
// comportement changer selon ce qu'un autre client a laissé traîner.
func TestLePorteurLEmporteSurLeCookie(t *testing.T) {
	h := serveurNavigateur(t, origineUI)
	c := clientNavigateur(t)
	ouvrirSession(t, h, c)

	// Un porteur INVALIDE avec un cookie VALIDE : si le porteur l'emporte, la
	// réponse est 401. S'il retombe sur le cookie, elle serait 200 — et une
	// session expirée continuerait de marcher sans que personne le voie.
	r := appelUI(t, c, http.MethodGet, h.URL+"/auth/whoami",
		map[string]string{"Authorization": "Bearer " + jetonBienForme()}, "")
	if r.code != http.StatusUnauthorized {
		t.Errorf("code = %d, attendu 401 : le porteur invalide est retombé sur le cookie", r.code)
	}
}
