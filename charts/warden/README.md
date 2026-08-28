# Chart warden

Déploie le control plane : les rôles `api`, `git` et `controller` d'un même
binaire (ADR-0006), plus les migrations en hook de pré-installation.

```sh
helm install warden charts/warden -n warden-dev --create-namespace \
  --set ingress.api.host=warden.example.com \
  --set ingress.git.host=warden-git.example.com
```

## Avant d'installer : le secret partagé

La chart ne crée **aucun** Secret. Un secret dans un dépôt git est un secret
publié, et cette règle ne souffre pas d'exception pour un « exemple ».

```sh
kubectl create secret generic warden-internal -n warden-dev \
  --from-literal=internal-token="$(openssl rand -base64 32)" \
  --from-literal=forge-token="<jeton de lecture de la forge>"
```

| Clé | À quoi elle sert |
|---|---|
| `internal-token` | Authentifie l'API interne **et** signe les jetons de push et de lecture (ADR-0024, ADR-0028). Qui le détient peut frapper une portée arbitraire : son rayon de souffle est le rôle `git` entier. 256 bits d'aléa. |
| `forge-token` | Lecture sur la forge, pour rafraîchir le miroir de la branche protégée (ADR-0028). Un seul credential pour tout le déploiement en v1 ; un dépôt privé par équipe en exigera un par dépôt. |

## Le premier administrateur

Personne ne naît administrateur, et aucun endpoint n'accorde ce droit — sans
quoi la première personne à se connecter sur une instance neuve posséderait la
configuration de toute l'organisation.

```sh
kubectl exec -n warden-dev deploy/warden-api -- \
  wardend user add --subject alice --admin
```

Son autorisation est l'accès à la base, pas un mot de passe : c'est la seule qui
existe avant qu'un compte existe. Le mot de passe n'est jamais accepté en
argument ; la commande l'affiche une fois, ou le demande sur le terminal.

## Ce que la chart ne rend PAS configurable

Une valeur de chart est une chose qu'un déploiement peut se tromper à régler. En
ajouter une sur un contrôle de sécurité, c'est ajouter une façon de le désactiver
en silence — et personne ne relit un `values.yaml` pour vérifier qu'un contrôle
est toujours actif.

Ne sont donc pas des valeurs : les règles RBAC, les `securityContext`, la
séparation par port du rôle `git` (8081/8082/8083), `ingress: []` sur les pods
runner, et les stratégies `Recreate` à réplica unique de `git` et du contrôleur.
Le détail et les raisons sont en tête de `values.yaml`.

## Le fichier qui compte le plus

`templates/networkpolicy.yaml`. Il porte un contrôle que le code **ne peut pas**
porter : le §2 pose qu'un playbook arbitraire équivaut à une exécution de code
dans le pod, donc ce qui borne ce que ce code peut joindre n'est pas le binaire
du runner — qu'un playbook peut ignorer — mais la politique réseau qui l'entoure.

**Vérifier que le CNI l'applique réellement.** Sur un cluster où les
NetworkPolicy sont acceptées mais ignorées, tout fonctionnera à l'identique et
rien ne sera confiné.

## Trous connus, et assumés

- **Aucun périmètre réseau.** L'egress des runners est **ouvert** : un playbook
  joint n'importe quelle adresse sur n'importe quel port, avec la clé de son
  environnement. L'ADR-0020 pose que cette politique EST l'autorisation de la
  v1 ; il n'en reste donc aucune. Les droits par environnement (ADR-0033)
  empêchent d'**obtenir** une clé, ce qui est réel — mais pas qu'un run légitime
  de `staging` joigne une machine de `prod`. À reprendre avant toute production.
- **Un seul credential de forge** pour tout le déploiement (ADR-0020, v1).
- **`imagePullSecrets` absent.** Si le registre est privé, l'ajouter aux
  ServiceAccounts `warden-api` et `warden-git` — et au pod runner, dont la
  podspec est construite par le code et non par la chart.
- **`api.replicas` > 1 n'a jamais été vérifié.** Le canal de dispatch (ADR-0018)
  est tenu par un pod, et rien ne garantit qu'un runner atteigne celui qui a
  construit son ordre.

## Les rôles PostgreSQL

Les trois rôles de l'ADR-0008 sont déclarés à l'opérateur CNPG, pas créés par la
migration. Celle-ci sait les créer, mais l'utilisateur applicatif n'a pas
`CREATEROLE` — et le lui donner serait pire que le problème : dans PostgreSQL,
`CREATEROLE` permet de se doter de droits qu'on n'a pas.

Sur une base qui n'est **pas** gérée par CNPG (`postgres.enabled=false`), les
créer à la main avec un rôle qui en a l'autorité :

```sql
CREATE ROLE warden_ingest NOLOGIN;
CREATE ROLE warden_reader NOLOGIN;
CREATE ROLE warden_diff_reader NOLOGIN;
```

Le bloc de la migration 00001 est idempotent : les rôles existants sont sautés.

## Ce que l'exploitation doit savoir

- Le rôle `git` est en **instance unique**, PVC RWO, stratégie `Recreate`
  (ADR-0006). Pendant son redéploiement, aucun run ne démarre. Point de
  défaillance unique assumé, que l'ADR-0027 a élargi à la disponibilité de la
  forge.
- Le `fsGroup: 65532` du rôle `git` n'est pas décoratif. Sans lui, git refuse
  d'ouvrir les dépôts du volume — « dubious ownership » — et le symptôme est un
  HTTP 500 sur chaque push, dont la cause n'apparaît que dans les journaux du
  pod.
- Le PVC des dépôts porte `helm.sh/resource-policy: keep`. C'est le magasin
  d'audit : un `helm uninstall` ne l'emporte pas.
- Les migrations tournent en hook `pre-install,pre-upgrade`, donc **avant** que
  le nouveau binaire démarre. ArgoCD traduit ces annotations en PreSync.
- Le tag d'image par défaut est mutable (`dev`) et la politique de tirage est
  `IfNotPresent`. Republier le même tag ne redéploiera donc pas l'image :
  poser `image.pullPolicy=Always`, ou utiliser des tags immuables.

## Reprendre un déploiement existant

Les noms de ressources et les étiquettes de sélection sont **identiques** à ceux
des anciens manifestes de `deploy/` : la chart reprend le déploiement en place,
sans détruire le volume des dépôts. Vérifié par un `kubectl apply
--dry-run=server`, qui n'a signalé aucun champ immuable.

Avec Helm en direct, l'adoption d'objets créés hors release demande
`--take-ownership` (Helm ≥ 3.17) ou une annotation manuelle. Avec ArgoCD, faire
pointer l'Application sur `charts/warden` suffit.
