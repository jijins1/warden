# ADR-0028 — Le jeton de push porte sa propre portée, et le serveur écrit le miroir

**Date** : 2026-08-14
**Statut** : Accepted
**Complète** : [ADR-0027](0027-ecriture-du-role-git.md), [ADR-0024](0024-api-interne-du-role-git.md)

## Contexte

Deux ADR de suite ont laissé la même question ouverte, faute d'être bloquées par
elle. L'ADR-0026 a livré la lecture contre un port `Authorizer` sans
implémentation ; l'ADR-0027 a livré l'écriture contre un `PushAuthorizer` sans
implémentation. Aucune des deux n'a pu être câblée, et la raison est la même :

> Où vit l'état d'autorisation ?

Le rôle `git` **ne parle pas à PostgreSQL**, et son `doc.go` le pose comme
délibéré. La portée d'un push vient pourtant du rôle `api`, seul détenteur de la
politique d'accès. Il faut donc traverser cet écart, et la façon de le traverser
détermine ce que `gitd` doit conserver.

L'ADR-0027 a par ailleurs laissé une dette bloquante :

> Le chemin d'écriture ne doit pas être câblé tant que le miroir de la principale
> n'est pas alimenté par le serveur.

Cette ADR tranche les deux.

## Solutions envisagées — l'état d'autorisation

### Option A — `api` provisionne `gitd` à la réservation
L'API interne gagne un endpoint d'octroi ; `gitd` garde les portées en mémoire et
sur son PVC.
- **Avantage** : révocation possible avant expiration.
- **Rejetée parce que** : elle ajoute un état à un composant en **instance
  unique**, avec tout ce qu'un état traîne — cycle de vie, purge, et des runs en
  vol qui perdent leur autorisation au moindre redéploiement. L'ADR-0006 rappelle
  que ce rôle est un point de défaillance unique assumé ; lui donner de l'état
  mutable élargit ce que sa perte coûte.

### Option B — `gitd` lit PostgreSQL
- **Rejetée parce que** : elle contredit frontalement l'ADR-0024, le `doc.go` du
  paquet, et fait sortir tout `gitd` de l'étage 1 de l'ADR-0010. Le bénéfice ne
  vaut pas la révocation de trois positions tenues.

### Option C — Le jeton porte sa portée, signée ✅ (retenue)
`api` frappe un jeton qui **contient** la portée — dépôt, préfixe de ref,
expiration — et la signe en HMAC-SHA256 avec le secret déjà partagé par les deux
rôles (ADR-0024). `gitd` vérifie la signature et lit la portée.
- **Choisie parce que** : elle **supprime l'état** au lieu de le déplacer. Rien à
  stocker, rien à synchroniser, rien à purger, rien à perdre au redémarrage, et
  `gitd` reste sans base. Le secret existe déjà : aucun composant nouveau.

## Amendement du même jour — la portée est un PRÉFIXE, pas une ref exacte

Le premier jet scopait le jeton à UNE ref exacte, ce qui imposait à la CLI de
demander son identifiant de run au serveur avant de pousser. Une question a
suffi à défaire ce raisonnement : à quoi sert cet aller-retour ?

Il ne servait qu'à faire attribuer le ULID par le serveur. En scopant le jeton à
`refs/audit/<utilisateur>/`, la CLI tire le sien, et disparaissent d'un coup :

- l'endpoint de réservation ;
- la création de run en deux temps en base ;
- l'amendement du §5.2, dont la séquence « push d'audit puis `POST /runs` »
  redevient valable telle quelle.

**Ce que le relâchement coûte, sans l'enrober** : un jeton rejoué permet de créer
des refs arbitraires dans le namespace d'audit de son porteur. Il ne permet
toujours ni de lire ni d'écraser celui d'autrui — le contrôle de fraîcheur,
l'écriture unique et le masquage des refs tiennent tous trois sans changement. Une
ref d'audit sans ligne de run est inerte, et l'était déjà.

La barre finale du préfixe est obligatoire et vérifiée aux deux bouts :
`refs/audit/bob` couvrirait aussi `refs/audit/bobby/`.

**Le champ `Mainline` disparaît du jeton.** C'est une propriété du dépôt et non
de l'octroi : elle ne varie pas d'un push à l'autre, et la porter dans chaque
jeton donnait deux sources de vérité au même fait. Le rafraîchissement du miroir
l'écrit désormais dans `refs/warden/mainline`, que le hook lit — un chemin
serveur-vers-serveur, sans passage par le client, donc à propriété de sécurité
identique.

