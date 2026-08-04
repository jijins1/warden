# ADR-0004 — Maintenance des dépôts serveur par le binaire git

**Date** : 2026-08-04
**Statut** : Accepted

> **Amendé par l'[ADR-0005](0005-binaire-git-comme-moteur-unique.md).** go-git
> ayant été abandonné, la frontière entre deux moteurs décrite dans la section
> Décision n'a plus d'objet, et le risque d'interopérabilité listé en dette
> disparaît. Tout le reste — tâches, `gc.auto=0`, verrou, délai de grâce du
> `prune`, base comme source de vérité, métrique de fraîcheur — reste en vigueur.

## Contexte

L'ADR-0002 retient go-git sur tout le chemin chaud. go-git ne fournit ni
`repack`, ni `prune`, ni `gc`, ni écriture de `commit-graph`. Or
`specs/initial.md` §6.2 exige une politique de GC et de rétention **dès le
début**, précisément à cause des « refs orphelines par milliers ».

Sans mécanisme de maintenance, trois choses cassent :

1. **La rétention est décorative.** Supprimer une ref d'audit expirée ne libère
   aucun objet et donc aucun disque : sans `prune`, l'occupation croît
   indéfiniment quelle que soit la politique décidée.
2. **Les performances se dégradent.** Chaque push dépose un pack ou des objets
   loose ; la recherche d'objet devient linéaire en nombre de packs et les
   objets loose épuisent les inodes.
3. **L'ancestry PROD coûte cher.** Sans `commit-graph`, la vérification que le
   SHA appartient à la branche protégée (§3.2) traverse tout l'historique à
   chaque run de production.

Le problème est **exclusivement côté serveur** : la CLI n'écrit que dans un
object store temporaire jeté après le run, et le runner ne fait que lire.

## Solutions envisagées

### Option B — Implémenter `repack` et `prune` au-dessus de go-git
Écrire en Go le calcul d'accessibilité, la génération de packs et leur échange
atomique.
- **Rejetée parce que** : c'est un projet à part entière, sans aucune valeur
  produit, et c'est exactement la catégorie de code dont un bug détruit
  silencieusement des artefacts d'audit — la donnée qu'on est censé garantir.

### Option C — Rotation de dépôts par fenêtre de rétention
Un bare repo par fenêtre (au mois, par exemple) ; expirer une fenêtre revient à
supprimer le dépôt entier.
- **Avantage** : aucun `prune`, suppression en O(1), aucune contention avec les
  pushes.
- **Rejetée parce que** : casse la déduplication entre fenêtres — chaque nouvelle
  fenêtre repart d'un dépôt vide, donc le premier push de chaque utilisateur
  retransmet l'arbre complet — et casse le diff « run contre run précédent » dès
  qu'il est à cheval sur deux fenêtres, alors que ce diff est la justification
  même du choix de git (§6.5).

### Option D — Sortir de git pour l'artefact (CAS)
- **Rejetée parce que** : rouvre la décision produit #3. Le problème posé ici est
  d'exploitation, pas un défaut du modèle git.

### Option A — Binaire `git` pour la maintenance serveur ✅ (retenue)
Appel au binaire `git`, hors du chemin d'un run, sur les bare repos du serveur.
- **Choisie parce que** : la fonctionnalité existe et est éprouvée, pour un coût
  de développement quasi nul ; elle s'exécute dans **notre propre image**, dont
  on fige la version. Cela ne réintroduit pas la dépendance rejetée par
  l'ADR-0002 : ce rejet portait sur le poste de l'utilisateur et sur le chemin
  chaud, ni l'un ni l'autre concernés ici.

## Décision

Le binaire `git` est autorisé côté serveur, **exclusivement pour la maintenance
planifiée**, jamais dans le traitement d'un run.

La frontière est ce qui rend la décision tenable et doit être tenue dans le
code :

| Périmètre | Moteur |
|---|---|
| Chemin d'un run : capture, push, réception, quarantaine, promotion, requêtes d'audit, matérialisation | go-git, exclusivement |
| Maintenance planifiée des bare repos | Binaire `git` |

Mise en œuvre :

- `git maintenance run` avec les tâches `incremental-repack`, `loose-objects` et
  `commit-graph`, plus un `prune` explicite lors des passes de rétention.
- `gc.auto=0` sur tous les bare repos : aucune maintenance ne doit jamais être
  déclenchée par un push.
- **Verrou par dépôt** : une seule passe de maintenance à la fois. Les pushes
  restent autorisés pendant le repack ; seule la phase de `prune` est exclusive.
- **Délai de grâce du `prune` strictement supérieur à la durée maximale d'un
  run**, `activeDeadlineSeconds` compris. C'est le paramètre le plus dangereux
  du produit : mal réglé, il supprime les objets d'un run en vol.
- La base applicative reste la **source de vérité** de ce qui doit exister. La
  maintenance ne décide rien : elle matérialise, en supprimant les refs déjà
  marquées expirées puis en purgeant.
- Version de `git` figée dans l'image serveur et vérifiée au démarrage.

## Conséquences

- **Avantages** : la politique de rétention devient réellement applicable ;
  l'ancestry PROD est bornée par le `commit-graph` ; quasi aucun code à écrire
  et aucun format à maintenir.

- **Contraintes** :
  - Le binaire `git` devient une dépendance de l'image serveur : à figer, à
    suivre côté CVE et à mettre à jour.
  - La frontière go-git / binaire doit être **contrôlée** — aucun appel à `git`
    en dehors du paquet de maintenance. À vérifier en revue, idéalement à
    outiller.
  - La maintenance introduit une interaction entre deux implémentations de git
    sur le même dépôt.

- **Dette technique éventuelle** :
  - **Interopérabilité à valider, et c'est le vrai risque.** `git maintenance`
    produit des artefacts que go-git doit continuer à relire : multi-pack-index,
    cruft packs et leurs `.mtimes`, bitmaps, `commit-graph`. En principe les
    packfiles restent lisibles isolément et ces fichiers ne sont que des couches
    d'accélération — mais cela doit être **prouvé par un test d'interopérabilité**
    (maintenance complète, puis lecture et push/fetch par go-git), pas supposé.
  - La maintenance est un job planifié, donc un point de fuite de la même famille
    que le contrôleur du §8.7 : si elle cesse de tourner, personne ne s'en rend
    compte. Prévoir une métrique d'ancienneté de la dernière passe réussie par
    dépôt, et une alerte.
