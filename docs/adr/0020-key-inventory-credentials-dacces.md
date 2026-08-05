# ADR-0020 — `key-inventory` : les credentials d'accès derrière un composant, Secrets Kubernetes en v1

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

Trois passages de la spec décrivent la gestion des credentials, et tous les trois
supposent Vault :

- le §7 — « idéalement Vault Agent injector avec un lease qui expire à la fin du
  run. **Pas de Secret Kubernetes long-vécu** » ;
- le §8.3 — « aucun credential avant le claim. Un lease Vault ouvert 45 minutes
  sur un pod inactif est exactement ce qu'on cherche à éviter » ;
- le §10.2, qui classe les contrôles d'autorisation et met en tête les
  **certificats SSH éphémères dont les principals sont dérivés des droits de
  l'utilisateur** — « la cible refuse le cert si l'utilisateur n'a pas le droit.
  **C'est le contrôle qui compte.** »

**Vault n'existe pas dans l'environnement cible.** Ces trois énoncés ne sont donc
pas des décisions différées, ce sont des **cibles sans implémentation**, et la
spec ne le dit nulle part. Une v1 doit fonctionner sans eux, en connaissant le
prix.

Deux points de cadrage, découverts en instruisant la question.

**Il y a deux familles de secrets, et une seule relève de ce composant.**

| | Origine | Exposition |
|---|---|---|
| **Credentials d'accès** (clés SSH) | le serveur | jamais dans un playbook, jamais dans un `--diff` |
| **Variables du playbook** (`vault_db_password`…) | le **shell de l'utilisateur**, via `forward_env` (§5.4) | rendues dans les fichiers — ce sont elles que l'ADR-0017 masque |

Le §5.4 pose que le fichier de configuration projet ne contient « jamais de
valeurs sensibles, seulement des noms de variables », les valeurs venant de
l'environnement du shell. La seconde famille **transite** donc, elle ne se stocke
pas côté serveur. Ce composant est le seul magasin de secrets du control plane.

**Le montage est fermé.** L'ADR-0018 l'a établi pour l'ordre de run et ça vaut ici
aussi : un volume est fixé à la création du pod, et le pod est pré-chauffé
(ADR-0015). Un Secret monté serait de surcroît présent sur un pod oisif, soit
exactement le « credential avant le claim » que le §8.3 interdit.

## Solutions envisagées

### Option A — Attendre Vault
Ne pas implémenter la gestion des credentials tant que la cible du §10.2 n'est
pas déployable.
- **Rejetée parce que** : elle conditionne la v1 à un composant que personne ne
  prévoit d'installer, et le §10.2 dit précisément « ne pas réimplémenter,
  intégrer » — on n'écrira donc pas de substitut non plus. Le produit resterait
  indéfiniment sans chemin d'accès aux hôtes.

### Option B — Secret Kubernetes monté dans le pod
Le modèle standard : un volume de Secret dans la podspec.
- **Rejetée parce que** : incompatible avec le pré-chauffage, pour la même raison
  qui a motivé l'ADR-0018. Et à supposer le problème résolu, le credential serait
  présent avant le claim, ce que le §8.3 interdit explicitement.

### Option C — Les credentials portés par le client
La CLI transmet la clé d'accès avec la requête, comme elle transmet déjà les
variables du §5.4.
- **Rejetée parce que** : faire porter le credential d'accès par le client revient
  à laisser l'utilisateur choisir sa propre autorisation. C'est l'inverse exact de
  la leçon d'Atlantis du §5.4.2, où le serveur détient la politique et filtre la
  requête. Le §5.4 interdit d'ailleurs déjà toute valeur sensible dans le fichier
  projet.

### Option D — Un composant `key-inventory`, Secrets Kubernetes comme premier adaptateur ✅ (retenue)
Le cœur demande de quoi joindre des hôtes ; un adaptateur répond. La v1 répond
depuis un Secret Kubernetes, une version ultérieure depuis Vault.
- **Choisie parce que** : elle rend la v1 possible sans rien réimplémenter, et
  elle place la limitation là où elle est visible — dans un adaptateur nommé —
  plutôt que dispersée dans le code du claim.

## Décision

**Un rôle sortant `key-inventory`** au sens de l'ADR-0019, dont la question est
*avec quoi joindre ces hôtes en tant que cet utilisateur*.

### La signature est le cœur de la décision

