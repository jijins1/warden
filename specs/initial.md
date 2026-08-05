# Spécification — Proxy d'exécution Ansible

> Document d'architecture destiné à guider l'implémentation.
> Il décrit **quoi** construire et **pourquoi**, pas le code.
> Les rationales sont volontairement conservées : elles évitent de re-décider
> à l'envers un point déjà tranché.

---

## 1. Problème et positionnement

### 1.1 Le constat

Ansible est un excellent moteur d'exécution mais un mauvais point de contrôle :
n'importe qui disposant du binaire et d'une clé SSH peut atteindre n'importe
quelle machine, sans trace exploitable et sans autorisation granulaire.

Les outils existants (AWX, Semaphore, Rundeck) résolvent l'autorisation et
l'audit, mais **imposent que le code vienne d'un SCM synchronisé**. En
pratique : commit → push → sync du projet → lancement. La boucle de
développement passe de 5 secondes à plusieurs minutes, ce qui pousse les
ingénieurs à retourner sur un bastion en SSH.

### 1.2 Le positionnement

**« AWX sans le sync SCM »** : conserver la boucle de dev locale (édition,
lancement immédiat depuis le répertoire courant) tout en imposant
authentification, autorisation, audit et exécution centralisée.

Le produit se compose de trois éléments :

| Élément | Rôle |
|---|---|
| **CLI** (binaire unique) | Remplace `ansible-playbook`. Capture le contexte local, l'envoie, streame le résultat |
| **Server** (control plane) | AuthN/AuthZ, politique, ordonnancement, journal d'audit, relais de stream |
| **Runner** (pod Kubernetes) | Exécute réellement le playbook, émet des events structurés |

### 1.3 Ce qui n'est pas dans le périmètre

- Réimplémenter le moteur Ansible → on utilise **ansible-runner**
- Réimplémenter la gestion de secrets → **Vault** (ou équivalent)
- Réimplémenter l'émission de certificats SSH → **Vault SSH CA** / Teleport
- Une interface de création de playbooks

---

## 2. Principe de sécurité fondateur

**À lire avant toute décision d'implémentation.**

Un proxy Ansible **ne réduit pas** le pouvoir d'Ansible. Si l'utilisateur peut
envoyer un playbook arbitraire, il dispose d'une exécution de code arbitraire
dans le runner, via :

- `delegate_to: localhost`, `local_action`
- lookup plugins, filtres Jinja custom, `library/`, `action_plugins/`
- `command:` / `shell:` sur le runner lui-même
- inventaire inline, `add_host`

Conséquences directes, non négociables :

1. **L'autorisation ne peut pas être appliquée au niveau Ansible.**
   Elle est appliquée au niveau **credential** (certificats SSH courts, scopés
   par principal, dérivés des droits de l'utilisateur) et **réseau**
   (NetworkPolicy egress). Un playbook qui triche doit être refusé *par la
   cible*, pas par le proxy.

2. `--limit` est un garde-fou contre l'erreur humaine, **jamais** un contrôle
   de sécurité. Ne jamais le présenter comme tel dans la documentation ni dans
   l'UI.

3. **L'audit documente l'intention, il n'empêche rien.** Quelqu'un qui exécute
   du code arbitraire avec un credential peut l'exfiltrer.

4. Le runner s'exécute donc en supposant qu'il est hostile.

---

## 3. Les deux modes de confiance

C'est la décision structurante du produit. Chaque environnement est déclaré
dans l'un des deux modes.

### 3.1 Mode DEV (non-prod)

- Le code exécuté provient du **répertoire de travail local** de l'utilisateur
- Code arbitraire autorisé
- Isolation forte du runner (cf. §7)
- Audit best-effort mais complet en pratique
- Objectif : boucle de dev sous 3 secondes de surcoût

### 3.2 Mode PROD

- **Seuls les commits présents sur la branche principale protégée du repo
  serveur peuvent être exécutés.** Aucun code local n'est accepté.
- La CLI envoie un SHA, pas un diff. Le serveur vérifie que ce SHA est
  ancêtre/égal du HEAD de la branche protégée.
- Le fichier de config projet (§5.4) vient également de cette branche.
- Optionnel selon l'environnement : `--check` obligatoire avant apply,
  approbation d'un second utilisateur, fenêtre de maintenance.

### 3.3 Règle de promotion

Un run en prod ne doit **jamais** être la première exécution propre d'un
playbook. Le passage dev → prod repart d'un pod neuf (ce qui est déjà le cas
par construction, cf. §8) et d'un checkout du commit de la branche protégée.

---

## 4. Vue d'ensemble des flux

