// Package gitd est le rôle `git` : le seul détenteur du volume des dépôts bare.
//
// Il sert le smart HTTP (receive-pack, upload-pack), tient la quarantaine et le
// scan de secrets, la promotion, la vérification d'ancestry en mode PROD (§3.2),
// les diffs d'audit (§6.5), la maintenance planifiée (ADR-0004) et le cycle de
// vie des espaces de noms de run (ADR-0007).
//
// Une seule instance, PVC RWO, redéploiement en Recreate : c'est un point de
// défaillance unique assumé (ADR-0006). Pendant son indisponibilité, aucun run
// ne démarre dans l'organisation.
//
// Il est joignable depuis les pods runner, donc depuis du code arbitraire : sa
// surface exposée doit rester minimale et son autorisation couverte par un test
// négatif — un run qui tente de lire la ref d'un autre run doit échouer.
//
// Arêtes autorisées (ADR-0006) : store, git. Ce paquet n'importe ni api, ni
// controller.
package gitd
