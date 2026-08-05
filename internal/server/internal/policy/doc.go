// Package policy compose la configuration effective d'un run et applique
// l'autorisation (§10.2).
//
// Ordre de composition, strictement (ADR-0009) :
//  1. politique serveur de l'environnement (allowed_overrides, allowlists) ;
//  2. `.ansibleproxy.yml` LU DEPUIS LE COMMIT, filtré par 1 ;
//  3. flags explicites de la requête, filtrés par 1.
//
// Le fichier du client est une requête, jamais une politique (décision #7,
// leçon Atlantis). Les variables d'environnement suivent une allowlist, jamais
// une denylist (#6) : les noms viennent du commit, les valeurs du client, et
// les valeurs ne sont JAMAIS journalisées (#5). C'est le résultat effectif qui
// est journalisé, pas ce que le client a demandé.
//
// Paquet séparé de `api` parce que c'est le cœur du modèle de sécurité : il doit
// être testable sans passer par HTTP.
package policy
