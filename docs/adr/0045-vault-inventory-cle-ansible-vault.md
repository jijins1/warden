# ADR-0045 — `vault-inventory` : la clé Ansible Vault, une par environnement

**Date** : 2026-09-26
**Statut** : Accepted
**Complète** : [ADR-0020](0020-key-inventory-credentials-dacces.md), [ADR-0038](0038-flags-ansible-check-diff-limit.md)

## Contexte

`docs/reste-a-faire.md` nomme explicitement un trou : l'ADR-0038 refuse
`--vault-password-file`, `--vault-id` et `--ask-vault-pass` sur l'allowlist des
flags Ansible, parce qu'ils feraient venir le secret d'un fichier du pod, hors
du magasin de l'ADR-0020. C'est marqué comme *« le seul refus de l'ADR-0038 qui
laisse un besoin RÉEL sans alternative »* : `forward_env` (§5.4) couvre les
secrets du shell de l'utilisateur, pas les secrets **versionnés dans le
dépôt** — un `vault_db_password: !vault |...` dans un fichier de variables
Ansible.

Le produit a déjà résolu une fois un problème de cette forme, pour un autre
secret. L'ADR-0020 pose deux familles de secrets et un seul magasin côté
serveur pour l'une d'elles :

| | Origine | Exposition |
|---|---|---|
| **Credentials d'accès** (clés SSH) | le serveur | jamais dans un playbook, jamais dans un `--diff` |
| **Variables du playbook** (`vault_db_password`…) | le **shell de l'utilisateur**, via `forward_env` | rendues dans les fichiers — c'est ce que l'ADR-0017 masque |

Un secret Ansible Vault n'appartient à AUCUNE des deux. Il ne donne accès à
aucun hôte — ce n'est pas un credential d'accès. Et il n'est pas transmis à
chaque run depuis le shell de l'utilisateur — il est **committé, chiffré,
dans le dépôt**, et doit être déchiffrable par n'importe quel run qui le lit,
qu'un humain particulier soit présent ou non ce jour-là.

Deux points de cadrage, tranchés avant l'implémentation.

**Aucun secret du produit ne transite aujourd'hui vers un humain.** La clé
SSH, le jeton git, le mot de passe d'élévation vont tous exclusivement
serveur → pod. Introduire un `warden vault reveal` casserait cette propriété
pour la première fois, alors que le besoin réel — chiffrer une nouvelle
valeur pour un playbook — ne demande pas de connaître la clé, seulement de
pouvoir s'en servir.

**La granularité suit celle des clés SSH.** Une clé par environnement, pas de
`--vault-id` nommés multiples en v1. Le besoin documenté dans
`reste-a-faire.md` ne demande qu'un secret par environnement ; en ajouter
plusieurs maintenant serait de la surface non demandée.

## Solutions envisagées

### Option A — Attendre un vrai magasin de secrets (HashiCorp Vault)
Ne rien implémenter tant qu'un composant dédié n'est pas déployé, comme
l'option A de l'ADR-0020 pour les credentials d'accès.
- **Rejetée parce que** : même raisonnement que l'ADR-0020 — ça conditionne
  une fonctionnalité déjà demandée à un composant que personne ne prévoit
  d'installer, et ça laisse le produit sans réponse indéfiniment à un besoin
  déjà nommé « réel ».

### Option B — Autoriser `--vault-password-file` sur l'allowlist, pointant un fichier posé par l'opérateur sur chaque pod
Amender l'ADR-0038 pour accepter le flag, le fichier étant posé par ailleurs
(volume, ConfigMap).
- **Rejetée parce que** : ça réintroduit exactement ce que l'ADR-0018 interdit
  pour les credentials d'accès — un secret monté, présent AVANT le claim, sur
  un pod pré-chauffé (ADR-0015). Ce serait aussi une clé UNIQUE pour tous les
  environnements faute d'un mécanisme pour la faire varier par run, ce que
  l'ADR-0038 n'a pas les moyens d'exprimer via un simple nom de fichier fixe.

