// Package gitwire porte le contrat de l'API interne du rôle `git`.
//
// Types et (dé)sérialisation uniquement : ni client, ni serveur. La même
// discipline que `internal/wire/dispatch` et `internal/wire/ingest`, dont il
// reprend le nom pour cette raison.
//
// Il ne vit PAS sous `internal/wire/` malgré cette parenté. Ce répertoire est lié
// par les trois binaires, donc par l'image du pod runner, hostile par hypothèse
// (ADR-0011) : y placer le contrat interne du control plane le publierait à un
// adversaire sans qu'aucun runner n'en ait l'usage. Il vit donc à côté de `core`
// et de `gitd`, avec sa propre règle depguard qui lui interdit d'importer quoi
// que ce soit du produit.
//
// Ce paquet est partagé par les deux extrémités — `gitd` le sert,
// `outbound/gitclient` l'appelle — alors que la décomposition de l'ADR-0019 leur
// interdit de se voir. C'est un assouplissement délibéré des règles depguard,
// motivé par l'ADR-0024 : deux définitions jumelles du même message dérivent en
// silence, et la dérive ne se voit qu'en production.
package gitwire
