# ADR-0023 — Identité d'amorçage du pod runner : jeton porteur émis par le contrôleur

**Date** : 2026-08-06
**Statut** : Accepted

## Contexte

L'ADR-0018 se termine sur une dette nommée : « **l'identité d'amorçage n'est pas
conçue.** Tant qu'elle ne l'est pas, cette ADR décrit un transport sans son
contrôle d'accès. C'est le premier sujet à traiter en phase 1, avant le protocole
lui-même. » Le canal de dispatch ne peut donc pas s'implémenter avant cette
décision, puisque son premier octet est une authentification.

Le besoin est étroit et il faut le garder étroit. Pour ouvrir le flux, le pod
doit prouver **une seule chose** : *je suis le pod chaud N*. Il n'a rien à lire,
rien à écrire, aucun run à nommer. Tant qu'aucun run ne lui est attribué, cette
preuve ne vaut rien.

Trois contraintes ferment les chemins habituels :

| Contrainte | Effet |
|---|---|
| `automountServiceAccountToken: false` (§7) | L'identité native du pod est désactivée, et le §7 nomme son activation « l'erreur classique ». |
| Le pod est pré-chauffé (§8.2, ADR-0015) | Tout ce qui vient de la podspec est fixé **avant** qu'un run existe. |
| Le control plane n'a aucun droit hors de son namespace | Contrainte d'exploitation : le cluster héberge des services réels. Rien de cluster-scoped. |

Un quatrième point, découvert en instruisant la question et qui déborde de
l'identité : l'ADR-0018 note en dette que « le lien entre `run_id` et pod doit
être établi **côté serveur**, jamais accepté depuis le corps d'une requête », et
que c'est « vrai du dispatch comme de l'ingestion ». Or l'ingestion reçoit
aujourd'hui `(run_id, seq)` d'un pod hostile et le schéma de l'ADR-0021 note
explicitement que son `id` devinable n'est « sans conséquence QUE PARCE QUE » ce
lien est établi côté serveur. **Rien ne l'établit pour l'instant.** L'identité
d'amorçage est ce qui manque aux deux endroits, pas seulement au dispatch.

## Solutions envisagées

### Option A — Jeton de ServiceAccount projeté, à audience dédiée
Un volume `projected` avec une source `serviceAccountToken` d'audience
`warden-dispatch`. Le jeton n'est pas valable contre l'API Kubernetes puisqu'il
porte une autre audience ; le §7 est donc respecté dans son intention. Le
serveur le valide par `TokenReview`.
- **Avantage** : rien à générer, rien à stocker, rien à faire tourner. Expiration
  et rotation assurées par le kubelet, et un jeton lié à un pod supprimé cesse
  d'être valide — trois propriétés obtenues gratuitement.
- **Rejetée parce que** : `TokenReview` est une ressource **non namespacée**. La
  valider exige un `ClusterRole` et un `ClusterRoleBinding`, donc une permission
  hors du namespace du produit — ce que la contrainte d'exploitation interdit. La
  validation locale contre le JWKS du cluster déplace le problème sans le
  résoudre : elle exige `system:service-account-issuer-discovery`, également
  cluster-scoped. S'ajoute une dépendance de version : lier le jeton à un pod
  précis suppose que `TokenReview` remonte `pod-name` / `pod-uid` dans les extras
  de l'utilisateur, ce qui dépend de la version de Kubernetes — et sans ce lien,
  le pod A peut se présenter comme le pod B, donc voler l'ordre, le jeton git et
  la clé d'accès d'un run appartenant à un autre utilisateur.

### Option B — mTLS, un certificat client par pod
Le contrôleur émet un certificat court par pod chaud, le serveur exige un
certificat client sur le port de dispatch.
- **Avantage** : l'authentification est portée par la couche TLS déjà obligatoire
  (ADR-0018), et l'expiration est native.
- **Rejetée parce que** : il faut une CA — la créer, la stocker, la faire tourner,
  et décider ce qui se passe quand elle expire. C'est un composant
  d'infrastructure entier pour prouver « je suis le pod N », alors que le §10.2
  dit « ne pas réimplémenter, intégrer », et que l'ADR-0020 a déjà constaté que
  l'environnement cible n'a pas de gestionnaire de secrets. Le coût est celui
  d'une PKI, la capacité obtenue tient en une ligne.

