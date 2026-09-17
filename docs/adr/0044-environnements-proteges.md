# ADR-0044 — Options par environnement : un avertissement déclaré, et un gel indépendant des droits

**Date** : 2026-09-17
**Statut** : Accepted
**Complète** : [ADR-0033](0033-droits-par-environnement.md)

## Contexte

Le badge d'environnement du navigateur (`w-env-badge`) distinguait visuellement
un run sur `prod` : bordure et couleur d'avertissement, pour qu'une action sur
cet environnement se voie avant qu'on clique. La comparaison était câblée en
dur dans le composant :

```ts
protected readonly critique = computed(() => this.env().toLowerCase() === 'prod');
```

Le commentaire du composant assumait explicitement ce choix : « `prod` est
distingué visuellement, et c'est le seul nom câblé […] en énumérer d'autres
reviendrait à inventer une taxonomie que le produit n'a pas ». C'était vrai
tant qu'aucune configuration n'existait pour porter cette taxonomie — mais ça
voulait dire qu'un déploiement dont l'environnement sensible s'appelle
`production`, `prd`, ou tout autre nom, n'avait AUCUN moyen d'obtenir le même
avertissement. Le produit reconnaissait un nom, pas une intention.

## Solutions envisagées

### Option A — Étendre le nom reconnu à une liste statique côté client
Remplacer `=== 'prod'` par une recherche dans un tableau déclaré dans le code
Angular (`environment.ts` ou équivalent).
- **Avantage** : aucun changement serveur, un seul fichier touché.
- **Rejetée parce que** : la liste resterait figée au build du frontend. La
  protéger différemment par déploiement — ce qui est le cas normal, chaque
  cluster ayant ses propres noms d'environnement — demanderait de recompiler
  l'interface plutôt que de committer une ligne de configuration.

### Option B — Ajouter un champ au document des droits (ADR-0033)
Réutiliser `grants.yaml`, avec une clé `protected: [...]` à côté
d'`environments:`.
- **Avantage** : un seul fichier, un seul ConfigMap, un seul intervalle de
  réconciliation à opérer.
- **Rejetée parce que** : les deux notions sont de nature différente.
  `environments` contrôle un ACCÈS — sans droit, pas de clé SSH, donc pas de
  machine joignable. Ce nouveau champ n'est qu'un AVERTISSEMENT visuel, sans
  aucune conséquence de sécurité. Les fusionner dans un même document ferait
  passer un simple avertissement pour un contrôle, ce que l'ADR-0033 met
  justement en garde de ne pas faire (« un contrôle qui ressemble à un
  contrôle sans en être un est pire que pas de contrôle du tout »). Un
  administrateur qui modifie `grants.yaml` pour ajouter un badge ne doit pas
  se retrouver à un clic de retirer un droit d'accès par erreur.

### Option C — Fichier de configuration séparé, réconcilié en base, servi à tout utilisateur authentifié ✅ (retenue)
Un second document versionné (`protected.yaml`), monté et réconcilié par le
même rôle `controller`, mais dans une table et un fichier distincts de ceux
des droits. Exposé par une route `GET /environments/protected` accessible à
tout compte authentifié, pas seulement aux administrateurs.
- **Choisie parce que** : ça garde les deux notions séparées à tous les
  étages — fichier, table, port du cœur — donc aucune confusion possible entre
  « qui peut atteindre cet environnement » et « cet environnement mérite un
  avertissement ». Et l'ouverture à tout utilisateur authentifié est
  nécessaire à l'usage réel : le badge apparaît dans la liste des runs et leur
  détail, pas seulement dans l'administration, donc un compte sans droit
  d'administration doit pouvoir le voir se colorer sur ses propres runs — sans
  révéler rien de plus que des noms d'environnement déjà visibles ailleurs
  dans le produit.

Le badge seul était le besoin initial. Il s'est avéré, en le concevant,
qu'un second besoin voisin existait : pouvoir **geler le lancement de run**
sur un environnement — pendant une maintenance ou un incident — sans toucher
aux droits individuels (ADR-0033), et sans avoir à les réaccorder ensuite. Les
deux ont donc été traités dans le même document, comme deux OPTIONS
indépendantes par environnement plutôt que comme deux mécanismes séparés :

```yaml
environments:
  prod:
    protected: true   # badge visuel
    allowRun: false   # gel du lancement de run
  preprod:
    protected: true
```

## Décision

Même charpente que l'ADR-0033, en plus simple :

1. **`internal/server/internal/core/protected.go`** — `ProtectedDocument` est
   une CARTE (comme `GrantDocument`), pas une liste : chaque environnement
   porte ses propres options (`RawEnvironmentOptions{Protected, AllowRun
   *bool}`). `AllowRun` est un **pointeur**, pas un `bool` : la clé absente
   doit se lire « autorisé », pas la valeur zéro d'un bool qui gèlerait
   silencieusement tous les runs au premier oubli d'écrire `allowRun: true`.
   `ValiderLesProteges`, pure, valide chaque nom (`core.ValidateEnvironment`)
   et rend une liste triée d'`EnvironmentOptions` normalisées. À la différence
   de `PlanifierLesDroits`, un document VIDE n'est **pas** refusé : oublier de
   déclarer un environnement ne retire aucun accès, au pire ça retire un
   badge, au mieux ça laisse `allowRun` à son défaut sûr.

2. **`internal/server/internal/outbound/protectedfile/`** — lecteur de fichier
   avec détection de changement par empreinte de contenu, même mécanique que
   `grantfile`.

