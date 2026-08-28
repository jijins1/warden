# ADR-0032 — Comptes locaux et sessions : une identité vérifiée, en attendant OIDC

**Date** : 2026-08-23
**Statut** : Accepted
**Complète** : [ADR-0011](0011-un-binaire-par-domaine-de-confiance.md), [ADR-0020](0020-key-inventory-credentials-dacces.md), [ADR-0023](0023-identite-damorcage-des-pods.md)

## Contexte

Le produit n'avait aucune authentification. Le middleware refusait tout, sauf
derrière `WARDEN_INSECURE_DEV_AUTH=1` où il croyait un en-tête `X-User-Subject`
sur parole. L'audit disait « alice » parce qu'un client l'avait affirmé — et
comme `is_admin` se lit sur cette identité, n'importe qui pouvait se déclarer
administrateur.

La plomberie était pourtant là : injection d'en-tête côté CLI, jeton de push
borné et signé (ADR-0028), règle « aucun secret dans argv ni dans une URL ». Il
manquait le maillon qui VÉRIFIE.

OIDC reste la cible. Cet ADR décrit ce qui le précède : des comptes locaux, avec
mot de passe, en base.

## Décisions

### 1. Un compte local est un `users.type = 'local'`

Aucune table d'utilisateurs parallèle. Un compte OIDC sera un
`users.type = 'oidc'`, avec la même clé naturelle `(type, subject)`. Le jour où
OIDC arrive, `local_credential` cesse d'être alimentée et rien d'autre ne bouge —
ni les runs, ni l'audit, ni les clés étrangères.

### 2. `local_credential` est une table SÉPARÉE

Pas de colonne `password_hash` sur `users`, et la raison décisive n'est pas la
normalisation : **`warden_reader` a SELECT sur `users`** pour que l'interface
d'audit nomme les acteurs. Une empreinte dans cette table serait donc lisible par
le rôle de LECTURE, et une empreinte lisible est une empreinte attaquable hors
ligne. La séparer permet de ne lui accorder aucun droit dessus.

Ni `warden_reader` ni `warden_ingest` n'ont le moindre GRANT sur
`local_credential` et `session`.

### 3. argon2id, paramètres dans l'empreinte

Format PHC : `$argon2id$v=19$m=19456,t=2,p=1$sel$empreinte`.

**m=19 Mio et non 64 Mio** — la « deuxième option recommandée » de la RFC 9106,
que la RFC donne comme équivalente en sécurité. Le premier profil ferait sortir
le pod par l'OOM sous quelques connexions simultanées, c'est-à-dire qu'un
durcissement du hachage deviendrait un déni de service.

Les paramètres voyagent DANS l'empreinte, et la vérification les relit de la
chaîne plutôt que des constantes. C'est ce qui permettra de durcir le coût sans
invalider ce qui est en base — testé explicitement.

Une empreinte illisible rend une ERREUR distincte de « mauvais mot de passe ».
Les confondre ferait passer une corruption de données pour un utilisateur qui se
trompe de touche, des mois durant.

### 4. Le jeton de session est haché en SHA-256 nu, pas en argon2

C'est la différence d'ENTROPIE. Un mot de passe humain a quelques dizaines de
bits et doit être rendu coûteux à essayer ; ce jeton en a 256, tirés au hasard, et
aucune quantité de calcul ne le retrouve. Lui appliquer argon2 coûterait 19 Mio à
chaque requête authentifiée pour ne rien acheter.

Seule l'empreinte est stockée — même règle que le jeton d'amorçage des pods
(ADR-0023). Compromettre une sauvegarde de la base ne donne aucun jeton
utilisable.

### 5. Ce qu'un endpoint de connexion refuse de DIRE

C'est la seule surface qu'un appelant anonyme atteint, donc tout ce qui la
protège est dans le cœur :

- **une seule erreur** pour compte inconnu, mauvais mot de passe et compte
  désactivé. Les distinguer confirmerait qu'un compte existe ;
- **une empreinte leurre** : quand le sujet n'existe pas, la dérivation a lieu
  QUAND MÊME. Sans elle, un compte inconnu répond en une microseconde et un
  compte existant en cinquante millisecondes — de quoi énumérer les comptes au
  chronomètre ;
- **un délai plancher de 250 ms**, qui uniformise le temps de réponse ;
- **quatre dérivations simultanées au maximum**. Sans ce plafond, l'endpoint est
  un déni de service à lui seul : chaque tentative alloue 19 Mio. Combiné au
  plancher, il borne le martelage à environ seize tentatives par seconde.

Il n'y a PAS de verrouillage de compte après N échecs. C'est une lacune assumée :
le verrouillage est aussi un levier de déni de service contre un utilisateur
nommé, et le plafond de débit ci-dessus est le frein qui ne se retourne pas.

### 6. Changer un mot de passe RÉVOQUE toutes les sessions, dans la même transaction

On change un mot de passe parce qu'on le croit compromis. Laisser vivre les
sessions ouvertes avec l'ancien ne protégerait de rien. Un appelant qui devrait
penser à faire les deux finirait par oublier — donc c'est le magasin qui l'impose.

