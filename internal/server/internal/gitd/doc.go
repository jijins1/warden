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
// Ce paquet n'est ni un adaptateur entrant ni un adaptateur sortant du cœur : le
// rôle `git` est un SERVICE DISTINCT, qui sert le protocole git aux runners et à
// la CLI sans passer par le cœur, et que le cœur atteint par
// `outbound/gitclient`. Il vit donc à côté de la décomposition hexagonale de
// l'ADR-0019, pas dedans — l'ADR le rangeait à tort côté entrant.
//
// Arêtes autorisées : `outbound/store` et le socle `internal/git`. Ce paquet
// n'importe ni `core`, ni `inbound`, ni les autres adaptateurs.
package gitd
