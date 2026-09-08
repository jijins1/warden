# ADR-0040 — `warden submit` suit le run, et son code de sortie le dit

**Date** : 2026-09-08
**Statut** : Accepted
**S'appuie sur** : [ADR-0008](0008-postgresql-comme-journal-et-transport-devents.md), [ADR-0011](0011-un-binaire-par-domaine-de-confiance.md)

## Contexte

Premier run de production réussi, et un retour immédiat : *« le run a marché
mais ma console ne m'a pas affiché les logs en direct »*.

Ce n'était pas une panne. Le suivi en direct **existe et fonctionne** :
`GET /runs/{run_ref}/stream` est générique — il prend un `run_ref`, pas un genre
de run — `stream.Client` gère déjà la reconnexion avec reprise à l'offset, et
`warden enroll` s'en sert pour afficher son run pendant qu'il tourne.
L'interface web aussi.

`warden submit` ne s'y attachait simplement **jamais**. Il déclarait le run,
imprimait trois lignes et rendait la main.

Le maillon n'avait pas été posé, et rien ne le signalait — au contraire, tout
autour supposait qu'il l'était :

- le doc de `stream.New` dit textuellement que l'oublier « donnerait un
  `warden submit` qui pousse, lance le run, puis échoue à en suivre la sortie —
  le pire des trois résultats » ;
- le §5.6 décrit le Ctrl-C d'un `submit` attaché, et le §5.7 la reconnexion
  automatique avec replay. Les deux n'ont de sens que branchés ;
- `internal/cli/internal/render` existait avec un `doc.go` annonçant exactement
  ce rôle — et pas une ligne de code.

## Ce qui est décidé

### 1. `submit` suit par défaut

C'est ce que fait `ansible-playbook`, et c'est ce que l'utilisateur attendait
sans avoir eu à le demander. `--no-follow` rend la main dès le run déclaré.

Le drapeau est le **négatif**. Un `--follow` obligerait à le taper à chaque fois
pour obtenir le comportement évident, et laisserait par défaut celui qui vient
de surprendre.

**Conséquence assumée** : la commande devient bloquante. Un script qui l'appelle
et repart aussitôt attendra désormais la fin du run. C'est un changement de
contrat, et `--no-follow` est la porte de sortie.

### 2. Le code de sortie porte l'issue du run — quand la commande la connaît

Attachée, la commande sait comment le run s'est terminé : un run `failed` ou
`interrupted` rend un code non nul, comme `ansible-playbook`. C'est ce qui rend
`warden submit` utilisable dans un pipeline.

Détachée, elle rend **0** dès que la soumission a réussi, et c'est tout ce
qu'elle peut affirmer honnêtement : elle ne sait rien de la suite.

Un statut de fin **inconnu** compte comme un échec. Supposer la réussite est le
mode de défaillance le plus coûteux d'un produit d'audit — c'est la même règle
que `issueDuRun` applique déjà côté serveur.

### 3. `warden logs <run_ref>` existe, parce que sans elle `--no-follow` est un piège

Se détacher sans pouvoir se rattacher n'est pas une option, c'est une impasse.
`warden logs` rejoue le même affichage sur un run déjà lancé — après
`--no-follow`, après un Ctrl-C, ou depuis une autre machine.

Elle ne coûte presque rien parce que tout existait : `Flux.Follow` sert le flux
« à partir de fromSeq, **historique compris** » (ADR-0008), donc un run terminé
se rejoue du début. Il n'y a pas deux chemins « rattrapage » et « direct ».

### 4. Ctrl-C DÉTACHE, et le dit

Le §5.6 veut que Ctrl-C **annule** le run, avec escalade SIGINT → SIGTERM →
suppression du pod. Ce n'est pas ce qui est livré ici, et il faut le dire :
**le serveur n'expose aucun endpoint d'annulation**. Les routes de run sont
`GET /runs`, `GET /runs/{ref}`, `.../stream`, `.../diff` et `POST /runs`. Il n'y
a rien à appeler.

Le comportement livré est donc le détachement, avec un message qui **nomme ce
qui ne s'est pas passé** :

```
détaché — le run 01JZQ… CONTINUE côté serveur.
« warden logs 01JZQ… » pour s'y rattacher.
```

Sans cette phrase, l'utilisateur croit avoir arrêté un run qui continue de
modifier ses machines. C'est le seul mode de panne inacceptable ici, et il coûte
deux lignes à fermer. Le §5.6 reste ouvert dans `docs/reste-a-faire.md`.

## Solutions envisagées

### Option A — Suivre derrière `--follow` ✅ rejetée
- **Rejetée parce que** : elle laisse par défaut le comportement qui a motivé le
  retour. On ne corrige pas une surprise en la mettant derrière un drapeau.

### Option B — Suivre si la sortie est un terminal
- **Rejetée parce que** : le comportement dépendrait du contexte, donc du
  gréement du shell appelant. Astucieux, plus dur à expliquer, plus dur à
  tester, et deux invocations identiques ne feraient pas la même chose.

### Option C — Suivre par défaut, `--no-follow` pour s'en passer ✅ (retenue)

## Conséquences

- L'affichage regroupe **par tâche**, comme `ansible-playbook`. Sur une flotte,
  la question posée est « où en est cette tâche » et non « qu'a fait cette
  machine » ; une ligne par event, à plat, devient illisible dès la troisième
  machine.
- `unreachable` a son propre marqueur et ne se confond pas avec `failed` : une
  machine injoignable est un problème de réseau ou de clé, pas de playbook, et
  les deux ne se corrigent pas au même endroit.
- Le bilan de fin est écrit **même sur erreur de flux** : une coupure après huit
  minutes ne doit pas emporter ce qu'on a déjà vu passer.
- `render` répète le vocabulaire d'issue (`succeeded`, `failed`, `interrupted`)
  en littéraux. La CLI ne peut importer ni `internal/server/internal/core`
  (ADR-0011) ni `internal/wire/ingest` (fermé par depguard : c'est un contrat
  runner → serveur). C'est le raisonnement que `wire/ingest` tient déjà pour
  lui-même — le contrat de fil est la frontière, chaque domaine y répète le
  vocabulaire au lieu de l'emprunter.

## Ce que ça n'achète pas

Le §5.8 — l'affichage des **hôtes en vol**, que `render/doc.go` annonce comme un
différenciateur produit — n'est pas livré. Ce qui est affiché est ce qui est
TERMINÉ, tâche par tâche ; savoir sur quelle machine un run est en train de
bloquer reste hors de portée, et c'est précisément ce qu'on cherche pendant un
incident.

Le Ctrl-C du §5.6 non plus, faute d'endpoint d'annulation. Voir §4 ci-dessus.
