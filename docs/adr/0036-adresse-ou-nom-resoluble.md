# ADR-0036 — L'adresse d'une machine accepte un nom, et le risque change de main

**Date** : 2026-09-01
**Statut** : Accepted
**Renverse** : la §2 de l'[ADR-0030](0030-enrolement-des-machines.md) — « le formalisme : l'adresse est obligatoire »

## Contexte

Premier usage réel du produit, sur une flotte de cinq machines décrites par un
dépôt Ansible. L'inventaire de ce dépôt existait déjà et nommait ses machines ;
plusieurs n'avaient pas d'`ansible_host`, parce qu'Ansible n'en a pas besoin —
il résout le nom au moment de se connecter, comme n'importe quel client SSH.

Pour enrôler ces machines dans warden, il fallait leur trouver une adresse IP et
la ressaisir dans un fichier de déclaration. Deux descriptions de la même flotte,
dont l'une est tenue à la main et ne se met à jour que quand quelqu'un y pense.

L'ADR-0030 avait posé l'adresse comme obligatoire, avec un argument qui n'a rien
perdu de sa force :

> Dépendre d'une résolution DNS faite **dans le pod** ferait enrôler une machine
> que l'opérateur croyait désigner — et une machine enrôlée à la mauvaise adresse
> est une machine dont on croit détenir l'accès.

La question posée ici n'est donc pas « cet argument était-il faux ». Il ne l'est
pas. Elle est : **ce que cette contrainte protège, est-ce elle qui le protège ?**

## Solutions envisagées

### Option A — Résoudre le nom côté CLI, stocker l'IP
La CLI résout `kodi.lan` au moment de la déclaration et envoie `192.168.16.2`.
- **Avantage** : aucun changement de schéma, la base garde son `inet`.
- **Rejetée parce que** : elle résout au mauvais endroit et au mauvais moment.
  La connexion a lieu **depuis le pod**, des semaines plus tard ; le résolveur du
  poste de l'opérateur n'a aucune autorité sur ce que le pod verra. Pire, elle
  fige une IP dans la base : le jour où la machine change d'adresse, warden garde
  l'ancienne et l'opérateur ne le sait pas, alors que le nom, lui, aurait suivi.
  Elle donne l'apparence de la contrainte de l'ADR-0030 sans sa substance.

### Option B — Un second champ, `hostname`, à côté de `address`
- **Rejetée parce que** : deux champs pour une seule question — où se connecte-t-on
  — dont il faudrait alors décider lequel gagne. C'est la forme la plus sûre de
  se retrouver avec deux vérités.

### Option C — `address` accepte une adresse OU un nom ✅ (retenue)
Le champ garde son nom et son rôle ; sa forme s'élargit. La colonne passe de
`inet` à `text`, avec une contrainte de forme grossière ; `core.ValiderAdresse`
distingue les deux cas et garde, pour les IP, les refus qui n'ont de sens que
pour elles (`0.0.0.0`, le multicast).

- **Choisie parce que** : **ce n'est pas la forme de ce champ qui protège de
  joindre la mauvaise machine, c'est `host_key`.** L'empreinte épinglée est
  vérifiée à chaque connexion et fait échouer le run si elle a changé
  (`ErrHostKeyMismatch`) — et elle le fait aussi bien pour un nom que pour une
  adresse. Une IP mal recopiée mène tout autant à la mauvaise machine ; ce qui
  arrête les deux cas est le même contrôle, et il ne bouge pas.

## Décision

`ManagedHost.Address` est une chaîne. Elle vaut une adresse IP, ou un nom que
**le pod** sait résoudre.

- `core.ValiderAdresse` tente l'adresse d'abord, le nom ensuite. Une IP subit les
  refus qui la concernent ; un nom n'est jugé que sur sa forme (RFC 1123, 253
  caractères), parce que sa résolubilité est une question que seul le résolveur
  du pod peut trancher, au moment de la connexion.
- La migration `00012` passe `managed_host.address` en `text` avec une contrainte
  de forme délibérément grossière — elle écarte ce qui ne peut être ni l'un ni
  l'autre. Le jugement fin reste dans le cœur, qui peut nommer la machine fautive.
- Le chemin de connexion **ne change pas** : `wire.Enroll.Address` et
  `enroll.Cible.Address` étaient déjà des chaînes, et la connexion est un
  `net.JoinHostPort` suivi d'un `DialContext`. Un nom y fonctionnait déjà.

## Conséquences

- **Positives** :
  - L'inventaire Ansible du dépôt redevient la seule description de la flotte.
    C'est ce qui rend possible l'enrôlement de masse qui s'appuie dessus.
  - Une machine qui change d'IP suit son nom, sans qu'on ait à corriger une fiche.

- **Négatives / risques acceptés** :
  - **La résolution a lieu dans le pod, pas chez vous.** C'est exactement ce que
    l'ADR-0030 refusait. Un nom qui résout sur le poste de l'opérateur peut ne pas
    résoudre depuis le cluster, ou y résoudre vers autre chose. L'échec est alors
    bruyant — la connexion ne s'établit pas — mais le succès *vers la mauvaise
    machine* n'est rattrapé que par l'empreinte, et seulement si elle est épinglée.
  - **Le pod ne lit aucun `ssh_config`.** Le runner utilise
    `golang.org/x/crypto/ssh` sur un `net.Dialer` nu. Un alias qui n'existe que
    dans le `~/.ssh/config` de l'opérateur passe la validation et échoue à la
    connexion. C'est le mode de panne le plus probable de cette décision, et il
    est dit dans les messages d'erreur de la CLI plutôt que laissé à découvrir.
  - **La base ne valide plus la forme d'une adresse IP.** Le type `inet` le
    faisait gratuitement ; une contrainte `CHECK` par expression régulière ne
    peut pas en faire autant. La validation fine repose désormais sur le cœur,
    donc sur le fait que tout chemin d'écriture passe par `SaveHost`.
  - Le `DOWN` de la migration **supprime** les lignes dont l'adresse est un nom :
    les reconvertir en `inet` demanderait une résolution, que la base ne fait pas
    et que personne ne devrait faire à sa place.

- **Dette technique éventuelle** :
  - Rien n'avertit qu'un nom enrôlé a cessé de résoudre depuis le cluster tant
    qu'un run ne le rencontre pas. Une commande qui vérifie l'inventaire depuis
    un pod — l'équivalent d'un `ansible -m ping` — reste à écrire.
