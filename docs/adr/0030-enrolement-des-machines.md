# ADR-0030 — Enrôlement des machines : deux modes, deux magasins, et l'egress qu'on a ouvert

**Date** : 2026-08-19
**Statut** : Accepted
**Complète** : [ADR-0020](0020-key-inventory-credentials-dacces.md), [ADR-0018](0018-canal-de-dispatch-sse-vers-le-runner.md), [ADR-0023](0023-identite-damorcage-du-pod-runner.md)
**Amende** : le §10.2 de `specs/initial.md`

## Contexte

Le lot précédent a livré l'exécution : un playbook tourne, ses events remontent,
le run se clôt. Il s'est terminé sur une limite énoncée sans détour — aucun run
ne peut joindre une machine gérée.

Trois manques formaient un seul trou :

1. **`keyinventory` ne contenait qu'un `doc.go`.** L'ADR-0020 avait décidé le
   composant et son premier adaptateur ; rien n'avait été écrit, et
   `core/order.go` laissait `Order.SSHKey` vide en le disant en commentaire.
2. **Aucune table d'hôtes.** Le modèle `environnement → scope d'hôtes` du §10.2
   n'existait nulle part : `run.environment` était une chaîne sans rien derrière.
3. **Aucun chemin pour poser une clé sur une machine.** Le produit supposait
   qu'un opérateur l'avait fait, sans jamais le dire ni le vérifier.

## Solutions envisagées

### Option A — Mode déclaratif seul
L'opérateur pose les clés lui-même et déclare ses machines à warden.
- **Avantage** : le produit ne touche jamais à un mot de passe d'utilisateur.
- **Rejetée comme seule option parce que** : elle laisse tout le coût
  d'adoption sur l'opérateur, sur une flotte où chaque machine est un geste
  manuel. Le §5.7 pose que si l'ingénieur ne comprend pas, il retourne au
  bastion SSH ; s'il doit y retourner *pour installer le produit*, il n'y entre
  jamais. Elle est **retenue comme l'un des deux modes**.

### Option B — Mode assisté seul
Warden se connecte et pose la clé.
- **Rejetée comme seule option parce que** : elle impose au produit de voir un
  mot de passe humain, ce qu'une organisation qui gère déjà ses
  `authorized_keys` par sa propre automatisation n'a aucune raison d'accepter.

### Option C — Les deux modes, convergeant sur le même état ✅ (retenue)
Le formalisme est unique ; seul le chemin qui y mène diffère.
- **Choisie parce que** : ce qu'il faut savoir d'une machine ne dépend pas de la
  façon dont sa clé y est arrivée. Un seul inventaire, un seul épinglage, une
  seule requête d'audit — et `enrollment_mode` retient la différence là où elle
  compte réellement : dans un cas le produit a vu passer un mot de passe, dans
  l'autre non.

## Décisions

### 1. Deux magasins pour une clé

La partie **privée** vit dans un Secret Kubernetes (ADR-0020) ; la partie
**publique**, l'empreinte et les métadonnées vivent dans PostgreSQL.

Un seul magasin obligerait à choisir entre un journal d'audit qui contient des
secrets — répliqué, sauvegardé, jamais purgé (ADR-0013) — et une configuration
qu'on ne peut pas requêter. La migration 00007 énonce la règle et la table ne
porte jamais de clé privée, comme `runner_pod` ne porte jamais de jeton.

`Issue(user, env, hostScope)` est écrite telle que l'ADR-0020 l'a spécifiée.
`user` et `hostScope` sont **reçus et ignorés** : c'est une décision locale à
l'adaptateur, et ce sont ces deux paramètres qui rendront Vault indolore.

### 2. Le formalisme : l'adresse est obligatoire

```yaml
hosts:
  - host: db-prod-02        # le nom TEL QUE l'inventaire Ansible le nomme
    address: 10.20.0.42     # une ADRESSE : c'est là qu'on se connecte
    login: ansible
    host_key: "ssh-ed25519 AAAA..."   # facultatif, et c'est lui qui protège
                                      # du premier contact aveugle
