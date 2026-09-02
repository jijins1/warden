package ansible

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Machine est ce qu'on retient d'un hôte d'inventaire.
type Machine struct {
	// Nom est la clé sous `hosts:`, celle par laquelle Ansible le désigne — donc
	// celle sous laquelle warden doit l'enrôler pour que les events d'un run s'y
	// rapprochent.
	Nom string

	// Adresse est `ansible_host`, ou le NOM lui-même à défaut.
	Adresse string

	// Deduite dit que l'adresse vient du nom, faute d'`ansible_host`.
	//
	// Retenu et non silencieux : la déduction suppose qu'une résolution existe
	// DEPUIS LE CLUSTER, ce que l'opérateur est le seul à pouvoir confirmer. Une
	// commande qui l'affiche donne à quelqu'un l'occasion de dire non.
	Deduite bool

	Login string // ansible_user
	Port  int    // ansible_port, 0 quand il n'est pas posé

	// Groupes sont tous les groupes qui contiennent la machine, triés.
	Groupes []string
}

// TailleMax borne le document accepté.
//
// Un inventaire de flotte fait quelques kilooctets ; ce plafond en laisse passer
// beaucoup. Il existe pour qu'un chemin erroné échoue en le disant plutôt qu'en
// remplissant la mémoire.
const TailleMax = 1 << 20

// Lire rend les machines d'un inventaire, qu'il soit un fichier ou un
// répertoire.
//
// Les machines sont triées par nom : la sortie d'une commande qui énumère une
// flotte ne doit pas changer d'ordre d'un appel à l'autre.
func Lire(chemin string) ([]Machine, error) {
	fichiers, err := fichiersDe(chemin)
	if err != nil {
		return nil, err
	}

	// La clé est le nom de machine : un hôte listé dans deux groupes est UNE
	// machine, dont on accumule les groupes.
	vues := map[string]*Machine{}
	for _, f := range fichiers {
		if err := lireUn(f, vues); err != nil {
			return nil, err
		}
	}

	noms := make([]string, 0, len(vues))
	for n := range vues {
		noms = append(noms, n)
	}
	sort.Strings(noms)

	out := make([]Machine, 0, len(noms))
	for _, n := range noms {
		m := vues[n]
		if m.Adresse == "" {
			// Le cœur de l'ADR-0036 : sans `ansible_host`, c'est le nom qui sert
			// d'adresse, et la résolution devient l'affaire du pod.
			m.Adresse = m.Nom
			m.Deduite = true
		}
		if m.Deduite && !adresseUtilisable(m.Adresse) {
			return nil, fmt.Errorf("machine %s : aucun `ansible_host`, et son nom ne peut pas "+
				"servir d'adresse — donnez-lui un `ansible_host`", m.Nom)
		}
		sort.Strings(m.Groupes)
		out = append(out, *m)
	}
	return out, nil
}

// fichiersDe rend les documents à lire, dans un ordre stable.
//
// Un répertoire est le cas COURANT et non l'exception : `inventory:` vaut
// typiquement `inventories/{{ env }}`, qu'Ansible traite comme un répertoire
// d'inventaire.
//
// Seul le premier niveau est lu, et `group_vars/` comme `host_vars/` sont
// ignorés par construction — ce sont des sous-répertoires. Le doc du paquet dit
// pourquoi ils ne sont pas suivis.
func fichiersDe(chemin string) ([]string, error) {
	if chemin == "" {
		return nil, errors.New("aucun chemin d'inventaire")
	}

	info, err := os.Stat(chemin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inventaire %s : introuvable", chemin)
	}
	if err != nil {
		return nil, fmt.Errorf("inventaire %s : %w", chemin, err)
	}
	if !info.IsDir() {
		return []string{chemin}, nil
	}

	entrees, err := os.ReadDir(chemin)
	if err != nil {
		return nil, fmt.Errorf("inventaire %s : %w", chemin, err)
	}

	var out []string
	for _, e := range entrees {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yml", ".yaml":
			out = append(out, filepath.Join(chemin, e.Name()))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("inventaire %s : aucun fichier .yml ou .yaml "+
			"au premier niveau", chemin)
	}
	sort.Strings(out)
	return out, nil
}