### Option C — Jeton porteur émis par le contrôleur, injecté par variable d'environnement
Le contrôleur tire un aléa à la création du pod et le passe en `env` dans la
podspec.
- **Avantage** : le plus simple, aucun objet Kubernetes supplémentaire.
- **Rejetée parce que** : le secret devient une partie de l'objet `Pod`. Or l'objet
  `Pod` est précisément celui que le produit **liste, observe et journalise** le
  plus — le reconciler du §8.7 liste tous les pods au démarrage, l'informer les
  garde en mémoire, et le premier `kubectl describe` d'un incident l'affiche. Un
  secret dans le champ le plus consulté du système est un secret qui finira dans
  un log.

### Option D — Jeton porteur émis par le contrôleur, dans un Secret par pod ✅ (retenue)
Le contrôleur tire 256 bits d'aléa, crée un Secret dédié au pod, monte ce Secret
en lecture seule dans le pod, et ne conserve que l'**empreinte SHA-256** du jeton
dans PostgreSQL. Le pod présente le jeton pour ouvrir son canal de dispatch et
pour ingérer ses events.
- **Choisie parce que** : c'est le seul mécanisme qui reste **entièrement dans le
  namespace**, qui lie le porteur à un pod précis **par construction** plutôt que
  par une propriété dépendant de la version de Kubernetes, et dont le coût
  correspond à la capacité obtenue. L'état vit dans notre base et non dans etcd,
  ce qui est déjà la position du §8.8.

## Décision

**Le contrôleur émet, pour chaque pod chaud qu'il crée, un jeton d'amorçage de
256 bits, et n'en conserve que l'empreinte.**

### Ce que le jeton permet, exhaustivement

1. Ouvrir le canal de dispatch de l'ADR-0018 et y recevoir l'ordre destiné à
   **ce pod**, s'il en existe un.
2. `POST` des events d'ingestion sur le run **attribué à ce pod**, et sur aucun
   autre.

Il ne permet aucune lecture, ne nomme aucun run, et ne donne rien tant que le
pod n'est pas réclamé. C'est la « capacité unique » que l'ADR-0018 décrivait.

### Le `run_id` cesse d'être un paramètre

C'est la conséquence qui compte, et elle vaut pour les deux surfaces exposées au
domaine hostile :

> Le serveur résout `pod → run` depuis sa propre base, à partir du jeton présenté.
> Un `run_id` figurant dans un corps de requête est au mieux redondant, au pire
> une tentative — dans les deux cas il n'est pas la source.

C'est ce qui rend enfin vrai l'énoncé sur lequel repose le schéma de l'ADR-0021,
et c'est le test négatif que l'ADR-0018 exigeait pour son troisième port : un pod
chaud ne doit rien pouvoir obtenir d'un run qui ne lui est pas attribué.

### Cycle de vie

| Étape | Effet en base |
|---|---|
| Le contrôleur crée le pod | Ligne `runner_pod`, empreinte du jeton, clé de pool (utilisateur, dépôt, environnement) |
| Le pod ouvre son canal | `ready_at` renseigné — le pod devient **réclamable** |
| Le canal tombe | `ready_at` remis à `NULL` — le pod cesse d'être réclamable |
| Claim | `run_id` et `claimed_at` renseignés, en une seule instruction |
| Fin du run, ou mort du pod | `released_at` renseigné — le jeton est refusé à jamais |

Le claim est un `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)`. Le
`SKIP LOCKED` est ce que le §8.4 demandait sous le nom de « claim atomique » :
deux commandes concurrentes du même utilisateur ne peuvent ni recevoir le même
pod, ni **s'attendre** — la seconde ne voit aucun pod libre et tombe en cold
start, ce qui est exactement le comportement spécifié.

Le claim exige `ready_at IS NOT NULL` : on ne réclame que des pods **connectés**.
L'ADR-0018 décrivait le claim comme « choisir une connexion oisive dans une
structure que le serveur détient déjà » ; c'est ce prédicat, à ceci près que la
structure est une table et non une carte en mémoire — voir plus bas.

### Un pod n'est jamais rendu au pool

`released_at` est définitif. Le §8.2 pose qu'« un pod n'est jamais réutilisé pour
deux runs » ; la conséquence sur le jeton est qu'il n'y a **aucune révocation à
outiller** : sa durée de vie est celle du pod, et le pod meurt à la fin du run.
C'est la première fois dans ce projet qu'un secret n'a pas besoin de la procédure
de rotation que les ADR-0017 et 0020 appellent sans l'avoir écrite.

