# ADR-0053 — Connexion Active Directory et droits par groupe

**Date** : 2026-10-28
**Statut** : Proposed
**Complète** : [ADR-0032](0032-comptes-locaux-et-sessions.md), [ADR-0033](0033-droits-par-environnement.md)

## Contexte

Les utilisateurs n'existent que dans warden : un compte local par personne,
créé par `wardend user add`, un mot de passe argon2id, et des droits nommés un
par un dans `grants.yaml`. L'ADR-0032 présentait ces comptes comme ce qui
PRÉCÈDE l'annuaire, et l'ADR-0033 réduisait le modèle du §10.2 à
`utilisateur → environnement` en attendant « l'annuaire qui porte les groupes ».

L'annuaire de l'organisation est un Active Directory on-prem. On veut :

- se connecter avec son compte AD, sans compte à créer dans warden ;
- donner les droits à des GROUPES AD, pas à des personnes une par une ;
- garder un accès quand l'AD est indisponible.

## Solutions envisagées

### Option A — OIDC devant l'AD (ADFS, Entra ID)
La cible du §10.1 : le serveur ne voit jamais le mot de passe, groupes dans un
claim.
- **Rejetée pour ce lot parce que** : l'organisation n'expose pas de fournisseur
  OIDC devant son AD on-prem. En monter un (ADFS, Keycloak fédéré) est un projet
  d'infrastructure à part, et la CLI devrait passer au device flow. Rien de ce
  qui suit ne l'empêche plus tard : un compte OIDC sera un `users.type = 'oidc'`
  comme un compte AD est un `users.type = 'ldap'`, et les droits par groupe
  ci-dessous ne savent pas d'où viennent les groupes.

### Option B — Groupes copiés en droits individuels par le contrôleur
Le contrôleur interroge l'AD périodiquement, résout chaque groupe en membres et
écrit des `environment_grant` par utilisateur.
- **Rejetée parce que** : il faut alors un compte de service qui énumère tout
  l'annuaire en boucle, et des utilisateurs provisionnés avant même leur
  première connexion. Surtout, `environment_grant` cesse d'être la trace de ce
  que `grants.yaml` a décidé : une ligne y dirait « alice a prod » alors que la
  décision était « le groupe GG-Warden-Prod a prod ».

### Option C — Bind LDAPS à la connexion, groupes figés dans la session ✅ (retenue)
`POST /auth/login` reste le même. Le serveur vérifie le mot de passe par un bind
LDAPS, lit les groupes de l'utilisateur, nestés compris, et les range AVEC la
session. `grants.yaml` accorde des environnements et l'administration à des
groupes.

## Décisions

### 1. Le compte local d'abord, l'annuaire sinon, sans repli

`core.Authenticator.Login` cherche d'abord un compte local ACTIF portant ce
login :

- **il existe** : seul son mot de passe local est vérifié. Un refus reste un
  refus, l'annuaire n'est pas consulté. Sinon, un compte local homonyme ne
  protégerait rien ;
- **il n'existe pas, ou il est désactivé** : l'annuaire répond, et lui seul. Un
  bind réussi provisionne l'utilisateur (`users.type = 'ldap'`) et ouvre une
  session avec ses groupes ;
- **l'AD est injoignable** : la connexion échoue avec une erreur 500 « annuaire
  indisponible ». On ne bascule pas automatiquement sur un compte local : une
  coupure réseau ne doit pas ouvrir une porte par des mots de passe que personne
  ne surveille.

Les comptes locaux restent donc, comme **bris de glace** : vérifiés avant l'AD,
ils fonctionnent même quand il est en panne. Ce sont quelques comptes au nom
distinct de tout compte AD (`breakglass-…`), à mot de passe long, gardés sous
scellé.

Faire passer une personne à l'AD, c'est **désactiver** son compte local
(`warden user disable --subject …`) : un compte désactivé compte comme absent, et son
login part vers l'AD. Les deux identités (`(local, alice)` et
`(ldap, <GUID>)`) restent distinctes, pour la même raison que dans l'ADR-0021 :
un rapprochement erroné dans un journal d'audit est pire qu'une absence de
rapprochement.

