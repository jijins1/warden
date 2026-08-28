// Étage 1 de l'ADR-0010 : ni Docker, ni cluster, ni base. Si ce fichier se met
// à en avoir besoin, c'est qu'un test a glissé d'étage.
package config

import (
	"strings"
	"testing"
)

// exemple reprend le fichier du §5.4 tel qu'il y est écrit, augmenté de
// l'inventaire templatisé. Il sert de témoin : si le schéma cesse de l'accepter,
// c'est la spec qu'on a cassée, pas un cas limite.
const exemple = `server: https://ansible-proxy.example.com
default_env: staging
inventory: inventories/{{ env }}
runner_image: registry.example.com/mon-equipe/runner:2.3.1
forward_env:
  - VAULT_ADDR
  - MY_APP_VERSION
audit_include:
  - inventories/**
`

func TestParseNominal(t *testing.T) {
	p, err := Parse([]byte(exemple))
	if err != nil {
		t.Fatalf("le fichier du §5.4 doit être accepté : %v", err)
	}
	if p.Server != "https://ansible-proxy.example.com" {
		t.Errorf("server = %q", p.Server)
	}
	if p.DefaultEnv != "staging" {
		t.Errorf("default_env = %q", p.DefaultEnv)
	}
	// Le template est rendu TEL QUEL. La substitution appartient au cœur, qui
	// seul connaît l'environnement effectif (ADR-0009).
	if p.Inventory != "inventories/{{ env }}" {
		t.Errorf("inventory = %q, le template ne doit pas être résolu ici", p.Inventory)
	}
	if p.RunnerImage != "registry.example.com/mon-equipe/runner:2.3.1" {
		t.Errorf("runner_image = %q", p.RunnerImage)
	}
	if len(p.ForwardEnv) != 2 || p.ForwardEnv[0] != "VAULT_ADDR" {
		t.Errorf("forward_env = %v", p.ForwardEnv)
	}
	if len(p.AuditInclude) != 1 || p.AuditInclude[0] != "inventories/**" {
		t.Errorf("audit_include = %v", p.AuditInclude)
	}
}

// Un fichier vide n'est pas une erreur de forme : c'est un projet qui ne
// demande rien. yaml.v3 rend io.EOF dans ce cas, qu'il ne faut pas laisser
// remonter tel quel — « EOF » ne dit rien à qui lit le message.
func TestParseVide(t *testing.T) {
	for _, src := range []string{"", "   \n", "# rien que des commentaires\n"} {
		p, err := Parse([]byte(src))
		if err != nil {
			t.Errorf("%q doit être accepté comme configuration vide : %v", src, err)
		}
		if p.Server != "" || p.Inventory != "" || len(p.ForwardEnv) != 0 {
			t.Errorf("%q devrait donner une configuration nulle, donne %+v", src, p)
		}
	}
}

func TestParseRefus(t *testing.T) {
	cas := []struct {
		nom string
		src string
		// attendu est un fragment que le message DOIT contenir. L'ADR-0009 exige
		// que l'erreur nomme la clé en cause : un « champ inconnu » nu enverrait
		// l'utilisateur relire tout son fichier.
		attendu string
	}{
		{
			nom:     "champ inconnu",
			src:     "server: a\nforward_envs: [X]\n",
			attendu: "forward_envs",
		},
		{
			// Une clé écrite deux fois est un mode de panne silencieux : la
			// seconde gagne, et personne ne voit la première.
			nom:     "clé dupliquée",
			src:     "default_env: staging\ndefault_env: prod\n",
			attendu: "default_env",
		},
		{
			// Le cas que KnownFields NE VOIT PAS quand l'ancre porte sur un champ
			// connu : sans le parcours de l'arbre, l'alias serait expansé en
			// silence.
			nom:     "ancre et alias sur des champs connus",
			src:     "default_env: &e staging\ninventory: *e\n",
			attendu: "ancre",
		},
		{
			nom:     "document trop gros",
			src:     "server: " + strings.Repeat("a", MaxSize) + "\n",
			attendu: "taille",
		},
		{
			// Le YAML est valide mais ce n'est pas un mapping : décoder une
			// séquence dans une structure donnerait une erreur de type illisible.
			nom:     "racine qui n'est pas un mapping",
			src:     "- server: a\n",
			attendu: "mapping",
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("%s doit être refusé", c.nom)
			}
			if !strings.Contains(err.Error(), c.attendu) {
				t.Errorf("le message doit contenir %q, il vaut : %v", c.attendu, err)
			}
		})
	}
}

