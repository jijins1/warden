# ADR-0013 — Aucune purge avant mesure : la rétention des refs d'audit est différée

**Date** : 2026-08-05
**Statut** : Accepted

## Contexte

`specs/initial.md` §6.2 exige une politique de GC et de rétention **dès le
début**, au motif des « refs orphelines par milliers ». Le §14 laisse la question
ouverte. En l'instruisant, trois constats déplacent l'arbitrage.

**Le problème est exclusivement DEV.** En mode PROD, la CLI envoie un SHA déjà
présent sur la branche protégée (§3.2) : aucune ref d'audit n'est poussée. Le
périmètre se réduit à une ref par run DEV.

**Une ref d'audit n'est pas un îlot.** L'ADR-0003 a explicitement écarté le
commit orphelin — qui rendait « le GC trivial » — au profit du commit chaîné sur
la base. Il en découle deux régimes de coût sans rapport entre eux :

| Base du commit d'audit | Ce que la ref retient réellement |
|---|---|
| Sur le miroir (cas courant) | le seul delta ; l'ascendance est épinglée par le miroir de toute façon |
| Sur une branche locale non poussée | une chaîne privée que rien d'autre ne retient |

Le second cas est la conséquence que l'ADR-0003 a acceptée en toutes lettres.
La distribution des coûts est donc très asymétrique : **compter les refs ne
mesure rien.**

**Le volume n'est pas là.** L'ADR-0008 a fait de PostgreSQL le journal *et* le
transport des events. Or **git déduplique par blob, PostgreSQL ne déduplique
rien** — c'est exactement la propriété qui a justifié git au §6.2 (« seul le
delta transite »), et elle n'existe pas du côté journal. Relancer cinquante fois
le même playbook coûte à git quelques dizaines d'octets et au journal cinquante
copies pleines.

Ordres de grandeur par run, **non mesurés**, à lire comme un rapport et non
comme des valeurs :

| | Par run |
|---|---|
| Artefact git | le delta d'un dépôt de texte : quelques Ko |
| Journal d'events | un event par tâche × par hôte (§6.3), payload de module inclus : quelques Mo |
| Payloads `--diff` | le before/after de chaque fichier modifié : **borné par rien** |

L'ADR-0008 a déjà inscrit en dette que « le volume réel d'events par run n'est
pas mesuré » et qu'il doit l'être en phase 1. Décider aujourd'hui une fenêtre de
rétention reviendrait donc à choisir un chiffre sans mesure, sur le poste qui
représente une fraction du volume.

**Enfin, rien n'attend cette décision.** L'ADR-0004 semblait en dépendre ; il
n'en dépend pas. Ses trois tâches — `incremental-repack`, `loose-objects`,
`commit-graph` — tiennent sur des motifs de performance et sur l'ancestry PROD
du §3.2. Le `prune` n'y intervient que « lors des passes de rétention » : zéro
passe, zéro `prune`, l'ADR-0004 reste intégralement implémentable.

## Solutions envisagées

### Option A — Fenêtre TTL par défaut dès maintenant (90 jours)
Toute ref d'audit expire au bout d'une durée fixe, surchargeable par
environnement.
- **Avantage** : conforme à la lettre du §6.2 ; borne la durée de vie du puits à
  secrets du §5.3 ; la rétention s'énonce en une phrase, ce qu'un système
  d'audit doit pouvoir faire.
- **Rejetée parce que** : le chiffre serait choisi sans aucune mesure, sur le
  poste minoritaire du volume. Pire, il ne se déclencherait pour la première
  fois que quatre-vingt-dix jours après la mise en service — c'est-à-dire au
  moment précis où l'on disposerait enfin des données montrant qu'il était mal
  choisi. Une purge est irréversible : se tromper dans ce sens ne se rattrape
  pas.

### Option B — Quota plutôt que durée
Garder les N derniers runs par utilisateur, ou purger les plus anciens dès qu'un
seuil d'occupation du PVC est franchi.
- **Avantage** : borne le disque, ce qu'un TTL ne fait pas.
- **Rejetée parce que** : l'asymétrie des coûts décrite plus haut rend le nombre
  de refs sans rapport avec le disque occupé — le quota bornerait donc mal ce
  qu'il prétend borner. Et une rétention dont on ne peut pas énoncer la durée
  n'est pas auditable : « on garde jusqu'à ce que ce soit plein » ne se
  documente pas dans une procédure. Enfin, la purge anticipée frapperait pendant
  les pics d'activité, quand les runs récents comptent le plus.

