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
| `PodsObserves` (`core/reconcile.go`) — `ObserverPods`, `DeletePod` | `outbound/orchestrator` | `PodFacts` : `Evicted`, `OOMKilled`, code de sortie |
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

Un adaptateur `outbound/docker` implémente `PodLauncher` et `PodsObserves`.
`outbound/vault` porte le client Vault et la consigne ; `keyinventory` et
`vaultinventory` gagnent chacun un adaptateur Vault qui reçoit ce client par
une interface déclarée chez eux, deux adaptateurs sortants ne s'important pas
(ADR-0019). Docker et Vault sont indépendants : Vault est utilisable aussi en
mode Kubernetes.

## Ce qui est proposé

### 1. Lancement des runners par l'API Docker

- Client `github.com/moby/moby/client`, la même famille que `moby/moby/api`
  déjà en dépendance (e2e).
- **Nom du conteneur = nom du pod** : `ContainerCreate` sur un nom existant
  renvoie un conflit — `LaunchPod` reste idempotent comme le core l'exige. Le
  conflit n'est un succès que si le conteneur a démarré : un lanceur mort entre
  la création et l'écriture du jeton laisse un conteneur `created` sans jeton,
  que `LaunchPod` supprime puis recrée.
- **Confinement par labels** (ADR-0010) : chaque conteneur porte les labels de
  l'orchestrateur, plus un label d'instance (`warden.io/instance`) qui tient
  le rôle du namespace. **Toute** requête de liste filtre sur eux, et aucun
  conteneur sans eux n'est inspecté comme nôtre ni supprimé.
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

Docker n'a pas d'équivalent à `activeDeadlineSeconds`. L'observation des
conteneurs, appelée à chaque passage du reconciler, tue ceux qui sont plus
vieux que `DeadlineSeconds` (date de création du conteneur) et les rapporte en
`DeadlineExceeded`. La cause se relit aux passages suivants depuis l'état du
conteneur — fin postérieure à la borne — sans mémoire partagée entre
répliques. Le cœur ne change pas. **Si le contrôleur est mort, plus rien ne borne un
run** : c'est une régression assumée par rapport à la décision actée n°13 — voir
« Questions ouvertes ».

### 5. Vault pour tous les secrets

- **Consigne** sur un montage **KV v2** `warden-transit`, réglé à
  `max_versions = 1` : `POST data/<clé>` pour `Stash`, `GET data/<clé>` puis
  `DELETE metadata/<clé>` pour `Take`, 404 → `core.ErrNoStash`. Même
  sémantique que `consigne.go`, y compris « l'échec de la suppression n'annule
  pas la lecture ».
- **La suppression vise `metadata/`, jamais `data/`** : un `DELETE data/<clé>`
  n'est qu'un effacement logique, annulable par `undelete` — la valeur
  resterait récupérable, ce qui casse « détruit à la lecture ». `DELETE
  metadata/<clé>` détruit toutes les versions et la clé elle-même.
- **Péremption** : `delete_version_after` n'efface que logiquement, il ne
  suffit pas. La valeur est écrite avec son horodatage ; `Take` refuse une
  valeur plus vieille qu'un TTL configuré et la détruit.
- **Ménage sans `list`** : les clés de consigne se dérivent du `run_ref`
  (`CleBecome`, `CleForwardEnv`, le `run_ref` lui-même). Le ménage part des
  runs terminés en base et détruit leurs clés à l'aveugle ; un 404 est un
  succès. Il n'a jamais besoin d'énumérer la consigne.
- **`keyinventory` et `vaultinventory`** sur KV v2 (chemins distincts), mêmes
  contrats que leurs adaptateurs Kubernetes.
- **Policy dédiée** : `create`, `update`, `read` sur `data/*` et `delete` sur
  `metadata/*` pour les chemins du produit, **sans `list`** sur la consigne —
  un serveur compromis ne peut pas énumérer les valeurs en attente.
