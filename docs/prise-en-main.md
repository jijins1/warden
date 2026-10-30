# Prise en main

Le chemin complet, de rien du tout à un playbook qui tourne sur une machine.

Écrit en suivant les commandes sur le déploiement de référence, pas de mémoire :
chaque étape a été exécutée. Ce qui n'a pas de commande est signalé comme tel ;
à ce jour, seule la pose du premier administrateur en est là.

Ce document suppose un serveur déjà installé. Pour l'installer, et pour le
chemin court : [`demarrage.md`](demarrage.md).

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

Deux modes, qui convergent sur le même état final — et deux façons de les
désigner : une machine à la fois, ou **toute la flotte depuis votre inventaire
Ansible**.

### Toute la flotte, depuis l'inventaire du dépôt

C'est le chemin à préférer quand le dépôt a déjà un inventaire. Depuis le dépôt :

```sh
warden enroll --all --env prod --dry-run   # voir avant de faire
warden enroll --all --env prod
```

warden lit le chemin déclaré par `inventory` dans `.warden.yml`, résout
`{{ env }}`, en tire les machines, les compare à `warden host list`, et **ne
traite que celles qui manquent**. Il affiche le plan, demande une fois
confirmation, puis enrôle machine par machine avec **un seul mot de passe**.

```
inventaire  inventories/prod
environnement  prod
4 machine(s) dans l'inventaire, 1 déjà connue(s) du serveur

1 machine(s) déjà enrôlée(s) dont l'inventaire dit autre chose.
Elles ne sont PAS touchées :

MACHINE  INVENTAIRE ≠ SERVEUR
kodi     adresse 192.168.16.2 ≠ 192.168.16.9

3 machine(s) à traiter :

MACHINE  ADRESSE        COMPTE  PORT  GROUPES
ionos    192.168.18.10  root    2222  distant
muzi     192.168.16.3   ruokki  22    cluster
tiny     tiny  (nom)    ruokki  22    cluster
```

Ce que warden lit de l'inventaire, et **rien d'autre** :

| clé | rôle |
|---|---|
| `ansible_host` | l'adresse. **Absente**, c'est le NOM qui sert d'adresse |
| `ansible_user` | le compte que warden ouvre |
| `ansible_port` | facultatif |

Les `vars:` écrits dans le document sont hérités, du groupe vers la machine.
**`group_vars/` et `host_vars/` ne sont pas lus** : reproduire à peu près la
précédence d'Ansible enrôlerait des machines sous le mauvais compte sans que rien
ne le dise. Une valeur Jinja est refusée pour la même raison. Si une machine n'a
pas d'`ansible_user`, le lot s'arrête **avant la première connexion** en la
nommant.

> **« (nom) »** signale une machine sans `ansible_host` : c'est son nom qui sert
> d'adresse, et il sera résolu **depuis le cluster** — pas depuis votre poste, et
> sans lire votre `ssh_config`.

Drapeaux utiles : `--group <nom>` restreint le lot, `--password-per-host` quand
les comptes ne partagent pas leur mot de passe, `--stop-on-error` pour s'arrêter
au premier échec au lieu de continuer, `--yes` pour ne pas confirmer le plan.

Si les clés sont **déjà posées** sur vos machines, la même lecture sert au mode
déclaratif :

```sh
warden host declare --from-inventory --env prod --dry-run
warden host declare --from-inventory --env prod
```

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
    address: 10.20.0.42      # où l'on se connecte : une adresse, ou un nom
                             # que LE POD sait résoudre
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
premier contact fait confiance à ce qui répond. C'est aussi la limite du chemin
`--from-inventory` : un inventaire Ansible ne porte pas de clé d'hôte, donc les
machines déclarées par ce chemin ne sont pas épinglées — `warden host list`
l'affiche en toutes lettres.

> `host forget` retire la ligne d'inventaire. Il ne retire **pas** la clé
> publique de l'`authorized_keys` de la machine.