### Option C — `warden vault reveal`, la clé en clair rendue à un administrateur
Symétrique du flux natif `ansible-vault` : l'opérateur récupère le mot de
passe et s'en sert localement pour chiffrer, déchiffrer, ou faire un `rekey`.
- **Rejetée parce que** : c'est le seul secret qui romprait la propriété
  « aucun secret ne transite vers un humain », qui tient pour tout le reste du
  produit. Le besoin réel — ajouter un secret chiffré à un playbook — ne
  demande pas cette exposition.

### Option D — Un composant `vault-inventory`, jamais de reveal, chiffrement server-side ✅ (retenue)
Une troisième famille de secrets, avec son propre magasin sur le modèle exact
de `key-inventory` (Secret Kubernetes pour la valeur, PostgreSQL pour les
métadonnées), et un seul point d'interaction humaine : `EncryptString`, qui
prend une valeur en clair et rend un bloc chiffré, sans jamais faire circuler
la clé elle-même.
- **Choisie parce que** : elle répond au besoin documenté sans réimplémenter
  ce que l'ADR-0020 a déjà décidé de ne pas réimplémenter, et sans introduire
  la première exception à « aucun secret vers un humain ». La limitation (pas
  de rekey outillé) est nommée et localisée, comme l'ADR-0020 l'a fait pour la
  rotation des clés SSH.

## Décision

**Un troisième rôle sortant, `vault-inventory`**, au sens de l'ADR-0019 : la
question qu'il répond est *avec quelle clé chiffrer et déchiffrer les secrets
versionnés de cet environnement*.

### Deux magasins pour une clé, comme pour les credentials d'accès

Le mot de passe en clair vit dans un Secret Kubernetes
(`warden-vault-<environment>`, clé de données `vault_password`), jamais en
base. PostgreSQL (migration `00015`, table `vault_key`) porte les métadonnées
interrogeables : une empreinte SHA-256 pour comparer sans révéler, un
pointeur vers le Secret, l'origine (`generated`/`imported`), et qui a posé la
clé. Même raisonnement que la migration `00007` : un seul magasin obligerait
à choisir entre un journal d'audit qui contient des secrets et une
configuration qu'on ne peut pas requêter (ADR-0013).

### Livraison : au claim, jamais monté

Lue par `wardend` au moment de la LIVRAISON de l'ordre — pas du claim — et
livrée au pod par le canal SSE de l'ADR-0018, dans un nouveau champ
`Order.VaultPassword`, exactement comme `SSHKey`. Jamais de volume monté :
même raisonnement que l'ADR-0020, un Secret monté serait présent sur un pod
pré-chauffé avant qu'un run ne le réclame.

### Le runner écrit un fichier, ansible-playbook le lit par son propre mécanisme

Contrairement à la clé SSH — chargée dans un `ssh-agent` puis effacée du
disque — la clé de vault est écrite dans `<pdd>/env/vault_password` (0600) et
RESTE sur le disque éphémère du pod pour la durée du run : Ansible relit ce
fichier à chaque tâche qui référence un `!vault |`, il n'y a pas d'équivalent
d'agent pour ce mécanisme. `--vault-password-file <chemin>` est ajouté à la
ligne de commande **par le runner lui-même**, jamais via le champ `Flags`
filtré par l'allowlist de l'ADR-0038 : ce n'est pas une réouverture du refus
de cette ADR, qui visait un flag *fourni par l'appelant* pointant un fichier
arbitraire, pas une valeur que ce paquet vient d'écrire lui-même à partir
d'un secret émis par le serveur — la même distinction qui vaut déjà pour
`SSHKey`.

Puisque ce fichier reste en clair pendant tout le run, contrairement à la clé
SSH, la clé de vault ENTRE dans le jeu de rédaction de l'ADR-0017
(`redact.Secret`), au même titre que le mot de passe d'élévation : un module
qui la recopierait dans sa sortie, par bug ou par malice (§2), la verrait
masquée.