- **Authentification du serveur** : AppRole. Le `role_id` n'est pas un secret
  et passe en variable ; le `secret_id` est dans un fichier monté, seul secret
  à fournir au déploiement. Le serveur renouvelle
  son jeton Vault avant expiration et se réauthentifie quand il n'est plus
  renouvelable, en relisant le fichier : tourner le `secret_id` revient à
  remplacer le fichier, sans redémarrage.

### 6. Accès au socket Docker

L'accès au socket équivaut à root sur l'hôte, bien au-delà des droits
« namespace seulement » posés par l'ADR-0023. Le serveur ne parle **jamais**
directement au socket : il passe par un proxy filtrant qui n'autorise que les
routes `containers` (y compris `containers/{id}/attach`, une connexion
« upgradée » que le proxy retenu doit savoir relayer), `images/create` et
`networks` nécessaires, ou par un
démon Docker rootless dédié aux runners.

### 7. Sélection

Le mode Kubernetes reste le mode par défaut et ne change pas : le mode Docker
s'ajoute à côté, il ne le remplace pas.

Deux variables d'environnement, lues au démarrage dans `server.go` comme les
autres `WARDEN_*` :

- `WARDEN_ORCHESTRATOR` : `kubernetes` (défaut) ou `docker` ;
- `WARDEN_SECRETS` : `kubernetes` (défaut) ou `vault`.

Absentes, elles valent `kubernetes` : un déploiement existant, dont la chart
ne les pose pas, démarre exactement comme aujourd'hui.

Le mode Docker lit en plus `WARDEN_DOCKER_NETWORK` (obligatoire),
`WARDEN_DOCKER_INSTANCE` (`warden` par défaut) et
`WARDEN_DOCKER_REGISTRY_AUTH_FILE` ; l'adresse du démon vient de `DOCKER_HOST`,
qui pointe le proxy filtrant. Le mode Vault lit `WARDEN_VAULT_ADDR`,
`WARDEN_VAULT_ROLE_ID`, `WARDEN_VAULT_SECRET_ID_FILE`, et en option
`WARDEN_VAULT_CACERT`, `WARDEN_VAULT_APPROLE_MOUNT` (`approle`),
`WARDEN_VAULT_KV_MOUNT` (`warden`), `WARDEN_VAULT_TRANSIT_MOUNT`
(`warden-transit`) et `WARDEN_VAULT_TRANSIT_TTL_SECONDS` (600). Une valeur inconnue est
refusée au démarrage. Les combinaisons sont libres, sauf `docker` +
`kubernetes`, refusée au démarrage : sans cluster, il n'y a pas de Secrets
Kubernetes où ranger les clés.

## Plan d'implémentation

1. **Runner** : jeton sur stdin (`EnvTokenFile=-`), tests unitaires.
2. **`outbound/vault`** : client et consigne ; adaptateurs Vault de
   `keyinventory` et `vaultinventory`.
   Tests contre un faux serveur HTTP ; un test d'intégration contre un Vault
   en conteneur (testcontainers, déjà en dépendance).
3. **`outbound/docker`** : `LaunchPod`, `DeletePod`, `ObserverPods`, derrière
   une interface étroite du client moby pour les tests sans démon (ADR-0010).
   Un test d'intégration contre le démon Docker du CI.
4. **Borne de durée** dans l'observation des conteneurs, pour le mode Docker.
5. **Câblage** dans `server.go`, configuration, validation au démarrage.
6. **Déploiement** : un `compose.yaml` d'exemple — serveur, PostgreSQL, Vault,
   proxy du socket, réseau des runners.
7. **e2e** : la chaîne complète en mode Docker, à côté de la chaîne Kubernetes
   existante, qui reste la référence et continue de tourner telle quelle.

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

- Le cœur ne change pas ; deux adaptateurs s'ajoutent. Le mode Kubernetes reste
  le défaut, sans changement de configuration ni de chart.
- Le runner gagne un mode de lecture du jeton.
- Le mode Docker offre des garanties plus faibles que le mode Kubernetes
  (borne de durée, egress, périmètre des droits du serveur). Elles doivent être
  documentées comme telles, pas découvertes.
- Vault devient une dépendance du mode Docker, et une option du mode Kubernetes.
