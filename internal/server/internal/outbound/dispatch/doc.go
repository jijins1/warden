// Package dispatch est le canal de contrôle vers les pods runner (ADR-0018) :
// SSE sortant, ouvert par le pod dès son démarrage, sur un PORT DISTINCT de
// celui de l'API et de celui de l'ingestion, avec sa propre règle d'ingress.
//
// Il transporte, dans ce sens uniquement : l'ordre de run (SHA, environnement,
// flags EFFECTIFS après filtrage par la politique — jamais la demande brute du
// client), le jeton git de l'ADR-0007, les credentials du claim (ADR-0020) et
// les signaux de contrôle dont l'annulation du §5.6.
//
// Il ne transporte PAS les events : ils remontent par `inbound/ingest`. Séparer
// les deux sens est ce qui empêche une saturation du flux de logs d'emporter la
// capacité d'annuler un run, et préserve le rôle PostgreSQL restreint à INSERT
// de l'ingestion.
//
// # Double nature
//
// C'est un adaptateur SORTANT du cœur — le cœur lui dit « lance ce run » — servi
// par un endpoint HTTP ENTRANT, puisque c'est le pod qui ouvre la connexion.
// Cette asymétrie est réelle et doit rester visible dans le code plutôt que
// masquée derrière une abstraction qui prétendrait l'inverse.
//
// # Routage entre réplicas
//
// Le claim est un UPDATE conditionnel en base (ADR-0006), mais la connexion SSE
// du pod réclamé est tenue par UN réplica, pas nécessairement celui qui a gagné
// le claim. L'ordre doit donc être routé jusqu'au bon réplica — par le même
// LISTEN / NOTIFY que le flux d'events, en sens inverse. L'ADR-0018 décrit le
// claim comme « choisir une connexion oisive », ce qui n'est vrai qu'à un seul
// réplica.
//
// # Interdictions
//
// Le corps des messages n'est JAMAIS journalisé : il porte des credentials, et
// un log de debug sur cet endpoint exposerait ce que toute l'ADR-0017 protège.
// Le lien entre run_id et pod est établi côté serveur, jamais accepté depuis le
// corps d'une requête.
//
// Le protocole est propre au domaine runner : il ne réutilise pas
// `internal/wire/ws`, que l'ADR-0011 interdit au pod hostile de lier.
package dispatch