### Ce que cet amendement a fait découvrir

Le raisonnement qui justifiait de retirer l'ancre du jeton était : « une ancre
absente exclut moins d'objets, donc rend le contrôle plus strict, et un pousseur
honnête détient de toute façon son propre historique ». **La seconde moitié est
fausse**, et un test l'a montré immédiatement.

Git NÉGOCIE : un client ne renvoie pas les objets que le serveur a annoncé
détenir. Une ref annoncée mais non exclue ressemble donc, pour le contrôle, à un
objet revendiqué sans être transmis — et le push honnête est refusé avec un
message parlant de vol.

**L'invariant exact** : l'ensemble exclu doit coïncider avec ce que
`receive.hideRefs` laisse visible. Ni plus — ce serait un trou — ni moins — ce
serait un refus injustifié. Trois familles : le miroir des branches protégées, le
pointeur de principale, et le namespace d'audit du pousseur lui-même. C'est le
risque de divergence que le plan initial avait classé « élevé » et que j'avais
cru supprimer en simplifiant l'ancre ; il est épinglé par
`TestBuildingOnAnythingVisibleIsAccepted`.

## Décision

### 1. La portée voyage dans le jeton, signée

Format : `wpg1.<base64url(corps)>.<base64url(HMAC)>`, le corps portant
`{dépôt, préfixe de ref, expiration}`.

Quatre points de conception, chacun couvert par un test :

- **La signature est vérifiée AVANT le décodage du corps.** Décoder d'abord
  ferait passer du JSON non authentifié par l'analyseur, surface offerte
  gratuitement à un appelant hostile.
- **Comparaison en temps constant.** Un `==` sur des octets s'arrête au premier
  écart, et la durée du refus décrit alors combien de préfixe était juste.
- **Séparation de domaine.** La version entre dans le calcul de la signature, pas
  seulement dans le préfixe. Le même secret authentifie l'API interne ; sans
  séparation, une chaîne valide pour l'un pourrait un jour être présentée à
  l'autre, et un futur format v2 au même corps aurait la même signature — donc
  serait rétrogradable.
- **Un seul motif d'échec.** Ni « signature fausse », ni « expiré », ni « corps
  illisible ». `gitd` est joignable depuis du domaine hostile, et chaque
  distinction dit au sondeur laquelle de ses hypothèses est la bonne.

**Authentique n'est pas bien formé.** Une signature prouve l'origine, pas la
validité : `gitd` revalide le préfixe en lui adjoignant une feuille plausible et
en vérifiant le tout par `git.NewRef` — valider le préfixe seul manquerait
l'essentiel, puisqu'il finit par une barre et n'est donc pas un nom de ref, alors
que les règles qui rendent une ref sûre portent sur des noms complets. Le contrat de fil ne peut pas le faire — `gitwire` n'a le droit
d'importer aucun paquet du produit, et c'est ce qui le tient à l'état de contrat.

### 2. Aucune révocation avant expiration, et pourquoi c'est tenable

C'est la contrepartie de l'option C, et elle est assumée plutôt que minimisée.

Ce qui la rend acceptable n'est pas un pari mais deux propriétés **déjà imposées
ailleurs** : la portée est bornée au namespace d'audit de son porteur, et une ref
d'audit est écrite une fois (ADR-0027). Un jeton rejoué ne peut donc ni lire ni
écraser le travail d'autrui ; il peut seulement créer des refs inertes chez lui.

### 2 bis. L'option écartée qui manquait à cette ADR

Faire transiter le push **par le rôle `api`**, en simple tuyau d'octets : `api`
authentifie, connaît la ref, et injecte la portée en en-têtes vers `gitd`, qui lui
fait confiance parce que le port ne lui est joignable que par lui. Aucune
cryptographie, aucun format, aucune expiration.

- **Rejetée parce que** : la simplification du modèle git faite en amont a
  transformé le push d'historique complet en CAS NORMAL. Chaque run ferait donc
  transiter un pack potentiellement énorme par un tier répliqué et dimensionné
  pour du JSON, avant d'atteindre le composant qui détient le disque. Le jeton
  existe pour que ces octets aillent en ligne droite.

### 3. Le secret partagé devient une clé de signature

