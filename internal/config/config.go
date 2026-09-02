package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// File est le nom du fichier de configuration projet (§5.4).
//
// EXPORTÉ et non recomposé de chaque côté : la CLI le lit dans le worktree, le
// rôle `git` l'extrait de l'arbre du commit. Deux littéraux jumeaux finissent
// par diverger, et la panne ne se voit alors qu'à l'exécution — c'est la leçon
// que ce dépôt a déjà payée avec `WARDEN_DISPATCH_URL`.
const File = ".ansibleproxy.yml"

// MaxSize plafonne le document.
//
// Le YAML vient de l'arbre d'un commit, donc d'une entrée non fiable
// (ADR-0009) : sans plafond, un fichier de plusieurs mégaoctets ferait travailler
// le parseur du rôle `api` dans le chemin chaud d'un run. 64 Kio laissent une
// marge considérable à un fichier dont l'exemple du §5.4 tient en dix lignes.
const MaxSize = 64 << 10

// Project est la requête du projet, telle que le fichier l'exprime.
//
// Ce n'est PAS la configuration effective du run : le cœur la compose en
// filtrant ceci par la politique serveur, puis en y superposant les flags
// explicites (ADR-0009). Le nom dit la requête, pas le résultat.
type Project struct {
	// Server est l'adresse de l'API du control plane, et le SEUL champ dont la
	// lecture soit nécessairement cliente : il faut joindre quelqu'un avant
	// qu'un commit existe de son côté (ADR-0009).
	//
	// L'adresse du rôle `git` ne figure pas ici. Elle est portée par la réponse
	// au jeton de push : le serveur dit déjà avec quel jeton et sous quel
	// préfixe de ref on écrit, dire où relève de la même phrase — et une adresse
	// d'infrastructure recopiée dans chaque dépôt devrait être réécrite partout
	// le jour où le service déménage.
	Server string `yaml:"server"`

	// Repo est le nom du dépôt CÔTÉ SERVEUR, celui que `warden submit --repo`
	// nomme quand il est donné.
	//
	// Il est lu par la CLI et envoyé dans la requête, ce que ce paquet
	// s'interdit partout ailleurs (ADR-0009). L'exception tient à ce qu'il n'est
	// pas de la configuration de RUN : c'est de l'ADRESSAGE, au même titre que
	// `server`. Il ne décide de rien de ce qui s'exécute — il désigne à qui l'on
	// parle. Aucun écart entre l'audité et l'appliqué ne peut en naître, parce
	// qu'il ne s'applique à rien.
	//
	// Troisième et dernière lecture cliente, avec `server` et `audit_include`.
	Repo string `yaml:"repo"`

	// DefaultEnv est l'environnement visé quand `--env` est absent.
	//
	// Il est résolu par le SERVEUR depuis le commit, et pas par la CLI : c'est
	// lui qui décide de l'inventaire par templatisation et sur lui que porte le
	// contrôle d'accès du §10.2.
	DefaultEnv string `yaml:"default_env"`

	// Inventory est le chemin de l'inventaire, RELATIF à l'arbre audité, et
	// facultatif — un playbook en `connection: local` n'en a pas besoin.
	//
	// Il est rendu TEL QUEL, template non résolu : la substitution a besoin de
	// l'environnement effectif, que ce paquet ne connaît pas et n'a pas à
	// connaître. Voir ResolveInventory.
	Inventory string `yaml:"inventory"`

	// RunnerImage est l'image du pod runner (ADR-0014). Omise, le défaut du
	// déploiement s'applique.
	//
	// Elle épingle du même coup ansible-core, l'interpréteur Python et les
	// collections : c'est ce champ qui porte la rejouabilité du run, et c'est à
	// ce titre que le §5.3 lève sa limite connue sur les collections Galaxy.
	RunnerImage string `yaml:"runner_image"`

	// ForwardEnv liste les NOMS des variables du shell à transmettre (§5.4).
	//
	// Des noms, jamais des valeurs : le fichier est commité, donc visible dans
	// l'audit. Les valeurs viennent du shell de l'appelant et ne sont jamais
	// journalisées.
	//
	// Ce paquet en valide la FORME et rien d'autre. Refuser les noms que le
	// produit se réserve est une décision, donc elle appartient au cœur.
	ForwardEnv []string `yaml:"forward_env"`

	// AuditInclude liste les pathspecs à réintégrer dans le commit d'audit
	// malgré les règles d'ignore (§5.3).
	//
	// Seconde lecture nécessairement cliente, que l'ADR-0009 ne nomme pas : ce
	// champ décide de ce qui ENTRE dans le commit, donc le lire depuis le commit
	// serait circulaire. La divergence reste DÉTECTABLE après coup — le fichier
	// est dans l'arbre, un auditeur compare ce qu'il réclamait à ce que l'arbre
	// contient — elle n'est simplement pas empêchable à la source.
	AuditInclude []string `yaml:"audit_include"`
}