## 7. Configurer le dépôt

Un dépôt doit être connu du serveur avant qu'on puisse y lancer un run. Depuis le
dépôt lui-même :

```sh
cd ~/src/mon-depot
warden repo declare
```

La commande déduit tout de git — le nom du dernier segment de l'URL d'`origin`,
le remote de sa configuration, la branche de référence — puis **affiche ce
qu'elle a trouvé et demande confirmation** avant d'écrire :

```
nom                   mon-depot
remote                https://forge.example.com/equipe/mon-depot.git
branche de référence  refs/heads/main

Déclarer ce dépôt auprès du serveur ? [o/N]
```

La question n'est pas une politesse. Quand le dépôt n'a pas été cloné — donc
quand git ne connaît pas la branche par défaut du remote — c'est la **branche
courante** qui sert de repli. Déclarer depuis une branche de feature épinglerait
le mauvais point de comparaison pour tous les runs du dépôt, et c'est ce moment
qui existe pour l'attraper. `--mainline-ref` la corrige, `--yes` saute la
question dans un script.

`mainline_ref` n'est pas décoratif : c'est la branche protégée contre laquelle le
`pre-receive` contrôle la fraîcheur du commit d'audit, et c'est le serveur qui la
détient — un dépôt qui déclarerait lui-même la sienne choisirait ce que le
serveur vérifie.

> **Si votre `origin` est en SSH**, la déduction échoue en le disant : le serveur
> ne sait cloner qu'en http(s). Donnez la forme http du même dépôt avec
> `--remote-url`.

`warden repo list` affiche les dépôts connus.

### Se passer de `--repo`

Une fois le dépôt déclaré, la commande affiche la ligne à ajouter au fichier de
projet :

```yaml
# .warden.yml
repo: mon-depot
```

`warden submit` n'a alors plus besoin de `--repo`. Le drapeau reste prioritaire
quand il est donné.

> ⚠ **Attendez que le serveur accepte ce champ avant de le commiter.** Le serveur
> relit `.warden.yml` depuis l'arbre du commit et **refuse les clés qu'il
> ne connaît pas** : contre un serveur plus ancien, cette ligne fait échouer
> *tous* les runs du dépôt.

## 8. Lancer un playbook

Depuis un clone local du dépôt :

```sh
warden submit --repo mon-depot --playbook play.yml --env prod --inventory hosts
```

```sh
warden submit --repo mon-depot --playbook play.yml -b -K   # -b élève, -K donne le mot de passe
```

> ⚠ **`-K` seul n'élève rien.** Il ne fait que transporter le mot de passe. Si
> votre playbook ne déclare pas `become: true` lui-même, il faut `-b` — c'est ce
> que fait `ansible-playbook -K`, et warden le sépare en deux drapeaux parce que
> les deux moitiés voyagent par des chemins différents : le drapeau est audité,
> le mot de passe ne l'est jamais.
>
> `-b` s'applique à **toutes** les tâches, y compris celles que le playbook avait
> laissées non élevées. `--become-user <compte>` vise un autre compte que `root`,
> et n'a de sens qu'avec `-b` — seul, il est refusé.

Les autres flags d'`ansible-playbook` acceptés, sous leur nom d'origine :

```sh
# Simuler : Ansible annonce ce qu'il ferait, et montre le contenu des changements.
warden submit --repo mon-depot --playbook play.yml --env prod --check --diff

# N'agir que sur une machine, ou sur un groupe de l'inventaire.
warden submit --repo mon-depot --playbook play.yml --env prod --limit web-01

# Ne jouer que les tâches portant ces tags, ou sauter celles-ci.
warden submit --repo mon-depot --playbook play.yml --env prod --tags deploy,config --skip-tags slow

# Détailler la sortie d'Ansible : -v, -vv, -vvv, -vvvv.
warden submit --repo mon-depot --playbook play.yml --env prod -vv
```