```
┌──────────┐   1. auth OIDC/LDAP        ┌──────────────────┐
│   CLI    │ ─────────────────────────► │     SERVER       │
│          │   2. push git (refs/audit) │  (control plane) │
│          │ ─────────────────────────► │                  │
│          │   3. POST /runs {sha, env} │  - AuthN/AuthZ   │
│          │ ─────────────────────────► │  - Policy        │
│          │   4. WS /runs/{id}/stream  │  - Audit store   │
│          │ ◄────────────────────────► │  - Scheduler     │
└──────────┘      (events + offset)     └────────┬─────────┘
                                                  │ 5. create Job
                                                  ▼
┌─────────────────┐                     ┌──────────────────┐
│  Event bus      │ ◄────────────────── │   RUNNER POD     │
│ (NATS/Redis)    │   6. events JSON    │  ansible-runner  │
└─────────────────┘                     └────────┬─────────┘
        ▲                                        │ 7. SSH (cert court)
        │ le server lit et relaie                ▼
                                         ┌──────────────────┐
                                         │  Hôtes cibles    │
                                         └──────────────────┘
```

> Ce schéma est daté sur deux points.
>
> **Le bus n'existe pas.** L'[ADR-0008](../docs/adr/0008-postgresql-comme-journal-et-transport-devents.md)
> retient PostgreSQL comme journal *et* transport : le runner `POST` ses events
> vers un endpoint d'ingestion, l'api sert l'historique depuis la table puis suit
> le direct par `LISTEN`/`NOTIFY`.
>
> **Il manque la flèche descendante.** Le pod étant pré-chauffé, il existe avant
> le run : ni la podspec ni un volume projeté ne peuvent porter l'ordre. Le
> runner ouvre un canal SSE sortant par lequel descendent l'ordre de run, le
> jeton git et les secrets du claim
> ([ADR-0018](../docs/adr/0018-canal-de-dispatch-sse-vers-le-runner.md)).

---

## 5. La CLI

### 5.1 Objectif d'expérience

L'utilisateur tape une commande quasi identique à `ansible-playbook` et obtient
une sortie quasi identique. Toute divergence doit être justifiée par une valeur
ajoutée visible.

```
$ <cli> run site.yml -e staging --limit web-01 --check
```

### 5.2 Séquence d'un run (mode DEV)

1. Résoudre la config projet (§5.4) et l'authentification
2. Déterminer le commit de base : le remote configuré côté serveur
3. **Construire un commit d'audit sans toucher au worktree de l'utilisateur**
   - `GIT_INDEX_FILE` pointant sur un index temporaire
   - `git add` du périmètre calculé (§5.3)
   - `git commit-tree` → SHA
   - push vers `refs/audit/<user>/<run-id>` sur le remote serveur
   - **L'index et le HEAD de l'utilisateur ne sont jamais modifiés.** C'est une
     exigence absolue : les gens ne veulent pas voir leur travail commité.
4. `POST /runs` avec le SHA, l'environnement, les flags, le fichier de config
5. Ouvrir le WebSocket et streamer
6. Afficher le résumé et le lien vers l'audit

### 5.3 Périmètre de l'upload

Point à trancher explicitement dès le début, il détermine la valeur de l'audit.

- Le `.gitignore` du projet exclut souvent **l'inventaire réel et les fichiers
  vault** — précisément ce qu'il faut pour rejouer le run.
- Choix retenu : un fichier **`.auditignore` distinct**, qui définit le
  périmètre de l'upload indépendamment du `.gitignore`.
- Par défaut : fichiers suivis + fichiers non suivis **non ignorés**, plus une
  liste d'inclusions explicites.
- **Scan de secrets obligatoire côté serveur** avant persistance. Le repo
  d'audit deviendra sinon un puits à secrets.

Cas à gérer : le commit de base n'existe pas côté serveur (commits locaux non
poussés) → il faut pousser le pack manquant.

**Limite connue à documenter :** les collections/rôles Galaxy ne sont pas dans
le diff. ~~Sans miroir interne avec versions figées, le run n'est pas rejouable
à l'identique. Prévoir un miroir et un `requirements.yml` pinné.~~ → Levée par
l'[ADR-0014](../docs/adr/0014-image-du-runner-declaree-par-le-projet.md) : les
collections viennent de l'image déclarée par le projet, qui épingle du même coup
`ansible-core`, l'interpréteur Python et le système. La rejouabilité dépend donc
de la précision de la référence d'image, choix laissé au projet.

### 5.4 Fichier de configuration projet

Nom proposé : **`.ansibleproxy.yml`** (YAML, pas format `KEY=value` — il y aura
besoin de structure).

Il est **commité dans le repo**, donc visible dans l'audit. Il ne contient
**jamais de valeurs sensibles**, seulement des **noms** de variables à
transmettre :

```yaml
server: https://ansible-proxy.example.com
default_env: staging
inventory: inventories/staging
# Image du pod runner (ADR-0014). Omise, l'image par défaut du produit est
# utilisée. Un projet ayant besoin de collections particulières publie la sienne.
runner_image: registry.example.com/mon-equipe/runner:2.3.1
forward_env:
  - VAULT_ADDR
  - MY_APP_VERSION
audit_include:
  - inventories/**
```

