package admin

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jijins1/warden/internal/cli/internal/ansible"
	"github.com/jijins1/warden/internal/cli/internal/client"
	"github.com/jijins1/warden/internal/cli/internal/projet"
	"github.com/jijins1/warden/internal/config"
)

// Inventaire est ce qu'une lecture d'inventaire rend, avec de quoi le dire.
type Inventaire struct {
	Machines []ansible.Machine

	// Chemin est l'inventaire effectivement lu, template résolu. Affiché : une
	// commande qui énumère une flotte doit dire D'OÙ elle la tient.
	Chemin string

	// Environnement est celui qui a servi à résoudre le template.
	Environnement string
}

// Depuis lit l'inventaire du dépôt courant, pour l'environnement visé.
//
// C'est la PREMIÈRE fois que la CLI lit `inventory` et `default_env` du fichier
// de projet, que le paquet `projet` s'interdisait jusqu'ici (ADR-0009). L'écart
// est réel et il est borné : on ne compose pas un run, on énumère des machines
// pour une commande d'administration. Rien de ce qui est lu ici ne part dans une
// requête d'exécution, et le serveur relira le fichier depuis le commit quand un
// run sera soumis.
func Depuis(repoDir, environnement string) (Inventaire, error) {
	p, err := projet.Lire(repoDir)
	if err != nil {
		return Inventaire{}, err
	}

	env := environnement
	if env == "" {
		env = p.DefaultEnv
	}
	if p.Inventory == "" {
		return Inventaire{}, fmt.Errorf("%s ne déclare pas d'`inventory` — "+
			"warden ne sait pas où trouver vos machines. Déclarez-le, ou passez "+
			"par un fichier de déclaration avec -f", config.File)
	}

	chemin, err := config.ResolveInventory(p.Inventory, env)
	if err != nil {
		return Inventaire{}, fmt.Errorf("champ inventory : %w", err)
	}

	machines, err := ansible.Lire(filepath.Join(repoDir, chemin))
	if err != nil {
		return Inventaire{}, err
	}
	return Inventaire{Machines: machines, Chemin: chemin, Environnement: env}, nil
}

// Filtrer retient les machines d'un groupe. Un groupe vide ne filtre rien.
func Filtrer(machines []ansible.Machine, groupe string) ([]ansible.Machine, error) {
	if groupe == "" {
		return machines, nil
	}

	var out []ansible.Machine
	for _, m := range machines {
		for _, g := range m.Groupes {
			if g == groupe {
				out = append(out, m)
				break
			}
		}
	}
	if len(out) == 0 {
		// Une faute de frappe dans un nom de groupe rendrait « aucune machine à
		// enrôler », ce qui ressemble beaucoup trop à « tout est déjà fait ».
		return nil, fmt.Errorf("aucune machine dans le groupe %q — groupes connus : %s",
			groupe, strings.Join(groupesDe(machines), ", "))
	}
	return out, nil
}

func groupesDe(machines []ansible.Machine) []string {
	vus := map[string]struct{}{}
	for _, m := range machines {
		for _, g := range m.Groupes {
			vus[g] = struct{}{}
		}
	}
	out := make([]string, 0, len(vus))
	for g := range vus {
		out = append(out, g)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"(aucun)"}
	}
	return out
}

// Ecart décrit une machine connue des deux côtés, mais pas de la même façon.
type Ecart struct {
	Machine ansible.Machine
	Connue  client.Host

	// Quoi nomme les champs qui divergent, dans l'ordre où on les affiche.
	Quoi []string
}

// Comparer sépare ce qui manque au serveur de ce qui y diffère.
//
// Les écarts sont SIGNALÉS et jamais corrigés : une machine enrôlée dont
// l'adresse a changé dans l'inventaire est une question — a-t-elle déménagé, ou
// quelqu'un s'est-il trompé de ligne ? La corriger d'autorité répondrait à la
// place de l'opérateur, et réenrôler suppose de reposer une clé.
func Comparer(inventaire []ansible.Machine, connues []client.Host) (manquantes []ansible.Machine, ecarts []Ecart) {
	index := make(map[string]client.Host, len(connues))
	for _, h := range connues {
		index[h.Name] = h
	}

	for _, m := range inventaire {
		h, connue := index[m.Nom]
		if !connue {
			manquantes = append(manquantes, m)
			continue
		}
		if quoi := diverge(m, h); len(quoi) > 0 {
			ecarts = append(ecarts, Ecart{Machine: m, Connue: h, Quoi: quoi})
		}
	}
	return manquantes, ecarts
}

// diverge compare ce que l'inventaire DIT à ce que le serveur SAIT.
//
// Un champ que l'inventaire ne pose pas n'est pas un écart : il ne contredit
// rien. Seule une valeur écrite des deux côtés et différente en est un.
func diverge(m ansible.Machine, h client.Host) []string {
	var quoi []string
	if m.Adresse != "" && h.Address != "" && m.Adresse != h.Address {
		quoi = append(quoi, fmt.Sprintf("adresse %s ≠ %s", m.Adresse, h.Address))
	}
	if m.Login != "" && h.Login != "" && m.Login != h.Login {
		quoi = append(quoi, fmt.Sprintf("compte %s ≠ %s", m.Login, h.Login))
	}
	// Le port absent des deux côtés vaut le défaut SSH : les comparer bruts
	// signalerait un écart entre « 22 » et « rien », qui n'en est pas un.
	if p, q := portOuDefaut(m.Port), portOuDefaut(h.Port); p != q {
		quoi = append(quoi, fmt.Sprintf("port %d ≠ %d", p, q))
	}
	return quoi
}

// portSSHParDefaut duplique la constante du cœur, que la CLI ne peut pas
// importer (ADR-0011).
const portSSHParDefaut = 22

func portOuDefaut(p int) int {
	if p == 0 {
		return portSSHParDefaut
	}
	return p
}
