# Démarrer avec warden

De zéro à un premier playbook exécuté, en choisissant d'abord **où** faire
tourner warden.

Ce document est le chemin court. Le détail de chaque commande, et les raisons
derrière chaque choix, sont dans [`prise-en-main.md`](prise-en-main.md) ; tout
ce qui touche à la chart est dans
[`charts/warden/README.md`](../charts/warden/README.md).

---

## Les pièces, en trente secondes

| Pièce | Où elle tourne | Ce qu'elle fait |
|---|---|---|
| `warden` (la CLI) | le poste de chaque utilisateur | se connecter, administrer, soumettre un run |
| `wardend` (le control plane) | votre infrastructure | rôles `api`, `git` et `controller`, plus la base PostgreSQL |
| `warden-runner` | un pod (ou un conteneur) **par run** | exécute le playbook avec `ansible-runner` |
| `warden-ui` | à côté de l'API | l'interface web (facultative) |

La CLI parle à **deux adresses** : l'API, et le port d'écriture du rôle `git`
par lequel partent les commits d'audit.

---

## 1. Choisir un mode de déploiement

| Mode | Pour quoi | Prérequis | Persistance |
|---|---|---|---|
| **A. Docker Compose** | essayer, faire une démo, développer | Docker, Go | aucune garantie : Vault en mémoire, authentification de dev |
| **B. Kubernetes, chart publiée** | une vraie instance d'équipe | un cluster, Helm ≥ 3.17, l'opérateur CNPG | base CNPG, volume des dépôts conservé |
| **C. Kubernetes, depuis les sources** | tester une branche du produit | B + Docker buildx, un registre | idem B |

> ⚠ Le mode A **n'est pas** une installation légère de production : l'API y
> accepte une identité affirmée sans mot de passe (`WARDEN_INSECURE_DEV_AUTH`)
> et un redémarrage de Vault perd les clés SSH.

---

## 2A. Docker Compose — essayer en local

Prérequis : Docker avec le plugin `compose`, Go 1.26, git ≥ 2.45.
`make doctor` dit ce qui manque.

```sh
# Les deux images que la stack lance
docker build -f build/wardend.Dockerfile -t warden/wardend:dev .
docker build -f build/runner.Dockerfile  -t warden/warden-runner:dev .

# Le répertoire que la forge locale sert, puis la stack
export WARDEN_FORGE_DIR=$PWD/deploy/compose/forge
mkdir -p "$WARDEN_FORGE_DIR"
docker compose -f deploy/compose/compose.yaml up -d --build

# Le premier compte — `grants.yaml` lui donne déjà l'environnement `prod`
docker compose -f deploy/compose/compose.yaml exec api \
  wardend user add --subject alice --admin
```

Ce que vous obtenez :

| Adresse | Quoi |
|---|---|
| <http://localhost:4280> | l'interface web — `localhost`, **pas** `127.0.0.1`, sinon le cookie de session ne part pas |
| `http://localhost:8080` | l'API (`WARDEN_SERVER`) |
| `http://localhost:8082` | le port d'écriture de `git` |
| `cible` | une machine gérée (sshd + Python), joignable des seuls runners |

La CLI, en mode développement :

```sh
make install
export WARDEN_SERVER=http://localhost:8080 WARDEN_SUBJECT=alice
warden whoami
```

