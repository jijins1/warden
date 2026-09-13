package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Ce fichier porte l'allowlist de flags du §5.5, que l'ADR-0021 avait écartée
// de la v1 et que l'ADR-0038 rouvre pour trois d'entre eux.
//
// # Pourquoi une allowlist, alors que le playbook est déjà du code arbitraire
//
// Ce n'est PAS ce qu'elle protège, et le prétendre serait le genre de
// commentaire qui ment que la §1 de `docs/reste-a-faire.md` a déjà coûté cher.
// L'utilisateur qui soumet un run exécute du Python de son choix sur les
// machines cibles (§2) : aucune liste de drapeaux ne lui retire ce pouvoir.
//
// Elle protège deux choses plus étroites, et réellement :
//
//   - L'AUDIT. `-e @/chemin`, `--vault-password-file`, `-i` en second
//     inventaire : tous font exécuter autre chose que ce que le commit d'audit
//     dit. Le produit vend « ce qui s'applique est ce qui est audité » ; un
//     drapeau qui change la source échapperait à cette phrase.
//   - LE PÉRIMÈTRE. `flags` était un `json.RawMessage` recopié tel quel depuis
//     le corps de la requête jusque sur l'`argv` d'`ansible-playbook`. Aucune
//     forme n'était vérifiée : un objet, un nombre, une liste de mille éléments
//     y passaient. Ce qui descend vers le domaine hostile de l'ADR-0011 doit
//     avoir une forme connue AVANT de partir.
//
// Le filtre ne peut vivre que là — côté serveur. `exec.Config.Flags` le dit
// déjà : une allowlist appliquée par le runner est une allowlist appliquée par
// le code qu'elle est censée contraindre.

// motifLimit borne la valeur de `--limit`.
//
// La syntaxe d'Ansible est reprise en entier sauf UNE forme : `@fichier`, qui
// fait lire la liste des machines dans un fichier du pod. C'est exactement le
// genre de drapeau qui déplace la source de vérité hors du commit d'audit, donc
// le seul refus qui a une raison d'être ici.
//
// Le reste est admis parce qu'Ansible s'en sert : `,` et `:` séparent, `!` nie,
// `&` intersecte, `~` introduit une regex, `*` joker, `[0:5]` borne un intervalle
// de nom, et un nom de machine porte points et tirets.
var motifLimit = regexp.MustCompile(`^[A-Za-z0-9_.:!&*,~\[\]-]+$`)

// longueurMaxValeur borne une valeur de drapeau.
//
// Généreux pour un `--limit` — qui tient en une poignée de noms — et sans
// rapport avec une limite d'Ansible : c'est la borne d'une chose qui descend sur
// une ligne de commande, et une ligne de commande a un plafond système.
const longueurMaxValeur = 1024

// drapeau décrit une forme acceptée sur la ligne de commande d'ansible-playbook.
//
// Une table et non une suite de `if` : les trois autres du §5.5 — `--tags`,
// `--skip-tags`, `-v` — s'ajouteront par une ligne, ce qui est la seule façon de
// rendre l'ajout suivant assez peu coûteux pour être fait.
type drapeau struct {
	nom string

	// verifier contrôle la valeur. Nil pour un drapeau NU, qui ne s'écrit alors
	// que `--check` et jamais `--check=quelque-chose`.
	//
	// Une fonction et non une regex : `--extra-vars` porte un `nom=valeur` dont
	// les deux moitiés obéissent à des règles différentes, et l'écrire en une
	// seule expression la rendrait illisible au premier changement.
	verifier func(string) error

	// repetable autorise plusieurs occurrences. Faux par défaut : deux `--limit`
	// laisseraient Ansible garder le dernier et l'audit garder les deux.
	//
	// `--extra-vars` est le seul à l'être, parce que poser trois variables
	// s'écrit `-e a=1 -e b=2 -e c=3` et pas autrement. L'unicité porte alors sur
	// le NOM DE VARIABLE, pas sur le drapeau — voir `cle`.
	repetable bool
}

