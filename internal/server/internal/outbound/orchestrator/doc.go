// Package orchestrator lance, surveille et supprime les pods runner : watch
// informer, reconciler, pool de pods pré-chauffés, reaper (§8.7).
//
// C'est l'adaptateur Kubernetes du cœur (ADR-0019). Il absorbe ce que la spec
// appelle le « contrôleur » : le nom `controller` a été libéré parce qu'il
// désigne un reconciler dans tout l'écosystème Kubernetes, et que le produit en
// contient un.
//
// # Testabilité
//
// C'est le port dont l'existence n'est pas négociable : l'ADR-0010 a décidé une
// stratégie de test SANS CLUSTER JETABLE, qui n'est réalisable que si client-go
// se trouve derrière une couture simulable. Ce n'est pas du confort, c'est le
// prérequis d'une décision antérieure.
//
// # Décisions portées
//
// Pas de CRD, l'état vit dans notre base (§8.8) : etcd n'est pas une base
// d'audit. Pas de finalizer (§8.7) — un finalizer dont le contrôleur ne tourne
// plus bloque le pod en Terminating pour toujours. `activeDeadlineSeconds` sur
// le Job borne le coût d'un run qui pend même si le contrôleur est mort.
//
// Le claim d'un pod chaud se fait EN BASE, par un UPDATE conditionnel, pas par
// une annotation Kubernetes : l'orchestrateur sort ainsi du chemin chaud d'un
// run (ADR-0006). Il n'entretient que l'inventaire du pool. L'ordre lui-même
// descend par `outbound/dispatch` (ADR-0018).
//
// La leader election par Lease ne garantit pas l'unicité : un contrôleur qui
// perd son bail sans le savoir peut agir en double. Toutes les actions doivent
// donc être idempotentes.
//
// La chute d'une connexion de dispatch signale la disparition d'un pod plus vite
// que l'informer, mais ne la PROUVE pas — une connexion perdue ne dit que
// l'injoignabilité. L'informer reste la source de vérité (ADR-0018).
//
// # Confinement obligatoire (ADR-0010)
//
// Namespace dédié, et SÉLECTEUR DE LABELS DANS TOUTE REQUÊTE — jamais un list
// non filtré, même dans un namespace dédié.
package orchestrator
