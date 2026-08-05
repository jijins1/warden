# ADR-0010 — Stratégie de test en trois étages, sans cluster jetable

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Warden dépend de trois substrats qui ne se simulent pas honnêtement :

- **Le binaire git** (ADR-0005), dont le comportement de `GIT_NAMESPACE`, de
  `git maintenance` et du protocole v2 est le mécanisme de sécurité de
  l'ADR-0007 ;
- **PostgreSQL** (ADR-0008), dont on utilise `ON CONFLICT` sur `(run_id, seq)`,
  le partitionnement par date et `LISTEN` / `NOTIFY` — soit trois comportements
  qu'aucun double de test ne reproduit ;
- **Kubernetes** (§8), dont on attend l'ordonnancement, le pull d'image, le
  respect des NetworkPolicy et le comportement réel d'un pod tué brutalement.

Le réflexe habituel est de couvrir le troisième avec un cluster jetable de type
kind. La question à trancher est de savoir si ce cluster répond réellement aux
questions qu'on lui pose, et à quel étage.

Deux exigences de la spec pèsent particulièrement lourd :

- §12 phase 0 exige une **mesure** du cold start — ordonnancement, pull,
  démarrage Python, chargement des collections. Une mesure faussée est pire
  qu'une absence de mesure, puisqu'elle conditionne le caractère obligatoire ou
  non du zygote (§8.5).
- L'ADR-0007 fait reposer l'isolation du runner sur l'egress NetworkPolicy. Un
  test de sécurité qui passe au vert sans rien vérifier est le pire résultat
  possible : il produit une confiance non fondée.

## Solutions envisagées

### Option A — Un cluster kind pour tout ce qui touche à Kubernetes
Un cluster jetable créé en CI et en local, cible unique des tests du contrôleur
et du runner.
- **Avantage** : un seul mécanisme à apprendre, cluster reproductible et jetable.
- **Rejetée parce que** : kind ment sur les deux questions qui comptent. Son CNI
  par défaut, kindnet, **n'applique pas les NetworkPolicy** — le test négatif
  exigé par l'ADR-0007 passerait sans rien prouver, et il faudrait installer
  Calico ou Cilium pour qu'il signifie quelque chose. Et mesurer un cold start
  sur un mono-nœud dont les images sont déjà locales ne mesure ni
  l'ordonnancement ni le pull, c'est-à-dire ni l'un ni l'autre des deux postes
  que la phase 0 cherche à chiffrer. À l'autre extrémité, kind est aussi
  surdimensionné pour la logique du reconciler : ~40 s de démarrage là où
  envtest en demande quelques-unes.

### Option B — Doubles de test pour PostgreSQL et l'API Kubernetes
Interfaces mockées, aucun service externe, tests entièrement en mémoire.
- **Rejetée parce que** : ce qu'on veut vérifier *est* le comportement du
  substrat. Un faux PostgreSQL confirmera toujours notre lecture de `ON
  CONFLICT` et de `NOTIFY`, y compris quand elle est fausse. Le mock vérifierait
  notre compréhension au lieu de la confronter.

### Option C — Trois étages, chacun avec l'outil adapté à sa question ✅ (retenue)
Séparer ce qui se teste sans infrastructure, ce qui exige un substrat réel mais
local, et ce qui n'a de sens que sur un vrai cluster.
- **Choisie parce que** : elle donne un étage rapide qui reste exécuté à chaque
  commit, et elle empêche que les tests les plus coûteux — qui sont aussi les
  seuls à répondre aux questions de la phase 0 et de l'ADR-0007 — soient
  remplacés par une imitation qui les rendrait rassurants et faux.

## Décision

| Étage | Objet | Outil | Fréquence |
|---|---|---|---|
| **1 — unitaire** | Logique pure ; moteur git sur bare repos temporaires (ADR-0005) ; composition de configuration (ADR-0009) | aucun : le binaire `git` et le filesystem | chaque commit |
| **2 — intégration** | Journal et transport d'events (ADR-0008) ; informer, reconciler, RBAC (§8.7) | **testcontainers** (PostgreSQL) et **envtest** (contrôleur) | chaque commit |
| **3 — bout en bout** | Cold start (§12 phase 0) ; egress du runner (ADR-0007) ; run tué brutalement (§6.4) | **le cluster k3s réel** | nocturne et à la demande |

