# ADR-0019 — Découpage hexagonal du control plane

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Le produit délègue l'essentiel de son travail à des outils : Kubernetes lance
les runners, PostgreSQL journalise et transporte, le binaire git matérialise les
commits, Vault émet les certificats. Le §10.2 dit d'ailleurs explicitement
« ne pas réimplémenter, intégrer ».

Le découpage actuel de `internal/server/internal/` — `api`, `gitd`,
`controller`, `ingest`, `store`, `policy`, `gitclient` — est un découpage **par
technologie**, où tous les paquets sont frères. L'ADR-0011 a posé une frontière
de confiance entre les trois binaires, imposée par le compilateur. Rien, en
revanche, ne sépare le métier de l'infrastructure **à l'intérieur** de
`wardend`.

Deux faits rendent cette séparation nécessaire plutôt que souhaitable.

**L'ADR-0010 a décidé une stratégie de test sans cluster jetable.** Elle n'est
réalisable que si `client-go` se trouve derrière une couture qu'on peut
simuler. Ce n'est pas une préférence de style : c'est le prérequis d'une
décision déjà prise.

**On a déjà changé d'avis une fois.** L'ADR-0002 avait retenu go-git ;
l'ADR-0005 l'a remplacée par le binaire git. Le coût de ce changement a été nul
parce qu'aucun code n'existait. Il ne le sera plus.

Les paquets ne contiennent aujourd'hui que des `doc.go`. Le moment est le seul
où ce découpage est gratuit.

## Solutions envisagées

### Option A — Conserver le découpage par technologie
Les paquets restent frères, la discipline de dépendance est tenue en revue.
- **Avantage** : rien à faire.
- **Rejetée parce que** : rien n'empêche le code métier de dépendre de
  `client-go` ou de `database/sql`, et l'expérience du dépôt dit que ce qui
  n'est pas refusé par le CI finit par arriver. Surtout, le test de niveau 2 de
  l'ADR-0010 devient impossible sans cluster, ce qui invalide une décision
  antérieure.

### Option B — Hexagonal à la Java : un paquet `ports` que les adaptateurs importent
Les interfaces vivent dans un paquet dédié ; chaque adaptateur l'importe pour
déclarer qu'il l'implémente.
- **Rejetée parce que** : c'est une transposition littérale d'une contrainte du
  langage source. Java exige `implements`, donc l'adaptateur doit importer le
  port. **Go n'a pas d'`implements`** : l'adaptateur satisfait l'interface
  structurellement. Créer un paquet `ports` importé par les adaptateurs
  réintroduirait une arête que le langage ne réclame pas, pour aucun bénéfice.

### Option C — Cœur et adaptateurs, interfaces déclarées par le consommateur ✅ (retenue)
Les interfaces sont déclarées **dans le cœur**, qui les consomme. Les
adaptateurs les satisfont sans les importer. Seul `cmd/wardend` connaît les
deux.
- **Choisie parce que** : le graphe de dépendances obtenu est plus strict que
  celui de l'hexagonal Java — `cœur → rien`, `adaptateur → cœur` pour les types
  du domaine, `main → tout` — et il est vérifiable mécaniquement.

## Décision

**Le control plane se découpe en un cœur et des adaptateurs.** Le découpage vaut
pour `wardend` uniquement.

### Les rôles

| Rôle | Responsabilité | Technologie |
|---|---|---|
| `core` | Le métier : cycle de vie d'un run, politique, claim, pool | aucune |
| `orchestrator` | Lance, surveille et supprime les runners | `client-go` — absorbe l'actuel `controller` |
| `store` | Écrit et relit les données d'audit | PostgreSQL |
| `flux` | Suit un run en temps réel | `LISTEN`/`NOTIFY`, dégradable en poll |
| `dispatch` | Canal vers le runner (ADR-0018) | SSE |
| `gitclient` | Ref de namespace, résolution de la config depuis le commit | binaire git (ADR-0005) |

Côté entrant, `api` (HTTP + WS vers la CLI), `ingest` (`POST` du runner) et
`gitd` (protocole git) **appellent** le cœur. Ils ne sont pas ses dépendances.

Il en résulte une asymétrie voulue : **le runner est joint par `dispatch` et
joint le cœur par `ingest`**. Deux adaptateurs, deux directions — c'est
précisément ce qui empêche une saturation du flux de logs d'emporter la capacité
d'annuler un run (ADR-0018).

### `store` et `flux` sont deux rôles, pas un

