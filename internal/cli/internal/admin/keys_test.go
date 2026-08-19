package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/admin"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/prompt"
)

const clePrivee = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----\n"

func ecrireCle(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	return chemin
}

func reponseCle(env string) client.Key {
	return client.Key{
		Environment: env,
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple warden-" + env,
		Fingerprint: "SHA256:exemple",
		Origin:      "imported",
		CreatedAt:   "2026-08-17T09:00:00Z",
	}
}

// L'import lit la clé d'un FICHIER — jamais d'argv. La ligne de commande d'un
// processus est lisible par tout utilisateur de la machine.
func TestLImportEnvoieLaCleLueDansLeFichier(t *testing.T) {
	var recu client.PutKey
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("méthode = %s, attendu PUT", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&recu)
		_ = json.NewEncoder(w).Encode(reponseCle("prod"))
	})

	var out strings.Builder
	err := admin.PoserCle(context.Background(), api, admin.OptionsCle{
		Environment: "prod",
		Cle:         &prompt.Source{Quoi: "la clé privée", Multiligne: true, Fichier: ecrireCle(t, clePrivee)},
	}, &out)
	if err != nil {
		t.Fatalf("PoserCle: %v", err)
	}

	// La clé traverse INTACTE, sauts de ligne internes compris : une clé
	// amputée d'une ligne est une clé illisible.
	if recu.PrivateKey != strings.TrimRight(clePrivee, "\n") {
		t.Errorf("clé envoyée = %q", recu.PrivateKey)
	}
	if recu.Generate {
		t.Error("l'import a demandé une génération")
	}

	// Et la sortie donne ce qu'un opérateur doit ALLER POSER. C'est la seule
	// raison d'être de la partie publique.
	if !strings.Contains(out.String(), "authorized_keys") ||
		!strings.Contains(out.String(), "ssh-ed25519 ") {
		t.Errorf("la sortie ne dit pas quoi déployer :\n%s", out.String())
	}
}

func TestLaGenerationNEnvoieAucuneCle(t *testing.T) {
	var recu client.PutKey
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recu)
		k := reponseCle("prod")
		k.Origin = "generated"
		_ = json.NewEncoder(w).Encode(k)
	})

	var out strings.Builder
	err := admin.PoserCle(context.Background(), api,
		admin.OptionsCle{Environment: "prod", Generate: true}, &out)
	if err != nil {
		t.Fatalf("PoserCle: %v", err)
	}
	if !recu.Generate || recu.PrivateKey != "" {
		t.Errorf("corps envoyé = %+v", recu)
	}
	// L'origine est traduite en ce qu'elle IMPLIQUE, pas seulement en ce
	// qu'elle vaut : c'est une conséquence de sécurité.
	if !strings.Contains(out.String(), "jamais quitté le cluster") {
		t.Errorf("la sortie ne dit pas ce que l'origine implique :\n%s", out.String())
	}
}

// Sans `--key-file` ni `--key-stdin`, la commande ÉCHOUE au lieu de lire
// l'entrée standard. Un `warden key import < inventaire.yml` qui prendrait la
// première ligne pour une clé serait découvert trop tard.
func TestLeCheminDImportNEstJamaisImplicite(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que la commande est incomplète")
	})

	source := func(f string, stdin bool) *prompt.Source {
		return &prompt.Source{Quoi: "la clé privée", Multiligne: true, Fichier: f, Stdin: stdin}
	}
	cas := map[string]admin.OptionsCle{
		"aucune source":         {Environment: "prod", Cle: source("", false)},
		"génération et stdin":   {Environment: "prod", Generate: true, Cle: source("", true)},
		"fichier et génération": {Environment: "prod", Generate: true, Cle: source("/tmp/x", false)},
		"fichier et stdin":      {Environment: "prod", Cle: source("/tmp/x", true)},
		"sans environnement":    {Generate: true},
	}
	for nom, o := range cas {
		t.Run(nom, func(t *testing.T) {
			var out strings.Builder
			if err := admin.PoserCle(context.Background(), api, o, &out); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

func TestUnFichierDeCleAbsentOuVideEstRefuse(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé")
	})

	fichier := func(chemin string) *prompt.Source {
		return &prompt.Source{Quoi: "la clé privée", Multiligne: true, Fichier: chemin}
	}

	var out strings.Builder
	err := admin.PoserCle(context.Background(), api, admin.OptionsCle{
		Environment: "prod", Cle: fichier("/n/existe/pas"),
	}, &out)
	if err == nil {
		t.Error("un fichier absent a été accepté")
	}

	err = admin.PoserCle(context.Background(), api, admin.OptionsCle{
		Environment: "prod", Cle: fichier(ecrireCle(t, "")),
	}, &out)
	if err == nil {
		t.Error("un fichier vide a été accepté")
	}
	// Le message nomme le FICHIER, pas « saisie vide » : c'est ce que
	// l'utilisateur doit aller regarder.
	if err != nil && !strings.Contains(err.Error(), "vide") {
		t.Errorf("erreur = %v", err)
	}
}

// L'empreinte en tête : c'est ce qu'un humain compare avec `ssh-keygen -l` sur
// une machine ou `ssh-add -l` dans un run.
func TestMontrerAfficheLEmpreinteEtLaClePublique(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reponseCle("prod"))
	})

	var out strings.Builder
	if err := admin.MontrerCle(context.Background(), api, "prod", &out); err != nil {
		t.Fatalf("MontrerCle: %v", err)
	}
	if !strings.Contains(out.String(), "SHA256:exemple") {
		t.Errorf("l'empreinte n'est pas affichée :\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ssh-ed25519 ") {
		t.Errorf("la clé publique n'est pas affichée :\n%s", out.String())
	}
}

func TestListerSansAucuneCleLeDit(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]client.Key{})
	})

	var out strings.Builder
	if err := admin.ListerCles(context.Background(), api, &out); err != nil {
		t.Fatalf("ListerCles: %v", err)
	}
	if !strings.Contains(out.String(), "aucune clé") {
		t.Errorf("sortie = %q", out.String())
	}
}