### Aucun reveal humain : `EncryptString` est le seul chemin

Le port `core.VaultStore` n'a pas de méthode qui rende la valeur en clair à
un appelant humain. Le seul chemin d'interaction est :

```
EncryptString(environment, plaintext, varName) -> bloc "!vault |" chiffré
```

Implémentée en s'appuyant sur `github.com/sosedoff/ansible-vault-go` plutôt
que sur une réimplémentation maison du format `$ANSIBLE_VAULT;1.1;AES256` : un
format à interopérabilité stricte avec Ansible, où une erreur d'un octet dans
un codec PBKDF2/AES-CTR/HMAC maison ne se verrait qu'au déchiffrement, en
production.

### Autorisation : le droit d'environnement, pas l'administration globale

Les trois routes (`GET/POST /environments/{env}/vault`,
`POST /environments/{env}/vault/encrypt`) passent par
`autoriserLEnvironnement` — le même droit d'accès par environnement (ADR-0033)
qu'un run sur cet environnement, pas `adminOuRien`. Contrairement à une clé
SSH — qui décide de ce qui peut joindre un hôte de production, donc relève de
l'administration globale (`/admin/keys`) — une clé de vault ne gouverne que le
déchiffrement des secrets d'un playbook, un périmètre déjà couvert par le
droit d'écriture sur l'environnement.

### Granularité

Une clé par environnement, même découpage que `ssh_key`. Pas de `--vault-id`
nommé en v1 : si le besoin apparaît, il s'ajoutera à la table `vault_key` par
une colonne de plus dans la clé primaire, sans changer le port `core.VaultIssuer`.

## Conséquences

- **Avantages** :
  - le besoin documenté dans `reste-a-faire.md` a une réponse, sans
    réimplémenter ce que l'ADR-0020 a déjà décidé de ne pas réimplémenter ;
  - le produit garde sa propriété « aucun secret ne transite vers un humain »
    intacte — `EncryptString` chiffre, il ne révèle rien ;
  - la signature du port (`VaultIssuer.Issue(environment)`) est délibérément
    plus simple que celle de `KeyIssuer` (pas de `user` ni `hostScope`) : rien
    dans le besoin documenté n'appelle une dérivation par utilisateur pour un
    secret de déchiffrement, contrairement à un certificat SSH éphémère.

- **Contraintes** :
  - `wardend` acquiert une permission RBAC de plus sur les Secrets de son
    namespace — déjà accordée en pratique par la règle existante de
    `charts/warden/templates/rbac.yaml`, qui ne restreint pas par nom, donc
    aucun changement de chart n'était nécessaire ;
  - la clé de vault, contrairement à la clé SSH, reste en clair sur le disque
    du pod pour la durée du run — risque déjà accepté pour le mot de passe
    d'élévation (`extravars`), pas un nouveau precedent, mais à ne pas oublier
    si un jour le produit resserre ce cas.

- **Dette technique assumée**, à lire sans atténuation :
  - **Pas de rotation outillée du CONTENU du dépôt.** Remplacer la clé
    stockée (rotation du Secret) ne re-chiffre PAS les fichiers déjà committés
    avec l'ancienne clé. Warden ne modifie jamais le contenu d'un dépôt — le
    SHA audité est en lecture seule — et comme la clé n'est jamais révélée,
    personne ne peut faire un `ansible-vault rekey` manuel avec les deux
    valeurs. Remplacer la clé d'un environnement casse donc le déchiffrement
    des secrets existants tant qu'ils n'ont pas été ré-encodés via
    `warden vault encrypt` sous la nouvelle clé. Miroir exact de la dette que
    l'ADR-0020 nomme pour la rotation des clés SSH — « elle doit être écrite »
    y est resté sans suite pour les clés SSH ; ici, l'absence est plus
    structurelle encore, puisqu'elle découle directement du refus du reveal.
  - Aucun chiffrement au repos par défaut : même limite que `ssh_key`, un
    Secret Kubernetes est du base64 dans etcd.