> ⚠ **La verbosité écrit dans l'audit, pas seulement à l'écran.** La sortie
> détaillée d'Ansible arrive dans le flux d'events du run, qui est persistant et
> servi par l'API. À partir de `-vvv`, Ansible imprime les arguments de chaque
> tâche et la commande de connexion. Les valeurs injectées par warden restent
> masquées (ADR-0017) et `no_log` est respecté ; ce qui devient lisible est ce
> qu'un playbook met lui-même en clair dans ses tâches.
>
> Les `v` se répètent **dans** le drapeau : `-v -v` ne fait pas `-vv`. Le serveur
> refuse deux niveaux envoyés ensemble ; en ligne de commande, le plus élevé
> l'emporte.

Le serveur n'accepte que ceux-là et refuse le reste en nommant la liste
(ADR-0038). Ils sont enregistrés dans le run, et `GET /runs/{run_ref}` les rend :
zéro changement sous `--check` ne se lit pas comme zéro changement sans.

### `-e`, et la liste blanche qui le rend possible

`-e nom=valeur` ne pose que les variables que le fichier de projet **du commit**
déclare. C'est le même mécanisme que `forward_env` : le fichier versionné dit les
noms, la ligne de commande n'apporte que les valeurs.

```yaml
# .warden.yml
extra_vars:
  - version
  - couleur
```

```sh
warden submit --playbook deploy.yml --env prod -e version=1.2.3 -e couleur=bleu
```

Un nom non déclaré est refusé, en nommant ceux qui le sont. Le drapeau est
répétable et existe sous ses deux noms, `-e` et `--extra-vars`.

> ⚠ **Les valeurs d'`extra_vars` sont AUDITÉES.** Elles sont enregistrées sur le
> run, servies par l'API et visibles dans les diffs — c'est le but : « quelle
> version a été déployée » est la question qu'un audit doit pouvoir répondre.
> **N'y mettez pas de secret.** `forward_env` est le champ dont les valeurs ne
> sont jamais journalisées, et le mot de passe d'élévation a son propre chemin.

> ⚠ Même précaution de déploiement que pour `repo:` : le serveur **relit ce
> fichier depuis le commit** et refuse les champs qu'il ne connaît pas. Un dépôt
> qui commite `extra_vars:` contre un serveur plus ancien verra *tous* ses runs
> refusés. Déployez le serveur d'abord.

Les noms commençant par `ansible_` sont **réservés** : ils décrivent comment le
run se connecte, et les poser depuis la requête sortirait de l'inventaire audité.
`ansible_become_password` en particulier finirait en clair dans la base, les
valeurs d'`-e` étant enregistrées.

`submit` **suit le run** et affiche son déroulement, tâche par tâche, comme
`ansible-playbook`. Il rend un **code non nul si le run échoue**, ce qui le rend
utilisable dans un pipeline.

```sh
warden submit --repo mon-depot --playbook play.yml --no-follow   # rendre la main tout de suite
warden logs 01JZQ8XKPT9V2H3M4N5P6Q7R8S                            # s'y rattacher plus tard
```

Le flux est **rejouable depuis le début** : `warden logs` sur un run déjà
terminé réaffiche tout. Il n'y a pas deux chemins « rattrapage » et « direct ».

> ⚠ **Ctrl-C détache, il n'annule pas.** Le run CONTINUE côté serveur et
> continue de modifier vos machines. Le message le dit et rappelle la commande
> pour se rattacher. L'annulation demande un endpoint que le serveur n'expose pas
> encore — voir `docs/reste-a-faire.md`.

**Le travail non commité part avec.** `submit` construit un commit d'audit depuis
votre arbre de travail — sans toucher à votre index ni à `HEAD` — le pousse, et
le runner exécute **ce commit exact**. C'est la raison d'être du produit : ce qui
s'est exécuté est ce qui a été audité.

L'inventaire et le playbook sont des chemins **relatifs au dépôt**.

### Le fichier de projet

