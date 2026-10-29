package submit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// FlagsAnsible porte les drapeaux d'`ansible-playbook` qu'un run peut demander
// (§5.5).
//
// Trois sur les sept du §5.5 au départ, et c'est un choix de PÉRIMÈTRE plutôt qu'une
// étape : l'ADR-0034 a déjà tranché que la compatibilité exhaustive avec
// `ansible-playbook` est un gouffre, et l'ADR-0038 retient ceux dont l'absence
// se paie tous les jours — le dry-run, ce qu'il aurait changé, et sur quoi.
//
// Ce ne sont PAS les quatre autres qui manquent le plus : `-e/--extra-vars`
// déplacerait la source de vérité hors du commit d'audit, ce que le produit
// vend précisément de ne pas faire. Il demande sa propre décision, pas une
// ligne de plus dans cette structure.
type FlagsAnsible struct {
	// Check est `--check` : Ansible annonce ce qu'il ferait sans le faire.
	Check bool

	// Diff est `--diff` : le contenu de ce qui change, tâche par tâche.
	//
	// Les valeurs injectées dans le run sont masquées par le rédacteur de
	// l'ADR-0017 avant que les events partent ; `run.secrets_unmasked` dit si
	// cette rédaction a échoué.
	Diff bool

	// Become est `--become` : élever sur TOUTES les tâches.
	//
	// Il complète `-K`, qui ne transportait que le mot de passe : sans lui, le
	// mot de passe ne servait à rien tant que le playbook ne déclarait pas
	// `become: true` de son côté.
	Become bool

	// BecomeUser est le compte visé par l'élévation, vide pour le défaut
	// d'Ansible — `root`. Il n'a de sens qu'avec Become.
	BecomeUser string

	// User est `--user`, le `-u` d'Ansible : le compte distant, vide pour
	// celui de l'inventaire (ADR-0048). De priorité inférieure à
	// `ansible_user`, il ne s'applique qu'aux machines dont l'inventaire ne
	// nomme pas le compte.
	User string

	// Limit est la valeur de `--limit`, vide quand il n'a pas été demandé.
	Limit string

	// Tags et SkipTags sont les valeurs de `--tags` et `--skip-tags` : des noms
	// de tag séparés par des virgules, vides quand rien n'a été demandé. Ils
	// bornent CE QUI s'exécute dans le playbook, comme Limit borne SUR QUOI.
	Tags     string
	SkipTags string

	// Verbosite est le NOMBRE de `v` demandés — 0 quand rien ne l'a été, 4 au
	// plus, comme `-v` à `-vvvv` chez `ansible-playbook`.
	//
	// Un entier et non quatre booléens jusqu'ici : la ligne de commande en
	// expose quatre parce que c'est ainsi qu'on les tape, mais ce qui descend
	// est UN réglage, et le serveur le traite comme tel — ses quatre noms
	// partagent une clé d'unicité (`core.familleVerbosite`).
	Verbosite int

	// ExtraVars porte les `nom=valeur` de `-e`, dans l'ordre où ils ont été
	// donnés.
	//
	// Les NOMS autorisés sont déclarés par `extra_vars:` dans le fichier de
	// projet, que le SERVEUR relit depuis le commit (ADR-0009, ADR-0039) : ce
	// paquet n'en sait rien et ne cherche pas à le savoir. Il ne contrôle que la
	// forme, qui ne dépend d'aucun contexte.
	//
	// Les VALEURS sont auditées — elles finissent dans `run.flags` et l'API les
	// sert. N'y mettez pas de secret : `forward_env` est le chemin dont les
	// valeurs ne sont jamais journalisées.
	ExtraVars []string
}

// VerbositeMax borne le niveau de verbosité.
//
// Quatre, comme l'allowlist du serveur : `ansible-playbook` accepte `-vvvvv` et
// au-delà, mais `-vvvv` est déjà le niveau du debug de connexion, et rien
// au-dessus n'ajoute d'information — seulement du volume dans une table d'events
// qui ne s'élague pas. Le serveur reste l'autorité ; refuser ici évite de payer
// une capture d'arbre et un push pour l'apprendre (§5.7).
const VerbositeMax = 4

// motifLimit borne la valeur de `--limit`.
//
// C'est le JUMEAU de `core.motifLimit`, côté serveur, et la duplication est
// délibérée — c'est le motif déjà en place dans ce dépôt pour la regex de nom de
// dépôt. Le serveur reste l'autorité : celui-ci n'existe que pour refuser sur le
// poste plutôt qu'après une capture d'arbre et un push (§5.7).
//
// Le refus qui compte est celui d'`@fichier`, qui ferait lire la liste des
// machines dans un fichier du pod au lieu du commit audité.
// motifCompte est le jumeau de `core.motifCompte`, même duplication assumée.
var motifCompte = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)

var motifLimit = regexp.MustCompile(`^[A-Za-z0-9_.:!&*,~\[\]-]+$`)

// motifTags est le jumeau de `core.motifTags`, même duplication assumée. Plus
// étroit qu'Ansible, qui admet un espace dans un nom de tag : le serveur le
// refuserait, autant le dire avant la capture et le push.
var motifTags = regexp.MustCompile(`^[A-Za-z0-9_.-]+(,[A-Za-z0-9_.-]+)*$`)