```

L'adresse n'est pas un confort. Dépendre d'une résolution DNS faite **dans le
pod** ferait enrôler une machine que l'opérateur croyait désigner — et une
machine enrôlée à la mauvaise adresse est une machine dont on croit détenir
l'accès. Stockée en `inet`, donc validée par PostgreSQL.

Le `host` est le nom **d'inventaire Ansible**, parce que c'est par lui qu'un
event de run — dont l'`host` vient d'Ansible — se rapproche d'une machine
enrôlée. Un identifiant à nous ne se rapprocherait de rien.

**Ce que la table n'est pas** : une source d'inventaire. Ce qui décide de ce qui
s'exécute vient du commit d'audit (ADR-0009) ; le dupliquer ici en ferait une
seconde vérité, et la première divergence se paierait sur une machine de
production.

### 3. Le mode assisté se connecte depuis un POD

Trois raisons, la dernière étant décisive :

- le canal de dispatch est le seul conçu pour porter des credentials (ADR-0018) ;
- donner au control plane un egress SSH vers des adresses arbitraires en ferait
  un pivot, et l'ADR-0020 note déjà que sa permission sur les Secrets est la
  seule dont l'abus est silencieux ;
- **c'est depuis le pod que la joignabilité compte.** Un enrôlement vérifié
  depuis `wardend` peut parfaitement échouer au premier run.

`x/crypto/ssh` et non `ansible.posix.authorized_key` : ce module exige Python
sur la cible, or une machine fraîche n'en a pas forcément — et c'est justement
le cas qu'un enrôlement doit couvrir.

L'enrôlement **vérifie** en rouvrant une session : sans cela on déclarerait
enrôlée une machine qu'aucun run ne peut joindre, et on le découvrirait sur un
playbook de production.

### 4. Un enrôlement est un RUN

`run` gagne un `kind`. Poser une clé d'accès sur une machine de production, par
une personne nommée, depuis un pod, est exactement ce que ce produit existe pour
journaliser — et tout ce dont l'opération a besoin existe déjà : le pod, le
canal, le flux d'events, l'idempotence de l'ingestion, la clôture du §6.4. Une
table parallèle aurait dupliqué les cinq.

Le prix : six colonnes de `run` ne valent que pour un playbook et deviennent
conditionnelles au genre. C'est **plus fidèle** que ce qui existait — elles
n'étaient obligatoires que parce qu'aucun autre genre n'existait — et deux
contraintes `CHECK` remplacent les `NOT NULL`, en couvrant aussi le cas
symétrique.

### 5. Le mot de passe ne fait que passer

Il est saisi par un humain, remis par `POST /admin/enrollments`, et lu quelques
secondes plus tard à la **livraison** de l'ordre. Entre les deux, il est dans une
**consigne** : un Secret Kubernetes, avec une différence qui change tout — `Take`
et non `Get`, **la lecture détruit**. Une valeur qui ne sert qu'une fois et qu'on
peut relire est une valeur qui traîne.

Pas PostgreSQL : une insertion laisserait une trace dans le WAL, donc dans les
sauvegardes et les réplicas, pour une durée de vie utile de l'ordre de la
seconde (ADR-0018).

**C'est le seul credential du produit qui vienne d'un humain et non du serveur.**
L'ADR-0020 borne son magasin aux credentials que le serveur ÉMET ; celui-ci fait
exception, et il faut le lire comme tel.

### 6. C'est la CLI qui déclare, pas le pod

Le pod rapporte l'empreinte constatée comme un **event ordinaire** — c'est une
donnée publique. La CLI la lit, l'affiche, demande confirmation, puis déclare la
machine avec son propre jeton d'administrateur.

Trois bénéfices, dont le troisième n'était pas cherché :

- le pod n'écrit jamais dans l'inventaire, donc `warden_ingest` n'a pas besoin
  d'y toucher ;
- ce que le pod raconte de la machine est relu par quelqu'un ;
- **la revue humaine du premier contact tombe au bon endroit.** Sans empreinte
  déclarée à l'avance, l'enrôlement fait confiance à ce qui répond, et cette
  question est le seul moment où l'on peut encore dire non. Elle refuse par
  défaut et dit *comment* comparer.

Le prix est un client WebSocket dans la CLI, qui n'existait pas.

### 7. La saisie est un composant, pas un helper

`prompt.Source` porte la décision « fichier, entrée standard, ou invite ? », et
`prompt.Declarer` enregistre `--ask-x`, `--x-file`, `--x-stdin`. Ce qui se
répétait n'était pas du code de confort : c'étaient des règles de sécurité — ni
`argv` ni environnement, jamais ; deux provenances refusées ; pas de lecture
implicite de l'entrée standard. Une règle répétée à quatre endroits est une règle
qu'un cinquième oubliera.

Elle sert déjà trois valeurs : la clé privée d'un import, le mot de passe d'un
enrôlement, et le mot de passe d'élévation de `warden submit --ask-become-pass`.

### 9. `become` passe par `extravars`, et c'est une MESURE qui l'a décidé

Trois chemins existent pour remettre un mot de passe d'élévation à
`ansible-runner`. Deux sont des pièges :

| Chemin | Verdict |
|---|---|
| `passwords` en kwargs | **rejeté** — détruit l'audit |
| `-e ansible_become_password=…` | **rejeté** — la valeur sur `argv` |
| `extravars` en kwargs ✅ | retenu |

**`passwords` fait basculer `ansible-runner` sous `pexpect`.** Le plugin d'audit
n'est alors plus utilisé : les trames du flux perdent leur champ `event`, il n'y
a plus ni `host`, ni `task`, ni `state`, et `job_events` reste VIDE sur disque.
L'élévation fonctionnerait, et l'audit disparaîtrait en silence — c'est
exactement ce que l'ADR-0021 redoute d'`ANSIBLE_STDOUT_CALLBACK`, par une autre
porte. Mesuré : 15 trames dont aucune structurée, contre 14 events structurés et
0 trame brute avec `extravars`.

Avec `extravars`, la commande composée porte `-e @<pdd>/env/extravars` : la
valeur est dans un FICHIER, jamais sur la ligne de commande, et elle n'apparaît
nulle part dans le flux d'events.

Le fichier est écrit par warden lui-même en 0600, et non laissé à
`ansible-runner` — qui pose bien 0600, c'est mesuré, mais s'en remettre pour la
protection d'un mot de passe à un comportement qu'on n'a pas écrit, c'est le
perdre le jour où il change.

Ce mot de passe transite par la MÊME consigne que celui d'un enrôlement : même
nature — il vient d'un humain —, même durée de vie, même règle.

### 8. L'egress des runners est OUVERT — et c'est un recul

À lire sans atténuation. `deploy/60-networkpolicy.yaml` n'applique plus aucune
règle de sortie aux pods runner.

**Ce que ça emporte :**

- le contrôle n°2 du §10.2 — que l'ADR-0020 désigne comme le **seul** contrôle
  d'autorisation réel de la v1 — n'existe plus. Tout run atteint toute machine,
  avec la clé de son environnement ;
- le contrôle n°3 de l'ADR-0027 tombe : le port d'écriture du rôle `git` (8082)
  redevient joignable, et il ne reste que le genre inscrit dans la signature du
  jeton — « un contrôle qui dépend d'une vérification », ce que la règle de
  lecture seule existait pour ne pas être seule à faire ;
- le port de l'API (8080) et l'API interne de `git` (8081) deviennent joignables,
  alors que l'ADR-0007 dit que le runner ne les joint jamais ;
- l'API Kubernetes devient joignable ; seul `automountServiceAccountToken: false`
  l'arrête encore.

**Pourquoi malgré tout** : le périmètre du §10.2 doit être posé par run à partir
du scope de l'utilisateur, et rien ne le calcule. Une NetworkPolicy standard ne
sait d'ailleurs pas résoudre un nom — mesuré sur le cluster cible, un k3s dont
l'applicateur est kube-router, sans Cilium ni Calico — donc un périmètre s'y
énumère en CIDR ou ne s'exprime pas. Le développement avance avec l'egress
ouvert plutôt qu'avec un produit qui ne joint rien.

**Ce qui tient encore** : `ingress: []`. Rien ne joint un pod runner.

Refermer se fait en supprimant une règle : les règles étroites sont dans
l'historique git, à restaurer telles quelles.

## Conséquences

- **Avantages** :
  - le produit a enfin un chemin d'accès aux machines, et il est auditable de
    bout en bout — un enrôlement est un run, avec ses events et sa clôture ;
  - les deux modes convergent sur un état unique, donc une seule requête d'audit
    répond à « que sais-je de cette machine » quelle que soit son origine ;
  - l'épinglage est appliqué par le code qui se connecte, pas par une consigne
    d'exploitation : un écart d'empreinte fait échouer et ne pose rien ;
  - la signature de l'ADR-0020 est enfin implémentée, donc vérifiée : `hostScope`
    et `expiry` s'avèrent suffisants pour ce que la v1 fait.

- **Contraintes** :
  - **Aucune autorisation par hôte.** Voir la décision 8. C'est le point à
    reprendre avant toute production, et il est plus grave que ce que l'ADR-0020
    décrivait : celle-ci se rabattait sur un contrôle qui n'existe plus.
  - **Le produit voit des mots de passe humains** : celui d'un enrôlement, celui
    de `become`. Éphémères, jamais persistés, jamais journalisés — mais vus, et
    c'est une posture différente de celle de l'ADR-0020, qui bornait son magasin
    aux credentials que le serveur ÉMET.
  - **Le mot de passe d'élévation est sur disque dans le pod** pendant le run,
    en 0600, lisible par le playbook. C'est acceptable — le playbook est le code
    de l'utilisateur et le mot de passe est le sien — et ça ne le serait pas
    pour un credential du serveur.
  - **La confiance à la première rencontre n'est pas protégée.** L'empreinte est
    capturée, affichée et soumise, mais la première connexion fait confiance à ce
    qui répond. `warden host declare` avec `host_key` est le chemin qui l'évite,
    et devrait être préféré en production.
  - `wardend` écrit désormais des Secrets de trois familles — jetons de pod,
    clés d'accès, consignes de passage. La permission RBAC n'a pas changé ; ce
    qu'elle recouvre, si.

- **Dette technique éventuelle** :
  - **Aucun désenrôlement réel.** `warden host forget` retire la ligne, pas la
    clé de l'`authorized_keys`. La commande le dit à chaque fois plutôt que de
    laisser croire à une révocation, mais le geste manque — et il manque aussi à
    la rotation, dont l'ADR-0020 notait déjà qu'elle n'est pas outillée.
  - **Aucun groupe.** Le §10.2 dit `utilisateur|groupe` ; seul l'utilisateur est
    traité, comme `is_admin` avant lui.
  - **`environment_grant` a été écartée.** Sans application par le réseau, elle
    n'appliquerait rien — le §10.2 classe le filtrage côté serveur en « confort,
    pas sécurité ». Elle reviendra avec le contrôle qu'elle est censée servir.
  - L'escalade Ctrl-C à trois niveaux du §5.6 n'est toujours traitée qu'au
    premier signal.