Les valeurs viennent de l'environnement du shell au moment de l'appel.

#### 5.4.1 Allowlist stricte des variables d'environnement

**Denylist interdite. Allowlist uniquement, appliquée côté serveur.**

Certaines variables sont de l'exécution de code déguisée :

| Variable | Effet |
|---|---|
| `ANSIBLE_CALLBACK_PLUGINS`, `ANSIBLE_ACTION_PLUGINS`, `ANSIBLE_LIBRARY`, `ANSIBLE_FILTER_PLUGINS` | Charge du Python arbitraire depuis le dossier uploadé |
| `ANSIBLE_CONFIG` | Redirige vers un `ansible.cfg` qui fait la même chose |
| `PYTHONPATH`, `LD_PRELOAD` | Idem, en amont |
| `ANSIBLE_STDOUT_CALLBACK`, désactivation de callbacks | **Coupe le plugin d'audit en silence** — la variable qui tue le produit |
| `ANSIBLE_HOST_KEY_CHECKING=False` | MITM |

#### 5.4.2 Le fichier client est une requête, pas une déclaration

Leçon d'Atlantis : le `atlantis.yaml` du repo pouvait à l'origine surcharger le
comportement serveur, donnant une escalade de privilèges à quiconque pouvait
ouvrir une PR. Correction : `allowed_overrides` explicites côté serveur.

À appliquer d'emblée :
- Le serveur détient la politique par environnement
- Il filtre la requête du client
- **Le résultat effectif est journalisé dans l'audit**, pas seulement ce que le
  client a demandé

### 5.5 Flags Ansible

**Ne pas faire de passthrough.** La compatibilité exhaustive des flags
`ansible-playbook` est un gouffre et une surface d'attaque. Allowlist
explicite, validée côté serveur, par environnement.

Minimum utile : `--limit`, `--tags`, `--skip-tags`, `--check`, `--diff`,
`-e/--extra-vars`, `-v`.

### 5.6 Gestion du Ctrl-C

Fréquent en usage réel, doit être irréprochable.

**Sémantique.** Ctrl-C conserve son sens natif : il **annule le run** (SIGINT
vers le processus dans le pod). Le détachement du stream prend une séquence
distincte (façon tmux). L'inverse trahirait le réflexe de l'utilisateur.

**Escalade** — Ansible ne meurt pas toujours au premier signal (workers forkés,
module `pause` qui intercepte SIGINT) :

| Action | Effet | Affichage |
|---|---|---|
| 1er Ctrl-C | SIGINT | « Interruption demandée, attente de la fin de la tâche courante… » |
| 2e Ctrl-C | SIGTERM | « Arrêt forcé en cours… » |
| 3e Ctrl-C | Suppression du pod | « Run tué. » |

Afficher explicitement l'état à chaque étape, sinon l'utilisateur martèle sans
savoir si son signal est parti.

**Audit.** L'abandon est un statut de première classe (§6.4), jamais un stream
tronqué.

### 5.7 Comportement en cas d'échec — critère d'adoption

Si l'ingénieur ne comprend pas ce qui s'est passé, il retourne au bastion SSH.
Chaque cas doit produire un message actionnable :

- perte réseau → reconnexion automatique et transparente, avec replay (§6.3)
- pod OOM-killed → message explicite + cause + lien vers l'audit
- host bloqué → cf. §5.8
- run tué par un admin → indiquer qui et pourquoi

### 5.8 Affichage des hôtes en vol — différenciateur

Comportement natif d'Ansible à connaître :

- **Host injoignable** (TCP refusé, DNS KO, timeout de connexion) : Ansible
  attend `timeout` secondes (**10 par défaut**), marque l'host `UNREACHABLE`,
  **le retire du play** et continue sur les autres. Borné, ce n'est pas un
  problème.
- **Host qui répond mais bloque** (swap, NFS mort, D-state, lock `apt`) :
  **aucun timeout par défaut**. Ansible attend indéfiniment. Pire, la stratégie
  `linear` (défaut) attend que *tous* les hosts finissent la tâche avant de
  passer à la suivante — un seul host lent gèle tout le play, et Ansible
  n'affiche le résultat par host qu'à la fin de la tâche.

Puisqu'on capture les events structurés, on sait **quels hosts sont encore en
vol**. Afficher :

```
TASK [install packages]  47/50 terminés
  en attente : db-03 (4m12s), db-07 (4m08s)
```

Ansible en CLI ne donne pas cette information. Effort faible, valeur très
visible sur les gros inventaires.

**Corollaire serveur :** imposer un `task_timeout` par défaut, configurable par
environnement. Un run qui pend indéfiniment bloque un slot et un pod.

---

## 6. Audit

### 6.1 Séparer l'artefact du journal

