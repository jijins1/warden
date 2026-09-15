// Étage 1 de l'ADR-0010 : un run qui n'exécute rien doit DIRE pourquoi.
//
// Le défaut que ce fichier verrouille a été constaté en production : un run
// visant un playbook inexistant se terminait sur `run <ref> : failed`, sans une
// seule ligne de plus, ni dans `warden logs` ni dans l'interface. Le message
// d'Ansible existait pourtant — dans une trame `verbose` que personne
// n'affichait.

package exec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/exec"
)

// messageDErreur rend le texte du premier event de diagnostic, vide s'il n'y en
// a pas.
func messageDErreur(t *testing.T, evs []events.Event) string {
	t.Helper()
	for _, e := range evs {
		var p map[string]string
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		if msg, ok := p[exec.ClePayloadErreur]; ok {
			if e.State != events.StateFailed {
				t.Errorf("un diagnostic doit être `failed`, obtenu %q", e.State)
			}
			return msg
		}
	}
	return ""
}

// TestUnPlaybookAbsentEstDitAvantDExecuterQuoiQueCeSoit est le cas de l'incident
// de production : une faute de frappe sur le nom du playbook.
//
// Le contrôle a lieu AVANT le lancement — d'où la vérification que le faux
// `ansible-runner` n'a jamais été appelé. Payer le démarrage d'un interpréteur
// Python pour apprendre qu'un fichier manque n'apporte rien, et le message
// d'Ansible est moins précis que celui-ci.
func TestUnPlaybookAbsentEstDitAvantDExecuterQuoiQueCeSoit(t *testing.T) {
	entree := fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	if err := os.Remove(filepath.Join(cfg.Workspace, cfg.Playbook)); err != nil {
		t.Fatalf("retrait du playbook : %v", err)
	}

	evs, statut, err := lancer(t, cfg)
	// PAS une erreur : le pilotage n'a pas échoué, c'est le run qui n'avait
	// rien à exécuter. Une erreur ferait échouer le rapport du run lui-même.
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != exec.StatutEchoue {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutEchoue)
	}

	msg := messageDErreur(t, evs)
	if msg == "" {
		t.Fatal("aucun diagnostic : le run reste muet, c'est le défaut d'origine")
	}
	if !strings.Contains(msg, cfg.Playbook) {
		t.Errorf("le diagnostic ne nomme pas le chemin fautif : %q", msg)
	}

	if _, err := os.Stat(entree); err == nil {
		t.Error("ansible-runner a été lancé alors que le playbook manquait")
	}
}

// TestUnInventaireAbsentEstDitAussi : même raisonnement, et l'ad-hoc en dépend
// entièrement — il n'a pas de playbook à vérifier.
func TestUnInventaireAbsentEstDitAussi(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	cfg := config(t)
	if err := os.Remove(filepath.Join(cfg.Workspace, cfg.Inventory)); err != nil {
		t.Fatalf("retrait de l'inventaire : %v", err)
	}

	evs, statut, err := lancer(t, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != exec.StatutEchoue {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutEchoue)
	}
	if msg := messageDErreur(t, evs); !strings.Contains(msg, cfg.Inventory) {
		t.Errorf("le diagnostic ne nomme pas l'inventaire : %q", msg)
	}
}