// Parse lit le document et refuse tout ce qui n'est pas strictement conforme.
//
// « Refuse » et non « ignore », sur les quatre fronts, parce qu'ignorer une clé
// de configuration est précisément le mode de panne que l'ADR-0009 cherche à
// éliminer : champ inconnu, clé dupliquée, ancre ou alias, document hors
// plafond. Le message NOMME toujours ce qui a été refusé — un « fichier
// invalide » nu enverrait l'utilisateur relire les dix lignes une par une.
//
// Un document vide rend une configuration nulle sans erreur : un projet qui ne
// demande rien est un état légitime.
func Parse(raw []byte) (Project, error) {
	if len(raw) > MaxSize {
		return Project{}, fmt.Errorf("%s : taille de %d octets, %d au plus",
			File, len(raw), MaxSize)
	}

	// L'arbre AVANT le décodage, et c'est l'ordre qui compte : `KnownFields` ne
	// voit pas un alias dont l'ancre porte sur un champ connu — il serait
	// expansé en silence, ce que l'ADR-0009 interdit nommément. C'est MESURÉ,
	// pas déduit d'une lecture de la documentation.
	var arbre yaml.Node
	if err := yaml.Unmarshal(raw, &arbre); err != nil {
		return Project{}, fmt.Errorf("%s : YAML illisible : %w", File, err)
	}
	if err := refuserAncres(&arbre); err != nil {
		return Project{}, fmt.Errorf("%s : %w", File, err)
	}
	if err := racineMapping(&arbre); err != nil {
		return Project{}, fmt.Errorf("%s : %w", File, err)
	}

	var p Project
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		// EOF est le document vide, et ce n'est pas une erreur. Le laisser
		// remonter tel quel afficherait « EOF », qui ne dit rien à personne.
		if errors.Is(err, io.EOF) {
			return Project{}, nil
		}
		return Project{}, fmt.Errorf("%s : %w", File, err)
	}

	if err := validerForwardEnv(p.ForwardEnv); err != nil {
		return Project{}, fmt.Errorf("%s : %w", File, err)
	}

	// Le template est validé À LA LECTURE et pas seulement à la résolution :
	// une faute de frappe doit se voir quand on lit le fichier, pas quand un run
	// part. L'environnement témoin ne sert qu'à exercer la syntaxe.
	if _, err := ResolveInventory(p.Inventory, "temoin"); err != nil {
		return Project{}, fmt.Errorf("%s : champ inventory : %w", File, err)
	}

	return p, nil
}

// racineMapping refuse un document dont la racine n'est pas un mapping.
//
// Le décodeur le refuserait aussi, mais avec un message parlant de types Go que
// personne n'a écrits dans son fichier.
func racineMapping(n *yaml.Node) error {
	if n.Kind == 0 || len(n.Content) == 0 {
		return nil // document vide, traité en aval
	}
	if n.Kind == yaml.DocumentNode {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("la racine doit être un mapping de clés, pas une séquence ni une valeur seule")
	}
	return nil
}

// refuserAncres parcourt l'arbre et refuse ancres et alias (ADR-0009).
//
// Ils ne sont pas refusés pour ce qu'ils font de mal ici, mais pour ce qu'ils
// coûtent à l'audit : un fichier dont la lecture demande de suivre des renvois
// n'est plus un fichier qu'on relit en post-mortem. Le refus est la même
// discipline que celle appliquée aux champs inconnus.
func refuserAncres(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Anchor != "" {
		return fmt.Errorf("ancre YAML %q refusée : la configuration doit se lire sans suivre de renvoi", n.Anchor)
	}
	if n.Kind == yaml.AliasNode {
		return fmt.Errorf("alias YAML %q refusé : la configuration doit se lire sans suivre de renvoi", n.Value)
	}
	for _, enfant := range n.Content {
		if err := refuserAncres(enfant); err != nil {
			return err
		}
	}
	return nil
}

