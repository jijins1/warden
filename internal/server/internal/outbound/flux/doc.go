// Package flux suit un run en temps réel : c'est l'adaptateur du port
// Follow(runID, fromSeq) du cœur (ADR-0019).
//
// Séparé de `store` bien que reposant aujourd'hui sur la même base. L'ADR-0008
// inscrit en dette que si le volume d'events dépasse ce que PostgreSQL absorbe,
// un repli consiste à déporter la table d'events vers un store dédié. Séparés,
// ce repli ne touche qu'un adaptateur ; fusionnés, il traverserait le cœur.
//
// # LISTEN / NOTIFY est une optimisation, pas le mécanisme de correction
//
// La correction vient de `seq` : un réplica qui ne recevrait aucune notification
// finit par tout servir correctement s'il relit périodiquement. NOTIFY ne fait
// que supprimer la latence de ce poll, qui reste donc en filet. Une
// implémentation entièrement par poll satisfait honnêtement le port.
//
// La notification ne porte que (run_id, seq), jamais l'event : la charge utile
// de NOTIFY est plafonnée, le réplica doit relire la table de toute façon pour
// ne jamais livrer ce qui n'est pas journalisé (§6.3), et les payloads --diff
// vivent dans une table à contrôle d'accès distinct — les diffuser par un canal
// de notification annulerait cette séparation.
//
// # Contraintes d'exploitation
//
// Une connexion LISTEN par réplica, jamais par client, avec fan-out en mémoire
// vers les WebSockets abonnés au run : sinon on épuise les connexions
// PostgreSQL en sessions oisives. Corollaire : LISTEN ne survit pas à un pooler
// en mode transaction — cette connexion doit être en mode session.
package flux
