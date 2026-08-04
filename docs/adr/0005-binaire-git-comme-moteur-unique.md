# ADR-0005 — Le binaire git comme moteur unique, en remplacement de go-git

**Date** : 2026-08-04
**Statut** : Accepted
**Remplace** : [ADR-0002](0002-go-git-plutot-que-binaire-git.md)

## Contexte

L'ADR-0002 avait retenu go-git contre le pilotage du binaire `git`. Les ADR
rédigés ensuite ont modifié les données du problème sur quatre points :

1. **L'ADR-0004 fait entrer le binaire `git` dans l'image serveur** pour la
   maintenance des dépôts. L'argument principal de l'ADR-0002 — aucune
   dépendance externe — ne vaut donc plus que pour le poste de l'utilisateur.
2. **Ce même ADR-0004 fait apparaître un risque d'interopérabilité** entre les
   deux implémentations sur le même dépôt (multi-pack-index, cruft packs,
   bitmaps, `commit-graph`). C'est un risque qu'on peut tester mais jamais
   éliminer, et il porte sur la donnée que le produit est précisément censé
   garantir.
3. **L'ADR-0003 rend le comportement en clone shallow structurant** — c'est l'un
   des trois cas d'erreur de la décision. Or le support shallow est justement
   une faiblesse connue de go-git.
4. Les autres limites recensées dans l'ADR-0002 — absence de filtres
   `clean`/`smudge`, absence de LFS, `.gitignore` réimplémenté — exigent chacune
   un mécanisme de détection et de refus. Cela représente du code et des tests
   pour aboutir, au mieux, à un **sous-ensemble strict** de ce que `git` fait
   nativement.

S'ajoute un fait qui invalide l'argument le plus fort de l'ADR-0002. Celui-ci
retenait que seule une bibliothèque permettait de filtrer l'annonce de refs par
utilisateur. C'est inexact : `hideRefs` accepte la négation (`!`) et le serveur
lance lui-même `receive-pack` / `upload-pack`, il peut donc passer des
`-c uploadpack.hideRefs=…` **calculés par connexion**. Le filtrage dépendant de
l'identité du pousseur est atteignable avec le binaire.

## Solutions envisagées

### Option A — Maintenir go-git par cohérence
Conserver la décision de l'ADR-0002 et absorber ses limites.
- **Rejetée parce que** : le solde des arguments s'est inversé. La maintenir
  imposerait d'écrire les mécanismes de détection et de refus du point 4,
  d'accepter le risque d'interopérabilité du point 2, et de fragiliser le point 3
  — le tout pour préserver une propriété (« aucune dépendance externe ») que
  l'ADR-0004 a déjà entamée.

### Option B — Hybride : binaire côté serveur, go-git côté CLI
- **Rejetée parce que** : déjà rejetée par l'ADR-0002 pour la même raison, qui
  n'a pas changé — deux implémentations à maintenir cohérentes sur le même format
  d'objets.

### Option C — Le binaire `git` comme moteur unique ✅ (retenue)
Piloter `git` en plumbing partout : CLI, serveur, runner.
- **Choisie parce que** : sémantiques exactes sur les dépôts du monde réel,
  `receive-pack` / `upload-pack` éprouvés, maintenance native, et suppression
  intégrale de la liste de contraintes de l'ADR-0002 ainsi que de son risque
  d'interopérabilité. C'est aussi moins de code à écrire et à tester.

## Décision

go-git est abandonné. Le binaire `git`, piloté en plumbing derrière un wrapper
testé, est le seul moteur du produit.

**Ce qui est conservé de l'ADR-0002** — la propriété ne dépendait pas de la
bibliothèque : la capture CLI se fait dans un `GIT_DIR` temporaire, dont
l'`objects/info/alternates` référence l'object store de l'utilisateur, avec un
`GIT_INDEX_FILE` temporaire. Le dépôt de l'utilisateur reste ouvert en lecture
seule — aucune écriture, pas même d'objet loose — tout en gardant ses objets
visibles pour que la négociation du push ne transmette que le delta.

**Neutralisation systématique de l'environnement**, à chaque invocation :
`GIT_CONFIG_GLOBAL` et `GIT_CONFIG_SYSTEM` sur `/dev/null`,
`-c core.hooksPath=/dev/null`, `GIT_TERMINAL_PROMPT=0`, `GIT_OPTIONAL_LOCKS=0`,
`LC_ALL=C`. L'authentification vers notre serveur est fournie par
`-c http.extraHeader`, jamais par le credential helper de l'utilisateur.

**Plumbing uniquement**, sorties `-z` ou `--porcelain=v2` ; aucun parsing de
porcelaine destinée à l'humain.

**Annonce de refs filtrée par connexion**, via `-c uploadpack.hideRefs` et
`-c receive.hideRefs` avec négation, calculés à partir de l'identité du
pousseur.

**Version plancher** de `git` vérifiée au démarrage de la CLI et figée dans les
images serveur et runner.

L'ADR-0004 reste valide dans ses paramètres de maintenance, mais sa notion de
frontière entre deux moteurs disparaît : il n'y a plus qu'un moteur.

## Conséquences

- **Avantages** :
  - sémantiques exactes : filtres `clean`/`smudge`, LFS, sparse-checkout,
    `.gitignore`, submodules, clones shallow ;
  - `receive-pack` / `upload-pack` et la génération de packs éprouvés ;
  - maintenance native, sans interaction entre deux implémentations ;
  - disparition des contraintes et de la dette de l'ADR-0002, y compris le test
    d'interopérabilité qui en constituait le principal risque ;
  - moins de code produit, donc moins de tests à écrire pour un comportement
    correct.

- **Contraintes** :
  - `git` devient requis sur le **poste de l'utilisateur**, avec une version
    plancher. Acceptable : la cible est un ingénieur travaillant déjà dans un
    dépôt git, ce que l'ADR-0003 impose par ailleurs.
  - La neutralisation de l'environnement doit être appliquée **systématiquement
    et testée** : une variable oubliée, et un hook ou une configuration de
    l'utilisateur s'exécute dans notre chemin.
  - Parsing de sortie, à confiner au plumbing et à couvrir par des tests.
  - Coût `fork`/`exec` d'environ 5 ms par appel, une dizaine d'appels par run :
    négligeable devant le budget de 3 s, mais à mesurer en phase 0.

- **Dette technique éventuelle** :
  - La version de `git` varie d'un poste à l'autre : prévoir une matrice de
    versions en CI, de la version plancher à la plus récente.
  - Si `git-lfs` est absent du poste alors que le dépôt l'utilise, la capture
    embarque des pointeurs au lieu du contenu. À détecter et à signaler
    explicitement, plutôt qu'à laisser produire un audit trompeur.
