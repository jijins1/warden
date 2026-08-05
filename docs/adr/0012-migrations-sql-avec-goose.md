# ADR-0012 — Migrations SQL embarquées avec goose, appliquées par un Job dédié

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

L'ADR-0008 fait de PostgreSQL le journal d'audit **et** le transport des events.
Son schéma est donc à la fois la donnée que le produit existe pour garantir et
un chemin chaud d'exécution. Trois contraintes en découlent, qui n'ont rien de
générique :

- La base a **deux identités de connexion** : le rôle applicatif complet, et un
  rôle d'ingestion restreint à `INSERT` sur la table d'events. Le second ne peut
  pas modifier de schéma, donc ne peut pas appliquer de migration.
- Le rôle `api` tourne à **N réplicas** (ADR-0006). Appliquer les migrations au
  démarrage d'un pod ferait démarrer N migrateurs concurrents.
- La table d'events est **partitionnée par date**. Une table partitionnée sans
  partition couvrant la date du jour **rejette les insertions** — ce qui, par la
  règle de l'ADR-0008 (« un run dont l'audit ne s'écrit pas doit s'arrêter »),
  arrêterait tous les runs de l'organisation.

## Solutions envisagées

### Option A — golang-migrate
Le plus répandu de l'écosystème, CLI et bibliothèque, `embed.FS` via `iofs`.
- **Rejetée parce que** : une migration interrompue laisse la base dans un état
  `dirty` qu'il faut débloquer manuellement. Sur une base d'audit, dont la
  restauration est déjà une procédure sensible, ajouter un état bloquant qui
  exige une intervention humaine est un mauvais compromis.

### Option B — atlas
Approche déclarative : on décrit l'état voulu du schéma, l'outil calcule le diff
et génère la migration versionnée.
- **Avantage** : les dérives de schéma sont détectables automatiquement.
- **Rejetée parce que** : c'est un outil externe avec son propre langage, et il
  s'accommode mal de ce qui n'est pas exprimable déclarativement — précisément
  le partitionnement et les `GRANT` par rôle, qui sont ici deux points centraux.

### Option C — tern
De l'auteur de `pgx`, PostgreSQL uniquement, minimal.
- **Rejetée parce que** : pas de migrations écrites en Go, et une adoption plus
  faible. Le premier point est éliminatoire : certaines opérations de schéma
  devront être conditionnelles ou itératives, ce que du SQL statique ne couvre
  pas.

### Option D — goose ✅ (retenue)
Migrations SQL ou Go, versionnées, embarquables en `embed.FS`.
- **Choisie parce que** : elle couvre les deux formes de migration, s'embarque
  dans le binaire — donc la version du schéma voyage avec la version du code —
  et n'introduit ni outil externe ni état bloquant à débloquer à la main.

## Décision

**goose**, migrations embarquées en `embed.FS` dans
`internal/server/internal/store/migrations/`.

**Application par une sous-commande dédiée, `wardend migrate`**, lancée par un
Job Helm en `pre-upgrade`. Jamais au démarrage d'un pod, jamais depuis le rôle
`ingest`. Le Job utilise le rôle PostgreSQL complet ; le rôle d'ingestion ne
reçoit que `INSERT` sur la table d'events, et ce `GRANT` fait partie des
migrations.

**Le partitionnement n'est pas une migration.** Créer la partition de la période
suivante est une tâche récurrente, pas un changement de schéma. Elle rejoint la
maintenance planifiée du rôle `git` (ADR-0004), qui est déjà le seul singleton
porteur de tâches périodiques. Deux exigences non négociables :

- les partitions sont créées **en avance**, jamais à la demande sur le chemin
  d'insertion ;
- l'absence de partition utilisable pour la date courante est une **alerte de
  premier niveau**, au même rang qu'une indisponibilité de la base — c'est un
  arrêt de tous les runs.

## Conséquences

- **Avantages** :
  - la version du schéma est celle du binaire, il n'y a rien à synchroniser
    séparément ;
  - un seul migrateur s'exécute, quel que soit le nombre de réplicas ;
  - les `GRANT` du rôle d'ingestion sont versionnés avec le schéma qu'ils
    restreignent, donc revus au même endroit.

- **Contraintes** :
  - Un Job `pre-upgrade` **bloque la mise à jour** s'il échoue. C'est le
    comportement voulu, mais il faut que son échec soit lisible : un déploiement
    qui ne démarre pas sans explication est le pire mode de panne.
  - Les migrations doivent rester compatibles avec la version précédente du code
    le temps d'un rolling update du rôle `api`. Toute migration destructive se
    fait donc en deux versions.
  - La création anticipée des partitions devient un service critique alors
    qu'elle a l'apparence d'une tâche d'entretien. Elle doit être supervisée
    comme telle.

- **Dette technique éventuelle** :
  - `pg_partman` couvrirait la gestion des partitions sans code de notre part.
    Écarté pour l'instant — c'est une extension à installer sur le cluster
    CloudNativePG, et la logique à écrire est courte. À reconsidérer si la
    politique de rétention devient non triviale, ce qui dépend de la question
    §14 encore ouverte sur la rétention des refs d'audit.
  - Aucune stratégie de restauration n'est définie à ce jour, ni pour la base ni
    pour le PVC du rôle `git` (déjà signalé par l'ADR-0006). Les deux devront
    être restaurés à un état cohérent l'un avec l'autre.
