// Package command porte l'ARBRE d'aide de la CLI : ce que chaque commande fait,
// comment on l'invoque, et quels drapeaux la concernent.
//
// # Pourquoi un arbre plutôt qu'un texte
//
// Ce paquet a remplacé une constante `texteUsage` de cent lignes, affichée
// intégralement à la moindre faute de frappe. Répondre le manuel entier à « je
// me suis trompé de drapeau » revient à ne pas répondre : l'utilisateur relit
// cent lignes pour en trouver deux.
//
// L'arbre rend trois niveaux, chacun répondant à la question de son niveau :
// l'index dit QUELLES commandes existent, la page d'un groupe dit ce qu'il fait
// et quels verbes il a, la page d'un verbe dit comment on l'invoque et avec
// quoi.
//
// # Les drapeaux viennent du code, pas de l'arbre
//
// L'arbre NOMME les drapeaux d'une commande ; il ne les décrit pas. La
// description est lue dans le `flag.FlagSet` réellement utilisé, ce qui est la
// seule façon de garantir qu'une page ne décrit pas un drapeau qui n'existe
// pas. Deux tests, dans le paquet `cli`, vérifient la correspondance dans les
// deux sens — dont celui qui compte le plus : un drapeau ajouté au code sans
// entrée d'aide fait échouer la suite, au lieu de rester invisible pour
// toujours.
//
// # Les codes de sortie
//
// Une aide DEMANDÉE est une commande qui a réussi : elle sort sur la sortie
// standard et rend 0. Une commande incomplète, inconnue ou mal formée sort sur
// l'erreur standard et rend 1. Le comportement en cas d'échec est un critère
// d'adoption (§5.7) : un message d'erreur doit dire ce qui se passe et ce qu'il
// faut faire — d'où la suggestion du voisin le plus proche, la faute de frappe
// étant la cause la plus fréquente.
package command