Ils reposent aujourd'hui sur la même base. Ce n'est pas une raison de les
confondre : l'ADR-0008 inscrit elle-même en dette que si le volume d'events
dépasse ce que PostgreSQL absorbe, un repli consiste à « déporter la table
d'events vers un store dédié ». Séparés, ce repli ne touche qu'un adaptateur ;
fusionnés, il traverse le cœur.

Le cœur ne demande donc pas « une base » mais `Follow(runID, fromSeq)`. Cette
forme est essentielle : la correction vient de `seq`, pas de `NOTIFY`. Une
implémentation par poll satisfait honnêtement le port, et `LISTEN`/`NOTIFY`
redevient ce qu'il est — une optimisation de latence, confinée à l'adaptateur.

### Ce qui ne passe pas derrière un port

**La politique est dans le cœur.** Le §5.4.2 — la leçon d'Atlantis — pose que le
serveur détient la politique, filtre la requête du client, et journalise le
résultat effectif. C'est la règle métier centrale. La placer derrière une
interface annoncerait qu'on pourrait en changer, alors qu'elle est précisément ce
qui ne doit jamais être délégué ni surchargé.

**Le runner et la CLI restent plats.** Le runner est une coquille autour
d'`ansible-runner` et d'un canal ; l'envelopper de ports doublerait son code pour
zéro optionalité, et l'ADR-0011 lui a déjà donné une séparation plus forte —
physique, par le compilateur.

### Ce qui n'est pas encore nommé

Trois sorties manquent à l'énumération et sont attendues : **Vault** (§8.3,
émission des credentials au claim), le **scan de secrets** (§5.3, obligatoire
avant persistance) et l'**AuthN** (§10.1, « OIDC en priorité, LDAP en
fallback » — deux implémentations écrites dans la spec). Seul `gitclient` n'était
pas différable : l'ADR-0007 le place sur le chemin du claim.

### Renommage

L'actuel `controller` disparaît en tant que rôle propre : informer, reaper et
reconciler (§8.7) sont des morceaux de l'adaptateur `orchestrator`. Le nom est
libéré plutôt que réaffecté au cœur, parce que « controller » désigne un
reconciler dans tout l'écosystème Kubernetes et que le produit en contient un.

### Application

Les frontières sont refusées par le CI, pas rappelées en revue — même principe
que l'ADR-0011. La règle depguard du cœur porte sur les paquets
d'infrastructure eux-mêmes :

```yaml
coeur:
  files: ["**/internal/server/internal/core/**"]
  deny:
    - pkg: database/sql
    - pkg: os/exec
    - pkg: net/http
    - pkg: k8s.io
```

Les règles actuelles entre frères — `api` autorisé vers `store`, `policy`,
`gitclient` — deviennent fausses et sont remplacées : les adaptateurs entrants
n'atteignent que le cœur, et l'assemblage n'a lieu que dans `cmd/wardend`.

## Conséquences

- **Avantages** :
  - le test de niveau 2 de l'ADR-0010 devient réalisable : l'orchestrateur se
    simule, donc le cycle de vie d'un run se teste sans cluster ;
  - la configuration depguard **rétrécit** — une règle sur les imports
    d'infrastructure du cœur remplace un graphe d'arêtes entre frères ;
  - le repli de l'ADR-0008 sur un store d'events dédié devient un changement
    d'adaptateur ;
  - les responsabilités sont énonçables en une ligne chacune, ce qui est la
    raison d'être du découpage autant que la testabilité.

- **Contraintes** :
  - **Un cœur pur impose des types du domaine distincts des types de transport.**
    Les structures de `wire` et les lignes SQL ne traversent pas le cœur ; il y a
    donc des conversions à écrire, et c'est le coût réel de cette décision.
  - La tentation d'un port par dépendance existe. Une interface avec une seule
    implémentation, un seul appelant et aucun besoin de test est un coût sans
    contrepartie : les six rôles ci-dessus sont la liste, pas un modèle à étendre
    par réflexe.
  - `dispatch` est à la fois un adaptateur sortant du cœur et un endpoint HTTP
    entrant, puisque c'est le runner qui ouvre la connexion. Cette double nature
    est réelle et doit être assumée dans le code plutôt que masquée.

- **Dette technique éventuelle** :
  - Le nouveau `.golangci.yml` sera **inerte tant que les paquets n'existent
    pas** : un `files:` qui ne correspond à rien ne refuse rien. Il faudra
    revérifier son mordant avec de vrais imports, comme cela a été fait pour les
    règles de l'ADR-0011.
  - Vault, le scan de secrets et l'AuthN ne sont pas modélisés. Ils le seront au
    moment où le claim sera implémenté, et il faudra résister à les traiter comme
    des détails de `core` faute de temps.
