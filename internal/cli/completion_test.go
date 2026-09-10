package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommandeCompletionEcritLeScriptDuShellDemande(t *testing.T) {
	cas := map[string]string{
		"bash": "_warden_complete",
		"zsh":  "compdef",
	}
	for shell, marqueur := range cas {
		t.Run(shell, func(t *testing.T) {
			var out bytes.Buffer
			if err := commandeCompletion([]string{shell}, &out); err != nil {
				t.Fatalf("commandeCompletion: %v", err)
			}
			if !strings.Contains(out.String(), marqueur) {
				t.Errorf("le script %s ne contient pas %q :\n%s", shell, marqueur, out.String())
			}
		})
	}
}

func TestCommandeCompletionShellInconnuEstUneErreur(t *testing.T) {
	if err := commandeCompletion([]string{"fish"}, &bytes.Buffer{}); err == nil {
		t.Fatal("aucune erreur pour un shell non supporté")
	}
}

func TestCommandeCompletionSansArgumentEstUneErreur(t *testing.T) {
	if err := commandeCompletion(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("aucune erreur sans argument")
	}
}

// lignes découpe la sortie de `__complete`, un candidat par ligne.
func lignes(t *testing.T, out *bytes.Buffer) []string {
	t.Helper()
	s := strings.TrimRight(out.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func contient(lignes []string, mot string) bool {
	for _, l := range lignes {
		if l == mot {
			return true
		}
	}
	return false
}

func TestInterneCompleteSansRienTapeListeLesCommandes(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), nil, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"submit", "login", "host", "completion"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// Un GROUPE (host, user, ...) n'accepte jamais de drapeau avant qu'un verbe
// soit choisi — voir `commandeHote`, qui ne parse les drapeaux qu'à partir du
// second argument. Seuls des verbes ont un sens ici.
func TestInterneCompleteUnGroupeNeProposeQueSesVerbes(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"host", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"declare", "list", "forget"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
	if contient(l, "--env") {
		t.Errorf("un drapeau a été proposé avant le verbe : %v", l)
	}
}

// Une FEUILLE (submit, login, ...) n'a que des drapeaux.
func TestInterneCompleteUneFeuilleNeProposeQueSesDrapeaux(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"--env", "--playbook", "--repo", "-b", "-K"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// Le troisième tiroir de la complétion : `host forget` ne propose pas les
// drapeaux de `host declare`.
func TestInterneCompleteUnVerbeNeMontreQueSesPropresDrapeaux(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"host", "forget", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, absent := range []string{"--from-inventory", "-f"} {
		if contient(l, absent) {
			t.Errorf("%q appartient à declare, pas à forget : %v", absent, l)
		}
	}
	if !contient(l, "--env") || !contient(l, "--host") {
		t.Errorf("les drapeaux de forget manquent : %v", l)
	}
}

func TestInterneCompleteFiltreParPrefixe(t *testing.T) {
	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--ch"}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	if len(l) != 1 || l[0] != "--check" {
		t.Errorf("candidats = %v, attendu [--check]", l)
	}
}

// Sans serveur configuré, la complétion d'une valeur dynamique ne propose
// rien — et surtout ne rend AUCUNE erreur : un Tab n'a pas de sortie standard
// où se plaindre.
func TestInterneCompleteSansServeurNeCraquePas(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--env", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("candidats inattendus sans serveur : %q", out.String())
	}
}

func TestInterneCompleteValeurDynamiqueEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/keys" {
			t.Errorf("chemin inattendu : %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"environment": "prod"},
			{"environment": "staging"},
		})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, srv.URL)

	var out bytes.Buffer
	if err := commandeInterneComplete(t.Context(), []string{"submit", "--env", ""}, &out); err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	l := lignes(t, &out)
	for _, attendu := range []string{"prod", "staging"} {
		if !contient(l, attendu) {
			t.Errorf("%q absent des candidats : %v", attendu, l)
		}
	}
}

// --host est scopé par --env quand il est déjà posé plus tôt sur la ligne.
func TestInterneCompleteValeurDynamiqueHostScopeeParEnv(t *testing.T) {
	var vuRequete string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vuRequete = r.URL.String()
		_ = json.NewEncoder(w).Encode([]map[string]string{{"host": "db-01"}})
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, srv.URL)

	var out bytes.Buffer
	err := commandeInterneComplete(t.Context(),
		[]string{"host", "forget", "--env", "prod", "--host", ""}, &out)
	if err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if !strings.Contains(vuRequete, "environment=prod") {
		t.Errorf("l'environnement n'a pas été transmis : %s", vuRequete)
	}
	if l := lignes(t, &out); !contient(l, "db-01") {
		t.Errorf("db-01 absent des candidats : %v", l)
	}
}

func TestInterneCompleteFlagInconnuNAPasDeSourceEtNeCraquePas(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvServeur, "http://exemple.invalide")

	var out bytes.Buffer
	err := commandeInterneComplete(t.Context(), []string{"submit", "--playbook", ""}, &out)
	if err != nil {
		t.Fatalf("commandeInterneComplete: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("candidats inattendus pour --playbook : %q", out.String())
	}
}
