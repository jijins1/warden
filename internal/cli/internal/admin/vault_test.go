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

func reponseCleDeVault(env string) client.VaultKey {
	return client.VaultKey{
		Environment: env,
		Fingerprint: strings.Repeat("a", 64),
		Origin:      "imported",
		CreatedAt:   "2026-08-17T09:00:00Z",
	}
}

func ecrireMotDePasse(t *testing.T, contenu string) string {
	t.Helper()
	chemin := filepath.Join(t.TempDir(), "vault-password")
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	return chemin
}

// L'import lit le mot de passe d'un FICHIER, pas d'argv — même règle que pour
// une clé SSH.
func TestLImportDeVaultEnvoieLeMotDePasseLuDansLeFichier(t *testing.T) {
	var recu client.PutVaultKey
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("méthode = %s, attendu POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&recu)
		_ = json.NewEncoder(w).Encode(reponseCleDeVault("prod"))
	})

	var out strings.Builder
	err := admin.PoserCleDeVault(context.Background(), api, admin.OptionsCleDeVault{
		Environment: "prod",
		Cle:         &prompt.Source{Quoi: "le mot de passe de vault existant", Fichier: ecrireMotDePasse(t, "un-mot-de-passe")},
	}, &out)
	if err != nil {
		t.Fatalf("PoserCleDeVault: %v", err)
	}
	if recu.Plaintext != "un-mot-de-passe" {
		t.Errorf("mot de passe envoyé = %q", recu.Plaintext)
	}
	if recu.Generate {
		t.Error("l'import a demandé une génération")
	}
	if !strings.Contains(out.String(), strings.Repeat("a", 64)) {
		t.Errorf("l'empreinte n'est pas affichée :\n%s", out.String())
	}
}

func TestLaGenerationDeVaultNEnvoieAucunMotDePasse(t *testing.T) {
	var recu client.PutVaultKey
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recu)
		k := reponseCleDeVault("prod")
		k.Origin = "generated"
		_ = json.NewEncoder(w).Encode(k)
	})

	var out strings.Builder
	err := admin.PoserCleDeVault(context.Background(), api,
		admin.OptionsCleDeVault{Environment: "prod", Generate: true}, &out)
	if err != nil {
		t.Fatalf("PoserCleDeVault: %v", err)
	}
	if !recu.Generate || recu.Plaintext != "" {
		t.Errorf("corps envoyé = %+v", recu)
	}
	if !strings.Contains(out.String(), "jamais quitté le cluster") {
		t.Errorf("la sortie ne dit pas ce que l'origine implique :\n%s", out.String())
	}
}

// `--generate` et l'import d'un mot de passe existant s'excluent.
func TestGenerateEtImportDeVaultSExcluent(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que la commande est incomplète")
	})

	var out strings.Builder
	err := admin.PoserCleDeVault(context.Background(), api, admin.OptionsCleDeVault{
		Environment: "prod", Generate: true,
		Cle: &prompt.Source{Quoi: "x", Fichier: ecrireMotDePasse(t, "x")},
	}, &out)
	if err == nil {
		t.Fatal("accepté à tort")
	}
}

func TestMontrerLaCleDeVaultAfficheLEmpreinte(t *testing.T) {
	api := serveur(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(reponseCleDeVault("prod"))
	})

	var out strings.Builder
	if err := admin.MontrerCleDeVault(context.Background(), api, "prod", &out); err != nil {
		t.Fatalf("MontrerCleDeVault: %v", err)
	}
	if !strings.Contains(out.String(), strings.Repeat("a", 64)) {
		t.Errorf("l'empreinte n'est pas affichée :\n%s", out.String())
	}
}

// Le SEUL chemin d'interaction humaine avec cette clé : une valeur en clair
// part, un bloc chiffré revient. Le mot de passe de vault lui-même ne doit
// apparaître nulle part dans la sortie.
func TestChiffrerEnvoieLaValeurEtAfficheLeBlocRendu(t *testing.T) {
	var recu struct {
		Plaintext string `json:"plaintext"`
		VarName   string `json:"var_name"`
	}
	api := serveur(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&recu)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"block": "db_password: !vault |\n          $ANSIBLE_VAULT;1.1;AES256\n          6161",
		})
	})

	var out strings.Builder
	err := admin.ChiffrerPourVault(context.Background(), api, "prod", "db_password",
		&prompt.Source{Quoi: "la valeur à chiffrer", Fichier: ecrireMotDePasse(t, "s3cr3t")}, &out)
	if err != nil {
		t.Fatalf("ChiffrerPourVault: %v", err)
	}
	if recu.Plaintext != "s3cr3t" || recu.VarName != "db_password" {
		t.Errorf("corps envoyé = %+v", recu)
	}
	if !strings.Contains(out.String(), "!vault |") {
		t.Errorf("le bloc chiffré n'est pas affiché :\n%s", out.String())
	}
	if strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("la valeur en clair a fuité dans la sortie :\n%s", out.String())
	}
}

func TestChiffrerSansEnvNiVarEstRefuse(t *testing.T) {
	api := serveur(t, func(http.ResponseWriter, *http.Request) {
		t.Error("le serveur a été appelé alors que la commande est incomplète")
	})

	var out strings.Builder
	valeur := &prompt.Source{Quoi: "x", Fichier: ecrireMotDePasse(t, "x")}
	if err := admin.ChiffrerPourVault(context.Background(), api, "", "db_password", valeur, &out); err == nil {
		t.Error("accepté à tort sans environnement")
	}
	if err := admin.ChiffrerPourVault(context.Background(), api, "prod", "", valeur, &out); err == nil {
		t.Error("accepté à tort sans nom de variable")
	}
}
