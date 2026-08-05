// Package git est l'enveloppe du binaire git, moteur unique du produit
// (ADR-0005) : exécution, timeouts, classification des erreurs, contrôle de
// version, types SHA / Ref / Namespace.
//
// Socle partagé par les trois domaines de confiance (ADR-0011).
//
// Ne doit contenir AUCUNE règle métier — ni quarantaine, ni scan de secrets, ni
// promotion, ni ancestry. Ces règles vivent dans le rôle `git` du serveur. Sans
// cette séparation, le pod hostile lie de la politique serveur et le découpage
// en domaines ne vaut plus rien.
package git