### 2. Le sujet d'un compte AD est son `objectGUID`

Pas le `sAMAccountName`. Un login se renomme et se réattribue ; l'ADR-0021 pose
que la clé naturelle est l'identifiant IMMUABLE du fournisseur. Le nom lisible
va dans `display_name`, rafraîchi à chaque connexion.

Conséquence : on n'accorde pas un droit à une personne AD par son nom dans
`grants.yaml`. On l'accorde à un groupe. Les entrées sans préfixe restent des
comptes LOCAUX, avec exactement la sémantique de l'ADR-0033.

### 3. Les groupes sont ceux de la session, pas ceux de l'utilisateur

Une table `session_group`, remplie à l'ouverture de la session et jamais
modifiée ensuite. Un droit de groupe vaut tant qu'une session VIVANTE de
l'utilisateur porte ce groupe.

C'est ce qui borne la prise d'effet d'un retrait de groupe dans l'AD (§10.3)
sans interroger l'annuaire à chaque requête. Pour que la borne soit acceptable,
**une session AD dure 10 heures**, contre 30 jours pour un compte local
(`WARDEN_LDAP_SESSION_TTL_HOURS`). Un départ ou un changement d'équipe prend
effet au plus tard le lendemain matin, et `warden logout` ou la désactivation
du compte le font tout de suite.

Ce choix vaut aussi à la LIVRAISON de l'ordre (ADR-0033, décision 3). Un run
enregistré dont l'auteur s'est déconnecté, ou dont la session a expiré, ne
reçoit pas la clé que seul son groupe lui donnait.

### 4. `grants.yaml` accepte des groupes, et une clé `admins`

```yaml
environments:
  prod:
    - group:GG-Warden-Prod
    - breakglass-ops        # compte local, comme avant
  staging:
    - group:GG-Warden-Dev
admins:
  - group:GG-Warden-Admin
```

- Le préfixe `group:` désigne un groupe AD par son `sAMAccountName`, comparé sans
  tenir compte de la casse, comme le fait l'AD.
- Un groupe n'a pas besoin d'exister côté warden : il n'y a rien à provisionner.
  Contrairement à un compte local inconnu, il ne fait donc pas échouer le
  document. Une coquille dans un nom de groupe donne un droit que personne
  n'exerce : c'est l'échec du bon côté, et il se voit dès le premier run refusé.
- `admins` ne prend que des groupes. L'administration d'un compte local reste
  posée par `wardend user add --admin`.
- Les règles de l'ADR-0033 s'appliquent telles quelles. La portée est
  l'environnement. Une clé `admins` ABSENTE ne touche à rien ; une liste vide
  retire l'administration à tous les groupes. La révocation est datée,
  l'application se fait tout ou rien, dans la même transaction que les droits
  individuels.

### 5. `is_admin` d'un compte AD est calculé par le cœur, jamais stocké

`users.is_admin` reste le drapeau du compte, et vaut `false` pour un compte
`ldap`. La session porte ses groupes. Le cœur (`core.EstAdministrateurParGroupe`)
les croise avec les groupes vivants d'`admin_group`, au login comme à chaque
requête. La règle n'est donc écrite qu'à un endroit, et la réponse de
`POST /auth/login` dit la même chose que `whoami`. Rien n'est à resynchroniser
quand `admins` change : le retrait d'un groupe prend effet à la requête
suivante.

### 6. Ce que l'adaptateur LDAP refuse

- **Mot de passe vide refusé avant tout bind.** Un bind simple avec un DN et un
  mot de passe vide est un bind NON AUTHENTIFIÉ que l'AD accepte (RFC 4513 §5.1.2).
  Sans ce garde-fou, n'importe quel login connu ouvre une session.
- **LDAPS ou StartTLS obligatoire**, certificat vérifié. Une URL `ldap://` sans
  StartTLS est refusée au démarrage : le mot de passe de l'utilisateur
  traverserait le réseau en clair.
- **Le login est échappé** (`ldap.EscapeFilter`) avant d'entrer dans le filtre de
  recherche.