Deux objets distincts qu'il ne faut pas fusionner :

| | Contenu | Stockage |
|---|---|---|
| **Artefact** | Le code exécuté | Git, identifié par un SHA |
| **Journal** | Qui, quand, quel SHA, quel inventaire, quels flags, quel exit code, tous les events | Store **append-only**, immuable |

**Capturer les events structurés d'ansible-runner** (un event par tâche/hôte
avec son résultat), **pas le stdout brut**. Un stdout est illisible à auditer et
impossible à requêter. Le stdout est reconstitué à l'affichage depuis les
events.

### 6.2 Git comme transport ET comme audit

Unifier les deux est le pari central : le push git dédoublonne par blobs, donc
seul le delta transite → boucle rapide *et* stockage minimal.

**Assumer que le choix de git est un choix d'UX d'audit, pas d'optimisation de
stockage.** Un simple CAS ferait le même travail de transport. Git ne se
justifie que si l'UI exploite réellement les diffs (§6.5). Si ce n'est pas le
cas, le CAS est plus simple.

Pièges à traiter :
- refs orphelines par milliers → politique de GC et de rétention dès le début
- un repo git n'est **pas tamper-evident** face à un admin
- non-répudiation : signer le commit avec l'identité OIDC de l'utilisateur
  (gitsign/sigstore) plutôt que « le serveur affirme que c'était Bob »

### 6.3 Persistance au fil de l'eau — exigence critique

Les events sont persistés **au fur et à mesure**, jamais bufferisés jusqu'à la
fin. C'est ce qui permet de savoir ce qui a été modifié quand un run est
interrompu brutalement.

### 6.4 Statuts de run et cause de terminaison

**« Interrompu » sans la cause est presque inutile** — ce sont des incidents
très différents :

| Cause | Signal |
|---|---|
| `user_interrupt` | L'utilisateur a vu quelque chose de travers — signal fort |
| `client_disconnect_timeout` | Accidentel, aucun signal |
| `server_task_timeout` | Config ou host bloqué |
| `pod_oom_killed` | Ressources |
| `pod_evicted` / `node_lost` | Infrastructure |
| `admin_killed` | Qui, et pourquoi |
| `unknown` | Le système ne sait pas |

**Machine à états :**

1. Statut initial `interrupted_unknown` écrit en base **avant** la création du
   pod
2. Le runner rapporte lui-même s'il le peut → cas normal, le plus riche
3. Sinon, un **watch informer** sur les pods récupère
   `containerStatuses[].lastState.terminated` (exit code, `reason`,
   `finishedAt`) et les events Node
4. Le **reconciler** clôture ce qui reste

**Garder `unknown` comme valeur possible.** Ce n'est pas une lacune de
conception, c'est un aveu honnête. Le supprimer par principe conduit à inventer
une cause plausible — bien pire en post-mortem. Objectif : le rendre rare, pas
l'éliminer.

**Piège majeur :** l'exit code du pod dit que le processus est mort, **pas ce
que le playbook a fait**. Un pod OOM-killed à la tâche 12 a peut-être déjà
modifié 30 machines. La question d'audit qui compte est « qu'est-ce qui a été
changé avant », et elle ne peut venir que du flux d'events (§6.3). La cause de
terminaison est un complément, pas la source de vérité.

### 6.5 Interface d'audit — deux publics

**L'auditeur / RSSI** veut requêter, pas lire des logs :
- liste filtrable : qui, quand, environnement, inventaire, statut, exit code
- **nombre de `changed`** — métrique clé. 0 changed = no-op ; 40 changed sur la
  prod un mardi à 2h du matin mérite un coup d'œil
- suppose d'indexer les events, pas le stdout

**L'ingénieur en post-mortem** veut comprendre :
- **diff entre le commit exécuté et la branche principale** → « qu'avait-il
  modifié en local ? »
- **diff entre ce run et le précédent sur le même inventaire** → « qu'est-ce
  qui a changé depuis que ça marchait ? »

C'est le geste que git offre gratuitement et que ni AWX ni Rundeck ne savent
faire. **C'est la justification du choix git.**

**Deux vues souvent oubliées :**

- **Vue par hôte**, pas seulement par run. « Tout ce qui a touché `db-prod-02`
  ce mois-ci » est la question la plus fréquente en incident. Index à prévoir
  dès le schéma initial.
- **Diff de configuration réelle** (si `--diff` actif) : Ansible sort le
  before/after des fichiers modifiés. Donnée la plus précieuse de l'audit et la
  plus dangereuse — elle contient les secrets rendus. **Redaction obligatoire,
  ou stockage chiffré séparé avec contrôle d'accès distinct.**

### 6.6 Signalement des runs abandonnés

Un run abandonné laisse l'infrastructure dans un état partiel. Signaler à
l'utilisateur suivant sur le même inventaire :

