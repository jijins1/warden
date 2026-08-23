# ADR-0033 — Droits par environnement : la moitié de l'autorisation qui ne dépend pas du réseau

**Date** : 2026-08-23
**Statut** : Accepted
**Complète** : [ADR-0020](0020-key-inventory-credentials-dacces.md), [ADR-0030](0030-enrolement-des-machines.md), [ADR-0032](0032-comptes-locaux-et-sessions.md)

## Contexte

L'ADR-0032 a donné au produit une identité vérifiée. Elle n'a rien fermé :
`POST /runs` lisait `payload.Environment`, l'écrivait tel quel et lançait. **Tout
compte authentifié pouvait lancer un run sur `prod`**, et le control plane lui
livrait alors la clé SSH de `prod` dans son pod. La `Policy` n'avait que cinq
méthodes, toutes en lecture, et aucune n'était consultée sur ce chemin.

Cette table avait été écartée du lot de l'ADR-0030, avec cet argument : le §10.2
classe le filtrage côté serveur en « confort, pas sécurité », donc la livrer sans
application par le réseau donnerait un contrôle qui ressemble à un contrôle.

**Cet argument était trop court, et c'est ce qui motive cet ADR.** La clé d'accès
est délivrée PAR ENVIRONNEMENT (ADR-0020). Un filtrage serveur empêche donc
réellement d'obtenir un credential — ce n'est pas du confort. Ce qu'il n'empêche
pas, c'est qu'un run légitime de `staging` sorte du périmètre de `staging`. C'est
une couche partielle, pas illusoire, et la nommer exactement est tout l'objet de
ce document.

## Décisions

### 1. Ce que ce contrôle achète, et ce qu'il n'achète pas

À écrire partout où il apparaît — schéma, message de la CLI, README — parce
qu'un contrôle dont on surestime la portée est plus dangereux que pas de contrôle.

| | |
|---|---|
| **ARRÊTE** | Obtenir la clé d'un environnement auquel on n'a pas droit. Sans droit, aucun credential, donc aucune machine joignable. |
| **N'ARRÊTE PAS** | Qu'un run légitime de `staging` joigne une machine de `prod`. L'egress du runner est ouvert (ADR-0030), et le §10.2 pose que « le périmètre d'un utilisateur est appliqué par le réseau ou il ne l'est pas ». |

### 2. Le modèle est réduit à `utilisateur → environnement`

Le §10.2 décrit `(utilisateur|groupe) → environnement → scope d'hôtes → mode`.

Les **groupes** attendent l'annuaire qui les porte — même raisonnement que pour
`is_admin`. Le **scope d'hôtes** attend le réseau qui l'appliquerait : sans lui,
il serait purement déclaratif, et c'est exactement ce qu'on refuse de livrer. Le
**mode** appartient à la phase 5 (DEV/PROD, §3).

### 3. Le contrôle est fait DEUX FOIS

- à la **création** du run (`POST /runs`), pour refuser tôt, avec un 403 qui
  nomme l'environnement — et avant tout effet : ni miroir rafraîchi, ni mot de
  passe d'élévation consigné ;
- à la **livraison** de l'ordre (`OrderBuilder.cleDAcces`), parce que c'est là que
  la clé sort.

Le second n'est pas une redondance : **c'est lui qui donne un sens à la
révocation**. Sans lui, retirer un droit ne ferait rien aux runs déjà
enregistrés, alors que le §10.3 exige qu'un changement de droits prenne effet. Un
run dont le pod n'a pas encore reçu sa clé ne l'obtiendra pas.

### 4. Le refus est le DÉFAUT

`VerifierLAcces` refuse quand le magasin de droits est absent, et refuse quand sa
lecture échoue. Un contrôle d'autorisation qui s'efface avec sa dépendance n'est
pas un contrôle, et un déploiement mal assemblé doit se voir tout de suite plutôt
que de tourner ouvert pendant des mois.

