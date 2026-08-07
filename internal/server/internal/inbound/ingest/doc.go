// Package ingest sert l'endpoint d'ingestion des events du runner.
//
// Servi par le rôle `api` mais sur un PORT DISTINCT, avec sa propre règle
// d'ingress NetworkPolicy et son propre rôle PostgreSQL restreint à INSERT
// (ADR-0008). Quand l'ADR-0007 dit que le runner ne joint jamais l'API, cela se
// lit « jamais le port de l'API ».
//
// Ce paquet est séparé de `api` dès le premier jour pour que sa promotion en
// rôle déployé à part reste un changement de déploiement, jamais un changement
// de code.
//
// L'insertion est idempotente sur (run_id, seq) : les réémissions du runner sont
// sans danger et l'ingestion se parallélise sans contrainte d'ordre.
//
// Adaptateur ENTRANT (ADR-0019) : il n'atteint que `core`. C'est le sens
// MONTANT du dialogue avec le pod ; le sens descendant passe par
// `outbound/dispatch`, et cette séparation est délibérée (ADR-0018).
//
// Le lien entre run_id et pod est établi côté serveur, jamais accepté depuis le
// body de la requête : un runner hostile peut mentir sur ses propres events —
// c'est irréductible, il est seul à observer l'exécution — mais il ne doit pas
// pouvoir toucher à ceux d'un autre run.
package ingest
