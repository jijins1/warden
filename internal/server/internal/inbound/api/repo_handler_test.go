// Étage 1 de l'ADR-0010 : ni base, ni conteneur. La propriété qui compte ici est
// l'AUTORISATION, et elle ne dépend d'aucun stockage réel.
//
// Test interne au paquet, à la différence des autres fichiers de test de `api` :
// il faut poser l'utilisateur authentifié dans le contexte sans traverser le
// middleware, lequel exige une base. Aller chercher un PostgreSQL pour vérifier
// qu'un non-administrateur ne voit rien serait le meilleur moyen de ne jamais
// lancer ce test.

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

// magasinFactice enregistre ce qu'on lui demande d'écrire.
type magasinFactice struct {
	repos   []core.Repo
	ecrites []core.Repo
	auteurs []int64
	err     error
}

func (m *magasinFactice) GetRepo(_ context.Context, name string) (core.Repo, error) {
	for _, r := range m.repos {
		if r.Name == name {
			return r, nil
		}
	}
	// Enveloppé comme le fait le vrai magasin : le handler distingue « dépôt non
	// configuré » d'une panne, et un faux qui ne respecte pas ce contrat testerait
	// un chemin que la production n'emprunte jamais.
	return core.Repo{}, fmt.Errorf("dépôt %q non configuré: %w", name, core.ErrNotFound)
}

func (m *magasinFactice) ListRepos(_ context.Context) ([]core.Repo, error) {
	return m.repos, m.err
}

func (m *magasinFactice) UpsertRepo(_ context.Context, r core.Repo, by int64) error {
	if m.err != nil {
		return m.err
	}
	m.ecrites = append(m.ecrites, r)
	m.auteurs = append(m.auteurs, by)
	return nil
}

func serveurAvec(t *testing.T, magasin core.RepoStore) *Server {
	t.Helper()
	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs:   stubPort{},
		Users:  stubPort{},
		Diffs:  stubPort{},
		Events: stubPort{},
		Repos:  magasin,
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}
	return s
}

// requete construit une requête portant l'utilisateur déjà authentifié.
func requete(t *testing.T, methode, cible string, user *core.User, corps string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), methode, cible, strings.NewReader(corps))
	if user != nil {
		r = withUser(r, user)
	}
	return r
}

func administrateur() *core.User    { return &core.User{ID: 1, DisplayName: "Ada", IsAdmin: true} }
func utilisateurNormal() *core.User { return &core.User{ID: 2, DisplayName: "Bob"} }

// TestLaSurfaceDAdministrationEstInvisibleAuxAutres est la propriété principale.
//
// 404 et non 403, délibérément. Un 403 confirmerait à un utilisateur ordinaire
// que cette surface existe — et la liste des dépôts avec leurs URL de forge est
// une carte de l'organisation, utile à qui cherche par où entrer.
func TestLaSurfaceDAdministrationEstInvisibleAuxAutres(t *testing.T) {
	magasin := &magasinFactice{repos: []core.Repo{
		{Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main"},
	}}
	s := serveurAvec(t, magasin)

	cas := []struct {
		nom  string
		user *core.User
	}{
		{"utilisateur ordinaire", utilisateurNormal()},
		{"administrateur désactivé", &core.User{ID: 3, IsAdmin: true, Disabled: true}},
		{"personne", nil},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.HandleListRepos(w, requete(t, http.MethodGet, "/admin/repos", c.user, ""))
			if w.Code != http.StatusNotFound {
				t.Errorf("GET : statut %d, attendu 404", w.Code)
			}
			if strings.Contains(w.Body.String(), "forge.invalid") {
				t.Errorf("la configuration a fuité : %s", w.Body.String())
			}

			w = httptest.NewRecorder()
			s.HandlePutRepo(w, requete(t, http.MethodPut, "/admin/repos/depot", c.user,
				`{"remote_url":"https://mechant.invalid/x.git","mainline_ref":"refs/heads/main"}`))
			if w.Code != http.StatusNotFound {
				t.Errorf("PUT : statut %d, attendu 404", w.Code)
			}
		})
	}

	if len(magasin.ecrites) != 0 {
		t.Fatalf("des écritures ont eu lieu sans droit d'administration : %+v", magasin.ecrites)
	}
}

// TestUnAdministrateurLitEtEcrit : le chemin nominal, et l'imputabilité.
func TestUnAdministrateurLitEtEcrit(t *testing.T) {
	magasin := &magasinFactice{}
	s := serveurAvec(t, magasin)

	w := httptest.NewRecorder()
	r := requete(t, http.MethodPut, "/admin/repos/depot", administrateur(),
		`{"remote_url":"https://forge.invalid/equipe/projet.git","mainline_ref":"refs/heads/main"}`)
	r.SetPathValue("repo", "depot")
	s.HandlePutRepo(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("statut %d, attendu 200 : %s", w.Code, w.Body.String())
	}

	if len(magasin.ecrites) != 1 {
		t.Fatalf("%d écriture(s), attendu 1", len(magasin.ecrites))
	}
	if magasin.ecrites[0].Name != "depot" {
		t.Errorf("nom écrit %q, attendu \"depot\"", magasin.ecrites[0].Name)
	}
	// La configuration d'un dépôt décide de ce qui s'exécutera en production :
	// savoir qui l'a changée est du même ordre que savoir qui a lancé un run.
	if magasin.auteurs[0] != 1 {
		t.Errorf("auteur %d, attendu 1", magasin.auteurs[0])
	}
}

