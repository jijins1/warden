# ADR-0003 — Commit d'audit chaîné sur le commit de base

**Date** : 2026-08-04
**Statut** : Accepted

## Contexte

En mode DEV, la CLI construit un commit représentant le worktree de
l'utilisateur et le pousse vers `refs/audit/<user>/<run-id>`
(`specs/initial.md` §5.2). Il faut décider de la **parenté** de ce commit :
a-t-il pour parent le commit de base du dépôt local, ou est-il orphelin ?

Ce choix commande directement un cas que la spec signale sans le trancher
(§5.3) : *« le commit de base n'existe pas côté serveur (commits locaux non
poussés) → il faut pousser le pack manquant »*.

À noter, parce que c'est contre-intuitif : la déduplication n'entre pas dans
l'arbitrage. Elle se fait par objet lors de la négociation du push, pas par le
graphe de commits ; elle est identique dans les deux options.

## Solutions envisagées

### Option A — Commit orphelin, sans parent
Le commit ne porte que l'arbre ; le SHA de base est enregistré en base
applicative comme métadonnée du run.
- **Avantage** : le cas « commit de base absent côté serveur » disparaît ; coût
  de push prévisible et borné à l'arbre courant ; aucune fuite des branches
  locales non poussées ; chaque ref d'audit est un îlot, ce qui rend le GC
  trivial.
- **Rejetée parce que** : l'artefact n'est plus autoportant. Le lien vers le
  contexte d'origine ne vit plus que dans la base applicative, et une ref
  d'audit consultée directement avec un outil git ne montre plus rien du dépôt
  dont elle provient.

### Option B — Commit chaîné sur le commit de base ✅ (retenue)
Le commit d'audit a pour parent le commit de base du worktree local.
- **Choisie parce que** : conforme à la lettre de §5.2 ; l'artefact est
  autoportant et lisible avec n'importe quel outil git ; les deux diffs qui
  justifient le choix de git (§6.5 — run contre branche principale, run contre
  run précédent) s'appuient sur un graphe réel plutôt que sur une jointure en
  base.

## Décision

Le commit d'audit est chaîné sur le commit de base du dépôt local. Le SHA de
base est **également** enregistré en base applicative, comme métadonnée du run,
pour rester requêtable sans traverser le graphe.

**Il n'existe aucun chemin de repli orphelin.** Les cas où il n'y a pas de base
atteignable sont des erreurs explicites, refusées côté CLI avant tout appel au
serveur :

| Cas | Comportement |
|---|---|
| Le répertoire de travail n'est pas un dépôt git | Erreur. Le produit exige un dépôt git. |
| Le dépôt n'a aucun commit (HEAD orphelin) | Erreur. `.ansibleproxy.yml` est commité dans le dépôt (§5.4) : un dépôt sans commit ne peut pas porter de configuration valide. |
| Clone shallow dont la base est inconnue du serveur | Erreur, après tentative. Message indiquant `git fetch --unshallow`. |

Le clone shallow n'est refusé **que** dans ce dernier cas : lorsque le commit de
base est déjà connu du serveur — le cas courant, la base étant sur le miroir de
la branche protégée — le push ne transmet que le nouveau commit et son
ascendance est complète côté serveur. Tronquer l'historique local est alors sans
conséquence.

Chaque message d'erreur indique la commande qui débloque la situation. Un
message non actionnable renvoie l'ingénieur au bastion SSH (§5.7).

## Conséquences

- **Avantages** : artefact autoportant ; diffs d'audit adossés au graphe git ;
  conformité à la spec sans écart à documenter.

- **Contraintes** :
  - Quand le commit de base est inconnu du serveur, le push embarque
    l'historique manquant : **le coût du premier push d'une branche locale
    diverge n'est pas borné**. Il faut mesurer le volume à transmettre avant
    d'envoyer, et avertir l'utilisateur au-delà d'un seuil.
  - Les commits locaux non poussés de l'utilisateur atterrissent sur le serveur
    d'audit. C'est une conséquence acceptée, mais elle doit être **documentée**
    et elle élargit le périmètre du scan de secrets obligatoire (§5.3) : le scan
    doit couvrir tous les objets nouveaux, pas seulement l'arbre du run.
  - Les refs d'audit maintiennent des chaînes de commits accessibles : la
    politique de rétention et de GC (§6.2) doit raisonner sur des chaînes, pas
    sur des objets isolés.
  - **Le mode DEV impose un dépôt git contenant au moins un commit.** C'est une
    contrainte d'adoption à documenter : un répertoire Ansible non versionné
    n'est pas exécutable par le produit, il faut l'initialiser d'abord.
  - Le cas courant du clone shallow décrit ci-dessus repose sur le support
    shallow du moteur git. Il est natif depuis l'ADR-0005 ; il restait à valider
    tant que go-git était retenu.

- **Dette technique éventuelle** : si le coût de push de l'historique manquant
  s'avère rédhibitoire à l'usage, le repli est de basculer sur l'option A en
  conservant `base_sha` en base — l'information nécessaire y est déjà.
