# Déploiement

Manifestes du control plane. Ordre d'application implicite par la numérotation.

```sh
kubectl apply -f deploy/00-namespace.yaml
kubectl create secret generic warden-internal -n warden \
  --from-literal=internal-token="$(openssl rand -base64 32)" \
  --from-literal=forge-token="<jeton de lecture Gitea>"
kubectl apply -f deploy/
```

## Ce que ce déploiement N'EST PAS

Il porte `WARDEN_INSECURE_DEV_AUTH=1`. Le produit n'a **aucune authentification
réelle** — OIDC et mTLS restent à écrire — et sans cet opt-in son middleware
refuse toute requête, ce qui est la bonne posture par défaut : un produit d'audit
qui accepterait « je suis alice » en silence serait pire qu'un produit qui refuse
de répondre.

Le poser vaut accepter que **n'importe quel appelant se déclare n'importe qui**.
Acceptable pour un déploiement `dev` sur un réseau de confiance. Nulle part
ailleurs.

## Le fichier qui compte le plus

`60-networkpolicy.yaml`. Il porte un contrôle que le code **ne peut pas** porter :
le §2 pose qu'un playbook arbitraire équivaut à une exécution de code dans le pod,
donc ce qui borne ce que ce code peut joindre n'est pas le binaire du runner —
qu'un playbook peut ignorer — mais la politique réseau qui l'entoure.

L'ADR-0020 le dit sans détour : faute de gestionnaire de secrets, en v1
« l'autorisation réelle repose entièrement sur la NetworkPolicy egress ». Ne pas
l'appliquer ne dégrade pas le produit, cela **supprime son contrôle d'accès**.

Vérifier que le CNI l'applique réellement : sur un cluster où les NetworkPolicy
sont acceptées mais ignorées, tout fonctionnera à l'identique et rien ne sera
confiné.

## Trous connus, et assumés

- **Aucun accès aux hôtes gérés.** L'egress des runners ne comporte pas le
  périmètre d'hôtes du §10.2, donc aucun playbook ne peut s'exécuter contre quoi
  que ce soit. C'est délibéré : une règle large — « tout le réseau privé » —
  donnerait à un playbook arbitraire l'accès de l'utilisateur le plus privilégié.
  La règle doit être posée PAR RUN, à partir du périmètre de son utilisateur, et
  rien ne le calcule encore.
- **Pas d'Ingress.** Le port d'écriture du rôle `git` et l'API doivent être
  joignables depuis les postes des développeurs ; comment ils entrent est une
  décision de site (hôte, TLS, authentification du reverse proxy).
- **Un seul credential de forge** pour tout le déploiement (ADR-0020, v1).
- **`imagePullSecrets` absent.** Si le registre Gitea est privé, l'ajouter aux
  ServiceAccounts `warden-api` et `warden-git` — et au pod runner, dont la
  podspec est construite par le code.

## Ce que l'exploitation doit savoir

- Le rôle `git` est en **instance unique** avec un PVC RWO et une stratégie
  `Recreate` (ADR-0006). Pendant son redéploiement, aucun run ne démarre. C'est un
  point de défaillance unique assumé, et l'ADR-0027 l'a élargi : il couvre
  désormais la disponibilité de la forge.
- Le `fsGroup: 65532` du rôle `git` n'est pas décoratif. Sans lui, git refuse
  d'ouvrir les dépôts du volume — « dubious ownership » — et le symptôme est un
  HTTP 500 sur chaque push, dont la cause n'apparaît que dans les journaux du pod.
- Le **premier administrateur se pose à la main**, en SQL : personne ne naît
  administrateur et aucun endpoint n'accorde ce droit (migration 00005).

  ```sql
  UPDATE users SET is_admin = true WHERE subject = '<sujet>';
  ```
