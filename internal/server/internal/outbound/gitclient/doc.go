// Package gitclient est le client du rôle `api` vers le rôle `git` : ancestry,
// diffs, quarantaine, promotion, lecture d'un blob de configuration.
//
// C'est LA SEULE ARÊTE AUTORISÉE entre deux rôles (ADR-0006). Tout le reste
// transite par PostgreSQL. Le rôle `api` n'a jamais accès au filesystem git.
//
// Adaptateur SORTANT du cœur (ADR-0019), et non différable : l'ADR-0007 place la
// création de la ref d'espace de noms sur le chemin du claim.
//
// L'API interne appelée ici est exposée sur un chemin distinct de celui du smart
// HTTP servi aux runners.
package gitclient