```
⚠ Le dernier run sur staging a été interrompu à la tâche 12
  par alice il y a 20 minutes (user_interrupt).
```

Évite de partir déboguer un état partiel sans le savoir. Ni AWX ni un bastion
ne donnent ça ; découle gratuitement du modèle.

---

## 7. Isolation du runner

**Un pod n'est pas une frontière de sécurité par défaut.** On exécute du code
arbitraire (§2).

| Contrôle | Détail |
|---|---|
| Rootless | `runAsNonRoot`, UID non privilégié |
| Filesystem | `readOnlyRootFilesystem`, `emptyDir` pour le workspace |
| Capabilities | Toutes droppées, seccomp `RuntimeDefault` |
| Volumes | Aucun `hostPath` |
| **ServiceAccount** | **`automountServiceAccountToken: false`** — sinon le playbook parle à l'API Kubernetes. Erreur classique |
| **Réseau** | **NetworkPolicy deny-all par défaut**, egress ouvert uniquement vers le scope de l'utilisateur. C'est le vrai contrôle d'autorisation, celui qui rattrape les `delegate_to` |
| Aucun accès au control-plane | Le runner ne joint jamais l'API. Son egress est limité au rôle `git`, à l'endpoint d'ingestion des events et au scope d'hôtes de l'utilisateur. Voir [ADR-0007](../docs/adr/0007-acces-du-runner-au-code.md) : le code arrive par un `git fetch` réduit à une seule ref via `GIT_NAMESPACE` |
| Cache Ansible | **Jamais partagé entre utilisateurs** (`~/.ansible`) → empoisonnement de collections |
| Nœuds | Node pool dédié + taints |
| Sandbox renforcée | **gVisor ou Kata** — coût de démarrage modeste au regard du reste |
| Secrets | Projected volume, token court. Idéalement Vault Agent injector avec un lease qui expire à la fin du run. **Pas de Secret Kubernetes long-vécu** |

---

## 8. Exécution sur Kubernetes

### 8.1 Le problème du cold start

Un Job par run est propre (isolation, cleanup, quotas natifs) mais le chemin
scheduling → pull d'image → démarrage Python → chargement des collections fait
facilement **10 à 30 secondes**. Sur un `ansible-playbook` qui prend 4 secondes
en local, **la boucle de dev est morte — et c'était tout l'argument face à
AWX**.

### 8.2 Modèle retenu : pod pré-chauffé, jamais réutilisé

**Décision structurante.**

- Le **premier** run d'une session paie le cold start
- À la fin d'un run, **le pod se tue**
- **Un nouveau pod est immédiatement allumé** et maintenu chaud pour la
  commande suivante
- TTL d'idle, puis extinction

**Propriété clé : un pod n'est jamais réutilisé pour deux runs.** On ne garde
pas de l'état chaud, on garde du **temps de démarrage déjà payé**.

Conséquences, qui simplifient massivement l'architecture :

- Aucun cache empoisonné, aucun fichier orphelin dans le worktree, aucune
  divergence « run n°14 »
- Le worktree est identique à froid et à chaud → **la restriction « pas de pod
  chaud en prod » devient inutile**. Le modèle s'applique partout, plus simple à
  expliquer et à auditer.
- Ce qui s'est exécuté correspond **exactement** au commit journalisé

### 8.3 Paramètres du pré-chauffage

- **Clé de pool** : (utilisateur, repo, environnement)
- **TTL adaptatif** plutôt que fixe. Une boucle de dev Ansible = un run toutes
  les 2-5 minutes ; après ~15 min sans activité, la session est finie.
  Prolonger tant que le rythme se maintient.
- **Aucun credential avant le claim.** Un lease Vault ouvert 45 minutes sur un
  pod inactif est exactement ce qu'on cherche à éviter. Injection au moment où
  le pod est réclamé.
- **Métrique de hit rate.** Sous 50 %, on brûle des ressources pour rien →
  raccourcir le TTL.

### 8.4 Deux pièges d'implémentation

**Orphelins.** Le pod chaud devient orphelin dès que le server redémarre. Il
faut un reaper, sinon on découvre 300 pods idle un lundi matin.

**Course.** Deux commandes lancées en parallèle par le même utilisateur : la
seconde doit tomber en cold start proprement, ni attendre ni recevoir le même
pod. → **claim atomique** ~~(annotation avec compare-and-swap, ou file côté
server)~~.

> Simplifié par l'[ADR-0018](../docs/adr/0018-canal-de-dispatch-sse-vers-le-runner.md) :
> les pods chauds étant connectés au serveur par un canal SSE sortant, réclamer
> revient à choisir une connexion oisive dans une structure que le serveur
> détient déjà. Plus de compare-and-swap sur un objet Kubernetes.

### 8.5 Optimisation zygote (phase ultérieure)

Le pod chaud étant dédié à (utilisateur, repo), il peut pré-charger pendant son
idle :

