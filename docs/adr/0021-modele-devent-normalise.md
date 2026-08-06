# ADR-0021 — Modèle d'event normalisé, identifiants et métadonnées de run

**Date** : 2026-08-06
**Statut** : Accepted

## Contexte

Le §11 dit d'`ansible-runner` : « moteur + flux d'events JSON, **à réutiliser tel
quel** ». Cela règle la question du moteur, pas celle de la **forme de
stockage** : garde-t-on le JSON tel qu'il sort, ou une forme à nous ?

Trois décisions antérieures rendent la question tranchante.

**L'ADR-0013 a supprimé toute purge.** Ce qui entre dans la table d'events y
reste. Le choix est donc définitif dans les deux sens : ce qu'on ne stocke pas
est perdu, ce qu'on stocke mal l'est pour toujours.

**L'ADR-0014 fait coexister plusieurs versions d'`ansible-core`.** Chaque projet
déclare son image, donc épingle sa version. À un instant donné, plusieurs formats
d'events circulent dans le même journal.

**Le §6.5 pose une requête transverse** — la vue par hôte, « la question la plus
fréquente en incident » — et l'ADR-0008 lui a déjà réservé un index
`(hôte, date)`. Un index suppose une colonne.

## Solutions envisagées

### Option A — Stocker le JSON brut d'`ansible-runner`
Une ligne, un payload, aucune interprétation.
- **Avantage** : aucune perte possible, aucune traduction à écrire, aucun
  couplage à maintenir quand `ansible-core` évolue.
- **Rejetée parce que** : la requête « tout ce qui s'est passé sur cette machine »
  dépendrait de **l'union de tous les formats jamais émis**, sans date de
  péremption puisqu'on ne purge pas. L'index `(hôte, date)` n'est pas
  implémentable sur une clé JSON dont la présence varie selon la version. Le brut
  déplace le coût du jour de l'écriture vers chaque lecture.

### Option B — Normalisation intégrale, payload d'origine jeté
- **Rejetée parce que** : la perte serait définitive (ADR-0013) et le traducteur
  serait seul juge de ce qui mérite d'être gardé, à un moment où personne n'a
  encore lu ces données en incident. Et un type d'event inconnu — module tiers,
  version plus récente que le traducteur — n'aurait nulle part où aller.

### Option C — Enveloppe normalisée, payload opaque ✅ (retenue)
Les champs qu'on interroge deviennent des colonnes ; le résultat de module reste
un JSONB qu'on ne perd pas.
- **Choisie parce que** : les requêtes portent sur des colonnes stables, qui nous
  appartiennent et survivent aux versions d'`ansible-core`, sans que rien de ce
  qui a été émis ne soit jeté.

## Décision

### Le critère de partage

**L'enveloppe contient ce qui existe pour tout event, quel que soit le module et
quelle que soit la version d'`ansible-core`.** Tout le reste va dans le payload.

C'est ce critère, et non une liste, qui tranchera les cas futurs. Application
immédiate : `stdout` et `stderr` **ne sont pas des colonnes**. Ils n'existent que
pour `command`, `shell`, `raw` et `script` ; un `template` n'a pas de stdout, il a
un `diff`. Deux colonnes vides la plupart du temps, et qui ne généralisent pas.

### Ce qu'on stocke : ce que git ne conserve pas

La règle est venue en instruisant la question, et elle tranche uniformément :

| | Git le conserve ? | |
|---|---|---|
| Le contenu à un SHA | oui, définitivement | ne pas stocker |
| La position d'une branche à une date | **non** — les reflogs expirent et sont locaux au miroir | stocker |
| Ce qui n'est pas dans le dépôt | sans objet | stocker |

C'est ce qui interdit de recopier `.ansibleproxy.yml` dans la base : le SHA le
fige, et le dupliquer annulerait la séparation artefact / journal du §6.1 — donc
l'argument du §6.2, « seul le delta transite », qui justifie git.

### Tables

```
run
  id                bigint, clé technique — jointures, clé étrangère des events
  run_ref           ULID opaque — nom de ref git (ADR-0007) et identifiant exposé
  user              §10.1
  environment, inventory, playbook
  audit_sha         le commit d'audit poussé par la CLI (§5.2)
  base_sha          son parent, déjà exigé par l'ADR-0003
  mainline_ref
  mainline_sha      où pointait la branche de référence à cet instant
  flags             --check, --diff, --limit, --tags tels qu'exécutés
  state             §6.4
  termination_cause §6.4 — « interrompu » sans la cause est presque inutile
  runner_image, runner_image_digest    ADR-0014
  runner_pod        corrélation avec les logs d'infrastructure, rien de plus
  changed_count     ADR-0008, dénormalisé : métrique d'audit principale
  secrets_unmasked  ADR-0017, avec le motif
  started_at, ended_at
  artifact_pruned_at  ADR-0013, nullable, dès la première migration

event               partitionnée par date (ADR-0008)
  run_id, seq       clé ; idempotence de l'ingestion
  host              index (hôte, date)
  task_number, task_name, role
  state             ok / changed / failed / skipped / unreachable / unknown
  occurred_at
  payload           JSONB — résultat de module, stdout et stderr compris

event_diff          table séparée, contrôle d'accès distinct (ADR-0008)
```

`artifact_pruned_at` paraît gratuit et ne l'est pas : l'ajouter plus tard sur une
table pleine coûte cher, et surtout sa présence force l'interface d'audit à
traiter le cas « code purgé » tout de suite, au lieu de renvoyer un 404 le jour de
la première purge.