La conséquence s'est vue immédiatement : quatre tests existants d'`OrderBuilder`
et cinq de `POST /runs` sont passés au rouge dès que le port a été déclaré. C'est
le défaut qui fait son travail.

Une panne de lecture rend une erreur **distincte** du refus de droit :
l'exploitant doit pouvoir séparer « pas autorisé » de « on n'a pas pu savoir ».

### 5. Un environnement VIDE est autorisé

Et ce n'est pas un trou. Un run sans environnement ne reçoit aucune clé —
`ValidateEnvironment` refuse la chaîne vide bien avant le magasin — donc il n'y a
aucun credential à autoriser. Sans clé, le pod ne joint aucune machine.

Le refuser casserait au contraire les playbooks en `connection: local`, qui n'ont
jamais eu besoin d'environnement et sont le cas de tous les runs du produit
jusqu'ici. La première version de ce contrôle les refusait ; les tests existants
l'ont montré.

### 6. Aucun raccourci pour les administrateurs

`is_admin` ne donne PAS implicitement accès à tous les environnements. Un
administrateur qui veut lancer sur `prod` s'accorde le droit, et cela laisse une
ligne datée et signée.

Sans cette règle, « qui pouvait atteindre `prod` » se répondrait sur deux
sources — cette table, plus la liste des administrateurs — et une question
d'audit à deux sources est une question à laquelle personne ne répond.

### 7. La révocation est DATÉE, jamais un DELETE

« Qui avait accès à `prod` le 3 mars » est une question d'audit, et une ligne
supprimée ne peut pas y répondre. Même règle que `users.disabled_at` et
`session.revoked_at`.

L'unicité porte donc sur un index **partiel** (`WHERE revoked_at IS NULL`) :
réaccorder après une révocation doit marcher, et une contrainte sur la paire nue
l'interdirait — il faudrait alors supprimer la ligne, donc perdre l'histoire.

### 8. La migration REPREND l'historique

Sans cela, elle coupe tout le monde : la table naît vide, et le premier run de
chaque utilisateur échoue sur un droit qui n'a jamais existé. Une migration qui
casse silencieusement tous ses utilisateurs est une mauvaise migration, quel que
soit le bien-fondé du contrôle qu'elle ajoute.

Chaque couple `(utilisateur, environnement)` présent dans la table `run` reçoit un
droit, daté du premier run, avec `granted_by` NULL. Ces lignes disent exactement
« ceci était déjà possible » — **pas** « ceci aurait dû l'être ». La CLI les
affiche « (historique) » pour que la distinction reste lisible.

Vérifié sur le cluster : 22 runs, un seul couple, un seul droit créé, daté du
2026-08-16.

### 9. La source des droits est un fichier de CONFIGURATION, pas une commande

Un ConfigMap versionné dans le dépôt de déploiement, monté dans le pod, et
réconcilié par le rôle `controller` :

```yaml
environments:
  prod:
    - alice
    - bob
  staging: []        # explicitement personne
```

**Aucune commande ne modifie les droits.** `warden grant list` les affiche ;
il n'existe ni `grant add`, ni `grant revoke`, ni `grant apply`, et aucune route
d'écriture côté API. La seule façon d'en changer est de committer.

Une première version passait par `warden grant apply -f`, poussé par un
pipeline. C'était un pas de trop : le cluster est déjà piloté par un outil
GitOps, et lui ajouter une étape de CI dupliquait ce qu'il fait déjà. Surtout,
garder une API d'écriture à côté du fichier ferait DEUX sources de vérité, dont
l'une serait silencieusement écrasée au passage suivant — un droit qu'on croit
avoir accordé et qui disparaît une minute plus tard est pire qu'une commande
absente.

**Le CONTRÔLEUR réconcilie, pas l'API.** C'est le rôle dont le métier est
justement de faire correspondre la base à ce que le monde extérieur dit — ici un
fichier plutôt qu'un cluster — et il tourne à un seul réplica. Dans `api`, qui
peut en avoir N, ce seraient N réconciliateurs sur le même document. Le rôle
`api` lit les droits en base, où le contrôleur les a projetés.

