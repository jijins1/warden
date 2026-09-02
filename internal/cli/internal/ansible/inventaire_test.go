package ansible_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/ansible"
)

func ecrire(t *testing.T, dir, nom, contenu string) string {
	t.Helper()
	chemin := filepath.Join(dir, nom)
	if err := os.WriteFile(chemin, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s: %v", nom, err)
	}
	return chemin
}

func fichier(t *testing.T, contenu string) string {
	t.Helper()
	return ecrire(t, t.TempDir(), "hosts.yml", contenu)
}

func parNom(t *testing.T, machines []ansible.Machine, nom string) ansible.Machine {
	t.Helper()
	for _, m := range machines {
		if m.Nom == nom {
			return m
		}
	}
	t.Fatalf("machine %s absente de %+v", nom, machines)
	return ansible.Machine{}
}

// L'inventaire de production, dans sa forme réelle : des groupes, des comptes
// qui diffèrent d'une machine à l'autre, et une machine sans `ansible_host`.
func TestLireUnInventaireDeFlotte(t *testing.T) {
	f := fichier(t, `
all:
  vars:
    ansible_user: ruokki
  children:
    cluster:
      hosts:
        kodi:
          ansible_host: 192.168.16.2
        muzi:
          ansible_host: 192.168.16.3
        tiny:
    impression:
      hosts:
        mainsail:
          ansible_host: 192.168.16.20
    distant:
      vars:
        ansible_user: root
      hosts:
        ionos:
          ansible_host: 192.168.18.10
          ansible_port: 2222
`)

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines) != 5 {
		t.Fatalf("%d machines, attendu 5 : %+v", len(machines), machines)
	}

	// Trié par nom, toujours : une commande qui énumère une flotte ne doit pas
	// changer d'ordre d'un appel à l'autre.
	if machines[0].Nom != "ionos" || machines[4].Nom != "tiny" {
		t.Errorf("ordre = %s … %s", machines[0].Nom, machines[4].Nom)
	}

	kodi := parNom(t, machines, "kodi")
	if kodi.Adresse != "192.168.16.2" || kodi.Login != "ruokki" || kodi.Deduite {
		t.Errorf("kodi = %+v", kodi)
	}
	if len(kodi.Groupes) != 1 || kodi.Groupes[0] != "cluster" {
		t.Errorf("groupes de kodi = %v", kodi.Groupes)
	}

	// Le groupe le plus proche l'emporte sur `all`.
	ionos := parNom(t, machines, "ionos")
	if ionos.Login != "root" || ionos.Port != 2222 {
		t.Errorf("ionos = %+v", ionos)
	}

	// Sans `ansible_host`, le NOM sert d'adresse — et la déduction est retenue,
	// pour que la commande puisse la montrer avant de se connecter.
	tiny := parNom(t, machines, "tiny")
	if tiny.Adresse != "tiny" || !tiny.Deduite {
		t.Errorf("tiny = %+v", tiny)
	}
	if tiny.Login != "ruokki" {
		t.Errorf("tiny n'hérite pas d'all : %+v", tiny)
	}
}

// `all` est le groupe implicite d'Ansible : il ne nomme pas une appartenance
// qu'un filtre pourrait viser.
func TestAllNEstPasUnGroupe(t *testing.T) {
	f := fichier(t, "all:\n  hosts:\n    web-01:\n      ansible_host: 10.0.0.1\n")

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines[0].Groupes) != 0 {
		t.Errorf("groupes = %v, attendu aucun", machines[0].Groupes)
	}
}

// Un inventaire sans `all`, avec des groupes au premier niveau, est une forme
// courante qu'Ansible accepte.
func TestGroupesAuPremierNiveau(t *testing.T) {
	f := fichier(t, `
web:
  hosts:
    web-01:
      ansible_host: 10.0.0.1
db:
  hosts:
    db-01:
      ansible_host: 10.0.0.2
`)

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("machines = %+v", machines)
	}
	if parNom(t, machines, "web-01").Groupes[0] != "web" {
		t.Errorf("groupes = %+v", machines)
	}
}

// Une machine dans deux groupes reste UNE machine, dont on accumule les groupes.
func TestUneMachineDansDeuxGroupes(t *testing.T) {
	f := fichier(t, `
web:
  hosts:
    web-01:
      ansible_host: 10.0.0.1
prod:
  hosts:
    web-01:
`)

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines) != 1 {
		t.Fatalf("%d machines, attendu 1 : %+v", len(machines), machines)
	}
	m := machines[0]
	if len(m.Groupes) != 2 || m.Groupes[0] != "prod" || m.Groupes[1] != "web" {
		t.Errorf("groupes = %v, attendus [prod web] triés", m.Groupes)
	}
	// L'adresse de la première occurrence est conservée : la seconde n'en donne
	// pas, et la perdre aurait fait retomber la machine sur son nom.
	if m.Adresse != "10.0.0.1" || m.Deduite {
		t.Errorf("machine = %+v", m)
	}
}

// Les groupes s'imbriquent, et l'héritage suit.
func TestGroupesImbriques(t *testing.T) {
	f := fichier(t, `
all:
  vars:
    ansible_user: ansible
  children:
    europe:
      vars:
        ansible_port: 2222
      children:
        paris:
          hosts:
            web-01:
              ansible_host: 10.0.0.1
`)

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	m := machines[0]
	if m.Login != "ansible" || m.Port != 2222 {
		t.Errorf("héritage = %+v", m)
	}
	if len(m.Groupes) != 1 || m.Groupes[0] != "paris" {
		t.Errorf("groupes = %v", m.Groupes)
	}
}

