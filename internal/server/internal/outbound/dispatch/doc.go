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
// Le claim est un UPDATE conditionnel en base, mais la connexion SSE du pod
// réclamé est tenue par UN réplica, pas nécessairement celui qui a gagné le
// claim. L'ordre est donc routé par le même LISTEN / NOTIFY que le flux
// d'events, en sens inverse (ADR-0018 amendée le 2026-08-06).
//
// La notification porte un POINTEUR — l'id du pod — et jamais la charge : le
// réplica qui détient la connexion va chercher l'ordre et les credentials
// lui-même, au moment de livrer. Les faire transiter par NOTIFY les écrirait
// dans le WAL de PostgreSQL, et ferait du journal d'audit un magasin de
// secrets.
//
// # Deux ports, aucune base
//
// Ce paquet ne connaît pas PostgreSQL : il déclare `Pods` et `Orders`, que
// `outbound/store` et l'assemblage implémentent. C'est ce qui le rend testable
// sans base (ADR-0010, étage 1), et l'écoute des notifications vit donc dans
// `store`, pas ici.
//
// # Identité d'amorçage
//
// Le pod s'authentifie par un jeton porteur émis par le contrôleur, dont seule
// l'empreinte est conservée (ADR-0023). Sa capacité unique est « je suis le pod
// chaud N » : il ne nomme aucun run, et le serveur résout `pod -> run` depuis sa
// propre base.
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
