# Architecture Decision Records

Journal des décisions d'architecture du projet. Une décision structurante se
documente ici **au moment où elle est prise**, avec les options rejetées et
leur raison de rejet — c'est ce qui évite de re-débattre le même point six mois
plus tard.

Le document de conception produit (`specs/initial.md`) décrit *quoi* construire
et récapitule les décisions produit (§13). Les ADR documentent les décisions
d'implémentation prises en cours de route.

## Index

| ADR | Titre | Statut |
|---|---|---|
| [0001](0001-go-pour-cli-et-serveur.md) | Go pour la CLI et le serveur | Accepted |
| [0002](0002-go-git-plutot-que-binaire-git.md) | go-git plutôt que le binaire git système | Superseded par 0005 |
| [0003](0003-commit-audit-chaine-sur-la-base.md) | Commit d'audit chaîné sur le commit de base | Accepted |
| [0004](0004-maintenance-des-depots-par-le-binaire-git.md) | Maintenance des dépôts serveur par le binaire git | Accepted (amendé par 0005) |
| [0005](0005-binaire-git-comme-moteur-unique.md) | Le binaire git comme moteur unique | Accepted |
| [0006](0006-un-binaire-trois-roles.md) | Un binaire, trois rôles déployés séparément | Accepted |
| [0007](0007-acces-du-runner-au-code.md) | Accès du runner au code par fetch scopé à un SHA | Accepted |
| [0008](0008-postgresql-comme-journal-et-transport-devents.md) | PostgreSQL comme journal et comme transport des events | Accepted |
| [0009](0009-configuration-resolue-depuis-le-commit.md) | La configuration effective est résolue depuis le commit | Accepted |
| [0010](0010-strategie-de-test-en-trois-etages.md) | Stratégie de test en trois étages, sans cluster jetable | Accepted |
| [0011](0011-un-binaire-par-domaine-de-confiance.md) | Un binaire par domaine de confiance, frontières imposées par le compilateur | Accepted |
| [0012](0012-migrations-sql-avec-goose.md) | Migrations SQL embarquées avec goose, appliquées par un Job dédié | Accepted |
| [0013](0013-retention-differee-des-refs-daudit.md) | Aucune purge avant mesure : la rétention des refs d'audit est différée | Accepted |

## Convention

- Numérotation séquentielle : `NNNN-titre-court.md`
- Langue : français
- Statut : `Accepted` pour toute décision en vigueur
- Au moins deux options envisagées, avec la raison de rejet explicite