Élargissement réel, à écrire plutôt qu'à découvrir. Qui détient ce secret pouvait
déjà appeler l'API interne ; il peut désormais frapper une portée de push pour
n'importe quelle ref de n'importe quel dépôt. Les deux capacités ont le **même
rayon de souffle** — le rôle `git` entier — ce qui est la raison pour laquelle le
partage a été jugé acceptable et non simplement commode. Une clé distincte reste
possible sans changer le format, si ce raisonnement cesse de tenir.

### 4. Le miroir de la principale est écrit par le serveur

`MirrorPath` sur le listener **interne**, jamais sur le smart HTTP. La raison est
plus forte que la symétrie avec `DiffPath` : cet endpoint fait **sortir une
connexion** du rôle `git` vers une adresse figurant dans la requête. Exposé à un
pod runner, il donnerait à du code arbitraire un moyen d'émettre des requêtes
depuis le composant qui détient le volume des dépôts.

**Répartition des rôles :** `api` détient la politique, lit la configuration du
dépôt et dit QUOI rafraîchir. `git` détient le volume et le credential et dit
COMMENT.

**Aucun credential ne transite dans le corps.** Le jeton de forge vit dans
l'environnement du rôle `git`. Un credential dans un corps de requête finit dans
un journal au premier incident.

**L'URL est revalidée par `gitd`.** `git.ParseRemote` refuse tout ce qui n'est pas
http/https — donc `ext::`, qui prend une commande shell et l'exécute, et
`file://`, qui atteindrait les dépôts voisins sur le volume. Elle vient de la
politique serveur, mais elle traverse une requête, et un endpoint qui fait sortir
des connexions est le dernier endroit où faire confiance sur parole.

**Le fetch est forcé.** La forge fait autorité sur la branche protégée : un
rebase ou un retour en arrière doit s'y refléter. La sûreté tient au SENS du
risque — un miroir qui recule rend l'ensemble exclu plus PETIT, donc le contrôle
de fraîcheur plus strict, jamais plus lâche.

**Un échec de forge est un 502, pas un 500.** La panne est chez une dépendance
externe, et le code le dit : un 500 enverrait l'opérateur fouiller les journaux
du rôle `git` alors que c'est le dépôt amont qui répond mal. La stderr de git ne
figure pas dans la réponse — elle cite volontiers l'URL, et une URL de forge
rendue à l'appelant finit recopiée dans un ticket.

## Conséquences

- **Avantages** :
  - les deux ports d'autorisation ont enfin une implémentation, et elle
    n'introduit **aucun état** ;
  - `gitd` reste sans base, en instance unique sans données à perdre, et à
    l'étage 1 de l'ADR-0010 ;
  - le miroir est écrit par le serveur, ce qui lève la condition que l'ADR-0027
    posait au câblage du chemin d'écriture.

- **Contraintes** :
  - **Aucune révocation avant expiration.** Bornée par l'écriture unique de la
    ref, comme expliqué plus haut, mais réelle.
  - **Un seul credential de forge pour tout le déploiement** en v1. C'est la
    posture que l'ADR-0020 a déjà constatée faute de gestionnaire de secrets, et
    la rotation reste le seul mécanisme de révocation. Un dépôt privé par équipe
    exigera un credential par dépôt.
  - Le credential est présenté en `Authorization: Bearer`. Gitea l'accepte ; une
    forge qui exigerait du Basic demandera d'étendre `Git.WithAuth`.
  - Le rôle `git` a désormais un **egress sortant**, avec la conséquence que
    l'ADR-0027 a écrite : son indisponibilité, qui empêche déjà tout run de
    démarrer, couvre maintenant celle de la forge.

- **Dette technique éventuelle** :
  - **La configuration de dépôt n'existe pas.** Personne ne sait encore quelle
    URL de forge et quelle branche protégée correspondent à quel dépôt : il
    manque une table et sa surface d'administration. Tant qu'elle n'existe pas,
    `RefreshMirror` n'a pas d'appelant.
  - ~~La réservation de run n'existe pas.~~ **Supprimée du besoin** par
    l'amendement ci-dessus : la CLI tire son propre identifiant, et la séquence du
    §5.2 tient telle quelle. Il reste que `POST /runs` n'existe pas.
  - Le chemin d'écriture **reste donc non câblé**, mais la raison a changé : ce
    n'est plus une propriété de sécurité manquante, c'est de la plomberie
    absente.
