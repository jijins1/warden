# ADR-0047 — Runners lancés par l'API Docker, secrets dans Vault

**Date** : 2026-10-19
**Statut** : Proposed
**Touche** : [ADR-0019](0019-decoupage-hexagonal-du-control-plane.md),
[ADR-0020](0020-key-inventory-credentials-dacces.md),
[ADR-0023](0023-identite-damorcage-du-pod-runner.md),
[ADR-0045](0045-vault-inventory-cle-ansible-vault.md)

## Contexte

Le control plane ne sait lancer ses runners que sur Kubernetes. La demande :
pouvoir les lancer **directement par l'API Docker**, sans cluster.

Le découpage hexagonal (ADR-0019) rend la chose possible sans toucher au
cœur : tout ce qui parle à Kubernetes est derrière des ports. Mais l'inventaire
de ces ports montre que Kubernetes ne sert pas qu'à lancer des pods — il porte
aussi des **garanties** et un **magasin de secrets** :

| Port | Implémentation actuelle | Ce que Kubernetes apporte |
|---|---|---|
| `PodLauncher` (`core/start.go`) — `LaunchPod`, `DeletePod` | `outbound/orchestrator` | Pod durci, `activeDeadlineSeconds`, jeton monté depuis un Secret |
| `PodObserver` (`core/reconcile.go`) — `ObserverPods` | `outbound/orchestrator` | `PodFacts` : `Evicted`, `OOMKilled`, code de sortie |
| Consigne (`core/hosts.go`) — `Stash`, `Take` | `outbound/orchestrator/consigne.go` | Secret `warden-transit-*`, détruit à la lecture |
| `keyinventory` | Secrets Kubernetes | Magasin des clés d'accès |
| `vaultinventory` | Secrets Kubernetes | Magasin des clés Ansible Vault |

Docker remplace sans peine la première ligne et presque la deuxième. Il n'a
**rien** pour les trois dernières hors mode Swarm. Un HashiCorp Vault sera
disponible dans les déploiements visés.

## Solutions envisagées

### Option A — Docker pour tout, secrets en fichiers montés ❌ rejetée

Les clés lues depuis des fichiers montés dans le conteneur du serveur, la
consigne en mémoire.

- **Rejetée parce que** : la consigne en mémoire ne vaut qu'avec **une seule
  réplique** — le mot de passe déposé par une réplique est invisible pour celle
  qui livre l'ordre — et un redémarrage perd les valeurs en attente. Les clés en
  fichiers imposent un redéploiement à chaque enrôlement, ce qui casse le
  modèle de l'ADR-0030. Acceptable en développement, pas au-delà.

### Option B — Consigne dans PostgreSQL ❌ rejetée

- **Rejetée parce que** : l'ADR-0018 pose qu'aucun credential n'y est
  persisté ; une insertion laisse une trace dans le WAL, donc dans les
  sauvegardes et les réplicas.

### Option C — Consigne par *response wrapping* Vault ❌ rejetée

Vault sait emballer une valeur derrière un jeton à usage unique avec TTL.

- **Rejetée parce que** : le jeton d'emballage doit être retrouvé par la clé
  de consigne (`run_ref`), donc rangé quelque part — on retombe sur le problème
  de départ.

### Option D — Consigne dans le *cubbyhole* Vault ❌ rejetée

- **Rejetée parce que** : le cubbyhole est propre au jeton Vault qui écrit.
  Deux répliques authentifiées séparément ne voient pas le même.

### Option E — Docker pour les runners, Vault pour tous les secrets ✅ (proposée)

Un adaptateur `outbound/docker` implémente `PodLauncher` et `PodObserver`. Un
adaptateur `outbound/vault` implémente la consigne, `keyinventory` et
`vaultinventory`. Les deux sont indépendants : l'adaptateur Vault est
utilisable aussi en mode Kubernetes.

## Ce qui est proposé

### 1. Lancement des runners par l'API Docker

- Client `github.com/moby/moby/client`, la même famille que `moby/moby/api`
  déjà en dépendance (e2e).
- **Nom du conteneur = nom du pod** : `ContainerCreate` sur un nom existant
  renvoie un conflit, traité comme succès — `LaunchPod` reste idempotent comme
  le core l'exige.
- **Confinement par labels** (ADR-0010) : chaque conteneur porte les labels de
  l'orchestrateur, et **toute** requête de liste filtre sur eux.
- **Durcissement transposé tel quel** : `ReadOnlyRootfs`, `CapDrop: ALL`,
  `SecurityOpt: no-new-privileges`, utilisateur `65534:65534`,
  `Privileged: false`, `tmpfs` pour l'espace de travail et `/tmp`, seccomp par
  défaut de Docker.
- **Authentification au registre** : en-tête `X-Registry-Auth` au pull,
  depuis la configuration de déploiement — jamais depuis le projet (même règle
  que `PullSecrets`).
- **Réseau** : un réseau Docker dédié, sur lequel `DispatchURL` et `IngestURL`
  sont joignables.
- `AutoRemove: false` : le reconciler doit pouvoir lire l'état d'un conteneur
  mort avant que `DeletePod` le supprime.

### 2. Le jeton d'amorçage passe par stdin

L'ADR-0023 a rejeté la variable d'environnement parce que l'objet le plus
consulté finit dans un log : `docker inspect` affiche `Env`, la même raison
s'applique. Un `docker cp` avant démarrage ne peut pas viser un `tmpfs`, monté
seulement au démarrage. Un bind mount suppose un système de fichiers partagé
entre le serveur et l'hôte Docker.