- interpréteur Python importé
- collections du `requirements.yml` installées
- objets git du repo déjà tirés

Le run se réduit alors à un checkout du commit et un `exec`. C'est là qu'on
descend sous la seconde de surcoût — le vrai différenciateur face à AWX.

### 8.6 Prérequis dans tous les cas

- **Image pré-baked par environnement**, collections incluses.
  `ansible-galaxy install` au démarrage est à proscrire.
- `imagePullPolicy: IfNotPresent` + pré-pull sur les nœuds
- Cache CAS partagé **en lecture seule** pour les objets git

### 8.7 Cycle de vie des pods — pas de finalizer

**Décision : pas de finalizer.**

Un finalizer garantit une fenêtre de lecture avant disparition de l'objet, mais :

- Les cas qui comptent (OOM, node down, éviction) **ne suppriment pas l'objet
  immédiatement** — le pod reste en `Failed` avec son statut lisible, souvent
  longtemps. Le finalizer n'apporte rien là.
- Il protège surtout contre notre propre TTL et le GC — qu'on évite plus
  simplement en ne posant pas de TTL et en supprimant nous-mêmes.
- **Risque réel** : un finalizer dont le contrôleur ne tourne plus bloque le pod
  en `Terminating` pour toujours → namespaces impossibles à supprimer, quotas
  saturés, `kubectl patch` manuel. Plaie classique de l'écosystème.
- Node disparu : le kubelet ne rapporte plus, le statut reste figé. Le
  finalizer n'aide pas non plus.

**Modèle retenu :**

1. **Watch informer** sur les pods portant le label du produit
2. Persistance du statut dès qu'il devient terminal
3. **Suppression explicite** par le contrôleur
4. Pas de `ttlSecondsAfterFinished`, pas de finalizer

**Le contrôleur devient un point de fuite** (s'il tombe 2 h, personne ne
nettoie). Deux filets :

- Le **reconciler au démarrage liste tous les pods portant le label**, pas
  seulement ceux connus en base. C'est ce qui rattrape les orphelins d'un crash.
- **`activeDeadlineSeconds` sur le Job** : ne supprime pas l'objet, arrête juste
  l'exécution. Borne le coût d'un run qui pend même si le contrôleur est mort,
  sans réintroduire le problème du TTL.

*(Un finalizer redeviendrait pertinent s'il fallait nettoyer des ressources
au-delà du pod : révoquer un lease Vault, libérer un verrou d'inventaire. Pour
de la lecture de statut, il coûte plus qu'il n'apporte.)*

### 8.8 Ne pas faire de CRD

La tentation « chaque run est un objet Kubernetes » est forte. **etcd n'est pas
une base d'audit** : rétention, limites de taille, requêtes. Kubernetes est
l'**ordonnanceur de calcul** ; l'état vit dans notre propre base. AWX a fait ce
choix, c'est le bon.

---

## 9. Streaming

Le morceau le plus risqué de l'architecture. **À prototyper en premier.**

### 9.1 Exigences

- Le run **survit à la déconnexion du client**. Un wifi qui saute ne doit pas
  interrompre un playbook.
- **Detach / reattach** explicite, façon tmux
- **Replay depuis un offset** à la reconnexion
- Survit à un rolling update du server (multi-réplica)

### 9.2 Architecture

**Ne jamais faire de connexion directe pod ↔ client** : le pod meurt, le stream
meurt.

```
runner ──POST events──► ingestion ──INSERT──► PostgreSQL ──read(seq)──► server ──WS──► CLI
```

Ce pattern résout d'un coup le detach/reattach, le rolling update et le
multi-réplica. Chaque event porte un offset monotone ; la reconnexion rejoue
depuis le dernier offset acquitté.

**Écart assumé — pas de bus.** Cette section prescrivait à l'origine un bus
persistant (NATS JetStream ou Redis Streams). L'[ADR-0008](../docs/adr/0008-postgresql-comme-journal-et-transport-devents.md)
retient PostgreSQL comme journal **et** comme transport : un bus rend un event
durable sans le rendre journalisé, ce qui rouvre exactement la fenêtre que le
§6.3 cherche à fermer. Le motif de la section — jamais de connexion directe
pod ↔ client — reste respecté. L'offset est assigné par le runner, le replay se
sert depuis la table et le direct par `LISTEN` / `NOTIFY`.

### 9.3 Entrée interactive

`vars_prompt`, `pause`, prompts `become` nécessitent un vrai PTY (resize,
Ctrl-C). **Recommandation : les interdire en mode serveur.** Position
parfaitement défendable, et énorme simplification. Message d'erreur explicite
côté CLI si un playbook en contient.

### 9.4 Verrou de concurrence

~~Verrou par (inventaire, environnement), ou plus finement par hôte. Deux
utilisateurs sur les mêmes machines simultanément est un bug qu'AWX gère mal —
**bon différenciateur**.~~

