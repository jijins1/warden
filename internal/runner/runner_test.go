// Test interne : `lireJeton` n'est pas exportée, et ce qu'elle porte — une
// contrainte de chemin — est un contrôle de sécurité qui mérite d'être vérifié
// plutôt que relu.

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLeCheminDuJetonEstContraintAuMontage est la propriété qui compte.
//
// Ce binaire tourne dans le MÊME conteneur que le playbook : tout ce qui vient de
// son environnement est, à terme, influençable par du code arbitraire (§2). Lire
// un chemin quelconque ferait de cette fonction une primitive de lecture de
// fichier au service de qui contrôle l'environnement — utilisable, par exemple,
// pour faire remonter le contenu d'un fichier dans un message d'erreur.
func TestLeCheminDuJetonEstContraintAuMontage(t *testing.T) {
	// Un fichier bien réel, hors du montage : le refus ne doit pas dépendre de
	// son absence.
	hors := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(hors, []byte("contenu qui ne doit pas sortir"), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}

	cas := []string{
		hors,
		"/etc/passwd",
		"/proc/self/environ",
		MountDir + "/../../etc/passwd",
		MountDir, // le répertoire lui-même, pas un fichier dedans
		"",
	}

	for _, chemin := range cas {
		t.Run(chemin, func(t *testing.T) {
			jeton, err := lireJeton(chemin)
			if err == nil {
				t.Fatalf("chemin accepté, jeton lu : %q", jeton)
			}
			// Et le contenu du fichier visé ne fuite pas dans l'erreur.
			if strings.Contains(err.Error(), "ne doit pas sortir") {
				t.Errorf("le contenu du fichier fuite dans l'erreur : %v", err)
			}
		})
	}
}

// TestUnJetonVideEstRefuse : un runner sans jeton ne peut rien faire, et le
// laisser continuer produirait un échec plus loin, avec un message parlant
// d'autre chose.
func TestUnJetonVideEstRefuse(t *testing.T) {
	// Le montage réel n'existe pas sur la machine de test ; on vérifie donc que
	// la contrainte s'applique AVANT la lecture, ce qui est justement l'ordre
	// voulu.
	if _, err := lireJeton(MountDir + "/token"); err == nil {
		t.Fatal("un jeton absent a été accepté")
	}
}

// TestLeRunnerNaccepteAucunArgument.
//
// La ligne de commande d'un pod est visible et modifiable par qui édite la
// podspec. Tout ce que le runner doit savoir vient du canal de dispatch, dont
// l'authenticité est établie par son jeton — pas d'argv.
func TestLeRunnerNaccepteAucunArgument(t *testing.T) {
	err := Run(t.Context(), []string{"--playbook", "site.yml"})
	if err == nil {
		t.Fatal("des arguments ont été acceptés")
	}
	if !strings.Contains(err.Error(), "aucun argument") {
		t.Errorf("refusé pour une autre raison : %v", err)
	}
}