Le conteneur est donc créé avec `OpenStdin` et `StdinOnce`. Le serveur s'y
attache, écrit le jeton, ferme. Le jeton n'apparaît dans **aucun** objet Docker.

**Changement du runner** : `wire.EnvTokenFile` accepte la valeur `-`, qui veut
dire « lire le jeton sur stdin jusqu'à EOF ».

### 3. Observation : `PodFacts` depuis `ContainerInspect`

| `PodFacts` | Source Docker |
|---|---|
| `Termine` | `State.Status` ∈ {`exited`, `dead`} |
| `ContainerReason` | `OOMKilled` si `State.OOMKilled`, sinon `Error`/`Completed` selon le code |
| `ExitCode` | `State.ExitCode` |
| `PodReason` | `DeadlineExceeded` si le conteneur a été arrêté par le reaper (voir 4) ; vide sinon |

`Evicted` et `NodeLost` n'existent pas : un hôte Docker perdu emporte le
serveur avec lui ou le prive de toute observation.

### 4. Borne de durée de vie

Docker n'a pas d'équivalent à `activeDeadlineSeconds`. Le reconciler arrête
les conteneurs plus vieux que `DeadlineSeconds` (date de création lue dans le
label) et marque la cause. **Si le contrôleur est mort, plus rien ne borne un
run** : c'est une régression assumée par rapport à la décision actée n°13 — voir
« Questions ouvertes ».

### 5. Vault pour tous les secrets

- **Consigne** sur un montage **KV v1** `warden-transit/<clé>` : `PUT` pour
  `Stash`, `GET` puis `DELETE` pour `Take`, 404 → `core.ErrNoStash`. Même
  sémantique que `consigne.go`, y compris « l'échec de la suppression n'annule
  pas la lecture ». KV v1 et non KV v2 : KV v2 garde les versions, un `DELETE`
  y laisse la valeur récupérable, ce qui casse « détruit à la lecture ».
- **Péremption** : KV v1 n'expire rien. La valeur est écrite avec son
  horodatage ; `Take` refuse une valeur plus vieille qu'un TTL configuré et la
  supprime. Un ménage périodique purge les valeurs jamais reprises.
- **`keyinventory` et `vaultinventory`** sur KV (chemins distincts), mêmes
  contrats que leurs adaptateurs Kubernetes.
- **Policy dédiée** : `create`, `update`, `read`, `delete` sur les chemins du
  produit, **sans `list`** sur la consigne — un serveur compromis ne peut pas
  énumérer les valeurs en attente.
- **Authentification du serveur** : AppRole, identifiants dans un fichier
  monté. C'est le seul secret à fournir au déploiement.

### 6. Accès au socket Docker

L'accès au socket équivaut à root sur l'hôte, bien au-delà des droits
« namespace seulement » posés par l'ADR-0023. Le serveur ne parle **jamais**
directement au socket : il passe par un proxy filtrant qui n'autorise que les
routes `containers`, `images/create` et `networks` nécessaires, ou par un
démon Docker rootless dédié aux runners.

### 7. Sélection

Configuration : `orchestrator: kubernetes | docker` et
`secrets: kubernetes | vault`, choisis au démarrage dans `server.go`. Les
combinaisons sont libres ; `docker` + `kubernetes` est refusé au démarrage.

## Plan d'implémentation

1. **Runner** : jeton sur stdin (`EnvTokenFile=-`), tests unitaires.
2. **`outbound/vault`** : consigne, puis `keyinventory`, puis `vaultinventory`.
   Tests contre un faux serveur HTTP ; un test d'intégration contre un Vault
   en conteneur (testcontainers, déjà en dépendance).
3. **`outbound/docker`** : `LaunchPod`, `DeletePod`, `ObserverPods`, derrière
   une interface étroite du client moby pour les tests sans démon (ADR-0010).
   Un test d'intégration contre le démon Docker du CI.
4. **Reaper de durée** dans le reconciler, pour le mode Docker.
5. **Câblage** dans `server.go`, configuration, validation au démarrage.
6. **Déploiement** : un `compose.yaml` d'exemple — serveur, PostgreSQL, Vault,
   proxy du socket, réseau des runners.
7. **e2e** : la chaîne complète en mode Docker.

## Questions ouvertes

- **Usage visé** : développement local et petits déploiements, ou production ?
  La réponse fixe le niveau d'exigence des deux points suivants.
- **Borne de durée sans contrôleur** : faut-il imposer un `timeout` dans le
  point d'entrée du runner ? L'image étant déclarée par le projet (ADR-0014),
  cela suppose un point d'entrée fourni par warden.
- **Egress du runner** (ADR-0007) : sans NetworkPolicy, le conteneur joint tout
  le réseau. Réseau Docker `internal` + proxy sortant, ou règles iptables sur
  l'hôte ?

## Conséquences

- Le cœur ne change pas ; deux adaptateurs s'ajoutent.
- Le runner gagne un mode de lecture du jeton.
- Le mode Docker offre des garanties plus faibles que le mode Kubernetes
  (borne de durée, egress, périmètre des droits du serveur). Elles doivent être
  documentées comme telles, pas découvertes.
- Vault devient une dépendance du mode Docker, et une option du mode Kubernetes.