Un dépôt peut déclarer ses conventions dans un `.warden.yml` **commité à sa
racine**, ce qui évite de retaper les mêmes drapeaux à chaque run. L'ancien nom,
`.ansibleproxy.yml`, est encore lu quand `.warden.yml` est absent :

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

### Une configuration par playbook

Un dépôt porte rarement un seul playbook. La clé `playbooks:` complète la
configuration pour l'un d'eux, désigné par son chemin **relatif à la racine du
dépôt** — celui que vous passez à `--playbook` :

```yaml
# .warden.yml
extra_vars:            # valables pour tous les playbooks du dépôt
  - version
  - couleur
playbooks:
  deploy.yml:
    extra_vars:        # en plus, pour deploy.yml seulement
      - other
    runner_image: registry.example.com/mon-equipe/runner:3.0.0
```

Une surcharge accepte `default_env`, `inventory`, `runner_image`, `forward_env`
et `extra_vars`. Les valeurs seules **remplacent** celles de la racine ; les
listes **s'y ajoutent**. Un nom déjà déclaré à la racine est refusé, comme
`server`, `repo` ou `audit_include` dans une surcharge : ils ne dépendent pas du
playbook. Un playbook absent de `playbooks:`, et tout run ad-hoc, reçoivent la
racine telle quelle.

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

# 4. le dépôt, depuis le dépôt
cd ~/src/mon-depot
warden repo declare

# 5. le run
warden submit --repo mon-depot --playbook play.yml --env staging --inventory hosts
```


---

## Trouver une commande

L'aide est **à tiroir** : trois niveaux, chacun répondant à la question de son
niveau.

```sh
warden --help                    # les commandes, une ligne chacune
warden help host                 # ce que « host » fait, et ses verbes
warden help host declare         # un verbe : usage, drapeaux, exemples
warden host declare --help       # la même page, depuis l'invocation
```

`--help` est reconnu **n'importe où** dans la ligne : `warden enroll --env prod
--help` répond par la page d'`enroll`, et non par une erreur de drapeau.

Une aide demandée sort sur la sortie standard et rend **0** — c'est une commande
qui a réussi. Une commande incomplète, inconnue ou mal formée sort sur l'erreur
standard et rend **1**, en ne montrant que ce qui la concerne :

```
$ warden hots
warden: "hots" n'est pas une commande de « warden ». Vouliez-vous dire "host" ?
« warden --help » les liste toutes.
```

---

## Ce que ces commandes n'achètent PAS

À lire avant d'en conclure quoi que ce soit sur la sécurité.

Le droit sur un environnement empêche d'**obtenir sa clé**, ce qui est réel :
sans clé, aucune machine joignable. Il n'empêche **pas** qu'un run légitime de
`staging` joigne une machine de `prod` — l'egress du pod runner est ouvert, et le
§10.2 pose que « le périmètre est appliqué par le réseau ou il ne l'est pas ».

De même, `--limit` **n'est jamais un contrôle de sécurité**, et ne le devient pas
en passant par warden : un run limité à `web-01` reste un run dont le pod a un
egress ouvert et une clé d'accès valable sur tout l'environnement. Il restreint
ce qu'Ansible fait, pas ce que le pod peut joindre.

`--check` n'est pas non plus une garantie d'innocuité : c'est Ansible qui décide
de le respecter, module par module, et un `command` ou un `shell` sans
`check_mode:` s'exécute quand même.

Les flags passés à `submit` sont en revanche **filtrés** depuis l'ADR-0038 : le
serveur n'accepte que `--check`, `--diff`, `--limit`, `--tags`, `--skip-tags`, `-b/--become`,
`--become-user`, `-e/--extra-vars` et `-v` à `-vvvv`, et refuse le reste en
nommant la liste. Ce filtre protège l'audit — un `-e @/chemin` ferait exécuter autre chose
que ce que le commit dit — et non les machines cibles, sur lesquelles le playbook
exécute déjà du code arbitraire.