var drapeauxAcceptes = []drapeau{
	// Le dry-run d'Ansible : le premier réflexe avant de toucher à la prod.
	{nom: "--check"},
	// L'élévation. Le produit savait déjà transporter le MOT DE PASSE d'élévation
	// (`-K`, ADR-0018) sans savoir demander l'élévation elle-même : un demi-
	// mécanisme, où `-K` ne servait à rien si le playbook ne déclarait pas
	// `become: true` lui-même.
	//
	// Ce n'est PAS une élévation de privilège nouvelle : elle exige toujours que
	// le compte y ait droit sur la machine, et le playbook exécute déjà du code
	// arbitraire (§2). Ce qu'elle change est le PÉRIMÈTRE — `--become` s'applique
	// à TOUTES les tâches, y compris celles que le playbook avait laissées non
	// élevées. C'est enregistré dans `run.flags`, donc visible en audit, et c'est
	// la raison pour laquelle ça peut l'être.
	{nom: "--become"},
	{nom: "--become-user", verifier: verifierCompte},
	// Le diff par tâche. Il ne rend PAS les secrets visibles : le rédacteur de
	// l'ADR-0017 est armé des valeurs injectées dans le run, et la clé d'accès
	// n'apparaît dans aucun résultat de module.
	{nom: "--diff"},
	{nom: "--limit", verifier: verifierLimit},
	// `--extra-vars` est le seul dont l'allowlist ne vit PAS dans ce fichier :
	// les noms autorisés sont déclarés par `extra_vars:` dans le commit, et
	// croisés par `composerFlags`. Ce qui est vérifié ici est la FORME, qui ne
	// dépend d'aucun contexte — c'est pourquoi elle peut l'être avant que le
	// rôle `git` ait travaillé (ADR-0039).
	{nom: "--extra-vars", verifier: verifierExtraVar, repetable: true},
}

// FlagsDemandes décode et valide le champ `flags` d'une requête de création.
//
// STRICT : le client doit envoyer un tableau de chaînes, et rien d'autre. La
// tolérance de `FlagsEnregistres` ne vaut que pour la relecture d'une colonne
// écrite avant ce contrôle — elle n'a pas à s'étendre à ce qui entre.
func FlagsDemandes(brut json.RawMessage) ([]string, error) {
	if len(brut) == 0 {
		return nil, nil
	}

	var demandes []string
	if err := json.Unmarshal(brut, &demandes); err != nil {
		return nil, fmt.Errorf("flags : un tableau de chaînes est attendu")
	}
	if len(demandes) == 0 {
		return nil, nil
	}
	if err := ValiderFlags(demandes); err != nil {
		return nil, err
	}
	return demandes, nil
}

// FlagsEnregistres relit la colonne `run.flags`.
//
// TOLÉRANT sur la forme, et il faut dire pourquoi : le schéma initial donne à
// cette colonne le défaut `'{}'::jsonb`, un OBJET, et toutes les lignes
// antérieures à ce fichier le portent — aucun client n'a jamais envoyé de flag.
// Refuser un objet ici rendrait indélivrable chaque run déjà en base.
//
// Il REVALIDE en revanche le contenu d'un tableau. C'est le dernier point avant
// que ces chaînes deviennent l'`argv` d'un processus tournant dans le domaine
// hostile de l'ADR-0011, et une ligne écrite par un serveur antérieur à ce
// contrôle n'a traversé aucune allowlist. Refuser la LIVRAISON est alors la
// bonne panne : le run reste enregistré et son état dit qu'il n'a pas tourné.
func FlagsEnregistres(brut []byte) ([]string, error) {
	if len(brut) == 0 {
		return nil, nil
	}

	var flags []string
	if err := json.Unmarshal(brut, &flags); err != nil {
		// `{}` et `null` sont les deux formes historiques du « pas de flag ».
		// Tout le reste est une incohérence, mais elle ne doit pas être plus
		// grave que l'absence : rendre nil est ce qui laisse le run partir.
		return nil, nil
	}
	if len(flags) == 0 {
		return nil, nil
	}
	if err := ValiderFlags(flags); err != nil {
		return nil, err
	}
	return flags, nil
}