### Option C — Rétention illimitée, mesure d'abord ✅ (retenue)
Aucune ref d'audit n'expire. La maintenance de l'ADR-0004 tourne sans passe de
`prune`. La décision est assortie de contreparties obligatoires et d'un critère
de réouverture daté.
- **Choisie parce que** : elle ne détruit rien tant qu'on ignore ce que la
  destruction coûterait et rendrait, elle porte l'effort sur le poste qui
  représente réellement le volume, et elle referme la question du §14 avec une
  réponse datée plutôt que de la laisser ouverte indéfiniment.

## Décision

**Aucune purge de refs d'audit.** La fenêtre est illimitée jusqu'à ce que la
phase 1 du §12 fournisse la mesure à laquelle l'ADR-0008 s'est déjà engagé.

Ce n'est tenable qu'avec trois contreparties, qui ne sont pas facultatives : la
décision n'est acquise qu'une fois qu'elles existent.

**1. Le cycle de vie des refs de namespace n'est pas de la rétention.**
`refs/namespaces/runs/<run-id>/…` (ADR-0007) est supprimée dès que le run atteint
un état terminal, avec un balayeur pour rattraper les orphelins d'un crash —
même motif et même mécanisme que le reconciler du §8.7, et avec le sélecteur
obligatoire de l'ADR-0010, jamais un parcours nu de toutes les refs. Une ref de
namespace qui fuit épingle les objets de son run pour toujours et neutraliserait
silencieusement toute politique ultérieure. Cette suppression est due quelle que
soit la politique de rétention, **y compris « aucune »**.

**2. L'occupation est mesurée et alertée.**
Le PVC du rôle `git` est unique et RWO (ADR-0006) : disque plein ne signifie pas
« plus d'audit », mais **plus aucun run dans l'organisation**. Sont exposés par
dépôt : taille occupée, nombre de refs d'audit, et ancienneté de la dernière
passe de maintenance réussie — cette dernière étant déjà exigée par l'ADR-0004.

