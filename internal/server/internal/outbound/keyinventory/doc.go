// Package keyinventory répond à « avec quoi joindre ces hôtes en tant que cet
// utilisateur » (ADR-0020).
//
//	Issue(user, env, hostScope) -> (credential, expiry)
//
// Issue, pas Get : une CA SSH ne retrouve pas une clé, elle en ÉMET une.
// hostScope et expiry ne servent à rien à l'adaptateur Kubernetes — il renvoie
// la même clé et une expiration vide — mais ce sont exactement les deux
// paramètres qui rendront l'adaptateur Vault indolore, les principals d'un
// certificat dérivant du périmètre (§10.2) et sa validité étant bornée.
//
// # Périmètre
//
// Ce paquet ne couvre QUE les credentials d'accès. Les variables du playbook
// (vault_db_password et consorts) viennent du shell de l'utilisateur via
// forward_env (§5.4) : elles transitent, elles ne se stockent pas côté serveur.
// C'est la famille que l'ADR-0017 masque dans les --diff ; celle-ci n'apparaît
// dans aucun diff.
//
// # Ce que la v1 ne fait pas
//
// Aucun certificat éphémère : le contrôle n°1 du §10.2 n'a pas
// d'implémentation, Vault n'existant pas dans l'environnement cible. En v1
// l'autorisation réelle repose ENTIÈREMENT sur la NetworkPolicy egress. Aucune
// expiration non plus : la rotation est le seul mécanisme de révocation.
//
// La lecture est faite par le control plane au moment du claim, et la livraison
// par `outbound/dispatch`. Jamais de volume monté : il serait fixé à la création
// du pod, donc présent avant le claim (§8.3), et le pod est pré-chauffé.
// Le runner conserve automountServiceAccountToken: false et n'atteint jamais
// l'API Kubernetes.
package keyinventory
