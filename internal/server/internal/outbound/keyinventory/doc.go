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
// d'implémentation, Vault n'existant pas dans l'environnement cible. Aucune
// expiration non plus : la rotation est le seul mécanisme de révocation, et
// elle n'est pas outillée.
//
// L'ADR-0020 rabattait l'autorisation sur le contrôle n°2 — la NetworkPolicy
// egress. CE REPLI N'EXISTE PLUS : l'egress des runners est ouvert
// (`deploy/60-networkpolicy.yaml`). Il faut donc lire ce paquet pour ce qu'il
// est devenu — un magasin d'ACCÈS, pas d'autorisation. La clé qu'il émet ouvre
// tout ce qui l'accepte, et rien dans le produit ne borne aujourd'hui ce que le
// playbook en fait.
//
// # Deux magasins pour une clé
//
// La partie PRIVÉE vit dans un Secret Kubernetes, la partie PUBLIQUE et les
// métadonnées dans PostgreSQL (migration 00007). La séparation est la décision :
// un seul magasin obligerait à choisir entre un journal d'audit qui contient des
// secrets — répliqué, sauvegardé, jamais purgé (ADR-0013) — et une configuration
// qu'on ne peut pas requêter.
//
// La lecture est faite par le control plane au moment de la LIVRAISON de l'ordre
// et non du claim (ADR-0018 amendée) : c'est ce qui garantit qu'aucun credential
// n'est persisté, ni en base ni dans le WAL par une notification. Jamais de
// volume monté — il serait fixé à la création du pod, donc présent avant le
// claim (§8.3). Le runner conserve automountServiceAccountToken: false et
// n'atteint jamais l'API Kubernetes.
package keyinventory
