// Package api sert l'API HTTP et le flux WebSocket : AuthN OIDC/LDAP (§10.1),
// création de run, relais des events vers la CLI, interface d'audit (§6.5).
//
// À la connexion, l'historique est servi depuis la table à partir de l'offset
// acquitté par le client, puis le direct est suivi par LISTEN / NOTIFY — la
// notification ne portant que run_id et seq, jamais l'event (ADR-0008).
//
// Arêtes autorisées (ADR-0006) : store, policy, gitclient. Ce paquet n'importe
// ni gitd, ni controller, ni ingest.
package api
