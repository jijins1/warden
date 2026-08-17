// Étage 1 de l'ADR-0010 : un httptest.Server, aucun cluster.
//
// Ce que ces tests protègent : le formalisme de déclaration se lit comme il est
// documenté, une erreur de forme se rapporte AVANT tout aller-retour réseau, et
// ce que la commande ne fait pas est dit à l'utilisateur.

package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/client"
)

// serveur monte une API factice qui renvoie ce qu'on lui donne.
func serveur(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	api, err := client.New(client.Config{BaseURL: srv.URL, Subject: "alice"})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return api
}

// ecrire pose un fichier de déclaration et rend son chemin.
func ecrire(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "hosts.yaml")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	return chemin
}

const documentValide = `
hosts:
  - host: db-prod-02
    address: 10.20.0.42
    login: ansible
    host_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple"
  - host: web-prod-01
    address: 10.20.0.43
    login: ansible
    port: 2222
`

// Le formalisme se lit tel qu'il est documenté dans l'aide de la CLI. Si ce
// test casse, c'est l'aide qui ment.
func TestLeDocumentDeDeclarationEstLuCommeDocumente(t *testing.T) {
	var recu client.Inventory
	var requete string

	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		requete = r.URL.String()
		_ = json.NewDecoder(r.Body).Decode(&recu)
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	var out strings.Builder
	err := admin.DeclarerHotes(context.Background(), api, ecrire(t, documentValide), "prod", &out)
	if err != nil {
		t.Fatalf("DeclarerHotes: %v", err)
	}

	if len(recu.Hosts) != 2 {
		t.Fatalf("machines envoyées = %d, attendu 2", len(recu.Hosts))
	}
	h := recu.Hosts[0]
	if h.Name != "db-prod-02" || h.Address != "10.20.0.42" || h.Login != "ansible" {
		t.Errorf("première machine mal lue : %+v", h)
	}
	if !strings.HasPrefix(h.HostKey, "ssh-ed25519 ") {
		t.Errorf("empreinte mal lue : %q", h.HostKey)
	}
	if recu.Hosts[1].Port != 2222 {
		t.Errorf("port = %d, attendu 2222", recu.Hosts[1].Port)
	}
	if !strings.Contains(requete, "environment=prod") {
		t.Errorf("requête = %q, l'environnement n'a pas suivi", requete)
	}
}

// JSON aussi : un seul analyseur pour les deux formats, donc les deux acceptent
// exactement les mêmes choses.
func TestLeDocumentPeutEtreEnJSON(t *testing.T) {
	var recu client.Inventory
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recu)
		_ = json.NewEncoder(w).Encode([]client.Host{})
	})

	const doc = `{"hosts":[{"host":"db","address":"10.0.0.1","login":"ansible"}]}`
	var out strings.Builder
	if err := admin.DeclarerHotes(context.Background(), api, ecrire(t, doc), "prod", &out); err != nil {
		t.Fatalf("DeclarerHotes: %v", err)
	}
	if len(recu.Hosts) != 1 || recu.Hosts[0].Name != "db" {
		t.Errorf("document JSON mal lu : %+v", recu.Hosts)
	}
}

// Une clé mal orthographiée est REFUSÉE, pas ignorée. `hostkey` au lieu de
// `host_key` désarmerait l'épinglage de l'empreinte sans que rien ne le dise.
func TestUnChampMalOrthographieEstRefuse(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que le document est invalide")
	})

	const doc = `
hosts:
  - host: db
    address: 10.0.0.1
    login: ansible
    hostkey: "ssh-ed25519 AAAA"
`
	var out strings.Builder
	err := admin.DeclarerHotes(context.Background(), api, ecrire(t, doc), "prod", &out)
	if err == nil {
		t.Fatal("un champ inconnu a été accepté")
	}
	if !strings.Contains(err.Error(), "hostkey") {
		t.Errorf("erreur = %v, elle devrait nommer le champ fautif", err)
	}
}

