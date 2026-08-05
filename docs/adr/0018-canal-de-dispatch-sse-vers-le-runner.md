# ADR-0018 — Canal de dispatch SSE du contrôle vers le runner

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Le §4 décrit les flux de la CLI vers le serveur, et l'ADR-0008 décrit ceux du
runner vers le serveur. Personne ne décrit le sens inverse : **comment l'ordre de
run descend jusqu'au pod.**

Ce n'est pas un oubli de plomberie, c'est une conséquence directe du
pré-chauffage. Le §8.2 pose que le pod est allumé **avant** qu'un run existe, et
l'ADR-0015 a rendu ce pré-chauffage obligatoire. Les trois mécanismes évidents
sont donc fermés :

| Mécanisme | Pourquoi il ne marche pas |
|---|---|
| `env` / `args` de la podspec | Le pod est créé avant le run. Il n'y a rien à y mettre. |
| Projected volume de Secret | Monté à la création, même problème — et le §8.3 exige **aucun credential avant le claim**. |
| Annotation lue par le pod | `automountServiceAccountToken: false` (§7). Le runner ne peut pas interroger l'API Kubernetes, et le §7 nomme explicitement l'activer comme « l'erreur classique ». |

Trois choses n'ont donc aujourd'hui aucun chemin d'arrivée dans le pod : l'ordre
de run lui-même (SHA, flags effectifs, environnement), **le jeton git de
l'ADR-0007** — dont l'ADR ne dit pas comment il entre — et les secrets injectés
au claim (§8.3). Le signal d'annulation du §5.6 a le même problème.

## Solutions envisagées

### Option A — Renoncer au pré-chauffage et tout passer par la podspec
Un pod par run, créé au moment du run, avec l'ordre dans ses variables
d'environnement.
- **Avantage** : c'est le modèle standard, sans canal ni identité d'amorçage.
- **Rejetée parce que** : le §8.1 pose que le cold start de 10 à 30 secondes
  « tue la boucle de dev, et c'était tout l'argument face à AWX ». L'ADR-0015 a
  mesuré et rendu le pré-chauffage obligatoire. Cette option échange le problème
  contre la raison d'être du produit.

### Option B — Le runner lit son ordre depuis l'API Kubernetes
Le contrôleur pose une annotation sur le pod, le runner la lit.
- **Rejetée parce que** : elle exige `automountServiceAccountToken: true`, que le
  §7 interdit nommément. Donner à du code arbitraire (§2) un jeton d'API
  Kubernetes pour lui transmettre un paramètre est disproportionné, et la
  capacité obtenue serait très supérieure au besoin.

### Option C — Un canal unique bidirectionnel portant ordres et events
Le runner ouvre un WebSocket ; le contrôle descend, les events remontent par le
même socket.
- **Avantage** : un seul canal, une seule règle d'egress, une seule
  authentification.
- **Rejetée parce que** : elle défait quatre propriétés acquises. L'ADR-0008 a
  isolé l'ingestion par **port distinct, ingress dédiée et rôle PostgreSQL
  restreint à `INSERT`** — le canal de contrôle, qui distribue des secrets,
  deviendrait aussi celui qui écrit le journal. Une saturation du flux de logs
  emporterait la capacité d'**annuler** le run : « je ne peux plus journaliser »
  et « je ne peux plus être arrêté » deviendraient le même incident. Un `POST`
  est sans état et absorbable par n'importe quel réplica, là où une connexion
  tenue épingle chaque runner à un réplica — un rolling update les couperait
  toutes au moment précis où le seuil de retry de l'ADR-0008 tue les runs. Enfin
  elle réintroduit l'affinité d'ordre que l'ADR-0008 avait explicitement écartée
  (« l'ingestion se parallélise sur N réplicas sans contrainte d'ordre »).

### Option D — SSE descendant, `POST` montant ✅ (retenue)
Le runner ouvre un flux SSE sortant vers un endpoint de dispatch. Le contrôle
descend par ce flux. Les events remontent par l'ingestion existante, inchangée.
- **Choisie parce que** : le besoin descendant est du contrôle — faible débit,
  faible fréquence — et SSE le couvre exactement avec une réponse HTTP ordinaire,
  sans négociation de protocole. L'ADR-0008 n'est pas amendée, seulement
  complétée.

## Décision

**Le pod runner ouvre, dès son démarrage et avant de détenir le moindre
credential, un flux SSE sortant vers un endpoint de dispatch.**

L'endpoint est servi par le rôle `api` (ADR-0006) sur un **port distinct** de
celui de l'API et de celui de l'ingestion, avec sa propre règle d'ingress
NetworkPolicy. L'egress du runner défini par l'ADR-0007 s'étend de ce port, et
de rien d'autre.

**Ce que le canal transporte, dans ce sens uniquement :**

- l'ordre de run — SHA, environnement, et les **flags effectifs après filtrage
  par la politique serveur** (§5.4.2), jamais la demande brute du client ;
- le jeton git de l'ADR-0007, qui n'avait pas de chemin de livraison ;
- les secrets du claim (§8.3) ;
- les signaux de contrôle, dont l'annulation du §5.6.

