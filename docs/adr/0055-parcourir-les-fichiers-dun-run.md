# ADR-0055 — Parcourir les fichiers d'un run : le code audité se lit en entier, sur une route distincte

**Date** : 2026-10-29
**Statut** : Proposed
**Complète** : [ADR-0006](0006-un-binaire-trois-roles.md), [ADR-0009](0009-configuration-resolue-depuis-le-commit.md), [ADR-0013](0013-retention-differee-des-refs-daudit.md)

## Contexte

Le diff d'audit d'un run (§6.5) montre ce qui a changé, et seulement ça. Pour
comprendre un changement, il faut souvent lire ce qui l'entoure : le rôle qui
appelle la tâche modifiée, la variable définie dans un autre fichier, le
gabarit que la tâche rend. Aujourd'hui, ce contexte se lit hors de warden, dans
la forge, sur un commit qu'il faut retrouver à la main — et qui n'est pas
forcément celui qui a tourné.

Le rôle `api` n'a pas accès au filesystem git (ADR-0006). La seule lecture de
fichier qu'il peut demander au rôle `git` est `POST /internal/v1/blob`, bornée
à 64 Ko et pensée pour `.ansibleproxy.yml` (ADR-0009). Son commentaire excluait
explicitement d'en faire un service de fichiers.

Ce que la fiche d'un run expose déjà : à quiconque peut voir le run
(`CanViewRun`), le diff entre l'arbre du run et la branche, le run précédent ou
la branche actuelle. Le diff montre des fichiers entiers dès qu'ils sont
ajoutés ou supprimés.

## Solutions envisagées

### Option A — Élargir la borne de `/internal/v1/blob`
- **Avantage** : aucune route nouvelle.
- **Rejetée parce que** : la borne de 64 Ko protège la lecture de
  configuration, qui a lieu sur le chemin de lancement d'un run. La relever
  pour tous ferait travailler le pod `git`, en instance unique, sur des
  contenus volumineux dans un chemin qui ne les demande pas.

### Option B — Lister l'arbre sans en lire le contenu
- **Avantage** : expose la structure du dépôt, pas son contenu.
- **Rejetée parce que** : la structure seule ne répond pas au besoin, qui est
  de LIRE le contexte d'un changement.

### Option C — Deux routes internes distinctes, sous le contrôle d'accès du diff ✅ (retenue)
- **Choisie parce que** : le lecteur d'un run lit déjà des fichiers entiers de
  son arbre par le diff. Lui ouvrir le reste du même arbre ne change pas QUI
  voit le code, seulement COMBIEN. La lecture de configuration garde sa borne
  et sa route.

## Décision

1. **Deux routes sur le listener interne du rôle `git`**, jamais sur le smart
   HTTP, pour la raison de l'ADR-0007 :
   - `POST /internal/v1/tree` liste les fichiers de l'arbre d'un commit
     (chemin et taille), bornée à 20 000 entrées ; au-delà, la réponse le dit
     (`truncated`) au lieu de couper en silence ;
   - `POST /internal/v1/file` lit un fichier, borné à 1 Mo ; au-delà, un refus
     typé (`too_large`), distinct d'un chemin absent.

   `/internal/v1/blob` et sa borne de 64 Ko restent inchangés.

2. **Deux routes publiques** : `GET /runs/{run_ref}/tree` et
   `GET /runs/{run_ref}/file?path=…`. Elles lisent TOUJOURS l'arbre du commit
   audité du run (`audit_sha`) : le code qui a tourné, quelle que soit la
   comparaison affichée par le diff.

3. **Même contrôle d'accès et mêmes refus que le diff** : `requireRunAccess`,
   un run d'autrui se présente comme absent ; code purgé ou dépôt inconnu en
   410 avec son motif (ADR-0013). Un chemin absent est un 404 `path_missing`,
   un fichier trop gros un 413 `file_too_large`.

4. **Le contenu est servi en `application/octet-stream` avec `nosniff`**, et
   l'interface l'affiche par interpolation, jamais comme du HTML : il vient du
   dépôt audité et n'est pas de confiance.

## Conséquences

- La fiche d'un run devient un lecteur du dépôt à ce commit. Ce qu'un
  utilisateur pouvait lire de proche en proche par les diffs, il le lit
  maintenant directement.
- Un secret commité en clair devient lisible en entier par tous les lecteurs du
  run, là où le diff n'en montrait que les lignes changées. Le scan de secrets
  à la poussée (§5.3) reste la défense ; cette route ne l'affaiblit pas mais
  rend son échec plus visible. Les fichiers chiffrés par `ansible-vault`
  s'affichent chiffrés.
- Le pod `git` sert des lectures de 1 Mo au plus, une à la fois et à la demande
  de l'interface : rien n'est préchargé.
- Un run dont le code est purgé ne se parcourt plus, comme il ne se diffe plus.
