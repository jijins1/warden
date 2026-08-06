// Package store est l'accès à PostgreSQL, journal d'audit et transport des
// events à la fois (ADR-0008) : connexion, migrations embarquées, runs, events,
// pool de pods.
//
// Un seul paquet, fichiers séparés par domaine : l'éclater interdirait d'écrire
// le statut d'un run et ses events dans la même transaction, ce que l'ADR-0008
// exige (ADR-0011).
//
// Adaptateur SORTANT du cœur (ADR-0019). Le suivi temps réel d'un run n'est PAS
// ici : c'est `outbound/flux`, séparé bien que reposant sur la même base, pour
// que le repli de l'ADR-0008 vers un store d'events dédié ne traverse pas le
// cœur.
//
// Les events sont stockés sous forme NORMALISÉE — enveloppe à colonnes (run_id,
// seq, hôte, tâche, statut, changed, horodatage) plus payload de module opaque.
// Le brut d'ansible-runner n'est pas la forme de stockage : l'ADR-0014 laissant
// chaque projet épingler son image, plusieurs versions d'ansible-core coexistent,
// et une requête « tout ce qui s'est passé sur cette machine » ne peut pas
// dépendre de l'union de tous les formats jamais émis. L'index (hôte, date) de
// l'ADR-0008 le supposait déjà.
//
// Deux constructeurs, pour les deux identités de connexion :
//   - Open, accès complet, utilisé par api, gitd et controller ;
//   - OpenIngest, qui ne rend qu'un type limité à l'ajout d'events. La
//     restriction réelle est le rôle PostgreSQL restreint à INSERT ; le type la
//     rend visible dans le code.
//
// Schéma dès la première version : table d'events partitionnée par date et clé
// (run_id, seq) ; index (hôte, date) ; compteur `changed` dénormalisé sur la
// ligne de run ; payloads `--diff` dans une table SÉPARÉE à contrôle d'accès
// distinct, parce qu'ils contiennent les secrets rendus (§6.5).
//
// Les migrations sont appliquées par `wardend migrate`, jamais au démarrage d'un
// pod (ADR-0012). Le partitionnement n'est pas une migration : une table sans
// partition pour la date du jour rejette les insertions, donc arrête tous les
// runs.
package store