Passez ensuite directement aux [premières actions](#4-premières-actions) —
en sautant l'étape « droits », déjà posée par `deploy/compose/grants.yaml`. Le
déroulé complet sur `cible` est dans
[`deploy/compose/README.md`](../deploy/compose/README.md#un-premier-run).

Arrêter :

```sh
docker compose -f deploy/compose/compose.yaml down      # garde la base
docker compose -f deploy/compose/compose.yaml down -v   # efface tout
```

---

## 2B. Kubernetes — avec la chart publiée

### Prérequis du cluster

- **L'opérateur [CloudNativePG](https://cloudnative-pg.io/)**, qui porte la
  base. Sans lui : `postgres.enabled=false` et une base existante, voir
  [la chart](../charts/warden/README.md#les-rôles-postgresql).
- **Un contrôleur d'Ingress**, et trois noms DNS : l'API, `git`, et l'UI (qui
  relaie l'API sous `/api`, en même origine).
- **Une StorageClass** pour le volume des dépôts et celui de la base (défaut :
  `local-path`).
- **Un CNI qui applique les NetworkPolicy** (Calico, Cilium…). Ailleurs, elles
  sont acceptées puis ignorées, et rien ne le signale.
- Facultatif : cert-manager pour les certificats TLS.

### Étape 1 — le namespace et le secret partagé

La chart ne crée **aucun** Secret : il se pose avant.

```sh
kubectl create namespace warden
kubectl create secret generic warden-internal -n warden \
  --from-literal=internal-token="$(openssl rand -base64 32)" \
  --from-literal=forge-token="<jeton de LECTURE sur votre forge git>"
```

`forge-token` permet au serveur de lire la branche protégée de vos dépôts de
playbooks (GitHub, GitLab, Gitea…).

### Étape 2 — vos valeurs

Les valeurs par défaut décrivent le déploiement de référence : **les hôtes et
les annotations d'Ingress sont à remplacer**. Un fichier minimal :

```yaml
# mes-valeurs.yaml
namespace:
  create: false                  # créé à l'étape 1

ingress:
  className: nginx               # votre contrôleur
  annotations:                   # retire les annotations Traefik du défaut
    traefik.ingress.kubernetes.io/router.middlewares: null
    cert-manager.io/cluster-issuer: letsencrypt-prod   # ou null sans cert-manager
  ui:                            # vide : l'UI n'est pas exposée
    host: warden-ui.example.com
    tlsSecret: warden-ui-tls
  api:
    host: warden.example.com
    tlsSecret: warden-api-tls
  git:
    host: warden-git.example.com
    tlsSecret: warden-git-tls
    annotations:
      traefik.ingress.kubernetes.io/router.middlewares.buffering: null

api:
  # L'adresse de `git` ANNONCÉE aux CLI : joignable depuis les postes,
  # donc l'Ingress, pas le Service interne.
  gitClientURL: https://warden-git.example.com

git:
  persistence:
    storageClass: standard       # la vôtre
postgres:
  storage:
    storageClass: standard

# Qui peut lancer où. C'est la SEULE source des droits (voir § 4).
grants:
  environments:
    staging:
      - alice
```

`null` et non une carte vide : Helm **fusionne** les cartes, donc `{}` ne
retirerait aucune annotation du défaut.

Registre privé ? Ajoutez un `docker-registry` Secret et `image.pullSecrets`,
voir les commentaires de `values.yaml`.

### Étape 3 — installer

La chart et les images portent **la même version** `0.0.N` : une chart tirée
en `0.0.N` déploie les images `0.0.N`. Prenez la dernière sur la page des
[releases](https://github.com/jijins1/warden/releases).

```sh
helm install warden oci://ghcr.io/jijins1/charts/warden \
  --version 0.0.N -n warden -f mes-valeurs.yaml
```

Les migrations tournent en hook **avant** que le serveur démarre. Les notes
affichées à la fin signalent un secret manquant ou des droits vides.

Avec ArgoCD : une Application qui pointe sur cette chart et ce fichier de
valeurs ; les hooks deviennent des PreSync.

### Étape 4 — vérifier

```sh
kubectl get pods -n warden
# warden-api, warden-git, warden-controller, warden-ui, et la base : Running
curl -fsS https://warden.example.com/version
```

### Étape 5 — le premier administrateur

Aucun endpoint n'accorde ce droit ; il se crée depuis le cluster :

```sh
kubectl exec -n warden deploy/warden-api -- \
  wardend user add --subject alice --admin
```

Le mot de passe est **tiré par le serveur et affiché une seule fois**.
Notez-le maintenant.

> Active Directory : voir `api.ldap` dans `values.yaml` et l'ADR-0053. Gardez
> malgré tout un compte local de bris de glace.

---

## 2C. Kubernetes — depuis les sources

Comme 2B, en remplaçant l'étape 3 :

```sh
make images-push REGISTRY=registry.example.com REGISTRY_OWNER=mon-equipe IMAGE_TAG=ma-branche
helm install warden charts/warden -n warden -f mes-valeurs.yaml \
  --set image.registry=registry.example.com/mon-equipe \
  --set image.tag=ma-branche --set image.pullPolicy=Always
```

`pullPolicy=Always` parce qu'un tag réutilisé n'est pas retiré par un nœud qui
l'a en cache. Un serveur construit ainsi est en version `dev` : il ne demande
pas aux CLI de se mettre à jour.

---

## 3. Installer la CLI

### Depuis une release (recommandé)

```sh
V=0.0.N                                   # la version du SERVEUR
OS=linux ARCH=amd64                       # darwin/arm64, windows/amd64…
curl -fLO https://github.com/jijins1/warden/releases/download/v$V/warden_${OS}_${ARCH}
curl -fLO https://github.com/jijins1/warden/releases/download/v$V/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 warden_${OS}_${ARCH} ~/.local/bin/warden
```

Prenez **la version du serveur**, pas forcément la dernière : `warden submit`
refuse de partir quand les deux diffèrent et propose de se mettre à niveau.
`warden upgrade` fait ce geste à la demande, en vérifiant la signature
minisign. Réseau fermé : `WARDEN_UPDATE_URL` pointe vers un miroir.

### Depuis les sources

```sh
make install       # → ~/.local/bin/warden, version « dev »
```

### Se connecter

```sh
export WARDEN_SERVER=https://warden.example.com
warden login --subject alice
warden whoami
```

À mettre dans votre `~/.zshrc` ou `~/.bashrc`. `WARDEN_SERVER` peut aussi venir
du `.warden.yml` d'un dépôt.

---

## 4. Premières actions

Dans cet ordre — le droit d'abord, sinon tout le reste est refusé. Un
« environnement » n'est pas un objet qu'on crée : c'est un nom que relient un
droit, une clé et des machines.

| # | Quoi | Commande | Détail |
|---|---|---|---|
| 1 | Donner le droit sur l'environnement | `grants.environments` dans les valeurs, puis `helm upgrade` | [§ 4](prise-en-main.md#4-accorder-le-droit-sur-lenvironnement--par-gitops) |
| 2 | Créer la clé SSH de l'environnement | `warden key generate --env staging` | [§ 5](prise-en-main.md#5-la-clé-daccès-de-lenvironnement) |
| 3 | Enrôler les machines | `warden enroll --all --env staging` | [§ 6](prise-en-main.md#6-les-machines) |
| 4 | Déclarer le dépôt de playbooks | `warden repo declare` (depuis le dépôt) | [§ 7](prise-en-main.md#7-configurer-le-dépôt) |
| 5 | Écrire `.warden.yml` | voir ci-dessous | [§ 8](prise-en-main.md#le-fichier-de-projet) |
| 6 | Lancer | `warden submit --playbook site.yml` | [§ 8](prise-en-main.md#8-lancer-un-playbook) |

### 1. Le droit

```yaml
grants:
  environments:
    staging: [alice]
```

```sh
helm upgrade warden oci://ghcr.io/jijins1/charts/warden --version 0.0.N \
  -n warden -f mes-valeurs.yaml
warden grant list          # ≈ 1 minute avant que le droit apparaisse
```

Aucune commande n'écrit les droits : ils vivent dans le déploiement, et passent
donc par la même revue que lui.

### 2. La clé

```sh
warden key generate --env staging   # la partie privée ne quitte jamais le serveur
warden key show     --env staging   # la publique, si vous la posez vous-même
```

### 3. Les machines

Depuis le dépôt de playbooks, quand il a déjà un inventaire :

```sh
warden enroll --all --env staging --dry-run   # voir le plan
warden enroll --all --env staging             # un mot de passe, puis la clé est posée
```

Ou une machine à la fois :

```sh
warden enroll --env staging --host web-01 --address 10.0.0.7 --login ansible
```

**Comparez l'empreinte affichée** avec celle de la machine
(`ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub`) avant de confirmer. Clés
déjà posées : `warden host declare`.

### 4. Le dépôt

```sh
cd ~/src/mes-playbooks
warden repo declare     # déduit nom, remote et branche de git, puis demande confirmation
```

Le remote doit être en **http(s)** : le serveur ne clone pas en SSH.

### 5. Le fichier de projet

```yaml
# .warden.yml, commité à la racine du dépôt
server: https://warden.example.com
repo: mes-playbooks
default_env: staging
inventory: inventories/{{ env }}
```

Le serveur relit ce fichier **dans le commit** soumis, et refuse une clé qu'il ne
connaît pas.

### 6. Le premier run

```sh
warden submit --playbook site.yml --check --diff   # d'abord à blanc
warden submit --playbook site.yml
```

Votre travail non commité part avec : c'est ce commit exact qui s'exécute, et
qui reste dans l'audit. `submit` suit le run et rend un code non nul en cas
d'échec. **Ctrl-C détache, il n'annule pas.**

L'historique est dans l'interface web, ou par `warden logs <run>`.

### Vérifier la chaîne sans toucher une machine

Un playbook `connection: local` tourne **sans environnement**, donc sans clé ni
machine. Il prouve toute la chaîne — session, commit d'audit, push, runner,
events — sur une instance neuve :

```yaml
# smoke.yml
- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - ansible.builtin.debug: { msg: "warden fonctionne" }
```

```sh
warden submit --playbook smoke.yml   # sans --env, et avant d'avoir posé default_env
```

Il suffit pour cela d'un dépôt déclaré (étape 4) : ni droit, ni clé, ni
machine.

---

## 5. Quand ça ne marche pas

| Symptôme | Cause probable |
|---|---|
| `api` et `git` ne démarrent pas | le secret `warden-internal` est absent (§ 2B, étape 1) |
| `submit` échoue sur une erreur DNS au push | `api.gitClientURL` pointe sur un Service interne, ou n'est pas posé : posez-le, ou `WARDEN_GIT_URL` côté poste |
| HTTP 500 à chaque push | le volume de `git` n'a pas les bons droits (`fsGroup`) — voir les journaux du pod `git` |
| « La CLI ne correspond pas au serveur » | `warden upgrade` |
| Un droit ajouté n'a pas d'effet | attendre ~1 minute (republication du ConfigMap), puis `warden grant list` |
| Run refusé sur une clé de `.warden.yml` | le serveur est plus ancien que le fichier : déployez le serveur d'abord |
| Machine refusée par ssh pendant un run | elle n'est pas déclarée avec sa clé d'hôte (`warden host list`) |
| UI : connexion qui ne tient pas (Compose) | ouvrir `localhost`, pas `127.0.0.1` |
| Nouveau tag poussé, ancien binaire qui tourne | tag réutilisé + `IfNotPresent` : versions immuables, ou `pullPolicy: Always` |

---

## 6. Avant de mettre en production

À lire **avant** de confier une flotte réelle à warden :

- **L'egress des runners est ouvert.** Le droit sur un environnement empêche
  d'obtenir sa clé, pas qu'un run de `staging` joigne une machine de `prod`.
  Bornez l'egress des pods runner par le réseau.
- **Vérifiez que votre CNI applique les NetworkPolicy** de la chart.
- **Activez les sauvegardes** (`postgres.backup`) : la base est le journal
  d'audit.
- **Utilisez des versions immuables** (`0.0.N`), jamais un tag mouvant.
- `api.replicas` > 1 n'a jamais été vérifié ; `git` et `controller` sont en
  instance unique, par conception.

La liste à jour : [« Trous connus » de la chart](../charts/warden/README.md#trous-connus-et-assumés)
et [`reste-a-faire.md`](reste-a-faire.md).