> Écarté par l'[ADR-0016](../docs/adr/0016-pas-de-verrou-de-concurrence.md) :
> **aucun verrou, à aucune granularité**. Un verrou par inventaire transforme le
> surcoût du produit en attente non bornée (§3.1) et le bastion du §5.7 le rend
> contournable ; un verrou par hôte donnerait une garantie fausse, puisque le §2
> pose que `--limit` n'est jamais un contrôle de sécurité. En contrepartie, le
> §6.6 est étendu aux runs **en cours** : l'information est donnée avant de
> partir, sans blocage ni confirmation.

---

## 10. Authentification et autorisation

### 10.1 AuthN

- OIDC en priorité (device flow pour la CLI), LDAP en fallback
- Token court côté CLI, refresh transparent

### 10.2 AuthZ

Modèle : `(utilisateur|groupe) → environnement → scope d'hôtes → mode`

Application réelle du contrôle, par ordre d'efficacité :

1. **Certificats SSH éphémères** (Vault SSH CA / Teleport) dont les
   *principals* sont dérivés des droits de l'utilisateur. La cible refuse le
   cert si l'utilisateur n'a pas le droit. **C'est le contrôle qui compte.**
2. **NetworkPolicy egress** scopée au périmètre de l'utilisateur
3. Filtrage d'inventaire côté serveur — confort, pas sécurité

**Ne pas réimplémenter** l'émission de certificats. Intégrer Vault ou Teleport.

### 10.3 Invalidation