**Ce qu'il ne transporte pas** : les events. Ils continuent de remonter par
`POST` vers l'ingestion (ADR-0008), avec leur `seq` assigné par le runner et
leur idempotence sur `(run_id, seq)`.

### Effets sur des décisions existantes

**Le claim devient une sélection, pas une course.** Le §8.4 exigeait un « claim
atomique, annotation avec compare-and-swap, ou file côté server ». Avec des pods
connectés, réclamer c'est choisir une connexion oisive dans une structure que le
serveur détient déjà. Le compare-and-swap sur objet Kubernetes disparaît.

**La chute de la connexion vaut signal de mort**, disponible en une seconde là
où l'informer du §8.7 met plus longtemps. C'est un **complément, pas un
remplacement** : le §8.7 reste la source de vérité, parce qu'une connexion qui
tombe ne prouve pas que le pod est mort, seulement qu'il n'est plus joignable.

**Le §8.3 devient réalisable tel qu'écrit.** « Aucun credential avant le claim »
n'était tenable par aucun mécanisme de podspec ; il l'est par un canal ouvert
avant le claim et alimenté après.

### Rupture du canal pendant un run

**Le run continue.** Il journalise toujours par l'ingestion, donc l'audit — la
seule chose que le produit garantisse réellement — n'est pas compromis. Ce qu'il
perd, c'est d'être annulable : le signal du §5.6 n'a plus de chemin.

Le contrôleur conserve le levier ultime, supprimer le pod, et
`activeDeadlineSeconds` (§8.7) continue de borner le coût d'un run qui pend. Le
runner, lui, ne réessaie **pas** d'ouvrir un nouveau canal en cours de run : une
reconnexion le rendrait de nouveau adressable et rouvrirait la question de savoir
à qui il parle, pour un bénéfice nul puisque l'ordre a déjà été reçu.

### Identité d'amorçage

C'est le point central de cette décision, et il n'est pas résolu par le canal —
seulement localisé.

Pour ouvrir le flux, le pod doit s'authentifier. Son identité native, le jeton
de ServiceAccount, est désactivée par le §7. Il lui faut donc une identité
injectée à sa création, **antérieure au run**, dont la capacité unique est
« je suis le pod chaud *N*, ai-je un run ? ». Elle ne donne accès à aucune
donnée, ne vaut rien tant qu'aucun run ne lui est attribué, et n'est pas le
jeton git — celui-ci arrive par le canal, après le claim.

## Conséquences

- **Avantages** :
  - les trois éléments sans chemin d'arrivée — ordre, jeton git, secrets — en ont
    un, et le même ;
  - l'ADR-0008 est complétée, pas amendée : l'ingestion garde son port, son
    ingress et son rôle PostgreSQL `INSERT` seul ;
  - une saturation du journal ne coûte jamais la capacité d'arrêter un run ;
  - le claim du §8.4 se simplifie et cesse de dépendre d'une écriture sur objet
    Kubernetes ;
  - SSE est une réponse HTTP ordinaire : traversée de proxy, observabilité et
    outillage sont ceux de HTTP, sans upgrade de protocole.

- **Contraintes** :
  - **Un troisième port exposé par le rôle `api`**, donc une troisième surface
    joignable depuis du code arbitraire. Elle doit être couverte par un test
    négatif, au même titre que celle de l'ingestion : un pod chaud ne doit rien
    pouvoir obtenir d'un run qui ne lui est pas attribué.
  - **Le canal est un chemin de distribution de secrets.** TLS obligatoire, et
    interdiction stricte de journaliser le corps des messages de dispatch — un
    log de debug sur cet endpoint exposerait ce que toute l'ADR-0017 cherche à
    protéger.
  - Des connexions longues et nombreuses — une par pod chaud — épinglées à un
    réplica. Le dimensionnement du rôle `api` en dépend, et un rolling update
    coupe tous les canaux oisifs simultanément. Ce n'est bénin que parce que les
    canaux oisifs se rouvrent et que les canaux en run ne sont plus nécessaires.
  - Le protocole du canal est propre au domaine runner. L'ADR-0011 interdit au
    runner de lier `internal/wire/ws` ; ce canal aura son propre paquet, distinct
    de celui de la CLI.

- **Dette technique éventuelle** :
  - **L'identité d'amorçage n'est pas conçue.** Tant qu'elle ne l'est pas, cette
    ADR décrit un transport sans son contrôle d'accès. C'est le premier sujet à
    traiter en phase 1, avant le protocole lui-même.
  - Le lien entre `run_id` et pod doit être établi **côté serveur**, jamais accepté
    depuis le corps d'une requête. C'est vrai du dispatch comme de l'ingestion, et
    ce n'était écrit nulle part.
  - SSE ne transporte que du texte. L'encodage des secrets dans le flux est un
    détail d'implémentation, mais un détail qui touche des valeurs sensibles :
    il doit être fixé explicitement, pas hérité d'un défaut de bibliothèque.
