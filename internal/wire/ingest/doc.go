// Package ingest porte le contrat réseau du lot d'events runner → ingestion
// (ADR-0008) : forme du lot, plafonds de nombre et de délai, accusé de
// réception.
//
// Séparé de `wire/ws` pour que le pod hostile ne lie pas le protocole WS du
// control plane, qu'il n'utilise jamais (ADR-0011).
//
// Types et (dé)sérialisation uniquement : ni client, ni serveur.
package ingest