// TestUnEchecSansAucuneTacheEstExplique couvre tout ce que le pré-contrôle ne
// peut PAS voir : erreur de syntaxe, variable indéfinie, inventaire vide.
//
// La fixture est la sortie RÉELLE d'`ansible-runner` sur un playbook
// introuvable, relevée dans l'image du runner. Elle porte le cas tel qu'il se
// présente : `failed`, aucune tâche, et le message uniquement dans `verbose`.
func TestUnEchecSansAucuneTacheEstExplique(t *testing.T) {
	fauxRunner(t, "flux-playbook-absent.jsonl", "")

	// Les fichiers EXISTENT : le pré-contrôle passe, et c'est bien le repli sur
	// le flux qu'on éprouve ici, pas le contrôle précédent.
	evs, statut, err := lancer(t, config(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != exec.StatutEchoue {
		t.Errorf("statut = %q, attendu %q", statut, exec.StatutEchoue)
	}

	msg := messageDErreur(t, evs)
	if !strings.Contains(msg, "could not be found") {
		t.Errorf("le diagnostic ne reprend pas le message d'Ansible : %q", msg)
	}
}

// TestUnRunOrdinaireNeProduitAucunDiagnostic est la non-régression qui compte.
//
// Un run qui RÉUSSIT émet déjà des trames `verbose` — MESURÉ : cinq, pour le
// seul avertissement de découverte d'interpréteur. Si le repli se déclenchait
// là, chaque run porterait un faux diagnostic.
func TestUnRunOrdinaireNeProduitAucunDiagnostic(t *testing.T) {
	fauxRunner(t, "flux-succes.jsonl", "")

	evs, statut, err := lancer(t, config(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != exec.StatutReussi {
		t.Fatalf("statut = %q, attendu %q", statut, exec.StatutReussi)
	}
	if msg := messageDErreur(t, evs); msg != "" {
		t.Errorf("diagnostic parasite sur un run réussi : %q", msg)
	}
}

// TestLeDiagnosticEstAssaini : ce message atteint un TERMINAL et un navigateur,
// et il vient du domaine hostile de l'ADR-0011.
func TestLeDiagnosticEstAssaini(t *testing.T) {
	cas := []struct {
		nom     string
		entree  string
		absent  string
		present string
	}{
		{
			// Une séquence ANSI complète, pas seulement l'échappement : retirer
			// l'ESC seul laisserait « [31m » à l'écran.
			nom:     "séquence ANSI",
			entree:  "avant \x1b[31mrouge\x1b[0m après",
			absent:  "[31m",
			present: "rouge",
		},
		{
			nom:     "retour chariot et effacement de ligne",
			entree:  "ligne\rrecouvrement\x1b[2K",
			absent:  "\r",
			present: "recouvrement",
		},
		{
			nom:     "caractères de contrôle",
			entree:  "a\x00b\x07c",
			absent:  "\x07",
			present: "a b c",
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			var p map[string]string
			if err := json.Unmarshal(exec.EventErreur(c.entree).Payload, &p); err != nil {
				t.Fatalf("payload illisible : %v", err)
			}
			msg := p[exec.ClePayloadErreur]
			if strings.Contains(msg, c.absent) {
				t.Errorf("%q subsiste dans %q", c.absent, msg)
			}
			if !strings.Contains(msg, c.present) {
				t.Errorf("%q a disparu de %q", c.present, msg)
			}
		})
	}
}

// TestLeDiagnosticEstBorneEtLeDit : la troncature est VISIBLE, contrairement à
// celle de `borne`. Un message amputé en silence se lit comme un message
// complet.
func TestLeDiagnosticEstBorneEtLeDit(t *testing.T) {
	var p map[string]string
	if err := json.Unmarshal(exec.EventErreur(strings.Repeat("a", 9000)).Payload, &p); err != nil {
		t.Fatalf("payload illisible : %v", err)
	}
	msg := p[exec.ClePayloadErreur]

	if len([]rune(msg)) > 3000 {
		t.Errorf("message de %d runes : la borne ne tient pas", len([]rune(msg)))
	}
	if !strings.Contains(msg, "tronqué") {
		t.Errorf("troncature silencieuse : %q", msg[max(0, len(msg)-40):])
	}
}

// TestLeDiagnosticResteDuJSONValide : le texte peut venir d'ansible-runner,
// donc d'un attaquant. Le coller dans un gabarit le laisserait sortir du champ.
func TestLeDiagnosticResteDuJSONValide(t *testing.T) {
	e := exec.EventErreur(`"} , "injecte": "oui`)
	if !json.Valid(e.Payload) {
		t.Fatalf("payload invalide : %s", e.Payload)
	}
	var p map[string]string
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("payload illisible : %v", err)
	}
	if len(p) != 1 {
		t.Errorf("le payload porte %d clés, attendu 1 : %v", len(p), p)
	}
}
