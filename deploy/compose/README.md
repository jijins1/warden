# warden en mode Docker + Vault

Une stack `compose` pour faire tourner warden **sans cluster** (ADR-0047) :
les runners sont lancés par l'API Docker, tous les secrets sont dans Vault.

> ⚠ Développement et démonstration seulement. Vault est en mode `-dev` (en
> mémoire : un redémarrage perd les clés), l'API accepte l'identité affirmée
> (`WARDEN_INSECURE_DEV_AUTH`), et le proxy du socket ne filtre pas finement.

## Ce qui tourne

| Service | Rôle |
|---|---|
| `postgres`, `migrate` | la base, et ses migrations au démarrage |
| `vault`, `vault-init` | Vault dev ; montages KV v2 `warden` et `warden-transit`, policy sans `list`, AppRole |
| `docker-proxy` | le seul accès au démon Docker — warden ne voit jamais le socket |
| `api` | API, canal de dispatch, ingestion ; lance les runners |
| `git` | dépôts d'audit ; push de la CLI sur `localhost:8082` |
| `controller` | droits, réconciliation des runs, ramassage et borne de durée des conteneurs |
| `cible` | une machine gérée (sshd + Python), joignable des seuls runners |
| `forge` | une forge git en HTTP, qui sert `$WARDEN_FORGE_DIR` |

Les runners rejoignent le réseau `warden-runners`, où ils joignent `api`,
`git` et `cible` par leur nom.

## Démarrer

```sh
docker build -f build/wardend.Dockerfile -t warden/wardend:dev .
docker build -f build/runner.Dockerfile  -t warden/warden-runner:dev .

export WARDEN_FORGE_DIR=$PWD/deploy/compose/forge   # les dépôts que la forge sert
mkdir -p "$WARDEN_FORGE_DIR"
docker compose -f deploy/compose/compose.yaml up -d --build

# Le premier compte. `grants.yaml` lui donne `prod`.
docker compose -f deploy/compose/compose.yaml exec api wardend user add --subject alice --admin
```

## Un premier run

La CLI, avec l'identité affirmée du mode développement :

```sh
go build -o ~/.local/bin/warden ./cmd/warden
export WARDEN_SERVER=http://localhost:8080 WARDEN_SUBJECT=alice
```

Un dépôt de playbooks publié sur la forge (HTTP « bête », d'où
`update-server-info`) :

```sh
git init --bare --initial-branch=main "$WARDEN_FORGE_DIR/demo.git"
# … dans votre dépôt de playbooks :
git push "$WARDEN_FORGE_DIR/demo.git" HEAD:refs/heads/main
git -C "$WARDEN_FORGE_DIR/demo.git" update-server-info

warden repo declare --name demo --remote-url http://forge:8000/demo.git \
  --mainline-ref refs/heads/main --yes
```

La clé de l'environnement (rangée dans Vault), puis la machine. L'enrôlement
assisté lance déjà un runner, et son mot de passe passe par la consigne Vault :

```sh
warden key generate --env prod
printf 'hunter2\n' | warden enroll --env prod --host cible --address cible \
  --login ansible --password-stdin
```

L'empreinte proposée se compare à celle de la machine :

```sh
docker compose -f deploy/compose/compose.yaml exec cible ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub
```

Puis, avec un inventaire qui nomme `cible` (`ansible_host: cible`,
`ansible_user: ansible`) et `inventory:` dans `.ansibleproxy.yml` :

```sh
warden submit --playbook site.yml --env prod
```

## Arrêter

```sh
docker compose -f deploy/compose/compose.yaml down      # garde la base
docker compose -f deploy/compose/compose.yaml down -v   # efface tout
```

Les conteneurs runner portent le label `warden.io/instance=warden-compose` ;
le contrôleur ramasse ceux qui sont terminés.