// Les alias d'Ansible 1.x vivent encore dans des inventaires réels.
func TestAliasHerites(t *testing.T) {
	f := fichier(t, `
web:
  hosts:
    web-01:
      ansible_ssh_host: 10.0.0.1
      ansible_ssh_user: deploy
      ansible_ssh_port: 2200
`)

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	m := machines[0]
	if m.Adresse != "10.0.0.1" || m.Login != "deploy" || m.Port != 2200 {
		t.Errorf("machine = %+v", m)
	}
}

// Un port peut s'écrire en chaîne : `ansible_port: "22"` est courant.
func TestPortEnChaine(t *testing.T) {
	f := fichier(t, "web:\n  hosts:\n    web-01:\n      ansible_host: 10.0.0.1\n      ansible_port: \"2222\"\n")

	machines, err := ansible.Lire(f)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if machines[0].Port != 2222 {
		t.Errorf("port = %d", machines[0].Port)
	}
}

func TestRefus(t *testing.T) {
	cas := map[string]struct {
		contenu string
		dit     string
	}{
		"jinja dans l'adresse": {
			"web:\n  hosts:\n    web-01:\n      ansible_host: \"{{ vip }}\"\n",
			"Jinja",
		},
		"jinja dans le compte": {
			"web:\n  hosts:\n    web-01:\n      ansible_host: 10.0.0.1\n      ansible_user: \"{{ compte }}\"\n",
			"Jinja",
		},
		"adresse non scalaire": {
			"web:\n  hosts:\n    web-01:\n      ansible_host: [10.0.0.1]\n",
			"chaîne est attendue",
		},
		"port hors bornes": {
			"web:\n  hosts:\n    web-01:\n      ansible_host: 10.0.0.1\n      ansible_port: 70000\n",
			"hors bornes",
		},
		"port illisible": {
			"web:\n  hosts:\n    web-01:\n      ansible_host: 10.0.0.1\n      ansible_port: \"deux\"\n",
			"entier est attendu",
		},
		"nom impropre à servir d'adresse": {
			"web:\n  hosts:\n    web_01:\n",
			"ansible_host",
		},
		"format INI": {
			"[web]\nweb-01 ansible_host=10.0.0.1\n",
			"INI",
		},
	}

	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			_, err := ansible.Lire(fichier(t, c.contenu))
			if err == nil {
				t.Fatal("accepté à tort")
			}
			if !strings.Contains(err.Error(), c.dit) {
				t.Errorf("l'erreur ne dit pas %q : %v", c.dit, err)
			}
			// Toute erreur NOMME la machine ou le fichier : c'est la raison
			// d'être d'une lecture locale.
			if !strings.Contains(err.Error(), "web") && !strings.Contains(err.Error(), "hosts.yml") {
				t.Errorf("l'erreur ne situe rien : %v", err)
			}
		})
	}
}

// Un répertoire est le cas COURANT : `inventory:` vaut le plus souvent
// `inventories/{{ env }}`.
func TestLireUnRepertoire(t *testing.T) {
	dir := t.TempDir()
	ecrire(t, dir, "cluster.yml", "cluster:\n  hosts:\n    kodi:\n      ansible_host: 10.0.0.1\n")
	ecrire(t, dir, "distant.yaml", "distant:\n  hosts:\n    ionos:\n      ansible_host: 10.0.0.2\n")
	// Ni lu ni gênant : ce n'est pas un document d'inventaire.
	ecrire(t, dir, "README.md", "# rien à voir\n")
	// Les sous-répertoires ne sont pas suivis — c'est ce qui exclut `group_vars/`.
	if err := os.MkdirAll(filepath.Join(dir, "group_vars"), 0o750); err != nil {
		t.Fatal(err)
	}
	ecrire(t, filepath.Join(dir, "group_vars"), "all.yml", "ansible_user: jamais-lu\n")

	machines, err := ansible.Lire(dir)
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("machines = %+v", machines)
	}
	// La conséquence de la frontière, vérifiée plutôt que supposée : un
	// `ansible_user` de `group_vars/` est invisible ici.
	if machines[0].Login != "" {
		t.Errorf("group_vars a été lu : %+v", machines[0])
	}
}

func TestRepertoireSansInventaire(t *testing.T) {
	dir := t.TempDir()
	ecrire(t, dir, "README.md", "# rien\n")
	if _, err := ansible.Lire(dir); err == nil {
		t.Fatal("un répertoire sans inventaire a été accepté")
	}
}

func TestCheminIntrouvable(t *testing.T) {
	_, err := ansible.Lire(filepath.Join(t.TempDir(), "absent.yml"))
	if err == nil || !strings.Contains(err.Error(), "introuvable") {
		t.Fatalf("erreur = %v", err)
	}
}

// Le plafond existe pour qu'un chemin erroné échoue en le disant.
func TestDocumentTropGrand(t *testing.T) {
	f := fichier(t, "web:\n  hosts:\n"+strings.Repeat("    h"+strings.Repeat("x", 40)+":\n", 30000))
	if _, err := ansible.Lire(f); err == nil || !strings.Contains(err.Error(), "dépasse") {
		t.Fatalf("erreur = %v", err)
	}
}

// Un inventaire vide n'est pas une erreur de forme : c'est zéro machine.
func TestInventaireVide(t *testing.T) {
	machines, err := ansible.Lire(fichier(t, "# rien\n"))
	if err != nil {
		t.Fatalf("Lire: %v", err)
	}
	if len(machines) != 0 {
		t.Errorf("machines = %+v", machines)
	}
}
