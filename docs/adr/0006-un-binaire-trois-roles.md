# ADR-0006 — Un binaire, trois rôles déployés séparément

**Date** : 2026-08-04
**Statut** : Accepted

## Contexte

Le serveur de `specs/initial.md` §4 est décrit comme un bloc unique — « control
plane ». Ce bloc porte en réalité cinq responsabilités dont les profils
d'exécution sont incompatibles :

| Responsabilité | État | Cardinalité | Chemin chaud |
|---|---|---|---|
| API HTTP + WS : auth, policy, création de run, relais (§9, §10) | aucun | N réplicas | oui |
| Transport git et requêtes d'audit (§5.2, §6.5) | **filesystem POSIX** | 1 | oui |
| Contrôleur Kubernetes : informer, reconciler, pool, reaper (§8.7) | cache informer | 1 actif | non |
| Maintenance des dépôts (ADR-0004) | verrou par dépôt | 1 | non |
| Persistance des events (§6.3) | aucun | N réplicas | oui |

Deux contraintes dominent.

**L'état git est un filesystem POSIX, pas une base.** Git s'appuie sur
l'atomicité de `rename()` et sur `fsync` pour ses verrous et la réécriture de
`packed-refs`. Le placer sur un volume partagé entre plusieurs écrivains est un
mode de corruption connu — et il porterait sur la donnée que le produit est
précisément censé garantir. C'est la contrainte qui a conduit GitLab à extraire
Gitaly.

**Le découpage est aussi une frontière de sécurité.** L'ADR-0007 établit que le
runner — où s'exécute du code arbitraire (§2) — doit joindre le service git pour
matérialiser son commit, sans jamais joindre l'API. Une NetworkPolicy sélectionne
des pods : cette distinction n'est exprimable que si les deux rôles sont des pods
différents.

## Solutions envisagées

### Option B — Un binaire, un seul déploiement
Tout en process, leader election pour la partie contrôleur, PVC RWX partagé
entre les réplicas pour les bare repos.
- **Avantage** : exploitation minimale, un seul objet à déployer.
- **Rejetée parce que** : elle impose plusieurs écrivains git concurrents sur un
  filesystem partagé, et elle rend inexprimable l'isolation réseau entre le
  service git et l'API. Elle n'évite même pas la complexité qu'elle prétend
  éviter : le contrôleur reste un singleton, donc la leader election est requise
  de toute façon.

### Option C — Services séparés dès le départ
Un dépôt, une CI et un cycle de release par service.
- **Rejetée parce que** : types dupliqués, versionnage d'une API interne et coût
  d'exploitation sans rapport avec la taille du produit. Les frontières qu'on
  cherche sont des frontières de *déploiement*, pas de *codebase*.

### Option A — Un binaire, trois rôles déployés séparément ✅ (retenue)
Un seul dépôt, un seul binaire, une seule release ; trois Deployments qui
lancent le même binaire avec un rôle différent.
- **Choisie parce que** : elle donne exactement les frontières nécessaires — un
  rôle avec état, un rôle singleton, un rôle sans état — sans payer la
  duplication de l'option C, et en gardant la possibilité de tout exécuter dans
  un seul process en développement et en test d'intégration.

## Décision

| Rôle | Cardinalité | Contenu |
|---|---|---|
| `api` | N réplicas, sans état | HTTP + WS, OIDC/LDAP, policy, création de run, relais des events, ingestion des events (ADR-0008), UI d'audit |
| `git` | 1, PVC **RWO** | Smart HTTP `receive-pack`/`upload-pack`, quarantaine, scan de secrets, promotion, ancestry PROD, diffs d'audit, maintenance planifiée (ADR-0004) |
| `controller` | 1 actif, leader election via `Lease` | Watch informer, reconciler, pool de pods chauds, reaper |

**Règle de couplage, à tenir dans le code :** aucun rôle n'appelle un autre rôle,
**sauf `api` → `git`**. Tout le reste transite par PostgreSQL.

Conséquences directes de cette règle :

- **Le claim d'un pod chaud se fait en base**, par un `UPDATE … WHERE
  state = 'idle' … RETURNING` conditionnel — et non par une annotation Kubernetes
  en compare-and-swap. L'atomicité exigée par §8.4 est obtenue gratuitement, le
  contrôleur sort du chemin chaud d'un run, et l'état reste dans notre base
  conformément à la décision produit #12. Le contrôleur ne fait qu'entretenir
  l'inventaire du pool.
- **Le rôle `api` n'a jamais accès au filesystem git.** Ancestry, diffs,
  quarantaine et promotion passent par une API interne exposée par le rôle `git`,
  sur un chemin distinct de celui du smart HTTP.
- **La maintenance de l'ADR-0004 tourne dans le rôle `git`**, seul détenteur du
  volume et du verrou par dépôt.

**Une seule instance du rôle `git`.** Aucune répartition des dépôts sur
plusieurs volumes n'est prévue, ni même préparée : la réponse à la croissance
est d'agrandir le volume.

**En développement et en CI**, le même binaire lance les trois rôles dans un seul
process ; seul le mode de déploiement diffère.

## Conséquences

- **Avantages** :
  - un seul codebase, une seule release, des types partagés ;
  - le seul rôle avec état est isolé, sauvegardable et déployable seul ;
  - l'isolation réseau exigée par l'ADR-0007 devient exprimable ;
  - le contrôleur, point de fuite identifié au §8.7, est hors du chemin d'un run.

- **Contraintes** :
  - Le rôle `git` est un **point de défaillance unique**, et c'est assumé : son
    PVC est RWO, donc le redéploiement se fait en `Recreate` et, pendant les
    quelques secondes d'indisponibilité, **aucun run ne démarre dans toute
    l'organisation**. La CLI doit retenter, et le message d'attente doit être
    explicite (§5.7).
  - Le PVC du rôle `git` porte l'artefact d'audit : sauvegarde et restauration à
    définir, au même titre que celles de PostgreSQL.
  - La règle « aucun rôle n'appelle un autre rôle » doit être vérifiée en revue.
    C'est elle qui empêche la dérive vers un maillage d'appels internes.

- **Dette technique éventuelle** :
  - Répartir les dépôts sur plusieurs instances du rôle `git` a été envisagé et
    **écarté comme anticipation inutile** : ce serait une table de routage
    constante et du code mort tant que le besoin n'est pas mesuré. Le jour où le
    volume ou le rayon d'explosion l'imposent, il faudra à la fois cette table de
    routage — donc reprendre tous les appels qui supposent aujourd'hui un dépôt
    unique — et un mécanisme de migration de dépôt entre instances. La clé de
    répartition serait alors le **dépôt**, jamais le run ni l'utilisateur : la
    déduplication par blobs et les diffs du §6.5 exigent que tous les runs d'un
    dépôt vivent dans le même bare repo.
  - La leader election du contrôleur repose sur les `Lease` de
    `coordination.k8s.io` : un contrôleur qui perd son bail sans s'en rendre
    compte peut agir en double. Les actions du contrôleur doivent rester
    idempotentes plutôt que de dépendre de l'unicité du leader.
