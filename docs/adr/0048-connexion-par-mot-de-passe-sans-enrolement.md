# ADR-0048 — Connexion par mot de passe, sans enrôlement, empreintes vérifiées

**Date** : 2026-10-19
**Statut** : Proposed
**Amende** : [ADR-0020](0020-key-inventory-credentials-dacces.md),
[ADR-0030](0030-enrolement-des-machines.md)

## Contexte

Certains nœuds authentifient leurs utilisateurs par l'annuaire LDAP (sssd /
PAM) : chacun s'y connecte sous son propre compte, avec son mot de passe
d'annuaire. Y poser la clé d'environnement de l'ADR-0020 — l'enrôlement de
l'ADR-0030 — n'a pas de sens : le compte de service n'existe pas, et la
politique de ces nœuds est justement qu'on s'y connecte en son nom.

Deux constats sur le code actuel :

- **Un run n'exige pas que ses machines soient enrôlées.** `managed_host`
  n'est lu que sur le chemin d'enrôlement ; l'inventaire vient du commit
  d'audit (ADR-0009). L'enrôlement ne sert qu'à poser la clé.
- **Les runs ne vérifient aucune empreinte.** `ansible-runner` pose
  `ANSIBLE_HOST_KEY_CHECKING=False` quand rien ne le fixe, et warden ne le
  fixe pas. Les empreintes épinglées à l'enrôlement ne servent qu'à
  l'enrôlement. Avec la clé d'environnement, un nœud usurpé n'obtient rien
  d'utilisable ailleurs ; avec un **mot de passe**, il le reçoit en clair.

## Solutions envisagées

### Option A — Certificats SSH émis par Vault ❌ écartée pour l'instant

Le contrôle n°1 du §10.2, et la raison d'être de la signature de
`KeyIssuer.Issue(user, env, hostScope)`. Écartée parce qu'elle suppose une CA
de confiance déployée sur les nœuds et une correspondance compte warden →
compte LDAP que le produit n'a pas encore (authentification locale seule).
Elle reste la cible.

### Option B — Clé de service publiée dans l'annuaire ❌ rejetée

- **Rejetée parce que** : le compte reste partagé, ce que la politique de ces
  nœuds refuse, et warden devrait écrire dans l'annuaire.

### Option C — Mot de passe LDAP fourni à chaque run ✅

L'utilisateur fournit son mot de passe au lancement, comme `-K` pour
l'élévation. Il transite par la consigne et n'est jamais persisté.

### Vérification des empreintes, pour l'option C

- **Premier contact accepté** ❌ : un nœud usurpé reçoit le mot de passe
  d'annuaire de l'utilisateur.
- **CA d'hôtes SSH** ❌ pour l'instant : suppose une CA côté infrastructure.
- **Déclaration seule** ✅ : le nœud n'est pas enrôlé — rien n'y est posé —
  mais il est **déclaré** avec sa clé d'hôte (`warden host declare`). Un run
  par mot de passe ne parle qu'aux nœuds dont l'empreinte est connue.

## Décision

1. **CLI** : `-k` / `--ask-pass`, `--pass-file`, `--pass-stdin` sur `submit`
   et `run` — les noms d'`ansible-playbook`, la même mécanique de saisie que
   `-K`. `-u` / `--user` fixe le compte distant.
2. **Transit** : le mot de passe est consigné sous `<run_ref>-connection`
   avant la création du run, repris et détruit à la livraison de l'ordre
   (`wire.Order.ConnectionPassword`), masqué dans le flux (ADR-0017).
3. **Runner** : `ansible_password` dans `env/extravars`, à côté de
   `ansible_become_password` et pour les mêmes raisons ; `sshpass` dans
   l'image.
4. **Empreintes** : quand un mot de passe est fourni, l'ordre porte les clés
   d'hôte des machines **déclarées** de l'environnement
   (`wire.Order.KnownHosts`). Le runner les écrit dans un `known_hosts`
   dédié et force `StrictHostKeyChecking=yes` sur ce seul fichier. Un nœud
   non déclaré, ou déclaré sans clé d'hôte, est refusé par ssh — avant
   que le mot de passe parte.

   Ces options passent en **extravars** (`ansible_host_key_checking`,
   `ansible_ssh_args`), pas en variables d'environnement. MESURÉ : un
   inventaire portant `ansible_host_key_checking: false` et
   `ansible_ssh_common_args: -o StrictHostKeyChecking=no` — forme courante,
   et honnête — désarmait la vérification posée par l'environnement. Les
   extravars ont la priorité la plus haute d'Ansible, et `ssh_args` précède
   les autres arguments sur la ligne de ssh, où la première valeur d'une
   option l'emporte. Le prix : un `ansible_ssh_args` de l'inventaire est
   ignoré pendant un run à mot de passe.
5. **Compte distant** : `--user` est le `-u` d'Ansible, donc de priorité
   inférieure à `ansible_user`. Les nœuds enrôlés gardent le compte de leur
   inventaire et leur clé ; les nœuds LDAP, sans `ansible_user`, prennent le
   compte de l'utilisateur. Un même run peut toucher les deux.

## Conséquences

- Un nœud LDAP se déclare, il ne s'enrôle pas. Sa clé d'hôte est la seule
  chose que warden en retient.
- Les logs du nœud nomment l'utilisateur réel, ce que la clé partagée ne
  permet pas (ADR-0020 : « les logs de l'hôte diront ansible »).
- Le mot de passe d'annuaire d'un utilisateur transite par Vault ou par un
  Secret Kubernetes le temps de la livraison, et vit en clair dans le
  conteneur du runner pendant le run — exactement comme celui d'élévation.
  Le playbook, code arbitraire (§2), peut le lire.
- **Hors périmètre, et à traiter** : les runs à clé ne vérifient toujours
  aucune empreinte. La même mécanique s'y appliquerait, au prix de refuser
  les nœuds déclarés sans clé d'hôte.