Si les droits changent (retrait d'un groupe LDAP), les pods chauds de
l'utilisateur doivent être détruits. Avec le modèle §8.2 le risque est faible
(aucun credential avant le claim), mais la règle reste : changement de rôle →
purge du pool.

---

## 11. Comparaison marché

| Outil | Ce qu'il fait | Écart avec le besoin |
|---|---|---|
| **AWX / AAP** | Exactement cette architecture : RBAC, credentials jamais exposés, exec engine, audit, Container Groups, Receptor | Le code doit venir d'un SCM synchronisé → boucle lente. **C'est le gap qu'on comble** |
| **Semaphore UI** | AWX-like léger | Même modèle « projet git », même lenteur |
| **Rundeck** | Jobs + ACL + audit | Runbooks figés, pas d'itération |
| **Atlantis** | Même pattern côté Terraform | Source de la leçon `allowed_overrides` (§5.4.2) |
| **Spacelift / env0** | Run isolé + policy-as-code (OPA) + worker pools | Modèle à copier |
| **GitHub Actions ARC** | Runner éphémère + warm pool | **Meilleur exemple à copier pour §8** |
| **Argo Workflows / Tekton** | Orchestration sur Kube | Modèle batch, pas interactif. Streaming reconnectable et latence poseront problème. **À éviter** sauf besoin de DAG |
| **Teleport / Boundary / Vault SSH** | Auth, certs courts, session recording | **À intégrer, pas à réécrire** |
| **ansible-runner** | Moteur + flux d'events JSON | **À réutiliser tel quel** |

---

## 12. Plan d'implémentation

Ordre choisi pour tuer les risques d'abord.

### Phase 0 — Mesure du cold start

**Objectif :** savoir si le modèle tient avant d'écrire le produit.

- Un run à froid de bout en bout, sans auth ni audit
- Instrumenter : scheduling, pull, démarrage Python, chargement collections,
  transfert git, exécution

**Critère de sortie :** connaître le surcoût réel. Si le pré-chauffage seul ne
descend pas sous ~3 s, le zygote (§8.5) devient obligatoire et non optionnel.

> **Fait.** Mesures et conclusion dans
> l'[ADR-0015](../docs/adr/0015-prechauffage-obligatoire-zygote-optionnel.md) :
> le pré-chauffage seul descend à ~1,1 s, donc le zygote reste optionnel. Le
> pré-chauffage, lui, devient obligatoire. Réserve : le poste « chargement
> collections » n'a pas pu être mesuré.

### Phase 1 — Streaming reconnectable ⚠️ risque principal

**Objectif :** valider le morceau qui invaliderait tout le reste.

- Bus d'events persistant avec offsets
- WS server ↔ CLI avec replay
- Detach / reattach
- Survie à un kill du server et à un rolling update
- Ctrl-C avec escalade à trois niveaux (§5.6)

**Critère de sortie :** un run de 10 minutes survit à trois coupures réseau
client et à un redémarrage du server, sans perte d'event.

### Phase 2 — Transport git + audit minimal

- Commit d'audit sans toucher au worktree (`GIT_INDEX_FILE` + `commit-tree`)
- Push vers `refs/audit/...`
- Persistance des events au fil de l'eau
- Machine à états des statuts + watch informer + reconciler (§6.4, §8.7)
- `.auditignore` et scan de secrets

**Critère de sortie :** tout run, y compris tué brutalement (OOM simulé,
suppression de pod, node drain), produit un enregistrement d'audit avec une
cause correcte et la liste des hôtes déjà modifiés.

### Phase 3 — Pré-chauffage

- Pool par (utilisateur, repo, environnement)
- Claim atomique
- TTL adaptatif, reaper, métrique de hit rate

**Critère de sortie :** surcoût du 2e run et suivants conforme à l'objectif ;
zéro pod orphelin après un crash du server.

### Phase 4 — Auth, AuthZ, isolation

- OIDC/LDAP
- Intégration Vault SSH CA
- NetworkPolicy, durcissement du pod (§7)
- Allowlist des flags et des variables d'environnement

### Phase 5 — Mode PROD

- Vérification que le SHA appartient à la branche protégée
- Politique par environnement, `allowed_overrides`
- Approbation, `--check` préalable

### Phase 6 — UI d'audit

- Liste filtrable, métrique `changed`
- Vue par hôte
- Diffs (commit vs branche, run vs run précédent)
- Diff de configuration avec redaction

---

## 13. Décisions actées — récapitulatif

| # | Décision | Raison |
|---|---|---|
| 1 | L'autorisation passe par les certificats SSH et le réseau, pas par Ansible | Un playbook arbitraire = RCE dans le runner |
| 2 | Deux modes : DEV (code local) / PROD (branche protégée uniquement) | Sinon illusion de contrôle |
| 3 | Git sert de transport **et** d'audit | Dedup par blobs + diffs exploitables en post-mortem |
| 4 | Le worktree de l'utilisateur n'est jamais modifié | Exigence non négociable d'adoption |
| 5 | Le fichier de config déclare des **noms** de variables, jamais des valeurs | L'audit ne doit pas devenir un puits à secrets |
| 6 | Allowlist des variables d'env, jamais denylist | `ANSIBLE_*` = exécution de code |
| 7 | Le fichier client est une requête ; le serveur détient la politique | Leçon Atlantis |
| 8 | Events structurés persistés au fil de l'eau, pas de stdout brut | Requêtable + survit aux morts brutales |
| 9 | Pod pré-chauffé mais **jamais réutilisé** | Latence sans état mutable — supprime une classe entière de bugs |
| 10 | Le pré-chauffage s'applique aussi en prod | Pod chaud ≡ pod froid par construction |
| 11 | Pas de finalizer | Risque de `Terminating` éternel > bénéfice |
| 12 | Pas de CRD, état dans notre base | etcd n'est pas une base d'audit |
| 13 | `activeDeadlineSeconds` sur le Job | Borne le coût même si le contrôleur est mort |
| 14 | `unknown` reste un statut valide | Mieux qu'une cause inventée |
| 15 | Prompts interactifs interdits en mode serveur | Simplification majeure, position défendable |
| 16 | Réutiliser ansible-runner, Vault/Teleport | Le produit, c'est le transport git + la policy + l'UX CLI |

---

## 14. Questions ouvertes

- ~~Choix du bus d'events~~ → tranché : pas de bus, PostgreSQL
  ([ADR-0008](../docs/adr/0008-postgresql-comme-journal-et-transport-devents.md))
- ~~Stratégie de miroir Galaxy et de pinning des collections~~ → tranché : pas
  de miroir, les collections viennent de l'image déclarée par le projet
  ([ADR-0014](../docs/adr/0014-image-du-runner-declaree-par-le-projet.md))
- Allowlist des registres d'images — différé, à traiter avec la politique
  serveur (ADR-0014)
- ~~Politique de rétention et GC des refs d'audit~~ → tranché : aucune purge
  avant mesure, à rouvrir en fin de phase 1
  ([ADR-0013](../docs/adr/0013-retention-differee-des-refs-daudit.md)). **Écart
  assumé à la lettre du §6.2**, qui demandait la politique dès le début.
- ~~Rétention du journal d'events et des payloads `--diff`~~ → tranché dans le
  même sens que les refs : aucune purge avant mesure ([ADR-0013](../docs/adr/0013-retention-differee-des-refs-daudit.md),
  section « Extension au journal »)
- ~~Redaction des diffs `--diff` : automatique, ou chiffrement + accès séparé ?~~
  → tranché : substitution exacte des valeurs injectées et de leurs dérivations
  calculables, faite dans le runner
  ([ADR-0017](../docs/adr/0017-redaction-des-diffs-par-substitution-exacte.md)).
  Deux exceptions en fail-open y sont assumées et signalées.
- ~~Granularité du verrou de concurrence : inventaire ou hôte ?~~ → tranché :
  aucun verrou, mais un signalement des runs en vol
  ([ADR-0016](../docs/adr/0016-pas-de-verrou-de-concurrence.md))
- Format exact du protocole WS (framing, ack, resize si PTY réintroduit)