Le seuil d'alerte se règle sur le délai nécessaire pour agir — étendre le volume
(la classe de stockage retenue autorise l'expansion) ou décider enfin d'une
politique — et non sur un pourcentage arbitraire.

Côté journal, même exigence : taille des partitions d'events, et **séparément**
celle de la table des payloads `--diff`, qui est la seule dont la croissance
n'est bornée ni par le nombre de tâches ni par le nombre d'hôtes.

**3. Deux risques sont acceptés, et enregistrés ici plutôt que découverts.**
- **Le puits à secrets du §5.3 n'expire jamais.** Les refs d'audit contiennent
  les commits locaux non poussés de l'utilisateur (ADR-0003). Le scan de secrets
  à la quarantaine devient donc la seule barrière : il n'y a plus de filet
  temporel derrière lui.
- **La croissance est monotone et sans limite haute.** Le seul mécanisme qui
  l'arrête est humain, déclenché par l'alerte du point 2.

**Ce que la décision n'autorise pas** : écrire un schéma qui rende la purge
coûteuse plus tard.

- La table d'events reste **partitionnée par date dès la première version**
  (ADR-0008). La partition est ce qui permettra de détacher une période en O(1)
  au lieu de la supprimer ligne à ligne.
- La ligne de run porte une colonne nullable `artifact_pruned_at` **dès la
  première migration**. La remplir un jour est gratuit ; ajouter la colonne sur
  une table pleine ne l'est pas. Surtout, sa présence oblige l'interface d'audit
  à traiter le cas « code purgé » tout de suite, au lieu de renvoyer un 404 le
  jour de la première purge.

**Critère de réouverture** : la fin de la phase 1, quand la mesure du volume
d'events par run existe. La question se réinstruit alors sur les trois postes
ensemble — refs d'audit, events, payloads `--diff` — et non sur le seul premier.

Un point relevé en instruisant celle-ci est à reprendre à ce moment-là, parce
qu'il corrige l'ADR-0004 : le délai de grâce du `prune` y est fixé « strictement
supérieur à la durée maximale d'un run ». **Le plancher réel est plus haut.**
Pendant la quarantaine du scan de secrets (§5.3), les objets tout juste poussés
ne sont accessibles depuis aucune ref, par construction. Un `prune` dont la
grâce est plus courte que la quarantaine supprimerait un push en cours
d'analyse. Le plancher est donc `max(durée de run, durée de quarantaine)`, et
l'ordre des opérations est imposé : supprimer la ref, attendre la grâce, purger
— jamais l'inverse.

Deuxième point à reprendre : **une purge non journalisée est indistinguable
d'une falsification.** Le §6.2 signale déjà qu'un dépôt git n'est pas
tamper-evident face à un administrateur ; si la suppression des refs n'est pas
elle-même auditée — qui, quand, en application de quelle règle — le produit
offre le déni parfait. Toute politique future doit donc journaliser ses propres
suppressions dans le journal applicatif.

## Extension au journal, même jour

La décision ci-dessus ne portait que sur les refs d'audit, la dette ci-dessous
renvoyant le journal à « le sujet suivant ». Ce sujet est tranché dans le même
sens, et pour les mêmes raisons : **aucune rétention sur la table d'events ni
sur celle des payloads `--diff`**, jusqu'à la même mesure.

Le mécanisme de sortie est d'ailleurs déjà en place : la partition par date de
l'ADR-0008 permettra de détacher une période en O(1) le jour où une politique
existera. Rien n'est donc rendu plus coûteux par l'attente.

**Mais une chose change de nature, et c'est le point important.** La table
`--diff` contient les valeurs de secrets rendues (§6.5), pas seulement du code.
Une rétention illimitée en fait un magasin de secrets permanent.

Conséquence directe : **la question §14 sur la redaction des `--diff` cesse
d'être une question parmi d'autres pour devenir le seul contrôle porteur.** Tant
qu'on purgeait, une redaction imparfaite avait une date de péremption. Sans
purge, ce qui entre dans cette table y reste, et la redaction à l'écriture est
la seule barrière. Elle passe donc en tête des questions ouvertes du §14.

## Conséquences

- **Avantages** :
  - aucune purge ne peut détruire un artefact d'audit avant qu'on sache ce
    qu'elle coûte et ce qu'elle rend ;
  - l'effort porte sur le poste qui représente le volume, le journal, plutôt que
    sur celui qui en représente une fraction ;
  - la question du §14 se referme avec une réponse datée et un critère de
    réouverture, au lieu de rester ouverte par défaut ;
  - l'ADR-0012 peut trancher `pg_partman` sans attendre : sa réserve portait
    explicitement sur le cas « si la politique de rétention devient non
    triviale », or elle est nulle.

- **Contraintes** :
  - **Écart assumé à la lettre du §6.2**, qui demandait la politique dès le
    début. L'esprit est tenu — les refs ne s'accumulent pas sans surveillance,
    et les refs de namespace ne s'accumulent pas du tout — mais la lettre non,
    et cet écart doit être reporté dans la spec.
  - **La décision a une date de péremption.** Sans la réouverture prévue,
    « différé » devient « oublié », ce qui est précisément le mode de panne que
    le §6.2 anticipait.
  - Le repli en cas d'urgence — disque plein avant qu'une politique existe —
    n'est pas outillé. Ce serait une opération manuelle sur un système d'audit,
    donc la situation à ne pas atteindre. C'est ce que l'alerte du point 2 doit
    rendre impossible.

- **Dette technique éventuelle** :
  - Les métriques du point 2 ne sont pas écrites. Tant qu'elles n'existent pas,
    la décision n'a pas ses contreparties : elle est conditionnelle, pas acquise.
  - ~~Rien n'est décidé pour la rétention du journal ni pour celle des payloads
    `--diff`.~~ → tranché le même jour dans le même sens, voir la section
    « Extension au journal » ci-dessus. Il en résulte une dette déplacée, pas
    supprimée : la redaction des `--diff` devient le seul contrôle porteur sur
    la donnée la plus sensible du produit.
