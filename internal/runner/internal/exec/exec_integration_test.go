//go:build integration

// Étage 2 de l'ADR-0010 : le VRAI `ansible-runner`, dans l'image du runner.
//
// Les fixtures de l'étage 1 sont une photographie : elles disent ce qu'une
// version a produit un jour. Ce fichier dit que la couture tient encore avec
// l'`ansible-runner` réellement installé — c'est ce qui fera parler le jour où
// l'image bouge, plutôt que de découvrir l'écart sur un run de production.
//
// Il se saute proprement là où le binaire est absent : le poste de
// développement n'a pas Python, et l'exiger ferait échouer l'étage entier pour
// une raison qui n'a rien à voir avec ce qu'il teste.
//
// Pour l'exécuter dans l'image :
//
//	go test -tags integration -c -o /tmp/exec.test ./internal/runner/internal/exec
//	docker run --rm --entrypoint /t/exec.test -v /tmp/exec.test:/t/exec.test:ro \
//	  --tmpfs /workspace:rw,uid=65534,gid=65534 warden-runner:dev -test.v

package exec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/events"
	wexec "github.com/jijins1/warden/internal/runner/internal/exec"
)

const playbookReel = `- name: verification warden
  hosts: all
  gather_facts: false
  tasks:
    - name: rien ne change
      ansible.builtin.command: /bin/true
      changed_when: false
    - name: un fichier change
      ansible.builtin.copy: { content: "bonjour\n", dest: "{{ cible }}" }
    - name: une tache sautee
      ansible.builtin.debug: msg=jamais
      when: false
`