// validerForwardEnv contrôle la FORME des noms, et rien de plus.
//
// Ces noms deviennent les clés d'un `env/envvars` lu par ansible-runner. Un nom
// portant un `=` ou un espace y produirait au mieux une variable inatteignable,
// au pire un fichier que le moteur relit de travers.
//
// Quels noms le produit REFUSE — ceux dont la surcharge couperait l'audit — est
// une décision, et elle vit dans le cœur (ADR-0009) : ce paquet analyse.
func validerForwardEnv(noms []string) error {
	vus := make(map[string]struct{}, len(noms))
	for _, n := range noms {
		if !nomDeVariable(n) {
			return fmt.Errorf("forward_env : %q n'est pas un nom de variable d'environnement "+
				"(lettres, chiffres et `_`, ne commençant pas par un chiffre)", n)
		}
		if _, deja := vus[n]; deja {
			return fmt.Errorf("forward_env : %q est listé deux fois", n)
		}
		vus[n] = struct{}{}
	}
	return nil
}

func nomDeVariable(n string) bool {
	if n == "" {
		return false
	}
	for i, c := range n {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// PlaceholderEnv est le seul substituable reconnu dans `inventory`.
//
// UN SEUL, délibérément. `{{ repo }}` et les suivants s'ajouteront quand
// quelqu'un les demandera ; l'inverse ne se retire pas une fois que des dépôts
// en dépendent.
const PlaceholderEnv = "env"

// ResolveInventory substitue les placeholders du chemin d'inventaire.
//
// Ce n'est PAS un moteur de templates, et ce refus est le point de la fonction.
// L'entrée vient de l'arbre d'un commit, donc d'une source non fiable : y
// brancher `text/template` offrirait les appels de méthode et l'amplification de
// sortie pour un besoin qui est une substitution de chaîne.
//
// La syntaxe est celle de Jinja parce que c'est celle que le lecteur d'un
// playbook a sous les yeux toute la journée. On en hérite les attentes : quelqu'un
// écrira `{{ env | default('staging') }}`. Traité littéralement, ça donnerait un
// répertoire portant ce nom et un « fichier introuvable » trois couches plus bas,
// très loin de la cause — donc c'est refusé en nommant la construction.
//
// La substitution a lieu CÔTÉ SERVEUR et jamais dans la CLI : sinon le template
// audité et le chemin appliqué pourraient diverger, ce que l'ADR-0009 existe
// précisément pour empêcher.
func ResolveInventory(tmpl, env string) (string, error) {
	var out strings.Builder
	reste := tmpl

	for {
		i := strings.Index(reste, "{{")
		if i < 0 {
			// Une accolade fermante orpheline ne signale rien d'exploitable : un
			// chemin peut légitimement en contenir. Seule l'ouverture engage.
			out.WriteString(reste)
			return out.String(), nil
		}
		out.WriteString(reste[:i])

		fin := strings.Index(reste[i:], "}}")
		if fin < 0 {
			return "", fmt.Errorf("`{{` sans `}}` correspondant dans %q", tmpl)
		}

		nom := strings.TrimSpace(reste[i+2 : i+fin])
		if strings.ContainsAny(nom, "|") {
			return "", fmt.Errorf("filtre Jinja refusé dans %q : "+
				"seul `{{ %s }}` est reconnu, sans filtre ni expression", tmpl, PlaceholderEnv)
		}
		if nom != PlaceholderEnv {
			return "", fmt.Errorf("placeholder %q inconnu dans %q : seul `{{ %s }}` est reconnu",
				nom, tmpl, PlaceholderEnv)
		}
		if env == "" {
			return "", fmt.Errorf("%q référence l'environnement mais aucun environnement n'est défini "+
				"— renseigner `default_env` ou passer `--env`", tmpl)
		}
		out.WriteString(env)

		reste = reste[i+fin+2:]
	}
}