- **Le compte de service ne sert qu'à CHERCHER** le DN de l'utilisateur et ses
  groupes. Le bind de l'utilisateur se fait sur une connexion à part, pour
  qu'aucune opération ne s'exécute jamais avec l'identité de l'utilisateur sur
  une connexion réutilisée.
- **Groupes imbriqués** résolus par l'AD lui-même
  (`member:1.2.840.113556.1.4.1941:=<DN>`), en une requête.

### 7. Le frein de l'ADR-0032 couvre aussi l'annuaire

La file de quatre tentatives simultanées et le plancher de 250 ms entourent le
bind LDAP comme la dérivation argon2. Sans eux, `/auth/login` deviendrait un
relais pour marteler l'AD, et les verrouillages de compte de l'AD se
retourneraient contre les utilisateurs nommés.

## Livraison et retour arrière

L'API tourne à plusieurs réplicas et se met à jour en rolling update : pendant
la bascule, l'ancienne et la nouvelle version servent ensemble. Un ancien pod ne
connaît ni les groupes ni l'administration par groupe, et refuse donc, par
intermittence, ce que le nouveau accorde. D'où **trois temps** :

1. **Livrer le code et la migration `00016` avec `api.ldap.enabled: false`.** La
   migration n'ajoute que des tables, l'ancienne version tourne dessus sans les
   voir.
2. **Préparer l'AD**, une fois tous les pods à jour : le Secret du compte de
   service (`api.ldap.bindPasswordSecret`), le ConfigMap de l'autorité des
   contrôleurs de domaine (`api.ldap.caConfigMap`), et le flux réseau du rôle
   `api` vers les contrôleurs de domaine (636, ou 389 avec StartTLS).
3. **Activer** `api.ldap.enabled`, puis poser les entrées `group:` et `admins`
   dans `grants`.

**Retour arrière.** Un simple redéploiement de la version précédente, mais dans
cet ordre :

- **retirer d'abord les entrées `group:` et la clé `admins`** de `grants`, et
  attendre qu'elles soient réconciliées. L'ancien contrôleur lit `group:…` comme
  un compte local inconnu, et un compte inconnu fait refuser le document ENTIER
  (ADR-0033) : plus aucun droit ne serait appliqué ni révoqué ;
- les comptes AD perdent alors tout accès, et ne peuvent plus se connecter : il
  ne reste que les comptes locaux de bris de glace ;
- **ne pas jouer le `Down` de la `00016` en production.** Il supprime
  l'historique des droits de groupe, qui est une trace d'audit. Les tables
  restent, ignorées par l'ancien code.

## Conséquences

- **Avantages** : plus de compte à créer par personne. Les droits se décrivent
  par équipe dans `grants.yaml`, et un départ se traite dans l'AD, sans commit
  côté warden. La CLI et l'interface web ne changent pas : même formulaire,
  même `warden login`.
- **Contraintes** :
  - le serveur voit passer le mot de passe AD, ce que l'option A aurait évité ;
  - une session AD dure 10 heures, donc il faut se reconnecter chaque jour ;
  - le rôle `api` a besoin d'un compte de service AD en lecture et d'un flux
    réseau vers les contrôleurs de domaine.
- **Ce que ça ne fait pas** :
  - pas de droit nominatif pour un compte AD (décision 2). Il faut passer par
    un groupe ;
  - pas de relecture des groupes en cours de session (décision 3). La borne
    est la durée de session ;
  - `warden grant list` montre les droits de groupe, mais pas qui en est membre
    en ce moment. Cette question se pose à l'AD ;
  - le dialogue avec l'AD n'est couvert par aucun test automatisé (filtre,
    refus contre panne, groupes imbriqués, StartTLS) : un OpenLDAP de test ne
    reproduit pas la règle de groupes imbriqués d'AD. Il se vérifie sur l'AD
    réel, à la mise en service ;
  - le périmètre réseau du §10.2 n'existe toujours pas. Un groupe donne la clé
    d'un environnement, pas plus que ne le faisait un droit individuel.
