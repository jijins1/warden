// Package events porte le schéma des events de run : structure, numéro de
// séquence, sérialisation, validation.
//
// Socle partagé par les trois domaines de confiance (ADR-0011) : produit par le
// runner, inséré par l'ingestion, servi par l'API, affiché par la CLI.
//
// L'offset est assigné par le runner et monotone par run, ce qui rend
// l'insertion idempotente sur (run_id, seq) (ADR-0008).
//
// Ne doit contenir aucune E/S : ni base, ni HTTP, ni fichier.
package events