func TestLeVraiAnsibleRunnerEstPilote(t *testing.T) {
	if _, err := exec.LookPath(wexec.Binaire); err != nil {
		t.Skipf("%s absent de ce poste", wexec.Binaire)
	}

	// Sous `/workspace` quand il existe — c'est le volume inscriptible du pod,
	// et le seul endroit où Ansible peut travailler avec une racine en lecture
	// seule (§7). Ailleurs, un répertoire temporaire suffit.
	base := "/workspace"
	if _, err := os.Stat(base); err != nil {
		base = t.TempDir()
	}
	espace := filepath.Join(base, "src")
	if err := os.MkdirAll(espace, 0o750); err != nil {
		t.Fatalf("préparation de l'arbre : %v", err)
	}

	cible := filepath.Join(base, "temoin.txt")
	ecrire(t, filepath.Join(espace, "play.yml"), playbookReel)
	ecrire(t, filepath.Join(espace, "hosts"), "localhost ansible_connection=local\n")

	r, err := wexec.New(wexec.Config{
		Workspace:      espace,
		PrivateDataDir: filepath.Join(base, "pdd"),
		Playbook:       "play.yml",
		Inventory:      "hosts",
		Flags:          []string{"--diff", "--extra-vars=cible=" + cible},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var evs []events.Event
	statut, err := r.Run(t.Context(), func(e events.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != wexec.StatutReussi {
		t.Fatalf("statut = %q, attendu %q", statut, wexec.StatutReussi)
	}

	// Chaque event doit franchir la validation du schéma : un event produit ici
	// et refusé à l'ingestion serait perdu au dernier mètre.
	for i := range evs {
		evs[i].Seq = int64(i)
		if err := evs[i].Validate(); err != nil {
			t.Errorf("event %d refusé par le schéma : %v", i, err)
		}
	}

	n := map[events.State]int{}
	var avecDiff int
	for _, e := range evs {
		n[e.State]++
		if e.Diff != nil {
			avecDiff++
		}
	}
	for _, etat := range []events.State{events.StateOK, events.StateChanged, events.StateSkipped} {
		if n[etat] == 0 {
			t.Errorf("aucun event %q : le mapping de l'ADR-0021 a changé sous nos pieds", etat)
		}
	}
	// Le diff sort du payload et part dans sa table séparée (§6.5). S'il
	// n'arrive plus, c'est que son emplacement a bougé dans ansible-core.
	if avecDiff == 0 {
		t.Error("aucun diff extrait alors que --diff est actif")
	}

	// La tâche a bien eu lieu : sans cela, un flux d'events parfaitement formé
	// pourrait décrire un playbook qui n'a rien fait.
	if _, err := os.Stat(cible); err != nil {
		t.Errorf("le playbook n'a pas écrit son témoin : %v", err)
	}
}

func ecrire(t *testing.T, chemin, contenu string) {
	t.Helper()
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s : %v", chemin, err)
	}
}

const playbookBecome = `- name: verification become warden
  hosts: all
  gather_facts: false
  tasks:
    - name: qui suis-je apres elevation
      ansible.builtin.command: id -un
      become: true
      changed_when: false
      register: eleve
    - name: temoin
      ansible.builtin.copy: { content: "{{ eleve.stdout }}\n", dest: "{{ cible }}" }
`

// Le mot de passe d'élévation contre le VRAI ansible-runner.
//
// Ce que ce test établit et que l'étage 1 ne peut pas : que `extravars` élève
// réellement, ET que le flux d'events reste STRUCTURÉ. C'est la seconde moitié
// qui compte — `passwords` en kwargs élève aussi, mais fait basculer
// ansible-runner sous pexpect et l'audit disparaît en silence.
//
// Il se saute là où l'élévation n'est pas possible : le poste de développement
// n'a pas de sudo sans mot de passe pour l'utilisateur de test.
func TestLeMotDePasseDElevationEleveVraimentSansCasserLeFlux(t *testing.T) {
	if _, err := exec.LookPath(wexec.Binaire); err != nil {
		t.Skipf("%s absent de ce poste", wexec.Binaire)
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		t.Skip("sudo absent : l'élévation n'est pas vérifiable ici")
	}
	motDePasse := os.Getenv("WARDEN_TEST_BECOME_PASSWORD")
	if motDePasse == "" {
		t.Skip("WARDEN_TEST_BECOME_PASSWORD non définie")
	}

	base := t.TempDir()
	espace := filepath.Join(base, "src")
	if err := os.MkdirAll(espace, 0o750); err != nil {
		t.Fatalf("préparation : %v", err)
	}
	cible := filepath.Join(base, "eleve.txt")
	ecrire(t, filepath.Join(espace, "play.yml"), playbookBecome)
	ecrire(t, filepath.Join(espace, "hosts"), "localhost ansible_connection=local\n")

	r, err := wexec.New(wexec.Config{
		Workspace:      espace,
		PrivateDataDir: filepath.Join(base, "pdd"),
		Playbook:       "play.yml",
		Inventory:      "hosts",
		Flags:          []string{"--extra-vars=cible=" + cible},
		BecomePassword: []byte(motDePasse),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var evs []events.Event
	statut, err := r.Run(t.Context(), func(e events.Event) { evs = append(evs, e) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if statut != wexec.StatutReussi {
		t.Fatalf("statut = %q", statut)
	}

	// L'élévation a bien eu lieu.
	brut, err := os.ReadFile(cible) // #nosec G304 -- chemin construit par le test
	if err != nil {
		t.Fatalf("le playbook n'a pas écrit son témoin : %v", err)
	}
	if strings.TrimSpace(string(brut)) != "root" {
		t.Errorf("l'élévation a rendu %q, attendu root", strings.TrimSpace(string(brut)))
	}

	// ET le flux est resté structuré : c'est ce que `passwords` détruirait.
	structures := 0
	for _, e := range evs {
		if e.State != events.StateUnknown {
			structures++
		}
	}
	if structures == 0 {
		t.Error("aucun event structuré : le flux d'audit a été perdu")
	}

	// Le mot de passe n'apparaît dans AUCUN payload.
	for i, e := range evs {
		if strings.Contains(string(e.Payload), motDePasse) {
			t.Fatalf("event %d : le mot de passe d'élévation est dans un payload", i)
		}
	}
}
