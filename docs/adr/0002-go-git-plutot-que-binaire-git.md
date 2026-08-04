# ADR-0002 — go-git plutôt que le binaire git système

**Date** : 2026-08-04
**Statut** : Superseded par [ADR-0005](0005-binaire-git-comme-moteur-unique.md)

> Cet ADR est conservé pour la trace du raisonnement. Sa décision a été
> renversée : voir l'ADR-0005 pour les éléments qui ont inversé le solde des
> arguments.

## Contexte

Le produit manipule des objets git à trois endroits (`specs/initial.md` §5.2,
§6.2, §8.6) :

- **CLI** : construire un commit représentant le worktree de l'utilisateur sans
  toucher à son index ni à son HEAD (décision produit #4, non négociable), puis
  pousser le delta vers le serveur.
- **Serveur** : recevoir le push, mettre en quarantaine, scanner, promouvoir la
  ref, vérifier l'ascendance d'un SHA en mode PROD, produire les diffs de l'UI
  d'audit, appliquer la rétention.
- **Runner** : matérialiser un commit dans un workspace éphémère.

Deux façons de faire : piloter le binaire `git` du système, ou embarquer une
bibliothèque. Le choix engage la correction de l'artefact d'audit — donc la
valeur du produit — et la latence du chemin chaud (budget de 3 s de surcoût,
§3.1).

## Solutions envisagées

### Option A — Shell-out vers le binaire `git` du système
Piloter `git` en plumbing (`ls-files`, `update-index`, `write-tree`,
`commit-tree`, `push`) derrière un wrapper, avec `GIT_INDEX_FILE` pointant sur
un index temporaire comme décrit en §5.2.
- **Avantage** : sémantiques exactes sur les dépôts du monde réel — filtres
  `clean`/`smudge`, LFS, sparse-checkout, `.gitignore`, submodules ;
  `receive-pack`/`upload-pack` éprouvés ; maintenance (`gc`, `repack`,
  `commit-graph`) fournie gratuitement.
- **Rejetée parce que** : elle place le chemin critique sous la dépendance d'un
  binaire externe dont on ne maîtrise ni la version ni la configuration sur le
  poste de l'utilisateur, et impose de neutraliser explicitement tout
  l'environnement git à chaque appel (config globale et système, `hooksPath`,
  prompts, locale) sous peine de comportements divergents et de hooks
  utilisateur exécutés dans notre chemin.

### Option B — Hybride : bibliothèque côté serveur, binaire côté CLI
Les entrées du serveur sont contrôlées (nos propres bare repos), celles de la
CLI ne le sont pas.
- **Rejetée parce que** : deux implémentations à maintenir cohérentes sur le
  même format d'objets, pour un bénéfice qui ne porte que sur les cas limites.

### Option C — go-git dans tout le produit ✅ (retenue)
Bibliothèque Go pure, aucune dépendance runtime externe.
- **Choisie parce que** :
  - aucune dépendance externe sur le poste de l'utilisateur, cohérent avec le
    binaire unique de l'ADR-0001 ;
  - **contrôle programmatique de l'annonce de refs côté serveur** : le filtrage
    par utilisateur (n'annoncer que la branche protégée et les N dernières refs
    d'audit du pousseur) se fait dans notre code, alors que la configuration
    `hideRefs` de git ne permet pas proprement un filtrage dépendant de
    l'identité de la connexion. C'est le point qui conditionne la latence quand
    le nombre de refs d'audit croît ;
  - **on n'utilise aucun index git** : les arbres sont construits directement en
    mémoire à partir de la liste de fichiers. La garantie « le worktree de
    l'utilisateur n'est jamais modifié » devient *structurelle* — le dépôt de
    l'utilisateur est ouvert en lecture seule — au lieu de reposer sur la
    discipline d'un `GIT_INDEX_FILE` correctement positionné à chaque appel.

## Décision

go-git sur tout le chemin chaud : capture et push côté CLI, réception,
quarantaine, promotion, requêtes d'audit côté serveur, matérialisation côté
runner. Aucun appel au binaire `git` dans le chemin d'un run.

Le dépôt de l'utilisateur est ouvert en lecture seule ; les objets produits sont
écrits dans un object store temporaire qui référence celui de l'utilisateur en
`alternates`, afin que la négociation du push ne transmette que le delta.

## Conséquences

- **Avantages** :
  - binaire unique sans dépendance runtime, sur le poste comme dans les images ;
  - annonce de refs filtrée par utilisateur, maîtrisée dans notre code ;
  - non-modification du dépôt utilisateur garantie par construction ;
  - aucune neutralisation de configuration ou de hooks utilisateur à maintenir.

- **Contraintes** :
  - **Pas de filtres `clean`/`smudge` ni de LFS.** Un dépôt dont le
    `.gitattributes` déclare un `filter=` produirait des blobs différents de
    ceux qu'aurait écrits git : le dedup côté serveur casse et le diff contre la
    branche principale affiche de fausses modifications. Ces attributs doivent
    être **détectés et refusés explicitement** — un audit faux est pire qu'un
    run refusé.
  - **Sémantique `.gitignore` réimplémentée.** Le périmètre de l'upload (§5.3)
    détermine la valeur de l'audit : des tests différentiels contre
    `git ls-files --cached --others --exclude-standard` sur un corpus de
    fixtures sont obligatoires en CI (git n'y sert que d'oracle de test).
  - Support limité des clones shallow — impacte l'ADR-0003.
  - Génération de packs et négociation moins optimisées que celles de git ; à
    mesurer en phase 0 contre le budget de 3 s.

- **Dette technique éventuelle** :
  - go-git ne fournit ni `gc`, ni `repack`, ni écriture de `commit-graph`. La
    maintenance des bare repos serveur (§6.2 : politique de GC et de rétention
    dès le début) n'a donc pas de solution en Go pur. **Tranché par l'ADR-0004** :
    appel au binaire `git` restreint à la maintenance planifiée côté serveur, où
    l'environnement est notre propre image — ce qui ne réintroduit pas la
    dépendance rejetée en Option A, qui portait sur le poste de l'utilisateur et
    sur le chemin chaud.
  - L'implémentation `receive-pack`/`upload-pack` de go-git est moins éprouvée
    que celle de git : à couvrir par des tests d'interopérabilité contre un
    client git réel.
