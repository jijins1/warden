// Package vaultinventory répond à « avec quelle clé chiffrer et déchiffrer
// les secrets Ansible Vault versionnés de cet environnement » (ADR-0045).
//
// # Périmètre
//
// Ce paquet est la TROISIÈME famille de secrets du produit. L'ADR-0020 en
// posait deux : les credentials d'accès (ce paquet, `keyinventory`) et les
// variables du playbook portées par le shell de l'utilisateur (`forward_env`,
// §5.4). Les secrets Ansible Vault n'appartiennent à aucune des deux — ils ne
// donnent accès à aucun hôte, et ils sont VERSIONNÉS dans le dépôt, pas
// transmis à chaque run.
//
// # Aucun reveal humain
//
// Contrairement à un mot de passe d'élévation ou celui d'un enrôlement, ce
// secret n'a JAMAIS vocation à transiter vers un humain — décision de cadrage
// de l'ADR-0045, cohérente avec le fait qu'aucun secret du produit ne le fait
// aujourd'hui. Le seul chemin d'interaction humaine est `EncryptString` :
// l'appelant donne une valeur en clair, il reçoit un texte chiffré, la clé ne
// fait jamais l'aller-retour.
//
// # Deux magasins pour une clé
//
// Le mot de passe en clair vit dans un Secret Kubernetes, ses métadonnées
// dans PostgreSQL (migration 00015) — même séparation que `keyinventory`, et
// pour la même raison (ADR-0013).
//
// # Ce que ce paquet ne fait pas
//
// Aucune rotation outillée du CONTENU du dépôt : remplacer la clé stockée
// casse le déchiffrement des secrets déjà chiffrés avec l'ancienne, tant
// qu'ils n'ont pas été ré-encodés via `EncryptString` sous la nouvelle. Warden
// ne modifie jamais le contenu d'un dépôt — le SHA audité est en lecture
// seule — donc cette dette est assumée, sur le même principe que la rotation
// non outillée des clés SSH (ADR-0020).
package vaultinventory