### Comparaison des empreintes

L'authentification est une **recherche par empreinte**, jamais une comparaison de
jeton : le serveur hache ce qu'on lui présente et cherche l'empreinte. Aucun
secret n'est comparé octet à octet, et la base ne contient jamais de jeton en
clair — la compromettre ne donne aucune identité de pod.

### Le lien avec le routage entre réplicas

Le claim écrit en base, mais la connexion SSE du pod réclamé est tenue par **un**
réplica, pas nécessairement celui qui a gagné le claim. Le réveil se fait par
`NOTIFY`, comme le flux d'events et dans le même esprit :

> **La notification porte un pointeur, jamais la charge.** Elle dit « le pod P a
> été réclamé », et le réplica qui détient sa connexion va chercher lui-même
> l'ordre et les credentials.

Ce n'est pas qu'une symétrie de style. L'ordre transporte le jeton git de
l'ADR-0007 et la clé d'accès de l'ADR-0020 : le faire transiter par `NOTIFY`
reviendrait à écrire ces secrets dans le WAL de PostgreSQL, donc à faire du
journal d'audit un magasin de secrets — précisément ce que l'ADR-0020 borne à
une seule famille de secrets, côté `key-inventory`. Avec un pointeur, **aucun
credential n'est jamais persisté** : il est lu et livré au moment de la livraison.

## Conséquences

- **Avantages** :
  - la dette centrale de l'ADR-0018 est levée, et le canal de dispatch devient
    implémentable ;
  - le lien `pod → run` est établi côté serveur pour le dispatch **et** pour
    l'ingestion, ce qui valide a posteriori une condition que le schéma de
    l'ADR-0021 supposait sans que rien ne l'assure ;
  - aucune permission Kubernetes hors du namespace, aucune PKI, aucune dépendance
    à la version du cluster ;
  - le claim atomique du §8.4 tient en une instruction SQL, sans compare-and-swap
    sur un objet Kubernetes ;
  - un secret dont la rotation n'a pas à être outillée, parce que sa durée de vie
    est celle d'un pod jamais réutilisé.

- **Contraintes** :
  - **Un objet Secret par pod chaud.** Il faut le créer avant le pod pour que le
    kubelet n'attende pas au montage, donc il existe brièvement sans
    `ownerReference`. Le reconciler du §8.7, qui liste déjà par label, doit
    ramasser les Secrets sans pod — c'est une charge de plus sur le composant que
    le §8.4 désigne comme point de fuite.
  - **Un jeton porteur reste un jeton porteur.** Le code exécuté dans le pod est
    hostile par hypothèse (§2) et peut le lire — c'est admis, puisque ce jeton ne
    donne accès qu'au run que ce pod exécute déjà. Ce qu'il faut protéger, c'est
    son transport : TLS obligatoire sur les deux ports, et interdiction de le
    journaliser, au même titre que le corps des messages de dispatch.
  - **L'exfiltration reste possible pendant le run**, et la fenêtre n'est fermée
    que par `released_at` à la mort du pod. La borne réelle est l'egress
    NetworkPolicy du §7, redevenue ici « le vrai contrôle » comme dans
    l'ADR-0020.
  - La ligne `runner_pod` est un état opérationnel dans une base d'audit qui ne
    purge rien (ADR-0013). Elle est petite et bornée par le nombre de pods, mais
    c'est la première table du schéma qui n'est pas de l'audit.

- **Dette technique éventuelle** :
  - **`ready_at` peut mentir.** Un réplica tué brutalement laisse des pods marqués
    connectés dont plus personne ne tient la connexion ; ils seront réclamés, et
    l'ordre n'arrivera pas. Il faut soit une reprise au démarrage — un réplica
    remet à `NULL` ce qu'il avait posé —, soit un délai de grâce après lequel un
    claim non livré est réattribué. Rien n'est décidé ici, et c'est le premier
    point à instruire quand le multi-réplica deviendra réel.
  - L'identité du **client CLI** est un sujet distinct (§10.1) et n'est pas
    traitée par cette décision. Les deux ne doivent pas converger : le jeton d'un
    pod ne doit jamais être accepté sur le port de l'API, ni l'inverse.
