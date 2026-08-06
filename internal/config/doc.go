// Package config porte le schéma strict de `.ansibleproxy.yml` (§5.4) : lecture,
// plafond de taille, alias et ancres YAML désactivés, champs inconnus refusés
// plutôt qu'ignorés.
//
// Socle partagé par les trois domaines de confiance (ADR-0011).
//
// Ce paquet ANALYSE, il ne DÉCIDE pas. La composition — politique serveur, puis
// configuration lue depuis le commit, puis flags explicites — appartient à
// `internal/server/internal/core` (ADR-0009, ADR-0019). Le contenu du commit est
// une entrée non fiable.
package config
