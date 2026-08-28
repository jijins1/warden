package core

import (
	"errors"
	"fmt"
	"sort"
)

// ErrDocumentVide refuse un document qui ne déclare aucun environnement.
var ErrDocumentVide = errors.New("le document ne déclare aucun environnement")

// GrantDocument est ce qu'un dépôt GitOps versionne.
//
// La forme est un ENVIRONNEMENT vers une liste de comptes, et non une liste de
// paires : c'est ainsi qu'on lit la question — « qui peut atteindre prod » — et
// c'est ce qui rend une revue de PR lisible. Une liste de paires obligerait le
// relecteur à agréger de tête pour voir qui gagne ou perd un accès.
type GrantDocument struct {
	Environments map[string][]string `json:"environments" yaml:"environments"`
}

// GrantChange est une ligne du plan.
type GrantChange struct {
	Subject     string
	Environment string
}

// GrantPlan est la différence entre ce que le document veut et ce qui est.
//
// Un PLAN et non une application directe : c'est lui qui permet le `--dry-run`,
// et le dry-run est ce qui rend la revue possible. Un changement de droits sur la
// production doit être lisible dans une pull request AVANT d'être appliqué, sans
// quoi le versionnement n'achète qu'un historique.
type GrantPlan struct {
	Accorder []GrantChange
	Revoquer []GrantChange

	// Inchanges compte ce qui est déjà conforme. Il n'est pas décoratif : un plan
	// vide et un plan qui ne touche rien parce que tout est déjà en place se
	// lisent pareil sans ce compteur, et l'opérateur ne sait pas s'il vient
	// d'appliquer un fichier ou un fichier vide.
	Inchanges int
}

// Vide dit si le plan ne change rien.
func (p GrantPlan) Vide() bool { return len(p.Accorder) == 0 && len(p.Revoquer) == 0 }

// PlanifierLesDroits calcule la différence, sans rien écrire.
//
// PURE : ni base, ni réseau. C'est ce qui rend la sémantique — la partie qui
// peut retirer l'accès de quelqu'un à la production — vérifiable sans
// infrastructure.
//
// LA PORTÉE est l'environnement, pas le document. Un environnement ABSENT du
// document n'est pas touché ; un environnement PRÉSENT voit sa liste faire
// autorité, donc un compte retiré de la liste est révoqué.
//
// Ce choix a deux conséquences voulues. Il permet de découper les droits en
// plusieurs fichiers — une équipe possède ses environnements sans pouvoir
// toucher ceux des autres. Et il borne le dégât d'un fichier tronqué : la
// troncature retire des environnements du document, ce qui les laisse
// INTACTS, au lieu de tout révoquer.
//
// La contrepartie est réelle et doit être connue : supprimer un bloc entier ne
// révoque rien. C'est le problème classique du « prune » ; `warden grant list`
// reste la seule vue complète.
func PlanifierLesDroits(doc GrantDocument, actuels []EnvironmentGrant) (GrantPlan, error) {
	if len(doc.Environments) == 0 {
		// Un document vide est presque toujours un accident — un fichier tronqué,
		// une mauvaise clé YAML. « Ne rien vouloir » et « n'avoir rien pu lire »
		// se ressemblent trop pour qu'on applique le premier sans le dire.
		return GrantPlan{}, ErrDocumentVide
	}

	// Ce qui est VIVANT aujourd'hui, indexé par environnement puis par compte.
	// Les droits révoqués sont ignorés : ils font partie de l'histoire, pas de
	// l'état.
	vivants := map[string]map[string]struct{}{}
	for _, g := range actuels {
		if g.Revoked() {
			continue
		}
		if vivants[g.Environment] == nil {
			vivants[g.Environment] = map[string]struct{}{}
		}
		vivants[g.Environment][g.Subject] = struct{}{}
	}

	var plan GrantPlan
	for _, env := range triees(doc.Environments) {
		if err := ValidateEnvironment(env); err != nil {
			return GrantPlan{}, fmt.Errorf("environnement %q : %w", env, err)
		}

		voulus := map[string]struct{}{}
		for _, sujet := range doc.Environments[env] {
			if sujet == "" {
				return GrantPlan{}, fmt.Errorf("environnement %q : un compte vide est déclaré", env)
			}
			voulus[sujet] = struct{}{}
		}

		// Ce qu'il faut accorder.
		for _, sujet := range triesDe(voulus) {
			if _, deja := vivants[env][sujet]; deja {
				plan.Inchanges++
				continue
			}
			plan.Accorder = append(plan.Accorder, GrantChange{Subject: sujet, Environment: env})
		}

		// Ce qu'il faut révoquer : présent en base, absent du document. C'est LA
		// moitié qui distingue une réconciliation d'un simple ajout, et c'est
		// elle qui donne au fichier sa valeur — retirer quelqu'un du fichier doit
		// lui retirer l'accès, sans quoi le versionnement ne sert qu'à consigner.
		for _, sujet := range triesDe(vivants[env]) {
			if _, voulu := voulus[sujet]; !voulu {
				plan.Revoquer = append(plan.Revoquer, GrantChange{Subject: sujet, Environment: env})
			}
		}
	}
	return plan, nil
}

// triees rend les clés dans un ordre stable.
//
// L'ordre n'est pas cosmétique : le plan s'affiche en `--dry-run`, il se compare
// d'une exécution à l'autre, et un ordre de parcours de map le rendrait
// différent à chaque fois pour un même fichier.
func triees(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func triesDe(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