// ValiderFlags refuse tout ce que l'allowlist ne nomme pas.
//
// Exportée : elle est le contrat, et les tests l'appellent directement plutôt
// que de le déduire d'un code HTTP.
//
// Elle ne contrôle PAS l'appartenance d'un `--extra-vars` à `extra_vars:` — ces
// noms viennent du commit, que cette fonction n'a pas. C'est `composerFlags` qui
// s'en charge, une fois la configuration lue.
func ValiderFlags(flags []string) error {
	vus := make(map[string]bool, len(flags))
	for _, f := range flags {
		nom, valeur, avecValeur := strings.Cut(f, "=")

		d := accepte(nom)
		if d == nil {
			// Le message NOMME ce qui est accepté : une liste fermée dont on ne
			// publie pas le contenu envoie chercher dans un code source.
			return fmt.Errorf("flag %q refusé : ce serveur accepte %s", f, listeAcceptee())
		}

		switch {
		case d.verifier == nil && avecValeur:
			return fmt.Errorf("flag %s n'attend pas de valeur", nom)
		case d.verifier == nil:
		case !avecValeur || valeur == "":
			return fmt.Errorf("flag %s attend une valeur, écrite %s=<valeur>", nom, nom)
		case len(valeur) > longueurMaxValeur:
			return fmt.Errorf("valeur de %s : %d caractères, maximum %d",
				nom, len(valeur), longueurMaxValeur)
		default:
			if err := d.verifier(valeur); err != nil {
				return fmt.Errorf("valeur de %s : %w", nom, err)
			}
		}

		// L'unicité porte sur la CLÉ et non sur le drapeau : deux `-e` sont
		// légitimes, deux `-e version=` ne le sont pas — Ansible garderait le
		// dernier et l'audit garderait les deux, donc le run cesserait de dire
		// ce qu'il a fait.
		k := cle(*d, valeur)
		if vus[k] {
			return fmt.Errorf("flag %s donné deux fois", k)
		}
		vus[k] = true
	}

	// `--become-user` SEUL n'élève rien : Ansible ne bascule que si `become` est
	// demandé, ici ou dans le playbook. Le laisser passer produirait un run qui
	// tourne sous le mauvais compte sans qu'aucun message ne le dise — et
	// l'audit enregistrerait un drapeau sans effet, ce qui est pire que rien.
	if vus["--become-user"] && !vus["--become"] {
		return errors.New("--become-user sans --become ne fait rien : Ansible n'élève " +
			"que si l'élévation est demandée")
	}
	return nil
}

// cle rend ce dont l'unicité est exigée pour un drapeau donné.
func cle(d drapeau, valeur string) string {
	if !d.repetable {
		return d.nom
	}
	nom, _, _ := strings.Cut(valeur, "=")
	return d.nom + " " + nom
}

func accepte(nom string) *drapeau {
	for i := range drapeauxAcceptes {
		if drapeauxAcceptes[i].nom == nom {
			return &drapeauxAcceptes[i]
		}
	}
	return nil
}

func listeAcceptee() string {
	noms := make([]string, 0, len(drapeauxAcceptes))
	for _, d := range drapeauxAcceptes {
		if d.verifier == nil {
			noms = append(noms, d.nom)
			continue
		}
		noms = append(noms, d.nom+"=<valeur>")
	}
	return strings.Join(noms, ", ")
}

// motifCompte borne un nom de compte système.
//
// Volontairement étroit : cette valeur descend sur la ligne de commande d'un
// processus du pod, et un compte Unix ne porte ni espace ni métacaractère de
// shell. Les formes acceptées couvrent ce qu'on rencontre — `postgres`,
// `www-data`, `deploy.ci`, `svc_app`.
var motifCompte = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)

// verifierCompte contrôle un nom de compte.
func verifierCompte(valeur string) error {
	if !motifCompte.MatchString(valeur) {
		return fmt.Errorf("%q n'est pas un nom de compte", valeur)
	}
	return nil
}

// verifierLimit contrôle un motif `--limit`.
func verifierLimit(valeur string) error {
	if !motifLimit.MatchString(valeur) {
		return fmt.Errorf("motif refusé : %q", valeur)
	}
	return nil
}

// verifierExtraVar contrôle la forme d'un `nom=valeur`, sans rien savoir du
// commit qui autorise le nom.
//
// Ce qui est refusé ici, et pourquoi :
//
//   - un nom qui n'est pas un identifiant Ansible — il ne serait pas
//     atteignable depuis un playbook, et `-e @fichier` s'y attraperait au
//     passage, faute de `=` ;
//   - une valeur portant du Jinja. Le produit ne l'évalue nulle part —
//     `config.ResolveInventory` refuse déjà les filtres, le lecteur d'inventaire
//     Ansible refuse `{{` — et une valeur templatisée ferait exécuter quelque
//     chose que la ligne enregistrée dans l'audit ne montre pas ;
//   - un caractère de contrôle. `ansible-runner` découpe `cmdline` à la façon
//     d'un shell : un saut de ligne dans une valeur y introduirait un argument
//     que l'allowlist n'a jamais vu.
func verifierExtraVar(valeur string) error {
	nom, v, ok := strings.Cut(valeur, "=")
	if !ok {
		return fmt.Errorf("%q n'est pas un `nom=valeur`", valeur)
	}
	if !nomDeVariableAnsible(nom) {
		return fmt.Errorf("%q n'est pas un nom de variable Ansible "+
			"(lettres, chiffres et `_`, ne commençant pas par un chiffre)", nom)
	}
	if strings.Contains(v, "{{") {
		return fmt.Errorf("valeur de %s : le produit n'évalue aucun Jinja, "+
			"et une valeur templatisée ne se lirait pas dans l'audit", nom)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("valeur de %s : caractère de contrôle refusé", nom)
		}
	}
	return nil
}