La réconciliation est adossée au passage existant (30 s) plutôt qu'à son propre
minuteur : le rythme est le même, et un second minuteur n'achèterait qu'un second
endroit où se tromper d'intervalle.

**DESTRUCTIF par construction.** Pour chaque environnement que le document nomme,
la liste fait autorité : un compte retiré du fichier est RÉVOQUÉ. Sans cela, le
versionnement ne servirait qu'à consigner ce qu'on a ajouté — or l'essentiel de
sa valeur est que retirer une ligne retire un accès, visiblement, dans une revue.

Quatre garde-fous, parce qu'un mécanisme qui peut retirer l'accès à la production
doit rater du bon côté :

1. **Fichier ABSENT = ne rien faire.** Le montage est `optional: true`, et un
   contrôleur démarré avant que le ConfigMap existe ne doit pas conclure
   « personne n'a de droit » et tout révoquer. C'est la distinction la plus
   importante du lecteur : absent n'est pas vide.
2. **Document VIDE = refusé**, et journalisé en erreur. « Ne rien vouloir » et
   « n'avoir rien pu lire » se ressemblent trop. Une liste vide sur un
   environnement nommé, elle, est un « personne » explicite et révoque.
3. **La portée est l'ENVIRONNEMENT, pas le document.** Un environnement absent
   n'est pas touché : les droits se découpent en plusieurs ConfigMaps, et un
   fichier tronqué laisse intacts ceux qu'il a perdus. La contrepartie est
   réelle — supprimer un bloc entier ne révoque rien, c'est le problème classique
   du « prune » — et `warden grant list` reste la seule vue complète.
4. **TOUT OU RIEN**, en une transaction, révocations d'abord. Un compte inconnu
   fait échouer le document entier : sans cela, une coquille retirerait des accès
   sans en donner aucun.

Le changement est détecté sur le CONTENU et non sur l'horodatage : un ConfigMap
monté est republié par le kubelet à chaque resynchronisation, et se fier à la
date ferait rejouer une réconciliation par minute pour rien.

Ni une erreur de lecture ni un échec d'application ne remontent dans la boucle :
elles sont journalisées et le passage suivant réessaie. Faire échouer la boucle
sur un YAML cassé arrêterait aussi la réconciliation des pods, qui n'a rien à
voir — et laisserait des runs en vol.

`granted_by` est NULL pour ces droits : personne ne les a accordés à la main.
L'auteur du changement est dans l'historique git, pas en base.

## Conséquences

**Vérifié sur le cluster réel**, dans l'ordre :

1. le droit repris de l'historique laisse passer le run — aucune rupture ;
2. `--env staging`, sans droit : **403**, avant tout effet ;
3. `warden grant revoke`, puis run sur `prod` : **403** ;
4. `warden grant add`, puis run sur `prod` : `succeeded` ;
5. la liste montre les deux lignes, dont la révoquée.

Puis, pour le chemin déclaratif, en modifiant le ConfigMap comme le ferait un
commit synchronisé :

6. ajouter `bob` au document : accordé **38 secondes** après le `apply` ;
7. le retirer du document : **révoqué**, 48 secondes après ;
8. `staging`, que le document ne nomme pas, reste intact tout du long ;
9. un document vide : refusé et journalisé, `ruokki` **garde** `prod` ;
10. la commande d'écriture n'existe plus : `warden grant add` répond
    « flag provided but not defined ».

**Ce que ça ne fait pas** :

- **le périmètre réseau n'existe toujours pas.** C'est le contrôle que le §10.2
  désigne, et il reste le trou principal du produit ;
- **aucune purge des pods chauds** au retrait d'un droit (§10.3). Le
  pré-chauffage n'existe pas encore, donc la question ne se pose pas — mais elle
  se posera avec lui, et cet ADR est l'endroit qui le rappelle ;
- **aucune expiration** d'un droit. Un accès accordé pour une intervention reste
  jusqu'à révocation explicite.
