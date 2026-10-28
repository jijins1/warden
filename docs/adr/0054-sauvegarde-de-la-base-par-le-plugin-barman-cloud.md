# ADR-0054 — Sauvegarde de la base par le plugin Barman Cloud de CNPG

**Date** : 2026-10-24
**Statut** : Accepted
**Complète** : [ADR-0008](0008-postgresql-comme-journal-et-transport-devents.md)

## Contexte

L'ADR-0008 fait de PostgreSQL le journal des runs et le transport des events.
La chart confie la base à l'opérateur CNPG, mais ne déclarait **aucune
sauvegarde** : la perte du volume emportait l'audit de tous les runs passés.

## Décision

La chart expose `postgres.backup` (désactivé par défaut). Activé, il :

- déclare le plugin `barman-cloud.cloudnative-pg.io` dans `spec.plugins` du
  `Cluster`, comme archiveur de WAL — restauration possible à n'importe quel
  instant ;
- crée un `ScheduledBackup` (méthode `plugin`, `immediate: true`).

La **destination** n'est pas dans la chart : elle référence par son nom
(`postgres.backup.objectStoreName`) un `ObjectStore` posé par l'infrastructure
de l'environnement, avec bucket, identifiants et rétention.

## Options rejetées

- **`spec.backup.barmanObjectStore` intégré à CNPG** : déprécié depuis CNPG
  1.26 au profit des plugins CNPG-I.
- **`ObjectStore` et identifiants dans la chart** : ils varient par
  environnement et porteraient un secret ou son chemin dans le produit.
- **`pg_dump` planifié** : pas d'archivage des WAL, donc pas de restauration à
  un instant précis, et un deuxième mécanisme à maintenir à côté de
  l'opérateur.
- **Sauvegarde fichier du volume (Velero)** : copie d'une base en cours
  d'écriture, sans garantie de cohérence.

## Conséquences

- Le plugin doit être installé à côté de l'opérateur CNPG ; sans lui, un
  `Cluster` qui le déclare ne démarre pas ses instances.
- Le cron du `ScheduledBackup` a six champs (secondes en tête).
