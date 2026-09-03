package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jijins1/warden/internal/config"
)

// NomsReserves liste les variables d'environnement que le produit refuse de
// laisser transmettre.
//
// # Ce que cette liste protège, et ce qu'elle ne protège pas
//
// Elle ne protège PAS l'utilisateur de lui-même : il exécute déjà du Python
// arbitraire par son playbook, et aucune liste ne changera ça. Elle protège
// L'AUDIT, c'est-à-dire la seule chose que l'utilisateur ne doit pas pouvoir
// couper — parce que c'est ce que le produit vend.
//
// # La première entrée est MESURÉE, pas supposée
//
// Le §5.4.1 désignait `ANSIBLE_STDOUT_CALLBACK` comme « la variable qui tue le
// produit ». La mesure dit le contraire : `ansible-runner` la réécrit sans
// condition (`config/_base.py:322`), donc la surcharger est sans effet.
//
// Le vrai tueur est `ANSIBLE_CALLBACK_PLUGINS`, et le mécanisme est en une
// ligne (`config/_base.py:303`) : ansible-runner PRÉFIXE la valeur de
// l'utilisateur devant son propre dossier de plugins. Ansible résout par nom le
// long de ce chemin, premier trouvé gagne — donc un `awx_display.py` déposé dans
// l'arbre audité est chargé SOUS LE NOM MÊME qu'ansible-runner s'apprête à
// utiliser. Mesuré : le flux d'events se vide, sans une erreur.
//
// # Les deux suivantes sont de la prudence assumée
//
// `PYTHONPATH` et `LD_PRELOAD` ont SURVÉCU à l'attaque menée contre elles, mais
// l'ombrage par paquet Python n'a pas été testé pour la première, et la seconde
// pas du tout. Les réserver est gratuit ; prétendre qu'elles sont sûres
// demanderait une preuve qu'on n'a pas.
var NomsReserves = []string{
	"ANSIBLE_CALLBACK_PLUGINS",
	"PYTHONPATH",
	"LD_PRELOAD",
}

// Requete porte ce que l'utilisateur a demandé au moment de l'appel.
//
// Les flags explicites et les VALEURS des variables d'environnement, et rien
// d'autre : l'ADR-0009 interdit qu'un champ de configuration transite par la
// requête. Ce qui vient du commit arrive par `config.Project`.
type Requete struct {
	// Environment et Inventory sont les flags explicites, vides quand
	// l'utilisateur ne les a pas donnés.
	Environment string
	Inventory   string

	// ForwardEnv porte les VALEURS lues dans le shell de l'appelant. Elles ne
	// peuvent venir que de là (§5.4), et elles ne sont JAMAIS journalisées.
	ForwardEnv map[string]string

	// Flags porte les drapeaux d'`ansible-playbook` demandés (§5.5), dont la
	// FORME a déjà été validée par `ValiderFlags`.
	//
	// Ce qui reste à décider ici est l'appartenance des `--extra-vars` à
	// l'`extra_vars:` du commit : c'est la seule partie de l'allowlist qui
	// dépende du projet, donc la seule qui ne pouvait pas être tranchée avant de
	// l'avoir lu.
	Flags []string
}

// Effectif est ce qui s'applique réellement, après composition.
//
// C'est LUI qui se journalise, et non ce que le client a demandé (§5.4.2).
type Effectif struct {
	Environment string
	Inventory   string
	RunnerImage string

	// ForwardEnv ne contient que ce qui a passé les deux filtres : déclaré dans
	// le commit ET fourni par le shell. Les valeurs sont sensibles.
	ForwardEnv map[string]string

	// Flags sont les drapeaux retenus, dans l'ordre où le client les a donnés.
	//
	// Identiques à ceux de la requête quand elle est acceptée : `composerFlags`
	// REFUSE au lieu d'amputer, contrairement à `composerForwardEnv`. Voir là-bas
	// pourquoi l'asymétrie est voulue.
	Flags []string
}