3. **Migration `00014_environnements_proteges.sql`** — table
   `protected_environment(environment PRIMARY KEY, protected boolean, allow_run
   boolean, declared_at)`. Pas de `revoked_at` daté comme `environment_grant` :
   il n'y a ici aucune question d'audit du type « qui avait ce droit le 3
   mars », puisque ni l'une ni l'autre colonne n'est un droit. Le contrôleur
   **remplace la table entière** à chaque réconciliation
   (`Store.ReplaceOptions`, DELETE puis INSERT en une transaction) plutôt que
   de calculer un plan Accorder/Revoquer par sujet.

4. **`internal/server/internal/core/protected.go` — `VerifierRunAutorise`** :
   la fonction d'enforcement du gel, cousine de `VerifierLAcces` mais avec
   DEUX différences délibérées, parce que ce n'est pas un contrôle d'accès à
   un credential :
   - un magasin **ABSENT** autorise (c'est l'INVERSE de `VerifierLAcces`, qui
     refuse tout par défaut) : le gel est une option qu'on active en
     déployant un fichier, un déploiement qui ne la monte pas doit continuer à
     lancer des runs normalement ;
   - une **panne de lecture** du gel n'empêche pas le run, elle est
     seulement journalisée — encore l'inverse de `VerifierLAcces`, qui
     considère qu'une panne ne doit jamais donner un accès. Le gel n'étant pas
     le contrôle de sécurité par défaut du produit, une panne transitoire ne
     doit pas transformer un déploiement dégradé en blocage total des runs.

   Appelée depuis `autoriserLEnvironnement` (`internal/server/internal/inbound/api/run_handler.go`),
   le même point de contrôle déjà partagé par les trois chemins de création de
   run (`POST /runs`, `POST /repos/{repo}/runs`, le pipeline adhoc), APRÈS la
   vérification des droits — les deux refus se traduisent en 403, et
   l'appelant n'a pas à distinguer pourquoi pour corriger sa demande.

5. **`internal/server/server.go`** — une TROISIÈME boucle de réconciliation,
   distincte de celle des pods : `WARDEN_PROTECTED_FILE` /
   `WARDEN_PROTECTED_SECONDS`, alignée sur le rythme des DROITS (défaut 10 s)
   et non sur celui des pods — `allowRun` est un gel dont la prise d'effet a
   la même urgence qu'une révocation.

6. **`GET /environments/protected`** — sous `router`, comme `/runs`, jamais
   sous `/admin` et jamais derrière `adminOuRien`. Ne rend QUE les
   environnements `protected: true` (le badge), pas `allowRun` : cette route
   sert le navigateur pour toute personne authentifiée, alors que le gel n'a
   besoin d'être lu que par le serveur lui-même, au moment de créer un run.
   Rend une liste vide plutôt qu'un 501 quand le port n'est pas monté :
   contrairement à `Access`, l'absence du BADGE ne doit bloquer personne — le
   GEL, lui, se comporte différemment, voir le point 4.

7. **`charts/warden`** — un second ConfigMap (`{{ fullname }}-protected`),
   monté dans un dossier séparé (`/etc/warden-protected`) du contrôleur, avec
   `values.protected.environments` (une carte, pas une liste) comme source de
   vérité déclarative — même principe que `grants` : aucune commande n'écrit
   ces options, seul un commit les change.

8. **Frontend** — `ProtectedEnvironmentsService` (chargé une fois, partagé par
   toutes les instances de `w-env-badge` via `providedIn: 'root'`) remplace la
   comparaison de nom :

   ```ts
   protected readonly critique = computed(() => this.#proteges.isProtected(this.env()));
   ```

   La classe CSS `.prod` devient `.critique` pour ne plus laisser croire
   qu'elle ne concerne que cet unique nom. Le frontend n'a connaissance QUE du
   badge — `allowRun` n'est pas exposé au navigateur ; un run gelé est refusé
   par le serveur avec un message explicite au moment de la soumission,
   plutôt que d'être anticipé par un bouton désactivé.

## Conséquences

- **Avantages** : un déploiement peut désormais marquer n'importe quel nom
  d'environnement comme protégé visuellement, ET geler le lancement de run
  sur un environnement précis — en committant quelques lignes, sans
  recompilation du frontend, et sans toucher au document des droits qui
  contrôle l'accès aux credentials.
- **Contraintes** : troisième fichier de configuration, troisième variable de
  rythme, troisième table à opérer — la simplicité de « un seul nom câblé »
  est perdue. `allowRun` ajoute par ailleurs un DEUXIÈME point d'échec
  possible à la création d'un run (après les droits), avec une politique de
  panne délibérément différente (fail-open) qu'il faut garder en tête en
  diagnostiquant un run refusé.
- **Dette technique éventuelle** : comme les droits, ceci reste un mécanisme
  **déclaratif sans commande d'écriture** — changer les options passe par un
  commit. Si un jour la CLI ou l'interface veut proposer un bouton « geler
  cet environnement », il faudra soit exposer une route d'écriture (avec le
  risque de double source de vérité que l'ADR-0033 refuse pour les droits),
  soit accepter le même délai qu'aujourd'hui — le sujet n'a pas été tranché
  ici faute de besoin exprimé. Le choix fail-open sur une panne de lecture du
  gel est également à revisiter si l'usage réel montre qu'un gel doit un jour
  devenir un contrôle de sécurité à part entière plutôt qu'une commodité
  opérationnelle — auquel cas il faudrait probablement l'aligner sur le
  fail-closed de `VerifierLAcces`.