```
Issue(user, env, hostScope) → (credential, expiry)
```

`Issue`, pas `Get` : une CA SSH ne retrouve pas une clé, elle en **émet** une.
`hostScope` et `expiry` ne servent à rien à l'adaptateur Kubernetes — il renvoie
la même clé et une expiration vide. Ce sont précisément les deux paramètres qui
rendront l'adaptateur Vault indolore, puisque les principals d'un certificat
dérivent du périmètre (§10.2) et que sa validité est bornée.

Même raisonnement que l'`artifact_pruned_at` de l'ADR-0013 : le paramètre inutile
aujourd'hui est gratuit, l'ajouter plus tard ne l'est pas.

### Chemin

Lecture par `wardend` **au moment du claim**, livraison au pod par le canal SSE
de l'ADR-0018. Jamais de montage, jamais de volume projeté. Le runner conserve
`automountServiceAccountToken: false` et n'a toujours aucun accès à l'API
Kubernetes : c'est le control plane qui lit le Secret, pas lui.

### Granularité

Elle relève de l'adaptateur, pas du cœur. Le cœur passe toujours `user` ; que la
v1 l'ignore et renvoie la clé de l'environnement est une décision locale et
réversible.

Ce qu'elle coûte est enregistré ici : **l'audit interne dira « alice », les logs
de l'hôte diront « ansible »**. La non-répudiation s'arrête à la frontière du
produit. Pour un produit d'audit, c'est une limite à documenter à l'intention des
utilisateurs, pas seulement des développeurs.

### Ce que la v1 ne fait pas

À lire sans atténuation, parce que c'est un recul par rapport à la spec :

- **Aucun certificat éphémère.** Le contrôle n°1 du §10.2 n'a pas
  d'implémentation. **L'autorisation réelle repose donc entièrement sur la
  NetworkPolicy egress** — le contrôle n°2, devenu le seul. Le périmètre d'un
  utilisateur est appliqué par le réseau ou il ne l'est pas.
- **Aucune expiration.** Une clé compromise reste valable jusqu'à rotation
  manuelle. Il n'y a pas de lease qui referme la fenêtre.
- **Aucun chiffrement au repos par défaut.** Un Secret est du base64 dans etcd ;
  k3s n'active pas le chiffrement sans configuration explicite. À vérifier sur le
  cluster avant d'y placer une clé de production.
- **`wardend` devient un point de compromission total.** Il reçoit `get` sur les
  Secrets de son namespace — et rien d'autre. Le compromettre donne toutes les
  clés, d'un coup, sans borne temporelle.

## Conséquences

- **Avantages** :
  - la v1 a un chemin d'accès aux hôtes sans réimplémenter ce que le §10.2
    interdit de réimplémenter ;
  - la limitation est nommée et localisée dans un adaptateur, au lieu d'être
    diffuse ;
  - la signature accueille Vault SSH CA sans toucher au cœur ni au runner ;
  - la distinction entre credentials d'accès et variables de playbook est posée,
    ce qui borne le magasin de secrets du serveur à une seule famille.

- **Contraintes** :
  - **Écart assumé au §7**, et il faut être précis sur sa nature : le Secret
    Kubernetes *est* long-vécu. Ce que la décision préserve, c'est l'intention —
    aucun credential long-vécu **dans le pod**, ni avant le claim. La lettre du
    §7 n'est pas tenue, et l'écart doit être reporté dans la spec.
  - Le §8.3 reste tenu, mais par le canal de l'ADR-0018, pas par un lease.
  - `wardend` acquiert une permission RBAC sur les Secrets, à surveiller comme
    telle : c'est la seule permission du produit dont l'abus est silencieux.

- **Dette technique éventuelle** :
  - **La rotation n'est pas outillée.** Sans expiration, la rotation est le seul
    mécanisme de révocation — c'est déjà la seule réponse aux expositions de
    l'ADR-0017, et cela fait maintenant deux décisions qui reposent sur une
    procédure qui n'existe pas. Elle doit être écrite.
  - L'adaptateur Vault n'existe pas ; tant qu'il n'existe pas, la signature
    `Issue` est une hypothèse non vérifiée. La première implémentation réelle
    dira si `hostScope` et `expiry` suffisent.
  - Le §10.2 doit être relu quand Vault arrivera : son ordre d'efficacité
    supposait le contrôle n°1 présent, ce qui n'est pas le cas.
