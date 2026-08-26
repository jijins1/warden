// Étage 1 de l'ADR-0010 : ni Docker, ni cluster, ni base.
//
// Test INTERNE au paquet : `poserLesVariables` n'est pas exportée, et
// l'exercer à travers `Run` demanderait le binaire `ansible-runner`, ce qui
// ferait glisser ce test d'étage.
package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Les variables transmises atterrissent dans `env/envvars`, qui est le mécanisme
// d'ansible-runner — pas un fichier à nous.
func TestPoserLesVariablesEcritEnvvars(t *testing.T) {
	pdd := t.TempDir()
	r, err := New(Config{
		Workspace:      t.TempDir(),
		PrivateDataDir: pdd,
		Playbook:       "site.yml",
		ForwardEnv: map[string]string{
			"VAULT_ADDR": "https://vault",
			// Une valeur tordue : sans échappement JSON, le fichier deviendrait
			// illisible et l'erreur parlerait de YAML plutôt que de la variable.
			"BIZARRE": "avec \"guillemet\" et\nsaut",
		},
	})
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	if err := r.poserLesVariables(); err != nil {
		t.Fatalf("poserLesVariables : %v", err)
	}

	chemin := filepath.Join(pdd, "env", "envvars")
	info, err := os.Stat(chemin)
	if err != nil {
		t.Fatalf("envvars absent : %v", err)
	}
	// 0600 posé par NOUS : s'en remettre au comportement d'ansible-runner pour
	// la protection d'un secret, c'est le perdre le jour où il change.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions %v, veut 0600", info.Mode().Perm())
	}

	brut, err := os.ReadFile(chemin) // #nosec G304 -- chemin de test
	if err != nil {
		t.Fatalf("lecture : %v", err)
	}
	var recu map[string]string
	if err := json.Unmarshal(brut, &recu); err != nil {
		t.Fatalf("envvars illisible : %v", err)
	}
	if recu["VAULT_ADDR"] != "https://vault" {
		t.Errorf("VAULT_ADDR = %q", recu["VAULT_ADDR"])
	}
	if recu["BIZARRE"] != "avec \"guillemet\" et\nsaut" {
		t.Errorf("BIZARRE mal échappée : %q", recu["BIZARRE"])
	}
}

// Sans variables, aucun fichier : un `envvars` vide ferait travailler
// ansible-runner sur un contenu inexistant à chaque run.
func TestPoserLesVariablesNEcritRienSansVariables(t *testing.T) {
	pdd := t.TempDir()
	r, err := New(Config{Workspace: t.TempDir(), PrivateDataDir: pdd, Playbook: "site.yml"})
	if err != nil {
		t.Fatalf("New : %v", err)
	}
	if err := r.poserLesVariables(); err != nil {
		t.Fatalf("poserLesVariables : %v", err)
	}
	if _, err := os.Stat(filepath.Join(pdd, "env", "envvars")); !os.IsNotExist(err) {
		t.Error("aucun envvars ne doit être écrit quand il n'y a rien à transmettre")
	}
}