// --- l'ad-hoc comme genre de run ---

// AdhocTarget est ce qu'un run ad-hoc exécute : un module Ansible, ses
// arguments, et le motif de machines qu'il vise — l'équivalent de
// `ansible <motif> -m <module> -a <args>`.
//
// Elle ne porte PAS le mot de passe d'élévation ni la clé d'accès : ceux-ci
// suivent le même chemin que pour un playbook (§5.5, ADR-0018), parce qu'un
// ad-hoc est un run de playbook auquel il ne manque QUE le playbook.
type AdhocTarget struct {
	Module      string `json:"module"`
	ModuleArgs  string `json:"module_args,omitempty"`
	HostPattern string `json:"host_pattern"`
}

// motifModule borne le nom d'un module Ansible.
//
// Volontairement PERMISSIF sur le choix du module : contrairement à `--limit`
// ou `--become-user`, cette forme n'est PAS une frontière de sécurité — un
// compte autorisé sur un environnement exécute déjà du code arbitraire via un
// playbook (`shell`, `command`), et une allowlist de modules ad-hoc ne fermerait
// rien de réel, seulement une fausse impression de contrôle. Ce qui est vérifié
// ici est la FORME uniquement : un nom court (`shell`) ou une FQCN pointée
// (`ansible.builtin.shell`), pour la même raison que `--extra-vars` — ce qui
// descend vers le domaine hostile de l'ADR-0011 doit avoir une forme connue
// avant de partir.
var motifModule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*){0,2}$`)

// longueurMaxModule borne le nom d'un module — une FQCN Ansible ne s'approche
// jamais de cette taille, et la borne n'existe que pour ce qui descend sur une
// ligne de commande.
const longueurMaxModule = 128

// Validate applique les invariants de la cible.
func (t AdhocTarget) Validate() error {
	if err := ValiderModule(t.Module); err != nil {
		return err
	}
	if err := ValiderModuleArgs(t.ModuleArgs); err != nil {
		return err
	}
	if err := verifierLimit(t.HostPattern); err != nil {
		// Le motif d'un run ad-hoc obéit à la même syntaxe qu'un `--limit` :
		// c'est le même langage de sélection de machines, seulement porté ici
		// en position plutôt qu'en drapeau.
		return fmt.Errorf("motif de machines : %w", err)
	}
	return nil
}

// ValiderModule contrôle la forme d'un nom de module Ansible.
func ValiderModule(nom string) error {
	if nom == "" {
		return errors.New("module absent : un run ad-hoc exécute un module, il faut le nommer")
	}
	if len(nom) > longueurMaxModule {
		return fmt.Errorf("nom de module : %d caractères, maximum %d", len(nom), longueurMaxModule)
	}
	if !motifModule.MatchString(nom) {
		return fmt.Errorf("%q n'est pas un nom de module Ansible valide", nom)
	}
	return nil
}

// ValiderModuleArgs contrôle les arguments d'un module ad-hoc.
//
// Une chaîne vide est légitime : beaucoup de modules de diagnostic (`ping`,
// `setup`) n'en prennent aucun. Le contrôle porte sur ce qui pourrait
// introduire un argument que personne n'a demandé une fois passé par
// `ansible-runner` — le même risque, et la même parade, que pour une valeur
// d'`--extra-vars` (`verifierExtraVar`).
func ValiderModuleArgs(valeur string) error {
	if len(valeur) > longueurMaxValeur {
		return fmt.Errorf("arguments du module : %d caractères, maximum %d", len(valeur), longueurMaxValeur)
	}
	for _, r := range valeur {
		if r < 0x20 || r == 0x7f {
			if r == '\t' {
				continue
			}
			return errors.New("arguments du module : caractère de contrôle refusé")
		}
	}
	return nil
}

// nomDeVariableAnsible est le jumeau de `config.nomDeVariable`, côté cœur.
//
// La duplication est celle que ce dépôt assume déjà pour la regex de nom de
// dépôt : `internal/config` ANALYSE le fichier, le cœur DÉCIDE, et l'un ne
// dépend pas de l'autre.
func nomDeVariableAnsible(n string) bool {
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