// Composer rend la liste à envoyer, dans un ordre stable.
//
// Stable et non « celui de la ligne de commande » : c'est cette liste qui est
// enregistrée dans `run.flags` et relue par l'audit, et deux runs identiques
// doivent s'y lire identiques.
func (f FlagsAnsible) Composer() ([]string, error) {
	var flags []string
	if f.Check {
		flags = append(flags, "--check")
	}
	if f.Diff {
		flags = append(flags, "--diff")
	}
	if f.Become {
		flags = append(flags, "--become")
	}
	if f.BecomeUser != "" {
		if !f.Become {
			// Ansible n'élève que si l'élévation est demandée : passer le compte
			// seul produirait un run sous le mauvais utilisateur, en silence.
			return nil, fmt.Errorf("--become-user %s sans --become (ou -b) ne fait rien : "+
				"Ansible n'élève que si l'élévation est demandée", f.BecomeUser)
		}
		if !motifCompte.MatchString(f.BecomeUser) {
			return nil, fmt.Errorf("--become-user %q : un nom de compte est attendu", f.BecomeUser)
		}
		flags = append(flags, "--become-user="+f.BecomeUser)
	}
	if f.User != "" {
		if !motifCompte.MatchString(f.User) {
			return nil, fmt.Errorf("--user %q : un nom de compte est attendu", f.User)
		}
		flags = append(flags, "--user="+f.User)
	}
	if f.Verbosite > 0 {
		if f.Verbosite > VerbositeMax {
			return nil, fmt.Errorf("-%s : le niveau le plus élevé est -%s",
				strings.Repeat("v", f.Verbosite), strings.Repeat("v", VerbositeMax))
		}
		flags = append(flags, "-"+strings.Repeat("v", f.Verbosite))
	}
	if f.Limit != "" {
		if !motifLimit.MatchString(f.Limit) {
			return nil, fmt.Errorf("--limit %q : un motif Ansible est attendu "+
				"(noms, groupes, `!` `&` `~` `*` et `,` ou `:` pour séparer)", f.Limit)
		}
		flags = append(flags, "--limit="+f.Limit)
	}
	for _, t := range []struct{ nom, valeur string }{
		{"--tags", f.Tags}, {"--skip-tags", f.SkipTags},
	} {
		if t.valeur == "" {
			continue
		}
		if !motifTags.MatchString(t.valeur) {
			return nil, fmt.Errorf("%s %q : des noms de tag séparés par des virgules "+
				"sont attendus (lettres, chiffres, `_`, `.` et `-`)", t.nom, t.valeur)
		}
		flags = append(flags, t.nom+"="+t.valeur)
	}

	// Les extra-vars EN DERNIER, et dans l'ordre de saisie : c'est celui dans
	// lequel Ansible les applique, et le seul que l'opérateur puisse relire dans
	// son historique de shell.
	vus := make(map[string]bool, len(f.ExtraVars))
	for _, v := range f.ExtraVars {
		nom, err := nomDExtraVar(v)
		if err != nil {
			return nil, err
		}
		if vus[nom] {
			// Ansible garderait la dernière ; l'audit garderait les deux.
			return nil, fmt.Errorf("-e %s donnée deux fois", nom)
		}
		vus[nom] = true
		flags = append(flags, "--extra-vars="+v)
	}
	return flags, nil
}

// nomDExtraVar contrôle la forme d'un `nom=valeur` et rend le nom.
//
// Ce qui est refusé ici l'est aussi côté serveur, et la duplication a la même
// justification que celle de `motifLimit` : refuser sur le poste évite de payer
// une capture d'arbre et un push pour apprendre une faute de frappe (§5.7).
func nomDExtraVar(v string) (string, error) {
	nom, valeur, ok := strings.Cut(v, "=")
	if !ok {
		return "", fmt.Errorf("-e %q : un `nom=valeur` est attendu — warden ne lit pas de "+
			"fichier de variables, le commit audité est la seule source", v)
	}
	if !nomDeVariable(nom) {
		return "", fmt.Errorf("-e %q : %q n'est pas un nom de variable Ansible "+
			"(lettres, chiffres et `_`, ne commençant pas par un chiffre)", v, nom)
	}
	if strings.Contains(valeur, "{{") {
		return "", fmt.Errorf("-e %s : le produit n'évalue aucun Jinja, et une valeur "+
			"templatisée ne se lirait pas dans l'audit", nom)
	}
	for _, r := range valeur {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("-e %s : caractère de contrôle refusé dans la valeur", nom)
		}
	}
	return nom, nil
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

// Repetable collecte un drapeau donné plusieurs fois.
//
// `flag` ne sait pas le faire seul : `fs.String` écrase à chaque occurrence, et
// `-e a=1 -e b=2` ne laisserait que le second. C'est la même mécanique que
// `prompt.Source`, qui implémente déjà `flag.Value` dans ce dépôt.
type Repetable []string

func (r *Repetable) String() string { return strings.Join(*r, " ") }

func (r *Repetable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// encoder rend la forme envoyée dans le corps de `POST /runs`.
//
// Vide rend nil et non `[]` : le champ est `omitempty`, et un tableau vide
// ferait passer les runs sans drapeau par le chemin de validation des drapeaux
// pour n'y rien trouver.
func encoder(flags []string) (json.RawMessage, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	brut, err := json.Marshal(flags)
	if err != nil {
		return nil, fmt.Errorf("encodage des flags : %w", err)
	}
	return brut, nil
}