// Composer applique l'ordre de composition de l'ADR-0009.
//
//  1. politique serveur de l'environnement — ÉCARTÉE en v1 (ADR-0021), donc
//     neutre ici. La couche existe dans l'ordre du code pour n'avoir pas à être
//     rétro-ajoutée ;
//  2. `.ansibleproxy.yml` lu depuis le commit ;
//  3. flags explicites de la requête.
//
// L'ordre 2 puis 3 est ce qui se voit dans le code : la configuration pose la
// valeur, le flag la remplace s'il existe.
//
// LE POINT DE VIGILANCE est l'inventaire templatisé. Il dépend de
// l'environnement EFFECTIF, donc l'environnement se résout AVANT lui. Inverser
// les deux ferait templatiser avec l'environnement du fichier alors que
// l'utilisateur en a demandé un autre — un run qui viserait la production avec
// l'inventaire de recette, sans que rien ne le signale.
func Composer(projet config.Project, req Requete) (Effectif, error) {
	eff := Effectif{
		Environment: projet.DefaultEnv,
		RunnerImage: projet.RunnerImage,
	}
	if req.Environment != "" {
		eff.Environment = req.Environment
	}

	switch {
	case req.Inventory != "":
		// Le flag explicite prime, et le template du fichier n'est alors pas
		// résolu du tout : l'utilisateur a désigné un chemin, pas une convention.
		eff.Inventory = req.Inventory
	default:
		inv, err := config.ResolveInventory(projet.Inventory, eff.Environment)
		if err != nil {
			return Effectif{}, fmt.Errorf("inventaire du projet : %w", err)
		}
		eff.Inventory = inv
	}

	forward, err := composerForwardEnv(projet.ForwardEnv, req.ForwardEnv)
	if err != nil {
		return Effectif{}, err
	}
	eff.ForwardEnv = forward

	flags, err := composerFlags(projet.ExtraVars, req.Flags)
	if err != nil {
		return Effectif{}, err
	}
	eff.Flags = flags

	return eff, nil
}

// PrefixeReserveExtraVar borne ce qu'un `extra_vars:` ne peut pas déclarer.
//
// Tout le préfixe et pas une liste de noms, et c'est le point : les variables
// `ansible_*` décrivent COMMENT le run se connecte — `ansible_become_password`,
// `ansible_ssh_private_key_file`, `ansible_user`, `ansible_host`,
// `ansible_python_interpreter`. Les laisser poser par `-e` rendrait à la requête
// le pouvoir de redéfinir la connexion, c'est-à-dire de sortir de ce que
// l'inventaire audité décrit — exactement ce que l'ADR-0009 ferme ailleurs.
//
// `ansible_become_password` mérite d'être nommé à part : les valeurs d'`-e`
// sont AUDITÉES (ADR-0039), donc l'autoriser écrirait un mot de passe en clair
// dans `run.flags`. Le mot de passe d'élévation a son propre chemin, qui ne
// persiste rien.
const PrefixeReserveExtraVar = "ansible_"

