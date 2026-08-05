# ADR-0007 — Accès du runner au code par fetch scopé à un SHA

**Date** : 2026-08-04
**Statut** : Accepted

## Contexte

`specs/initial.md` §7 exige que « le runner ne doit pas pouvoir joindre le server
autrement que par le bus d'events ». Mais le pod doit bien matérialiser l'arbre
du commit à exécuter, et §8.6 prévoit même un cache d'objets git côté runner. Les
deux énoncés ne sont pas compatibles tels quels : la spec ne dit pas par quel
chemin le code entre dans le pod.

Le problème n'est pas seulement de plomberie. Le principe fondateur du §2 est
qu'un playbook arbitraire équivaut à une exécution de code dans le runner. Tout
accès accordé au pod est donc un accès accordé à un attaquant potentiel. Si le
runner peut parler au service git avec les droits du produit, un playbook peut
lire **les refs d'audit de tous les utilisateurs** — c'est-à-dire l'intégralité
du code que l'organisation a exécuté.

La question à trancher est donc : quelle est la plus petite capacité qui suffise
à matérialiser un commit, et est-elle vérifiable ailleurs que dans la bonne
volonté du client ?

## Solutions envisagées

### Option B — Le serveur pousse le pack via le canal d'events
Aucun accès git depuis le runner : le control plane envoie les objets sur le seul
canal déjà autorisé, le pod les déballe.
- **Avantage** : respecte §7 à la lettre.
- **Rejetée parce que** : elle fait transiter des mégaoctets dans un canal
  dimensionné pour des events, elle impose un dépaquetage maison dans le pod à la
  place d'un `git fetch` éprouvé, et surtout elle casse le pré-chargement zygote
  du §8.5 — un pod inactif ne peut plus tirer les objets du dépôt à l'avance,
  puisqu'il n'a aucun moyen de les demander.

### Option C — Init container, puis bascule de NetworkPolicy
Un init container récupère le code, puis le contrôleur modifie les labels du pod
pour lui appliquer une politique réseau plus fermée avant l'exécution du
playbook.
- **Rejetée parce que** : une NetworkPolicy sélectionne un **pod**, pas un
  conteneur. La bascule est une course entre le contrôleur et le démarrage du
  conteneur principal, non atomique, non observable et impossible à auditer. Un
  contrôle de sécurité qui dépend d'un timing n'est pas un contrôle.

### Option A — Fetch depuis le service git, capacité réduite à un SHA ✅ (retenue)
Le runner fait un `git fetch` vers le rôle `git` (ADR-0006), avec un jeton dont
la portée est un seul run.
- **Choisie parce que** : elle réutilise le protocole git de bout en bout, elle
  préserve le pré-chargement zygote, et la capacité accordée est réductible à
  « un objet, celui qu'on s'apprête de toute façon à donner au pod ». Ce que le
  runner peut obtenir en abusant de son accès est exactement ce qu'il allait
  recevoir.

## Décision

Le pod runner récupère son code par `git fetch` vers le rôle `git`, jamais vers
le rôle `api`.

**La réduction de portée est structurelle, par espace de noms git.** Au moment du
claim, le rôle `git` crée pour le run une ref unique dans un espace de noms
dédié :

```
refs/namespaces/runs/<run-id>/refs/heads/run  →  <sha>
```

L'`upload-pack` servi au runner est lancé avec `GIT_NAMESPACE=runs/<run-id>`.
Le runner ne voit alors qu'**une seule ref**, celle de son propre run.

Ce point est le cœur de la décision : la restriction ne dépend ni d'un filtrage
de `hideRefs`, ni d'une inspection des lignes `want` du protocole, ni de la
discipline du client. Elle est appliquée par git lui-même, à l'extérieur de la
négociation. Un runner qui demande autre chose ne reçoit rien.

Le reste en découle :

- **Jeton de run**, audience `git`, valable pour la durée du run, révoqué à sa
  fin. Il n'ouvre que l'espace de noms du run.
- **`--depth=1` côté runner** : optimisation de bande passante, jamais un contrôle
  de sécurité. La chaîne de commits de l'ADR-0003 n'est pas utile au runner.
- **Egress du pod runner**, par NetworkPolicy : le rôle `git`, l'endpoint
  d'ingestion des events (ADR-0008), le scope d'hôtes de l'utilisateur (§10.2).
  Rien d'autre — en particulier jamais le port de l'API.
- **La ref d'espace de noms maintient les objets du run accessibles** pendant
  toute son exécution. Elle constitue une seconde protection contre le `prune` de
  l'ADR-0004, en plus du délai de grâce — qui restait jusqu'ici le seul rempart,
  et le paramètre le plus dangereux du produit.
- **Le reconciler supprime les espaces de noms des runs terminés**, y compris ceux
  dont il n'a pas vu la fin (§8.7). Sans cela on recrée exactement le problème des
  refs orphelines du §6.2.

## Conséquences

- **Avantages** :
  - le code arrive par un `git fetch` standard, sans mécanisme maison ;
  - la capacité accordée au pod est minimale et **vérifiable côté serveur**, pas
    négociée avec le client ;
  - le pré-chargement zygote (§8.5) reste possible : un pod inactif peut tirer les
    objets du dépôt avant d'être réclamé, donc avant de recevoir le moindre
    credential (§8.3) ;
  - les objets du run sont protégés du `prune` par une ref réelle.

- **Contraintes** :
  - Le rôle `git` est joignable depuis les runners, donc depuis du code
    arbitraire. Sa surface exposée doit rester minimale, et son autorisation
    couverte par des tests dédiés — dont un test négatif : un run tentant de
    lire la ref d'un autre run doit échouer.
  - §7 est respecté dans son intention mais pas dans sa lettre. L'écart doit être
    reporté dans la spec : le runner joint le rôle `git`, jamais le control plane.
  - Le cycle de vie des espaces de noms devient une responsabilité du reconciler,
    au même titre que celui des pods.

- **Dette technique éventuelle** :
  - Le contenu `git-lfs` ne vit pas dans les objets git. Un dépôt utilisant LFS
    matérialise des pointeurs dans le workspace du runner, qui n'a aucun accès au
    serveur LFS. À détecter et à refuser explicitement, dans la continuité de la
    dette signalée par l'ADR-0005 côté CLI.
  - Le jeton de run est présent dans le pod pendant l'exécution, donc lisible par
    le playbook. C'est acceptable puisqu'il n'ouvre que le run courant, mais cela
    suppose que sa portée ne soit **jamais** élargie par commodité.
