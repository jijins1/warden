# ADR-0011 — Un binaire par domaine de confiance, frontières imposées par le compilateur

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

L'ADR-0006 a découpé le control plane en trois rôles déployés séparément à
partir d'un binaire unique. Il ne dit rien de deux questions que
l'initialisation du dépôt force à trancher.

**Combien de binaires au total ?** Le control plane n'est pas le seul code du
produit : il y a la CLI, sur la machine de l'utilisateur, et l'agent qui pilote
`ansible-runner` dans le pod. Si l'agent devient un quatrième rôle de `wardend`,
alors l'image du pod contient le code de l'API, celui du contrôleur et leurs
dépendances. Or le §2 pose que le playbook exécuté dans ce pod est du code
arbitraire : ce binaire est **lisible par un adversaire**.

**Qu'est-ce qui empêche les frontières de se dissoudre ?** L'ADR-0006 énonce une
règle de couplage — « aucun rôle n'appelle un autre rôle, sauf `api` → `git` » —
et la confie à la revue de code. L'expérience du reste du projet est que ça ne
suffit pas : l'ADR-0007 a explicitement écarté un filtrage de refs applicatif au
profit de `GIT_NAMESPACE`, au motif qu'une restriction appliquée par le moteur
bat une restriction appliquée par convention. Le même critère doit valoir pour
l'organisation du dépôt.

Une frontière est particulièrement coûteuse à perdre : l'ADR-0009 décrit une
faille précise — quelqu'un réutilise la résolution de configuration de la CLI
côté serveur, et le fichier audité cesse de correspondre au fichier appliqué.
C'est un import, une ligne, invisible en revue si personne ne connaît l'ADR.

## Solutions envisagées

### Option A — Layout plat, règles déclarées dans `depguard`
`internal/server/api`, `internal/runner`, etc., et toutes les frontières
exprimées dans `.golangci.yml`.
- **Avantage** : familier, plat, facile à refactorer.
- **Rejetée parce que** : la frontière la plus sensible — le pod hostile ne doit
  pas lier le control plane — dépendrait d'un linter, donc de l'exécution du CI,
  et se contournerait avec un `//nolint`. C'est exactement le type de contrôle
  que l'ADR-0007 a refusé ailleurs dans le produit ; l'accepter ici serait
  incohérent.

### Option B — Multi-module Go, un `go.mod` par domaine
Chaque domaine devient un module, avec sa propre liste de dépendances.
- **Avantage** : la liste des dépendances du pod hostile devient un artefact
  auditable en soi.
- **Rejetée parce que** : `go list -deps ./cmd/warden-runner` et `govulncheck`
  par binaire donnent déjà cet audit sans rien changer au dépôt. En face, un
  dépôt multi-module impose des directives `replace`, une propagation de version
  à chaque changement partagé et un outillage nettement plus lourd — pour une
  équipe unique travaillant dans un seul dépôt.

### Option C — Un `internal/` imbriqué par domaine ✅ (retenue)
Chaque domaine de confiance est un répertoire exposant un point d'entrée unique,
tout le reste vivant sous un `internal/` imbriqué.
- **Choisie parce que** : la sémantique de `internal/` en Go est appliquée par le
  compilateur. Un import interdit n'est pas un avertissement, c'est un échec de
  `go build`. C'est le seul mécanisme de ce genre que le langage offre, et il
  couvre précisément la frontière dont la perte est la plus coûteuse.

## Décision

**Un binaire par domaine de confiance**, ce qui en donne trois — le critère
n'est pas un compte de composants mais la réponse à « un adversaire peut-il lire
ce binaire ? ».

| Binaire | Domaine | Lisible par un adversaire |
|---|---|---|
| `warden` | machine de l'utilisateur | oui, mais c'est *son* poste |
| `warden-runner` | pod runner | **oui, par construction (§2)** |
| `wardend` | control plane, rôles `api`/`ingest`/`git`/`controller` | non |

Grouper les rôles serveur dans un seul binaire reste justifié : aucun adversaire
n'y lit le binaire, l'ADR-0006 veut pouvoir exécuter les trois rôles dans un
process en dev et en CI, et un tag d'image unique fait du déploiement à la même
version le comportement par défaut — l'API `api` → `git` étant interne et non
versionnée, le décalage de version ne doit pas être commode.

**Organisation du dépôt :**