// TestLeNomVientDuCheminPasDuCorps ferme la divergence qui ferait écrire la
// configuration d'un dépôt en croyant en modifier un autre.
func TestLeNomVientDuCheminPasDuCorps(t *testing.T) {
	magasin := &magasinFactice{}
	s := serveurAvec(t, magasin)

	w := httptest.NewRecorder()
	r := requete(t, http.MethodPut, "/admin/repos/depot", administrateur(),
		`{"name":"un-autre-depot","remote_url":"https://forge.invalid/x.git","mainline_ref":"refs/heads/main"}`)
	r.SetPathValue("repo", "depot")
	s.HandlePutRepo(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : un nom contradictoire a été accepté", w.Code)
	}
	if len(magasin.ecrites) != 0 {
		t.Errorf("une écriture a eu lieu malgré la contradiction : %+v", magasin.ecrites)
	}
}

// TestConfigurationRefusee couvre ce que la validation doit attraper avant la
// base — et notamment les transports que le rôle `git` refuse de toute façon,
// mais bien plus tard et avec un message que personne ne peut corriger.
func TestConfigurationRefusee(t *testing.T) {
	s := serveurAvec(t, &magasinFactice{})

	cas := []struct {
		nom   string
		corps string
	}{
		{"transport exécutable", `{"remote_url":"ext::sh -c evil","mainline_ref":"refs/heads/main"}`},
		{"transport fichier", `{"remote_url":"file:///etc","mainline_ref":"refs/heads/main"}`},
		{"branche en nom court", `{"remote_url":"https://f.invalid/x.git","mainline_ref":"main"}`},
		{"branche avec traversée", `{"remote_url":"https://f.invalid/x.git","mainline_ref":"refs/heads/../../x"}`},
		{"URL absente", `{"mainline_ref":"refs/heads/main"}`},
		{"champ inconnu", `{"remote_url":"https://f.invalid/x.git","mainline_ref":"refs/heads/main","admin":true}`},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := requete(t, http.MethodPut, "/admin/repos/depot", administrateur(), c.corps)
			r.SetPathValue("repo", "depot")
			s.HandlePutRepo(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("statut %d, attendu 400", w.Code)
			}
		})
	}
}

// TestSansMagasinLaSurfaceRepond501 : un déploiement qui ne sert que la
// consultation d'audit reste valide.
func TestSansMagasinLaSurfaceRepond501(t *testing.T) {
	s := serveurAvec(t, nil)

	w := httptest.NewRecorder()
	s.HandleListRepos(w, requete(t, http.MethodGet, "/admin/repos", administrateur(), ""))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("statut %d, attendu 501", w.Code)
	}
}

// TestListeRenduePourUnAdministrateur vérifie que la lecture fonctionne, faute
// de quoi les refus ci-dessus ne prouveraient rien.
func TestListeRenduePourUnAdministrateur(t *testing.T) {
	magasin := &magasinFactice{repos: []core.Repo{
		{Name: "a", RemoteURL: "https://forge.invalid/a.git", MainlineRef: "refs/heads/main"},
		{Name: "b", RemoteURL: "https://forge.invalid/b.git", MainlineRef: "refs/heads/principale"},
	}}
	s := serveurAvec(t, magasin)

	w := httptest.NewRecorder()
	s.HandleListRepos(w, requete(t, http.MethodGet, "/admin/repos", administrateur(), ""))
	if w.Code != http.StatusOK {
		t.Fatalf("statut %d, attendu 200", w.Code)
	}

	var out []repoPayload
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].MainlineRef != "refs/heads/principale" {
		t.Errorf("liste inattendue : %+v", out)
	}
}

// stubPort satisfait les ports que `api.New` exige sans les exercer.
//
// Ce fichier ne teste que l'administration des dépôts ; les autres ports doivent
// exister pour que le serveur se construise, et rien de plus. Les faire échouer
// bruyamment plutôt que rendre des zéros signalerait tout de suite un test qui
// s'est mis à emprunter un chemin qu'il ne prétend pas couvrir.
type stubPort struct{}

var errPortNonUtilise = errors.New("port non utilisé par ce test")

func (stubPort) RunByRef(context.Context, string) (core.Run, error) {
	return core.Run{}, errPortNonUtilise
}
func (stubPort) ListRuns(context.Context, core.RunFilter) (core.RunListResult, error) {
	return core.RunListResult{}, errPortNonUtilise
}
func (stubPort) RunsByHost(context.Context, string, time.Time, time.Time) ([]core.RunListItem, error) {
	return nil, errPortNonUtilise
}
func (stubPort) PreviousRun(context.Context, core.Run) (core.Run, error) {
	return core.Run{}, errPortNonUtilise
}
func (stubPort) UserBySubject(context.Context, core.UserType, string) (core.User, error) {
	return core.User{}, errPortNonUtilise
}
func (stubPort) DiffForRun(context.Context, core.Run, int64) (json.RawMessage, error) {
	return nil, errPortNonUtilise
}
func (stubPort) Follow(context.Context, int64, int64) (core.EventStream, error) {
	return nil, errPortNonUtilise
}
