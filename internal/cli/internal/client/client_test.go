package client_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/client"
)

func monter(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("client.New : %v", err)
	}
	return c
}

// TestLIdentiteVoyageEnEnTete : le serveur ne répond à personne d'autre.
func TestLIdentiteVoyageEnEnTete(t *testing.T) {
	var vu string
	c := monter(t, func(w http.ResponseWriter, r *http.Request) {
		vu = r.Header.Get("X-User-Subject")
		_, _ = w.Write([]byte(`{"token":"t","ref_prefix":"refs/audit/1/","expires_at":1}`))
	})

	if _, err := c.IssuePushGrant(t.Context(), "depot"); err != nil {
		t.Fatalf("IssuePushGrant : %v", err)
	}
	if vu != "alice" {
		t.Errorf("identité transmise %q", vu)
	}
}

// TestUnDepotNonConfigureEstDistingue : la suite à donner est humaine, pas
// technique — un administrateur doit configurer le dépôt. Un message générique
// enverrait quelqu'un chercher un bug.
func TestUnDepotNonConfigureEstDistingue(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this repository has no server-side configuration yet", http.StatusNotFound)
	})

	_, err := c.IssuePushGrant(t.Context(), "depot")
	if !errors.Is(err, client.ErrRepoNonConfigure) {
		t.Fatalf("erreur non distinguée : %v", err)
	}
}

// TestUnServeurSansAuthentificateurEstDistingue : le serveur répond 501 tant
// qu'aucun authentificateur n'est configuré, et c'est délibéré de sa part. La
// CLI doit le dire, pas le traduire en panne.
func TestUnServeurSansAuthentificateurEstDistingue(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no authenticator configured", http.StatusNotImplemented)
	})

	_, err := c.IssuePushGrant(t.Context(), "depot")
	if !errors.Is(err, client.ErrAuthentification) {
		t.Fatalf("erreur non distinguée : %v", err)
	}
}

// TestLeMessageDuServeurEstRepris : ces messages sont rédigés côté serveur pour
// être lus par un humain (§5.7). Les reformuler les appauvrirait.
func TestLeMessageDuServeurEstRepris(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "run_ref must be a 26-character ULID", http.StatusBadRequest)
	})

	_, err := c.CreateRun(t.Context(), client.NewRun{RunRef: "x", Repo: "depot"})
	if err == nil {
		t.Fatal("erreur attendue")
	}
	if !strings.Contains(err.Error(), "26-character ULID") {
		t.Errorf("le message du serveur est perdu : %v", err)
	}
}

// TestUnJetonInutilisableEstRefuse : un jeton vide produirait un push refusé
// bien plus loin, avec un message parlant de git.
func TestUnJetonInutilisableEstRefuse(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"","ref_prefix":""}`))
	})

	if _, err := c.IssuePushGrant(t.Context(), "depot"); err == nil {
		t.Fatal("un jeton vide a été accepté")
	}
}

// TestUneAdresseNonHTTPEstRefusee : la CLI tient cette valeur du fichier de
// projet, donc d'un fichier versionné dans un dépôt.
func TestUneAdresseNonHTTPEstRefusee(t *testing.T) {
	for _, adresse := range []string{"", "ssh://serveur", "file:///etc", "ext::sh -c evil"} {
		if _, err := client.New(client.Config{BaseURL: adresse}); err == nil {
			t.Errorf("adresse acceptée : %q", adresse)
		}
	}
}

// Régression. `JoinPath` échappe le `?` : un filtre partait comme un segment de
// chemin littéral, et le serveur répondait 404 sur une requête valide. Le
// symptôme — « introuvable » sur `warden host list --env prod` — n'accusait
// rien de ce qui était en cause.
func TestUneQueryNEstPasAvaleeParLeChemin(t *testing.T) {
	var vuChemin, vuQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vuChemin, vuQuery = r.URL.Path, r.URL.Query().Get("environment")
		_ = json.NewEncoder(w).Encode([]client.Host{})
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := api.ListHosts(t.Context(), "prod"); err != nil {
		t.Fatalf("ListHosts: %v", err)
	}

	if vuChemin != "/admin/hosts" {
		t.Errorf("chemin reçu = %q, attendu /admin/hosts", vuChemin)
	}
	if vuQuery != "prod" {
		t.Errorf("environment reçu = %q, attendu prod", vuQuery)
	}
}

// Le nom d'un dépôt voyage dans le CHEMIN et dans le corps.
//
// Le serveur refuse la requête si les deux se contredisent — c'est sa façon de
// rendre impossible de configurer un dépôt en croyant en viser un autre. Le
// client doit donc les poser tous les deux, et depuis la même valeur.
func TestPutRepoPoseLeNomDansLeCheminEtDansLeCorps(t *testing.T) {
	var vuChemin, vuMethode string
	var vuCorps client.Repo
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vuChemin, vuMethode = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&vuCorps)
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	depot := client.Repo{
		Name:        "mon-depot",
		RemoteURL:   "https://forge.example.com/equipe/mon-depot.git",
		MainlineRef: "refs/heads/main",
	}
	if err := api.PutRepo(t.Context(), depot); err != nil {
		t.Fatalf("PutRepo: %v", err)
	}

	if vuMethode != http.MethodPut {
		t.Errorf("méthode = %q, attendu PUT", vuMethode)
	}
	if vuChemin != "/admin/repos/mon-depot" {
		t.Errorf("chemin = %q, attendu /admin/repos/mon-depot", vuChemin)
	}
	if vuCorps != depot {
		t.Errorf("corps = %+v, attendu %+v", vuCorps, depot)
	}
}

// Un nom vide est refusé AVANT le réseau : sans lui la requête partirait sur
// `/admin/repos/`, une route qui n'existe pas, et le 404 parlerait de droits
// d'administration au lieu de nommer la vraie cause.
func TestPutRepoRefuseUnNomVideSansAppeler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("le serveur a été appelé")
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := api.PutRepo(t.Context(), client.Repo{RemoteURL: "https://x/y.git"}); err == nil {
		t.Fatal("un dépôt sans nom a été accepté")
	}
}

func TestListReposLitLaListe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Repo{
			{Name: "ansible", RemoteURL: "https://forge/ansible.git", MainlineRef: "refs/heads/main"},
		})
	}))
	defer srv.Close()

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	depots, err := api.ListRepos(t.Context())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(depots) != 1 || depots[0].Name != "ansible" {
		t.Fatalf("dépôts = %+v", depots)
	}
}

// Un serveur antérieur à l'ADR-0049 n'a pas `/version` : il n'annonce rien, et
// ce silence ne doit pas devenir une erreur qui bloquerait toute soumission.
func TestUnServeurSansVersionNAnnonceRien(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })

	v, err := c.VersionServeur(t.Context())
	if err != nil {
		t.Fatalf("VersionServeur : %v", err)
	}
	if v != "" {
		t.Errorf("version = %q, attendue vide", v)
	}
}

func TestLaVersionDuServeurEstLue(t *testing.T) {
	c := monter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.0.42"}`))
	})

	v, err := c.VersionServeur(t.Context())
	if err != nil || v != "0.0.42" {
		t.Errorf("VersionServeur = %q, %v", v, err)
	}
}