**Le moteur git se teste à l'étage 1, pas 2.** Un bare repo temporaire coûte
quelques dizaines de millisecondes ; il n'y a aucune raison de le reléguer avec
ce qui exige un conteneur. C'est ce qui garde l'étage le plus fréquent utile.

**PostgreSQL vient de testcontainers, pas d'un service déclaré côté CI.** Le
même chemin de code sert en local et en CI, la version de PostgreSQL est
épinglée dans le code de test, et il n'y a rien à provisionner avant de lancer
`go test`.

**Le contrôleur se teste avec envtest**, qui lance un `kube-apiserver` et un
`etcd` réels comme processus locaux, sans conteneur ni kubelet. Les pods n'y
démarrent jamais — c'est acceptable, parce que l'étage 2 ne teste pas le
démarrage d'un pod mais les décisions du reconciler face à l'état de l'API.

**L'étage 3 vise le cluster k3s existant, jamais un cluster jetable.** Il est
multi-nœuds, il applique réellement les NetworkPolicy, et son pull d'image est
un vrai pull. C'est la seule configuration où les chiffres de la phase 0 et le
test négatif de l'ADR-0007 signifient quelque chose.

**Confinement de l'étage 3.** Le cluster héberge des services réels, et le rôle
`controller` supprime des pods. En conséquence, et sans exception :

- namespace dédié, jamais `default` ;
- ServiceAccount sans aucun droit hors de ce namespace ;
- **sélecteur de labels obligatoire dans toute requête** du reaper et du
  reconciler — jamais un `list` non filtré, même dans un namespace dédié ;
- `ResourceQuota` et `LimitRange` sur le namespace ;
- tout PVC nomme explicitement sa `storageClassName`. Le cluster déclare deux
  StorageClass `default` (`local-path` et `nfs-client`) : un PVC qui omet la
  classe peut atterrir sur NFS, c'est-à-dire exactement le mode de corruption
  décrit par l'ADR-0006. Le dépôt bare va sur une classe **locale et
  extensible** (`lvm-ubuntu`), conformément à « la réponse à la croissance est
  d'agrandir le volume ».

## Conséquences

- **Avantages** :
  - l'étage 1 reste assez rapide pour être exécuté en boucle pendant le
    développement ;
  - aucun test ne prétend valider une NetworkPolicy ou un cold start dans un
    environnement qui ne les reproduit pas ;
  - une seule dépendance d'infrastructure en CI — un démon Docker, que les
    runners hébergés fournissent nativement ;
  - le contrôleur est testable sans cluster, donc contribuable sans accès au
    cluster.

- **Contraintes** :
  - **L'étage 3 n'est pas joignable depuis un runner cloud** : le cluster est sur
    un réseau privé. Il faudra un runner self-hosted dans le cluster. Ce n'est
    pas bloquant avant la phase 0, mais ça le devient à partir d'elle.
  - L'étage 3 s'exécute sur un cluster partagé avec des services réels. Le
    confinement ci-dessus est une condition d'exécution, pas une bonne pratique.
  - envtest ne fait pas tourner de kubelet : tout ce qui dépend du démarrage
    effectif d'un pod glisse à l'étage 3, qui est le plus lent et le plus rare.
    La frontière entre les deux doit être tenue consciemment, sinon la
    couverture réelle du contrôleur se dégrade sans que rien ne le signale.
  - `kodi` est un nœud arm64 et il est `SchedulingDisabled` : les images sont
    construites pour amd64 uniquement. Si ce nœud redevient ordonnançable, il
    faudra soit des images multi-arch, soit un `nodeSelector`.

- **Dette technique éventuelle** :
  - Aucune mesure de couverture n'est encore branchée. L'objectif de 80 % porte
    sur les étages 1 et 2 ; l'étage 3 valide des propriétés, pas des lignes.
  - Le cluster ne fournit ni Vault ni équivalent : l'intégration Vault SSH CA de
    la phase 4 n'a pas d'environnement de test à ce jour.
  - La version de PostgreSQL utilisée par testcontainers et celle du `Cluster`
    CloudNativePG déployé sur k3s doivent être tenues alignées manuellement.
