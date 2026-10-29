// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package core_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// lecteurFactice rend ce qu'on lui a posé, ou l'erreur qu'on lui a posée.
type lecteurFactice struct {
	contenu []byte
	err     error

	// vus enregistre les appels, pour vérifier que le cœur n'invente pas de
	// dépôt ni de commit.
	vus []string
}

func (l *lecteurFactice) ReadProjectConfig(_ context.Context, repo, sha string) ([]byte, error) {
	l.vus = append(l.vus, repo+"@"+sha)
	if l.err != nil {
		return nil, l.err
	}
	return l.contenu, nil
}

const shaDeTest = "0123456789abcdef0123456789abcdef01234567"

func TestProjectConfigForLitLeCommit(t *testing.T) {
	l := &lecteurFactice{contenu: []byte("default_env: staging\ninventory: inventories/{{ env }}\n")}

	p, err := core.ProjectConfigFor(t.Context(), l, "depot", shaDeTest, core.ConfigFacultative)
	if err != nil {
		t.Fatalf("ProjectConfigFor: %v", err)
	}
	if p.DefaultEnv != "staging" {
		t.Errorf("default_env = %q", p.DefaultEnv)
	}
	// Le template n'est PAS résolu à ce stade : l'environnement effectif se
	// décide plus tard, et le résoudre ici figerait un choix qui n'est pas fait.
	if p.Inventory != "inventories/{{ env }}" {
		t.Errorf("inventory = %q, le template doit être intact", p.Inventory)
	}
	// Le cœur demande le dépôt et le commit qu'on lui a donnés, et rien d'autre.
	if len(l.vus) != 1 || l.vus[0] != "depot@"+shaDeTest {
		t.Errorf("appels = %v", l.vus)
	}
}

// L'absence du fichier est un état ORDINAIRE tant que le refus de l'ADR-0009
// n'est pas activé : un dépôt qui ne l'a pas adopté continue de tourner.
func TestProjectConfigForTolereLAbsence(t *testing.T) {
	l := &lecteurFactice{err: fmt.Errorf("pas là : %w", core.ErrNoProjectConfig)}

	p, err := core.ProjectConfigFor(t.Context(), l, "depot", shaDeTest, core.ConfigFacultative)
	if err != nil {
		t.Fatalf("l'absence ne doit pas être une erreur : %v", err)
	}
	if p.DefaultEnv != "" || p.Inventory != "" {
		t.Errorf("l'absence doit donner une configuration nulle, donne %+v", p)
	}
}

// Un lecteur nil est la tolérance de mise en place : un déploiement qui n'a pas
// branché ce chemin fait tourner ses runs comme avant.
func TestProjectConfigForSansLecteur(t *testing.T) {
	p, err := core.ProjectConfigFor(t.Context(), nil, "depot", shaDeTest, core.ConfigFacultative)
	if err != nil {
		t.Fatalf("un lecteur absent ne doit pas être une erreur : %v", err)
	}
	if p.Server != "" {
		t.Errorf("configuration attendue nulle, obtenue %+v", p)
	}
}

// Une panne de transport n'est PAS l'absence. Les confondre ferait démarrer un
// run sans sa configuration parce que le rôle `git` n'a pas répondu — et ce run
// s'exécuterait avec des valeurs par défaut que personne n'a écrites.
func TestProjectConfigForNeTolerePasUnePanne(t *testing.T) {
	l := &lecteurFactice{err: errors.New("connexion refusée")}

	if _, err := core.ProjectConfigFor(t.Context(), l, "depot", shaDeTest, core.ConfigFacultative); err == nil {
		t.Fatal("une panne de lecture doit faire échouer la résolution")
	}
}

// Le YAML vient d'un commit, donc d'une entrée non fiable : un fichier illisible
// doit faire échouer, en nommant ce qui a été refusé.
func TestProjectConfigForRefuseUnFichierInvalide(t *testing.T) {
	l := &lecteurFactice{contenu: []byte("champ_inexistant: 1\n")}

	_, err := core.ProjectConfigFor(t.Context(), l, "depot", shaDeTest, core.ConfigFacultative)
	if err == nil {
		t.Fatal("un champ inconnu doit faire échouer la résolution")
	}
	if !strings.Contains(err.Error(), "champ_inexistant") {
		t.Errorf("le message doit nommer la clé refusée : %v", err)
	}
}

func TestProjectConfigForExigeDepotEtCommit(t *testing.T) {
	l := &lecteurFactice{}

	for _, c := range []struct{ repo, sha string }{{"", shaDeTest}, {"depot", ""}} {
		if _, err := core.ProjectConfigFor(t.Context(), l, c.repo, c.sha, core.ConfigFacultative); err == nil {
			t.Errorf("repo=%q sha=%q doit être refusé", c.repo, c.sha)
		}
	}
	if len(l.vus) != 0 {
		t.Errorf("aucun appel ne devait partir, %v ont eu lieu", l.vus)
	}
}

// Le refus de l'ADR-0009, une fois l'exigence activée. Le message doit NOMMER
// le fichier : l'utilisateur n'a aucun moyen de deviner ce nom.
func TestProjectConfigForRefuseLAbsenceQuandExigee(t *testing.T) {
	l := &lecteurFactice{err: fmt.Errorf("pas là : %w", core.ErrNoProjectConfig)}

	_, err := core.ProjectConfigFor(t.Context(), l, "depot", shaDeTest, core.ConfigObligatoire)
	if err == nil {
		t.Fatal("l'absence doit être refusée quand la configuration est exigée")
	}
	if !strings.Contains(err.Error(), ".warden.yml") {
		t.Errorf("le message doit nommer le fichier : %v", err)
	}
}

// Un lecteur nil tolère MÊME sous exigence : un déploiement sans rôle `git`
// branché sur ce chemin ne peut pas poser la question, et refuser tous ses runs
// sur une question qu'on ne peut pas poser serait un refus mensonger.
func TestProjectConfigForSansLecteurTolereMemeSousExigence(t *testing.T) {
	if _, err := core.ProjectConfigFor(t.Context(), nil, "depot", shaDeTest,
		core.ConfigObligatoire); err != nil {
		t.Fatalf("un lecteur absent ne doit pas refuser : %v", err)
	}
}

// Le défaut du type zéro TOLÈRE : un déploiement qui ne se prononce pas continue
// de faire tourner ses runs. C'est ce qui rend la mise à jour non cassante.
func TestExigenceParDefautTolere(t *testing.T) {
	var defaut core.ExigenceConfig
	if defaut != core.ConfigFacultative {
		t.Error("le zéro d'ExigenceConfig doit tolérer, sinon une mise à jour arrête les runs")
	}
}