```
cmd/warden  cmd/wardend  cmd/warden-runner      mains minces

internal/
  events/            schéma des events (ADR-0008)
  git/               enveloppe du binaire git (ADR-0005)
  config/            schéma strict de .ansibleproxy.yml (ADR-0009)
  wire/ingest/       contrat du lot runner → ingestion
  wire/ws/           cadres WS CLI ↔ api

  cli/     cli.go    + internal/{command,upload,stream,render}
  runner/  runner.go + internal/{fetch,exec,report}
  server/  server.go + internal/{api,policy,ingest,gitd,gitclient,controller,store}
```

Trois règles de contenu, sans lesquelles le découpage ne vaut rien :

- **`internal/git` ne contient aucune règle métier** — ni quarantaine, ni
  promotion, ni ancestry, ni scan de secrets. C'est l'enveloppe du binaire, rien
  d'autre. Sinon le pod hostile se met à lier de la politique serveur, et le
  bénéfice disparaît sans que rien ne le signale.
- **`internal/config` analyse, ne décide pas.** La composition politique
  serveur → configuration du commit → flags appartient à
  `internal/server/internal/policy` (ADR-0009).
- **`wire` est scindé en `ingest` et `ws`.** Sans cette scission, le pod hostile
  lierait le protocole WS du control plane, qu'il n'utilise jamais.

**Nommage et découpage :** le rôle git est servi par `internal/server/internal/gitd`
— convention `sshd`/`wardend`, et aucune ambiguïté avec `internal/git`, qui est
l'enveloppe du binaire. `store` reste **un seul paquet**, fichiers séparés par
domaine : l'éclater interdirait d'écrire le statut d'un run et ses events dans
la même transaction, ce que l'ADR-0008 exige. Il expose deux constructeurs,
`Open` et `OpenIngest`, ce dernier ne rendant qu'un type limité à
`AppendEvents` — la restriction réelle restant le rôle PostgreSQL, le type la
rend visible dans le code.

**Conséquence directe sur les tests de l'ADR-0010 :** un test d'intégration du
`store` ne peut pas vivre dans un répertoire `test/`, il n'aurait pas le droit
d'importer le paquet. Il doit être **à côté du code testé**. La séparation des
étages se fait donc par build tag, jamais par répertoire :

| Étage | Tag | Commande |
|---|---|---|
| 1 — unitaire | aucun | `go test ./...` |
| 2 — intégration | `//go:build integration` | `go test -tags=integration ./...` |
| 3 — bout en bout | `//go:build e2e` | `go test -tags=e2e ./test/e2e/...` |

Propriété à tenir : **`go test ./...` ne réclame ni Docker ni cluster.**

## Conséquences

- **Avantages** :
  - `internal/runner` ne **peut pas** importer le control plane : c'est un échec
    de compilation, pas une remarque de revue ;
  - la faille décrite par l'ADR-0009 devient inatteignable — `internal/server` ne
    peut pas atteindre la résolution de configuration de la CLI ;
  - `cmd/` ne pouvant pas atteindre les internes de son domaine, les `main`
    restent minces par construction ;
  - la liste de dépendances de l'image du pod hostile est auditable par
    `go list -deps ./cmd/warden-runner`.

- **Contraintes** :
  - **Le compilateur ne couvre pas les rôles serveur entre eux.** `api` et
    `controller` sont frères sous le même `internal/` : Go les laisse s'importer.
    La règle de l'ADR-0006 reste donc tenue par `depguard`, avec les arêtes
    autorisées déclarées explicitement (`api → store, policy, gitclient`,
    `gitd → store, git`, `controller → store`). Le compilateur couvre la
    frontière de sécurité, le linter la frontière de conception — et le second
    est moins solide que le premier.
  - Déplacer un paquet d'un domaine à l'autre est délibérément pénible. C'est
    l'effet recherché, mais c'en est un.
  - Le niveau de répertoire supplémentaire surprend un contributeur qui ne
    connaît pas la sémantique de `internal/` imbriqué. À documenter dans le
    README du dépôt, pas seulement ici.

- **Dette technique éventuelle** :
  - Rien n'empêche `internal/cli` ou `internal/runner` d'importer le paquet
    `internal/server` lui-même — seuls ses internes sont protégés. Ce paquet doit
    donc rester réduit à `Role` et `Run`, et cet import unique être interdit par
    `depguard`.
  - Le socle partagé (`events`, `git`, `config`, `wire`) est lié par les trois
    binaires. Chaque ajout dans le socle grossit l'image du pod hostile : toute
    nouvelle dépendance y entrant doit être examinée à ce titre.