// composerFlags croise les NOMS de variables du commit avec les VALEURS de la
// requête, comme `composerForwardEnv` le fait pour le shell.
//
// L'ASYMÉTRIE avec `composerForwardEnv` est délibérée et vaut d'être lue : celui-ci
// REFUSE un nom non déclaré, là où l'autre le jette en silence.
//
// La raison est la nature de la source. `forward_env` puise dans un shell
// ambiant, où des variables existent sans que personne les ait voulues pour ce
// run : en écarter une n'a rien à signaler. Un `-e version=1.2.3` est au
// contraire un acte explicite de l'opérateur, tapé pour ce run-là. L'ignorer
// ferait partir un déploiement SANS la version demandée, et le message d'échec
// arriverait de la machine cible plutôt que d'ici.
func composerFlags(declares []string, demandes []string) ([]string, error) {
	if len(demandes) == 0 {
		return nil, nil
	}

	autorises := make(map[string]struct{}, len(declares))
	for _, n := range declares {
		if strings.HasPrefix(n, PrefixeReserveExtraVar) {
			return nil, fmt.Errorf("extra_vars : %s est réservée par warden et ne peut pas être "+
				"déclarée — les variables `%s*` décrivent comment le run se connecte, "+
				"et les poser depuis la requête sortirait de l'inventaire audité",
				n, PrefixeReserveExtraVar)
		}
		autorises[n] = struct{}{}
	}

	for _, f := range demandes {
		nom, ok := strings.CutPrefix(f, "--extra-vars=")
		if !ok {
			continue
		}
		nom, _, _ = strings.Cut(nom, "=")
		if _, permis := autorises[nom]; !permis {
			// Le message NOMME ce que le commit déclare : sans ça, l'utilisateur
			// ne sait pas s'il s'est trompé de nom ou s'il doit éditer le
			// fichier de projet.
			return nil, fmt.Errorf("extra-var %q : le fichier de projet du commit ne la déclare pas. "+
				"%s", nom, ceQueLeProjetDeclare(declares))
		}
	}
	return demandes, nil
}

func ceQueLeProjetDeclare(declares []string) string {
	if len(declares) == 0 {
		return "il ne déclare aucun `extra_vars:`"
	}
	tries := append([]string(nil), declares...)
	sort.Strings(tries)
	return "il déclare : " + strings.Join(tries, ", ")
}

// composerForwardEnv croise les NOMS du commit avec les VALEURS du client.
//
// Le croisement est le mécanisme d'allowlist : le fichier versionné déclare ce
// qui a le droit de passer, et une valeur envoyée pour un nom non déclaré est
// jetée sans bruit — elle n'a jamais été autorisée, il n'y a rien à signaler.
//
// Un nom RÉSERVÉ, lui, fait échouer bruyamment. C'est délibérément asymétrique :
// l'écarter en silence laisserait l'auteur du fichier chercher pendant une heure
// pourquoi son playbook ne voit pas sa variable, et c'est exactement le mode de
// panne que l'ADR-0009 refuse ailleurs en refusant les champs inconnus.
func composerForwardEnv(declares []string, valeurs map[string]string) (map[string]string, error) {
	if len(declares) == 0 {
		return nil, nil
	}

	reserves := make(map[string]struct{}, len(NomsReserves))
	for _, n := range NomsReserves {
		reserves[n] = struct{}{}
	}

	out := make(map[string]string, len(declares))
	for _, nom := range declares {
		if _, interdit := reserves[nom]; interdit {
			return nil, fmt.Errorf("forward_env : %s est réservée par warden et ne peut pas être "+
				"transmise — la surcharger couperait la capture d'audit du run", nom)
		}
		v, fournie := valeurs[nom]
		if !fournie {
			// Déclarée mais absente du shell : on ne transmet rien. Poser une
			// chaîne vide écraserait la valeur que la machine cible possède
			// peut-être déjà.
			continue
		}
		out[nom] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// NomsTransmis rend les noms retenus, triés.
//
// C'est ce qui se JOURNALISE — les noms, jamais les valeurs (décision produit
// n°5). Trié pour qu'une même composition produise la même ligne de journal :
// l'ordre d'itération d'une map en Go varie d'un run à l'autre, et un journal
// dont l'ordre bouge sans que rien n'ait changé se relit mal.
func NomsTransmis(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CleForwardEnv nomme la consigne des valeurs de variables d'un run.
//
// Distincte de CleBecome et du `run_ref` nu, pour la raison que CleBecome
// énonce déjà : deux clefs homonymes se marcheraient dessus le jour où quelque
// chose change, et la panne ne se verrait qu'à l'exécution.
func CleForwardEnv(runRef string) string { return runRef + "-forward-env" }