Idem pour la désactivation d'un compte : sans révocation, couper un accès ne
ferait rien avant l'expiration du jeton, jusqu'à trente jours après avoir cru le
couper.

L'ANCIEN mot de passe est exigé même quand l'appelant est déjà authentifié : un
poste laissé déverrouillé suffirait sinon à s'approprier le compte de façon
DURABLE, là où un jeton volé expire et se révoque.

### 7. Expiration absolue de trente jours, pas de fenêtre glissante

Une fenêtre glissante exigerait une écriture à chaque requête authentifiée — donc
sur le chemin le plus chaud du produit — pour n'acheter que le confort de ne pas
se reconnecter. `warden login` est une commande.

Trente jours est un arbitrage explicite : une session courte fait retaper un mot
de passe plusieurs fois par jour, ce qui pousse à le choisir mauvais ou à
contourner l'outil — or « un point de contrôle qu'on contourne ne contrôle rien »
est la thèse du produit. Le curseur est mis du côté de l'usage PARCE QUE la
révocation existe.

`last_seen_at` est rafraîchi paresseusement, au plus une fois par heure.

### 8. `/auth/login` est monté sur un routeur AU-DESSUS du middleware

Un routage et non une exception DANS le middleware. La différence n'est pas
cosmétique : une exception se compare à un chemin de requête, et un chemin se
déguise (`/auth/login/../runs`, un encodage, une barre en trop). Ici,
`/auth/login` n'est jamais passé au routeur protégé — il n'y a aucune comparaison
à tromper. Un test parcourt les variantes.

### 9. Le jeton vit dans un fichier 0600, indexé par serveur

Un fichier et jamais une variable d'environnement : l'environnement d'un
processus fuit dans les traces, les rapports de plantage et les scripts qu'on
partage.

Les droits sont vérifiés à la LECTURE et pas seulement posés à l'écriture : un
umask permissif, une restauration de sauvegarde ou un `cp` sans `-p` les
relâchent, et le fichier continue de fonctionner sans que rien ne le dise. Le
message nomme la commande qui répare.

Indexé par adresse de serveur : on parle à un warden de développement et à un de
production, et écraser l'un avec l'autre ferait pointer une commande sur le
mauvais parc. L'écriture est atomique — une écriture en place interrompue
perdrait TOUTES les sessions, y compris celles des serveurs qu'on ne touchait pas.

Le jeton est en clair dans ce fichier. Le chiffrer supposerait une clé, qui
devrait vivre sur la même machine : ce serait déplacer le problème en donnant
l'impression de le résoudre. Ce qui le protège est le mode 0600, comme pour une
clé SSH.

### 10. Le mot de passe initial est TIRÉ par le serveur

Jamais choisi par l'administrateur, jamais accepté en argument. Un mot de passe
choisi par un tiers est un mot de passe que ce tiers connaît, et il est presque
toujours faible parce qu'il doit être dicté.

Vingt caractères sur un alphabet de 31, sans les glyphes qu'on confond (`0`/`O`,
`1`/`l`/`I`) : environ 99 bits, destiné à être transmis puis remplacé.

Douze caractères minimum pour un mot de passe choisi, et AUCUNE règle de
composition — le NIST SP 800-63B les a retirées parce qu'elles produisent
« Motdepasse1! » et poussent à écrire le mot de passe sur un papier.

### 11. L'amorçage passe par `wardend`, pas par la CLI

`wardend user add --subject alice --admin`, lancé dans le cluster.

Son autorisation n'est pas un mot de passe mais l'ACCÈS À LA BASE — c'est-à-dire
le fait d'être l'opérateur du déploiement. C'est la seule autorisation qui existe
avant qu'un compte existe, et c'est le même raisonnement que `wardend migrate`.

## Conséquences

**Vérifié sur le cluster réel** : compte amorcé, `warden login`, fichier écrit en
0600, `warden submit` complet authentifié par le seul jeton — run `succeeded`
attribué à `ruokki` — puis `warden logout` et le jeton refusé en 401.

**`WARDEN_INSECURE_DEV_AUTH` est retiré du déploiement.** C'est le résultat qui
compte : l'en-tête `X-User-Subject` répond désormais 401, vérifié. Il reste dans
le code pour les tests de bout en bout, qui n'ont pas de terminal.

**Ce que ça ne fait pas** :

- **aucune autorisation nouvelle.** `is_admin` est enfin porté par une identité
  vérifiée, mais l'egress du runner reste ouvert (ADR-0030) et le périmètre du
  §10.2 n'existe toujours pas. Savoir QUI lance ne dit pas ce qu'il a le droit
  d'atteindre ;
- **aucun verrouillage après échecs répétés** (décision 5) ;
- **aucune purge des sessions mortes.** Elles s'accumulent ; les index sont
  partiels sur les vivantes, donc la lecture n'en souffre pas, mais la table
  grossit. Le réconciliateur de l'ADR-0031 est l'endroit naturel pour ça ;
- **aucun second facteur**, et aucune rotation forcée du mot de passe initial :
  rien n'oblige à remplacer celui que le serveur a tiré.
