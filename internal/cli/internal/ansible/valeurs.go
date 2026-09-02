package ansible

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// lireBorne lit un document sans dépasser le plafond.
func lireBorne(chemin string) ([]byte, error) {
	f, err := os.Open(chemin) // #nosec G304 -- chemin composé de l'inventaire déclaré par le projet
	if err != nil {
		return nil, fmt.Errorf("lecture de %s : %w", chemin, err)
	}
	defer func() { _ = f.Close() }()

	brut, err := io.ReadAll(io.LimitReader(f, TailleMax+1))
	if err != nil {
		return nil, fmt.Errorf("lecture de %s : %w", chemin, err)
	}
	if len(brut) > TailleMax {
		return nil, fmt.Errorf("%s dépasse %d octets — est-ce bien un inventaire ?",
			chemin, TailleMax)
	}
	return brut, nil
}

// premiereLigneUtile rend la première ligne qui n'est ni vide ni un commentaire.
func premiereLigneUtile(brut []byte) string {
	for _, l := range strings.Split(string(brut), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		return l
	}
	return ""
}

func clesTriees[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fusionner superpose des variables sans muter les cartes reçues.
//
// Une nouvelle carte à chaque niveau, et non une carte partagée qu'on modifie :
// le parcours est récursif et un frère écraserait les variables de son frère.
func fusionner(base, dessus map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(dessus))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range dessus {
		out[k] = v
	}
	return out
}

// chaine lit la première clé posée parmi celles données.
//
// Les alias hérités d'Ansible 1.x — `ansible_ssh_host`, `ansible_ssh_user` —
// sont acceptés parce qu'ils vivent encore dans des inventaires réels, et qu'un
// inventaire qui n'en porte que la forme ancienne serait autrement lu comme s'il
// ne disait rien.
func chaine(fichier, machine string, vars map[string]any, cles ...string) (string, error) {
	for _, k := range cles {
		v, pose := vars[k]
		if !pose || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%s, machine %s : `%s` vaut %v, une chaîne est attendue",
				fichier, machine, k, v)
		}
		if err := refuserJinja(fichier, machine, k, s); err != nil {
			return "", err
		}
		return s, nil
	}
	return "", nil
}

// entier lit un port.
//
// Le YAML rend un nombre en `float64` après le passage par JSON, et une chaîne
// est acceptée parce que `ansible_port: "22"` s'écrit couramment.
func entier(fichier, machine string, vars map[string]any, cles ...string) (int, error) {
	for _, k := range cles {
		v, pose := vars[k]
		if !pose || v == nil {
			continue
		}

		var n int
		switch t := v.(type) {
		case float64:
			n = int(t)
			if float64(n) != t {
				return 0, fmt.Errorf("%s, machine %s : `%s` vaut %v, un entier est attendu",
					fichier, machine, k, v)
			}
		case string:
			if err := refuserJinja(fichier, machine, k, t); err != nil {
				return 0, err
			}
			p, err := strconv.Atoi(strings.TrimSpace(t))
			if err != nil {
				return 0, fmt.Errorf("%s, machine %s : `%s` vaut %q, un entier est attendu",
					fichier, machine, k, t)
			}
			n = p
		default:
			return 0, fmt.Errorf("%s, machine %s : `%s` vaut %v, un entier est attendu",
				fichier, machine, k, v)
		}

		if n < 1 || n > 65535 {
			return 0, fmt.Errorf("%s, machine %s : port %d hors bornes", fichier, machine, n)
		}
		return n, nil
	}
	return 0, nil
}

// refuserJinja arrête une valeur qu'il faudrait évaluer pour comprendre.
//
// Ce paquet n'est pas un moteur de templates, pour la même raison que
// `config.ResolveInventory` n'en est pas un : l'évaluer demanderait le contexte
// de variables complet d'Ansible, que rien ici ne détient. Traiter la valeur
// littéralement enrôlerait une machine dont l'adresse est `{{ vip }}`, ce qui
// échouerait beaucoup plus tard et très loin de la cause.
func refuserJinja(fichier, machine, cle, valeur string) error {
	if !strings.Contains(valeur, "{{") {
		return nil
	}
	return fmt.Errorf("%s, machine %s : `%s` vaut %q — warden n'évalue pas les "+
		"expressions Jinja, il ne connaît pas les variables d'Ansible. Donnez une "+
		"valeur littérale, ou déclarez cette machine avec `warden host declare -f`",
		fichier, machine, cle, valeur)
}

// nomOuAdresse est la forme d'une valeur qui peut désigner une machine : une
// adresse IP, ou un nom d'hôte RFC 1123.
//
// Quatrième expression de cette famille dans le dépôt, et la duplication est ici
// FORCÉE : `core.ValiderAdresse` vit sous `internal/server/internal`, que la CLI
// ne peut pas importer (ADR-0011 — un binaire par domaine de confiance). Le
// serveur reste celui qui fait autorité ; ceci existe pour que l'erreur se
// rapporte avec l'inventaire sous les yeux.
var nomOuAdresse = regexp.MustCompile(
	`^[A-Za-z0-9:]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?$`)

// adresseUtilisable dit si une valeur peut servir d'adresse de connexion.
//
// Appliquée seulement à une adresse DÉDUITE du nom : une valeur écrite dans
// `ansible_host` est jugée par le serveur, qui fait autorité. Un nom de machine,
// lui, n'a jamais été écrit pour servir d'adresse — il peut légitimement
// contenir un `_`, qu'Ansible accepte et qu'une pile TCP refuse.
func adresseUtilisable(v string) bool {
	return v != "" && len(v) <= 253 && nomOuAdresse.MatchString(v)
}
