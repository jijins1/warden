// Package core est le métier du control plane (ADR-0019) : cycle de vie d'un
// run, composition de la configuration effective, autorisation, claim d'un pod
// du pool.
//
// # Les ports sont déclarés ici
//
// Les interfaces que le cœur consomme — orchestrateur, store, flux, dispatch,
// gitclient, key-inventory — sont déclarées DANS ce paquet, jamais dans un
// paquet `ports` partagé. Go n'a pas d'`implements` : l'adaptateur les satisfait
// structurellement, sans avoir à les importer. Le graphe reste donc
// `core -> rien`, `outbound -> core` pour les types du domaine, et seul
// `cmd/wardend` connaît les deux.
//
// # Aucune infrastructure
//
// Ce paquet n'importe ni database/sql, ni net/http, ni os/exec, ni k8s.io. La
// règle est refusée par le CI, pas rappelée en revue (ADR-0011, ADR-0019). Il en
// découle des types de domaine distincts de ceux de `wire` et des lignes SQL :
// les conversions sont le coût assumé de la décision.
//
// # La politique n'est pas un port
//
// Elle vit ici, et pas derrière une interface : la placer derrière un port
// annoncerait qu'on pourrait en changer, alors qu'elle est précisément ce qui ne
// doit jamais être délégué ni surchargé (§5.4.2, leçon Atlantis).
//
// Ordre de composition, strictement (ADR-0009) :
//  1. politique serveur de l'environnement (allowed_overrides, allowlists) ;
//  2. `.ansibleproxy.yml` LU DEPUIS LE COMMIT, filtré par 1 ;
//  3. flags explicites de la requête, filtrés par 1.
//
// Le fichier du client est une requête, jamais une politique (leçon Atlantis).
// Les variables d'environnement suivent une allowlist, jamais une denylist : les
// noms viennent du commit, les valeurs du client, et les valeurs ne sont JAMAIS
// journalisées. C'est le résultat effectif qui est journalisé, pas ce que le
// client a demandé.
package core