### `mainline_sha` corrige une promesse du §6.5

Le §6.5 promet un diff « run contre branche principale », et l'ADR-0003 a retenu
le commit chaîné précisément pour l'appuyer sur un graphe réel. **Sans cette
colonne, ce diff se calcule contre la branche telle qu'elle est aujourd'hui** — il
change donc à chaque consultation. Avec elle, le diff d'un run de mars reste celui
de mars.

Elle ne fait pas double emploi avec `base_sha` : celui-ci dit où le travail de
l'utilisateur est posé, celle-là où en était la référence. Ils coïncident quand
l'utilisateur est à jour et divergent exactement dans le cas qui intéresse un
auditeur.

Le nom de la ref est stocké avec le SHA : la branche de référence est une
configuration par environnement (§3.2), et un SHA seul cesse d'être interprétable
si elle change.

### Deux identifiants, deux rôles

`id` est un `bigint` : la table d'events le porte en clé et est partitionnée, or
8 octets contre 16 pour un UUID se paient sur la table qui grossit le plus et ne
se purge jamais.

`run_ref` est un ULID — alphabet alphanumérique majuscule, sûr comme nom de ref
git *et* dans une URL, sans les caractères que git refuse (`~ ^ : ? * [`, `..`,
tiret initial).

**La condition est dure** : un `id` séquentiel est devinable, et l'ingestion
reçoit `(run_id, seq)` d'un pod hostile. Cela ne pose aucun problème **parce que**
le lien `run_id` ↔ pod est établi côté serveur et jamais accepté depuis le corps
d'une requête. Ce n'était jusqu'ici qu'une note dans un `doc.go` ; c'est désormais
une condition de validité du schéma.

### Où se fait la normalisation

**Dans le runner.** L'agent `warden-runner` est notre binaire, injecté par
initContainer sur un `emptyDir` (ADR-0014) : l'image appartient au projet, l'agent
non. Sa version est la nôtre, et le traducteur se corrige par un redéploiement.

**Soupape obligatoire : un type d'event inconnu est stocké tel quel**, avec
`state = 'unknown'` et son payload d'origine intact. Jamais ignoré, jamais perdu.
C'est ce qui rend acceptable qu'un traducteur soit en retard sur une version
d'`ansible-core` déployée par un projet.

### Portée de la redaction

**L'ADR-0017 s'applique à tout le payload, pas aux seuls diffs.** Un
`shell: mysql -p{{ password }}` place le secret dans la ligne de commande, donc
dans un event ordinaire. La table `event_diff` a un contrôle d'accès distinct
parce qu'elle en contient *plus souvent*, pas parce qu'elle en aurait
l'exclusivité.

### La politique serveur est mise de côté

Il n'y a donc **aucun écart entre demandé et effectif** : une seule série de
colonnes de paramètres, pas de doublon. Le §5.4.2 — le résultat effectif est
journalisé, pas seulement la demande — se rouvrira le jour où la politique
existera, et il faudra alors ajouter la trace de ce qui a été filtré.

## Conséquences

- **Avantages** :
  - la vue par hôte du §6.5 est une requête sur colonnes indexées, stable quelles
    que soient les versions d'`ansible-core` en circulation ;
  - rien de ce qui a été émis n'est jeté, seule posture tenable quand l'ADR-0013
    rend toute perte définitive ;
  - le partage enveloppe / payload est énoncé par un critère, pas par une liste :
    les cas futurs se tranchent sans rouvrir la décision ;
  - le diff « run contre branche principale » devient reproductible ;
  - deux identifiants aux rôles distincts, l'un optimisé pour la table qui
    grossit, l'autre opaque pour ce qui sort du produit.

- **Contraintes** :
  - **Un traducteur à maintenir, et il est dans le domaine hostile.** Il doit être
    incapable de faire échouer un run : un event qu'il ne comprend pas devient
    `unknown`, il ne lève pas d'erreur.
  - Le payload JSONB reste non borné, comme les diffs. C'est le poste dont
    l'ADR-0013 dit que sa croissance n'est bornée « ni par le nombre de tâches ni
    par le nombre d'hôtes », et il est toujours sans mesure.
  - **Le §5.4.2 n'est pas tenu**, la politique étant mise de côté. C'est un écart
    assumé, pas un oubli, et il doit être reporté dans la spec.

- **Dette technique éventuelle** :
  - La **clé d'identité** de `run.user` n'est pas tranchée : le §10.1 prévoit OIDC
    et LDAP, donc deux formes de sujet. À décider avant la première migration,
    faute de quoi elle le sera par défaut.
  - Le mapping event `ansible-runner` → enveloppe n'est pas écrit. Il ne le sera
    correctement qu'au contact d'un vrai run : c'est un livrable de la phase 1,
    pas une conception préalable.
  - **L'allowlist de variables d'environnement du §5.4.1 est de la politique**, et
    elle est donc écartée avec elle. Si `forward_env` existe en v1, elle ne peut
    pas attendre : `ANSIBLE_STDOUT_CALLBACK` coupe le plugin d'audit en silence,
    et un produit qui ment est pire qu'un produit indisponible. À trancher au
    moment d'implémenter la CLI.
  - L'agrégation « N runs contiennent des secrets non masqués » exigée par
    l'ADR-0017 a maintenant sa colonne, pas sa vue.