// Les noms de forward_env deviennent des clés d'un fichier `env/envvars` lu par
// ansible-runner. Un nom qui n'en est pas un y produirait au mieux une variable
// inatteignable, au pire un fichier que le moteur relit de travers.
func TestParseForwardEnvNoms(t *testing.T) {
	bons := []string{"VAULT_ADDR", "_prive", "A1", "mixedCase"}
	for _, n := range bons {
		if _, err := Parse([]byte("forward_env: [" + n + "]\n")); err != nil {
			t.Errorf("%q est un nom de variable valide : %v", n, err)
		}
	}

	mauvais := []string{"AVEC-TIRET", "AVEC ESPACE", "AVEC=EGAL", "1CHIFFRE", "", "AVEC.POINT"}
	for _, n := range mauvais {
		_, err := Parse([]byte("forward_env: [\"" + n + "\"]\n"))
		if err == nil {
			t.Errorf("%q ne devrait pas être accepté comme nom de variable", n)
		}
	}

	// Un doublon n'est pas fatal en soi — la map finale l'absorberait — mais il
	// signale une erreur d'édition, et le refuser coûte une ligne.
	if _, err := Parse([]byte("forward_env: [A, B, A]\n")); err == nil {
		t.Error("un nom répété dans forward_env doit être refusé")
	}
}

func TestResolveInventory(t *testing.T) {
	cas := []struct {
		nom  string
		tmpl string
		env  string
		veut string
	}{
		// Les trois orthographes qu'Ansible accepte. Les gens taperont les trois.
		{"sans espace", "inventories/{{env}}", "staging", "inventories/staging"},
		{"un espace", "inventories/{{ env }}", "staging", "inventories/staging"},
		{"espaces multiples", "inventories/{{  env  }}", "prod", "inventories/prod"},
		{"aucun template", "inventories/statique", "staging", "inventories/statique"},
		{"vide reste vide", "", "staging", ""},
		{"deux occurrences", "{{ env }}/hosts-{{ env }}", "rec", "rec/hosts-rec"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got, err := ResolveInventory(c.tmpl, c.env)
			if err != nil {
				t.Fatalf("résolution refusée : %v", err)
			}
			if got != c.veut {
				t.Errorf("= %q, veut %q", got, c.veut)
			}
		})
	}
}

func TestResolveInventoryRefus(t *testing.T) {
	cas := []struct {
		nom     string
		tmpl    string
		env     string
		attendu string
	}{
		{
			// En empruntant la syntaxe de Jinja on hérite de ses attentes.
			// Traiter un filtre littéralement donnerait un répertoire nommé
			// `inventories/{{ env | default('staging') }}` et une erreur
			// « fichier introuvable » très loin de la cause.
			nom:     "filtre Jinja",
			tmpl:    "inventories/{{ env | default('staging') }}",
			env:     "staging",
			attendu: "filtre",
		},
		{
			nom:     "placeholder inconnu",
			tmpl:    "inventories/{{ repo }}",
			env:     "staging",
			attendu: "repo",
		},
		{
			// `inventories/` est un inventaire de type répertoire, qu'Ansible
			// accepte sans broncher — donc ça marcherait, en désignant autre
			// chose que ce qui était voulu.
			nom:     "environnement absent",
			tmpl:    "inventories/{{ env }}",
			env:     "",
			attendu: "environnement",
		},
		{
			nom:     "accolade non fermée",
			tmpl:    "inventories/{{ env",
			env:     "staging",
			attendu: "{{",
		},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			_, err := ResolveInventory(c.tmpl, c.env)
			if err == nil {
				t.Fatalf("%s doit être refusé", c.nom)
			}
			if !strings.Contains(err.Error(), c.attendu) {
				t.Errorf("le message doit contenir %q, il vaut : %v", c.attendu, err)
			}
		})
	}
}

// Le template est validé À LA LECTURE et pas seulement à la résolution : une
// faute de frappe dans le fichier doit se voir au moment où on le lit, pas au
// moment où un run part.
func TestParseValideLeTemplate(t *testing.T) {
	_, err := Parse([]byte("inventory: inventories/{{ env | upper }}\n"))
	if err == nil {
		t.Fatal("un filtre dans inventory doit être refusé dès la lecture")
	}
	if !strings.Contains(err.Error(), "inventory") {
		t.Errorf("le message doit nommer le champ : %v", err)
	}
}
