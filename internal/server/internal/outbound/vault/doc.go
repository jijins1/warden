// Package vault parle à HashiCorp Vault pour les secrets du produit (ADR-0047).
//
// Deux choses vivent ici :
//
//   - Client, un client minimal : authentification AppRole, et les trois
//     opérations KV v2 dont le produit a besoin — écrire, lire, détruire ;
//   - Consigne, le magasin des valeurs de passage (core.SecretStash) sur un
//     montage KV v2 dédié.
//
// Les clés d'accès et les clés Ansible Vault restent dans `keyinventory` et
// `vaultinventory`, qui reçoivent le Client par une interface déclarée chez
// eux : deux adaptateurs sortants ne s'importent pas (ADR-0019), et la logique
// de ces magasins — dérivation de la partie publique, chiffrement — est la même
// quel que soit l'endroit où la valeur est rangée.
//
// # Pas de SDK
//
// Le client officiel apporte des dizaines de dépendances pour quatre appels
// HTTP. Celui-ci tient en un fichier, se teste contre un faux serveur
// `httptest`, et ne fait rien que le produit n'utilise.
//
// # Détruire, pas effacer
//
// Sur KV v2, `DELETE data/<clé>` n'est qu'un effacement logique, annulable
// par `undelete` : la valeur resterait récupérable. Ce paquet ne connaît que
// `DELETE metadata/<clé>`, qui détruit toutes les versions et la clé
// elle-même.
//
// # Jamais une valeur dans une erreur
//
// Les erreurs de ce paquet remontent jusqu'à des traces. Elles citent le
// chemin et le statut, jamais le corps d'une requête.
package vault