// La validation est LOCALE : elle se rapporte avec le fichier sous les yeux,
// avant tout aller-retour réseau.
func TestUnDocumentIncompletEstRefuseAvantLAppel(t *testing.T) {
	cas := map[string]struct{ doc, attendu string }{
		"sans adresse": {
			"hosts:\n  - host: db\n    login: ansible\n", "address",
		},
		"sans compte": {
			"hosts:\n  - host: db\n    address: 10.0.0.1\n", "login",
		},
		"sans nom": {
			"hosts:\n  - address: 10.0.0.1\n    login: ansible\n", "host",
		},
		"sans environnement nulle part": {
			"hosts:\n  - host: db\n    address: 10.0.0.1\n    login: ansible\n", "environment",
		},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			api := serveur(t, func(http.ResponseWriter, *http.Request) {
				t.Error("le serveur a été appelé alors que le document est invalide")
			})
			var out strings.Builder
			err := admin.DeclarerHotes(context.Background(), api, ecrire(t, c.doc), "", &out)
			if err == nil {
				t.Fatal("accepté à tort")
			}
			if !strings.Contains(err.Error(), c.attendu) {
				t.Errorf("erreur = %v, elle devrait nommer %q", err, c.attendu)
			}
		})
	}
}

// Deux fois la même machine, ce sont deux réponses à la même question : la
// seconde écraserait la première en silence.
func TestUneMachineDeclareeDeuxFoisEstRefusee(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que le document est invalide")
	})

	const doc = `
hosts:
  - host: db
    address: 10.0.0.1
    login: ansible
  - host: db
    address: 10.0.0.2
    login: root
`
	var out strings.Builder
	err := admin.DeclarerHotes(context.Background(), api, ecrire(t, doc), "prod", &out)
	if err == nil {
		t.Fatal("un doublon a été accepté")
	}
	if !strings.Contains(err.Error(), "deux fois") {
		t.Errorf("erreur = %v", err)
	}
}

func TestUnFichierAbsentOuVideEstRefuse(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé")
	})

	var out strings.Builder
	if err := admin.DeclarerHotes(context.Background(), api, "", "prod", &out); err == nil {
		t.Error("un chemin vide a été accepté")
	}
	if err := admin.DeclarerHotes(context.Background(), api, "/n/existe/pas.yaml", "prod", &out); err == nil {
		t.Error("un fichier absent a été accepté")
	}
	if err := admin.DeclarerHotes(context.Background(), api, ecrire(t, "hosts: []\n"), "prod", &out); err == nil {
		t.Error("un document sans machine a été accepté")
	}
}

// Ce que la déclaration NE FAIT PAS doit être dit. Sans cette phrase, un
// opérateur peut croire que warden a posé la clé, alors qu'il vient seulement
// d'enregistrer qu'elle y est.
func TestLaDeclarationDitQuElleNaRienPose(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{
			{Name: "db-prod-02", Environment: "prod", Address: "10.20.0.42",
				Login: "ansible", Mode: "declared"},
		})
	})

	var out strings.Builder
	if err := admin.DeclarerHotes(context.Background(), api, ecrire(t, documentValide), "prod", &out); err != nil {
		t.Fatalf("DeclarerHotes: %v", err)
	}
	if !strings.Contains(out.String(), "warden n'a rien posé") {
		t.Errorf("la sortie ne dit pas ce que la commande n'a pas fait :\n%s", out.String())
	}
}

// Une machine sans empreinte n'est pas protégée d'un intermédiaire. Ça doit se
// voir dans la liste, pas seulement dans une documentation.
func TestLaListeSignaleLesMachinesNonEpinglees(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Host{
			{Name: "epinglee", Environment: "prod", Address: "10.0.0.1",
				Login: "ansible", Mode: "declared", HostKey: "ssh-ed25519 AAAA"},
			{Name: "aveugle", Environment: "prod", Address: "10.0.0.2",
				Login: "ansible", Mode: "declared"},
		})
	})

	var out strings.Builder
	if err := admin.ListerHotes(context.Background(), api, "prod", &out); err != nil {
		t.Fatalf("ListerHotes: %v", err)
	}
	if !strings.Contains(out.String(), "AUCUNE") {
		t.Errorf("la machine non épinglée ne se signale pas :\n%s", out.String())
	}
}

// Retirer une machine de l'inventaire ne retire RIEN de la machine. Laisser
// croire à une révocation serait le pire des silences.
func TestOublierDitQueLaCleResteSurLaMachine(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("méthode = %s, attendu DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	var out strings.Builder
	if err := admin.OublierHote(context.Background(), api, "prod", "db-prod-02", &out); err != nil {
		t.Fatalf("OublierHote: %v", err)
	}
	if !strings.Contains(out.String(), "authorized_keys") {
		t.Errorf("la sortie ne dit pas que la clé reste :\n%s", out.String())
	}
}
