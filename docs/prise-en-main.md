# Prise en main

Le chemin complet, de rien du tout à un playbook qui tourne sur une machine.

Écrit en suivant les commandes sur le déploiement de référence, pas de mémoire :
chaque étape a été exécutée. Ce qui n'a pas de commande est signalé comme tel.

---

## 0. Ce qu'un « environnement » est, et n'est pas

**Il n'existe aucune commande pour créer un environnement**, et ce n'est pas un
oubli : un environnement n'est pas un objet. C'est un NOM qui relie trois choses
tenues séparément, chacune par qui en a l'autorité.

| Ce qui le compose | Qui le pose | Où |
|---|---|---|
| **le droit** — qui a le droit d'y lancer | l'exploitant, par GitOps | `values.yaml`, jamais une commande |
| **la clé** — avec quoi on joint ses machines | un administrateur | `warden key generate` |
| **les machines** — ce qui en fait partie | un administrateur | `warden enroll` ou `warden host declare` |

Un environnement « existe » dès qu'un droit le nomme. Un run peut d'ailleurs
tourner **sans environnement** : sans environnement il n'y a pas de clé, donc
rien à autoriser — c'est le cas d'un playbook `connection: local`.

L'ordre ci-dessous n'est pas arbitraire : le droit d'abord, faute de quoi tout
le reste est refusé.

---

## 1. Installer la CLI

```sh
make install
```

Construit `warden` et le pose dans `~/.local/bin` — le répertoire que la
convention XDG réserve aux binaires d'un utilisateur, et que les distributions
récentes mettent dans le `PATH` sans rien demander. Si ce n'est pas le cas chez
vous, la cible le dit et donne la ligne à ajouter ; elle ne modifie aucun
fichier de configuration à votre place.

Ailleurs qu'à cet endroit : `make install PREFIX=/usr/local`, avec les droits
correspondants. Et `make uninstall` pour retirer ce que `make install` a posé.

> Pas `go install` : il dépose dans `$(go env GOPATH)/bin`, qui est rarement
> dans le `PATH`. L'échec est alors trompeur — l'installation réussit, puis la
> commande reste introuvable.

---

## 2. Configuration de la CLI

Deux adresses, parce que la CLI parle à deux surfaces qui n'ont ni le même
protocole ni la même politique. **Il n'y a pas de drapeau `--server`.**

```sh
export WARDEN_SERVER=https://warden.example.com        # l'API
export WARDEN_GIT_URL=https://warden-git.example.com   # le port d'écriture de git
```

`WARDEN_GIT_URL` est **facultative** : le serveur annonce l'adresse du rôle `git`
en même temps qu'il délivre le jeton de push. Elle ne sert qu'aux déploiements
qui ne l'annoncent pas encore, et n'a de toute façon d'usage que pour `submit` —
administrer des clés et des machines ne pousse aucun commit.

`WARDEN_SERVER` peut elle aussi venir du fichier de projet (§6 bis), auquel cas
la variable d'environnement la surcharge.

## 3. Ouvrir une session

```sh
warden login --subject alice     # le mot de passe est demandé au terminal
warden whoami                    # demande au SERVEUR qui il croit que vous êtes
warden logout
```

Le jeton est rangé dans `~/.config/warden/credentials.json`, en **0600**, indexé
par adresse de serveur — vous parlez à un warden de dev et à un de prod, et
écraser l'un par l'autre pointerait une commande sur la mauvaise flotte.

Pour les usages non interactifs : `--password-file <chemin>` ou
`--password-stdin`. Jamais sur `argv` : la ligne de commande d'un processus est
lisible par tout le monde sur la machine.

### Le tout premier compte

Personne ne naît administrateur et aucun endpoint n'accorde ce droit. Le premier
se crée **depuis le cluster**, parce que son autorisation est l'accès à la base —
la seule qui existe avant qu'un compte existe.

```sh
kubectl exec -n warden-dev deploy/warden-api -- \
  wardend user add --subject alice --admin
```

Le mot de passe est **tiré par le serveur** et affiché une seule fois. Un mot de
passe choisi par un administrateur est un mot de passe qu'il connaît, et il est
presque toujours faible parce qu'il faut le dicter.

> `wardend` (le binaire serveur) n'a que `add | passwd | list`. Désactiver un
> compte se fait depuis la CLI : `warden user disable --subject <compte>`.

## 4. Accorder le droit sur l'environnement — par GitOps

**Aucune commande n'écrit les droits.** La source est le document de
déploiement, réconcilié par le rôle `controller` :

