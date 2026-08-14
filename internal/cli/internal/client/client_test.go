package client_test

import (
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
