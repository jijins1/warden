// Package controller réconcilie les pods runner : watch informer, reconciler,
// pool de pods pré-chauffés, reaper (§8.7).
//
// Pas de CRD, l'état vit dans notre base (décision #12). Pas de finalizer
// (#11). `activeDeadlineSeconds` sur le Job borne le coût même si le contrôleur
// est mort (#13).
//
// Le claim d'un pod chaud se fait EN BASE, par un UPDATE conditionnel, pas par
// une annotation Kubernetes : le contrôleur sort ainsi du chemin chaud d'un run
// (ADR-0006). Il n'entretient que l'inventaire du pool.
//
// La leader election par Lease ne garantit pas l'unicité : un contrôleur qui
// perd son bail sans le savoir peut agir en double. Toutes les actions doivent
// donc être idempotentes.
//
// Confinement obligatoire (ADR-0010) : namespace dédié, et SÉLECTEUR DE LABELS
// DANS TOUTE REQUÊTE — jamais un list non filtré, même dans un namespace dédié.
//
// Arête autorisée (ADR-0006) : store. Ce paquet n'importe ni api, ni gitd.
package controller