```yaml
# values.yaml de la chart
grants:
  environments:
    prod:
      - alice
      - bob
    staging: []        # explicitement personne
```

```sh
helm upgrade warden charts/warden -n warden-dev -f mes-valeurs.yaml
```

Compter **environ une minute** avant l'effet : le kubelet met quelques dizaines
de secondes à republier un ConfigMap monté, et c'est lui qui domine, pas warden.

Pour chaque environnement **nommé**, la liste fait autorité — un compte retiré
est révoqué. Un environnement **absent** n'est pas touché, ce qui permet de
découper les droits en plusieurs fichiers et évite qu'un fichier tronqué révoque
tout. Un document **vide** est refusé : « ne rien vouloir » et « n'avoir rien pu
lire » se ressemblent trop.

```sh
warden grant list                # vue d'audit : les révoqués sont affichés aussi
```

## 5. La clé d'accès de l'environnement

```sh
warden key generate --env prod   # la partie privée ne quitte JAMAIS le cluster
warden key show     --env prod   # la publique, à poser dans les authorized_keys
warden key list
```

Ou importer une paire existante : `warden key import --env prod --key-file <chemin>`
(ou `--key-stdin`).

Une clé par environnement, partagée par ses machines : les logs de l'hôte diront
`ansible`, pas `alice`. La non-répudiation s'arrête à la frontière du produit.

## 6. Les machines

Deux modes, qui convergent sur le même état final.

### Mode assisté — warden pose la clé lui-même

```sh
warden enroll --env prod --host db-01 --address 10.20.0.42 --login ansible
```

Il se connecte par mot de passe, capture l'empreinte de la machine, ajoute la
clé aux `authorized_keys`, puis **se reconnecte avec la clé** — c'est cette
dernière étape qui distingue « la clé est posée » de « la machine est joignable ».

Il affiche alors l'empreinte et **demande confirmation**. Vérifiez-la sur la
machine avant de répondre :

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub
```

C'est le seul geste qui protège du premier contact aveugle. Refuser la
confirmation laisse la clé posée mais la machine hors inventaire ; le serveur le
dit et renvoie vers `warden host declare`.

### Mode déclaratif — la clé est déjà en place

```yaml
# machines.yaml
hosts:
  - host: db-01              # le nom TEL QUE l'inventaire Ansible le nomme
    address: 10.20.0.42      # une ADRESSE : c'est là qu'on se connecte
    login: ansible
    port: 22                 # facultatif
    host_key: "ssh-ed25519 AAAA..."   # facultatif, mais c'est lui qui épingle
```

```sh
warden host declare -f machines.yaml --env prod
warden host list --env prod
warden host forget --env prod --host db-01
```

`host_key` est facultatif et devrait ne pas l'être en production : sans lui, le
premier contact fait confiance à ce qui répond.

> `host forget` retire la ligne d'inventaire. Il ne retire **pas** la clé
> publique de l'`authorized_keys` de la machine.

## 7. Configurer le dépôt — ⚠ pas de commande

Un dépôt doit être connu du serveur avant qu'on puisse y lancer un run. **La CLI
n'a pas de commande pour ça** ; l'endpoint existe, il faut l'appeler à la main.

```sh
# Le fichier de session indexe par adresse, sans barre finale.
TOKEN=$(python3 -c "
import json, os
f = json.load(open(os.path.expanduser('~/.config/warden/credentials.json')))
print(f['servers'][os.environ['WARDEN_SERVER'].rstrip('/')]['token'])")

curl -X PUT "$WARDEN_SERVER/admin/repos/mon-depot" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"mon-depot",
       "remote_url":"https://forge.example.com/equipe/mon-depot.git",
       "mainline_ref":"refs/heads/main"}'