// groupe est la forme d'un groupe d'inventaire.
//
// Les étiquettes sont des étiquettes `json` : `sigs.k8s.io/yaml` convertit le
// YAML en JSON avant de déléguer à `encoding/json`, comme le fait déjà la
// lecture d'un fichier de déclaration.
//
// Pas de `UnmarshalStrict` ici, contrairement à celle-là, et l'écart est
// délibéré : un fichier de déclaration est écrit POUR warden, et une clé mal
// orthographiée y est une faute. Un inventaire Ansible ne l'est pas — il porte
// légitimement des dizaines de clés qui ne nous regardent pas, et les refuser
// rendrait la commande inutilisable sur le premier inventaire réel.
type groupe struct {
	Hosts    map[string]map[string]any `json:"hosts"`
	Vars     map[string]any            `json:"vars"`
	Children map[string]*groupe        `json:"children"`
}

func lireUn(chemin string, vues map[string]*Machine) error {
	brut, err := lireBorne(chemin)
	if err != nil {
		return err
	}

	// L'INI d'Ansible est un format à part entière, que ce paquet n'analyse pas.
	// Le reconnaître pour le refuser vaut mieux que de laisser le décodeur YAML
	// rendre une erreur de syntaxe sur une ligne qui n'a rien de fautif.
	if entete := premiereLigneUtile(brut); strings.HasPrefix(entete, "[") {
		return fmt.Errorf("%s ressemble à un inventaire au format INI, que warden ne lit pas — "+
			"convertissez-le en YAML, ou déclarez vos machines avec `warden host declare -f`", chemin)
	}

	var racine map[string]*groupe
	if err := yaml.Unmarshal(brut, &racine); err != nil {
		return fmt.Errorf("%s : %w", chemin, err)
	}

	// Les groupes de premier niveau sont parcourus dans l'ordre, pour que deux
	// lectures du même fichier rendent la même chose : une carte Go s'itère au
	// hasard, et un conflit de variables entre deux groupes se résoudrait
	// différemment d'un appel à l'autre.
	for _, nom := range clesTriees(racine) {
		if err := parcourir(chemin, nom, racine[nom], nil, vues); err != nil {
			return err
		}
	}
	return nil
}

// parcourir descend un groupe et ses enfants, en portant les variables héritées.
//
// L'héritage est celui du DOCUMENT et rien de plus : le groupe le plus proche
// l'emporte, et les variables propres à une machine l'emportent sur tout.
//
// Une machine listée dans PLUSIEURS groupes est visitée plusieurs fois, et la
// règle est alors : une valeur POSÉE l'emporte sur une valeur absente, et entre
// deux valeurs posées c'est la première dans l'ordre trié qui reste. La première
// moitié de cette règle n'est pas un détail — sans elle, `web-01` déclaré avec
// son `ansible_host` sous `web` et rappelé sans variables sous `prod` perdrait
// son adresse, et retomberait sur son nom.
func parcourir(fichier, nom string, g *groupe, herite map[string]any, vues map[string]*Machine) error {
	if g == nil {
		return nil
	}

	// `all` est le groupe implicite d'Ansible : il ne nomme pas une appartenance
	// qu'un `--group` pourrait viser, il contient tout.
	var chemin []string
	if nom != "all" {
		chemin = []string{nom}
	}

	vars := fusionner(herite, g.Vars)

	for _, h := range clesTriees(g.Hosts) {
		effectives := fusionner(vars, g.Hosts[h])
		m, deja := vues[h]
		if !deja {
			m = &Machine{Nom: h}
			vues[h] = m
		}
		if err := remplir(fichier, m, effectives); err != nil {
			return err
		}
		m.Groupes = append(m.Groupes, chemin...)
	}

	for _, enfant := range clesTriees(g.Children) {
		if err := parcourir(fichier, enfant, g.Children[enfant], vars, vues); err != nil {
			return err
		}
	}
	return nil
}

// remplir extrait les trois faits de connexion, sans écraser ce qui est déjà su.
//
// Les valeurs sont TOUJOURS lues, même pour une machine déjà vue : c'est ce qui
// permet de refuser un `{{ jinja }}` ou un port hors bornes où qu'il soit écrit,
// et pas seulement dans le premier groupe qui nomme la machine.
func remplir(fichier string, m *Machine, vars map[string]any) error {
	adresse, err := chaine(fichier, m.Nom, vars, "ansible_host", "ansible_ssh_host")
	if err != nil {
		return err
	}
	login, err := chaine(fichier, m.Nom, vars, "ansible_user", "ansible_ssh_user")
	if err != nil {
		return err
	}
	port, err := entier(fichier, m.Nom, vars, "ansible_port", "ansible_ssh_port")
	if err != nil {
		return err
	}

	if m.Adresse == "" {
		m.Adresse = adresse
	}
	if m.Login == "" {
		m.Login = login
	}
	if m.Port == 0 {
		m.Port = port
	}
	return nil
}