```

`mainline_ref` n'est pas décoratif : c'est la branche protégée contre laquelle le
`pre-receive` contrôle la fraîcheur du commit d'audit, et c'est le serveur qui la
détient — un dépôt qui déclarerait lui-même la sienne choisirait ce que le
serveur vérifie.

## 8. Lancer un playbook

Depuis un clone local du dépôt :

```sh
warden submit --repo mon-depot --playbook play.yml --env prod --inventory hosts
```

```sh
warden submit --repo mon-depot --playbook play.yml -K   # ou --ask-become-pass
```

**Le travail non commité part avec.** `submit` construit un commit d'audit depuis
votre arbre de travail — sans toucher à votre index ni à `HEAD` — le pousse, et
le runner exécute **ce commit exact**. C'est la raison d'être du produit : ce qui
s'est exécuté est ce qui a été audité.

L'inventaire et le playbook sont des chemins **relatifs au dépôt**.

### Le fichier de projet

Un dépôt peut déclarer ses conventions dans un `.ansibleproxy.yml` **commité à sa
racine**, ce qui évite de retaper les mêmes drapeaux à chaque run :

```yaml
server: https://warden.example.com     # où joindre le control plane
default_env: staging                   # environnement quand --env est absent
inventory: inventories/{{ env }}       # « {{ env }} » suit l'environnement effectif
runner_image: registry.example.com/mon-equipe/runner:2.3.1
forward_env:                           # NOMS des variables du shell à transmettre
  - VAULT_ADDR
audit_include:                         # chemins à joindre au commit d'audit
  - inventories/**                     # malgré les règles d'ignore
```

Avec ce fichier, la commande du dessus se réduit à :

```sh
warden submit --repo mon-depot --playbook play.yml
```

**Le serveur lit ce fichier DANS L'ARBRE DU COMMIT**, pas sur votre poste. Ce qui
s'applique est donc exactement ce qui est audité, et une CLI modifiée ne peut pas
faire diverger les deux. Votre copie locale ne sert qu'à trois choses que le
serveur ne peut pas faire à votre place : savoir où l'appeler, savoir quelles
variables lire dans votre shell, et savoir quoi mettre dans le commit.

Un drapeau explicite prime toujours sur le fichier — et l'inventaire templatisé
**suit** le drapeau : `--env prod` donne `inventories/prod`.

**`forward_env` déclare des NOMS, jamais des valeurs.** Les valeurs sont lues
dans votre shell au moment de l'appel, ne sont jamais journalisées, et sont
détruites dès qu'elles ont été remises au pod. Seuls les noms déclarés sont
consultés : votre environnement n'est jamais parcouru en entier.

Trois noms sont **réservés** et font échouer le run s'ils apparaissent dans
`forward_env` — `ANSIBLE_CALLBACK_PLUGINS`, `PYTHONPATH`, `LD_PRELOAD`. Le
premier permet de remplacer le plugin de capture d'audit par un faux, ce qui
viderait le flux d'events sans la moindre erreur. Voir l'ADR-0034.

Le schéma est **strict** : une clé inconnue, une clé écrite deux fois, une ancre
ou un alias YAML font échouer la lecture en nommant le fautif. C'est voulu —
ignorer silencieusement une clé de configuration est précisément le mode de panne
qu'on cherche à éliminer.

`--run-ref <ref>` reprend une soumission interrompue. Un run est un
enregistrement d'audit : il ne se rejoue pas sous le même identifiant, et le
serveur répond alors ce qu'est devenu le run plutôt que de le recréer.

---

## Récapitulatif — un environnement neuf de bout en bout

```sh
# 0. les deux adresses
export WARDEN_SERVER=https://warden.example.com
export WARDEN_GIT_URL=https://warden-git.example.com
warden login --subject alice

# 1. le droit — par GitOps, ~1 min d'effet
#    values.yaml :  grants.environments.staging: [alice]
helm upgrade warden charts/warden -n warden-dev -f mes-valeurs.yaml
warden grant list

# 2. la clé
warden key generate --env staging

# 3. la machine
warden enroll --env staging --host web-01 --address 10.0.0.7 --login ansible
warden host list --env staging

# 4. le dépôt, à la main faute de commande
curl -X PUT "$WARDEN_SERVER/admin/repos/mon-depot" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"mon-depot","remote_url":"https://forge/x.git","mainline_ref":"refs/heads/main"}'

# 5. le run
cd ~/src/mon-depot
warden submit --repo mon-depot --playbook play.yml --env staging --inventory hosts
```

---

## Ce que ces commandes n'achètent PAS

À lire avant d'en conclure quoi que ce soit sur la sécurité.

Le droit sur un environnement empêche d'**obtenir sa clé**, ce qui est réel :
sans clé, aucune machine joignable. Il n'empêche **pas** qu'un run légitime de
`staging` joigne une machine de `prod` — l'egress du pod runner est ouvert, et le
§10.2 pose que « le périmètre est appliqué par le réseau ou il ne l'est pas ».

De même, `--limit` n'est jamais un contrôle de sécurité, et les flags passés à
`submit` ne sont **pas** filtrés : la politique serveur du §5.5 n'existe pas
encore.
