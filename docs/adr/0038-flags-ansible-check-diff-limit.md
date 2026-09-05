# ADR-0038 — Trois flags Ansible, une allowlist, et le maillon qui manquait

**Date** : 2026-09-03
**Statut** : Accepted
**Amende** : [ADR-0021](0021-modele-devent-normalise.md)
**Complétée par** : [ADR-0039](0039-extra-vars-declarees-par-le-commit.md)
**S'appuie sur** : [ADR-0009](0009-configuration-resolue-depuis-le-commit.md), [ADR-0011](0011-un-binaire-par-domaine-de-confiance.md), [ADR-0034](0034-schema-du-fichier-de-projet.md)

## Contexte

Deuxième retour d'usage, en une phrase : *« je voudrais que les flags ansible
`--diff` `--check` `--limit` soient compatibles avec warden »*.

`docs/reste-a-faire.md` décrivait ce manque, et **se trompait sur son étendue** :

> **Le fil est branché partout sauf au premier maillon** : `client.NewRun.Flags`
> existe et n'est jamais rempli, `POST /runs` l'accepte, la colonne `run.flags`
> est là, `dispatch.Order.Flags` la transporte, et `exec.Config.Flags` la pose
> sur l'`argv`.

Le suivi du chemin, fait cette fois en entier, en trouve **trois** coupés :

1. **La CLI** n'a aucun drapeau — c'est celui qui était connu.
2. **`core.Run` ne portait pas `Flags`**, et ni `RunByID` ni `RunByRef` ne
   lisaient la colonne. Elle s'écrivait sans jamais se relire.
3. **`OrderBuilder.OrderFor` ne recopiait pas les flags dans l'ordre.** C'est le
   plus grave des trois, et il ne se serait vu qu'en production : un `--check`
   envoyé par un client tiers aurait été **enregistré comme demandé puis exécuté
   pour de vrai**. L'audit aurait dit « simulation » et la machine aurait changé.

Un quatrième constat, de forme : `flags` était un `json.RawMessage` recopié tel
quel du corps de la requête jusque sur la ligne de commande d'`ansible-playbook`.
Rien nulle part n'en vérifiait la forme — un objet, un nombre, une liste de
listes traversaient. Le commentaire d'`exec.Config.Flags` le disait honnêtement
depuis la §1 de `reste-a-faire.md`, et l'ADR-0021 l'assumait.

## Ce qui est décidé

**Trois flags, `--check`, `--diff` et `--limit`, portant exactement le nom
qu'`ansible-playbook` leur donne.** Une allowlist fermée les nomme, côté
serveur ; tout le reste est refusé en 400, avec un message qui publie la liste.

### Pourquoi une allowlist, alors que le playbook est déjà du code arbitraire

Ce n'est pas ce qu'elle protège, et le prétendre serait exactement le genre de
commentaire qui ment que la §1 de `reste-a-faire.md` a déjà coûté cher.
L'utilisateur qui soumet un run exécute du Python de son choix sur les machines
cibles (§2) : aucune liste de drapeaux ne lui retire ce pouvoir.

Elle protège deux choses plus étroites, et réellement :

- **L'audit.** `-e @/chemin`, `--vault-password-file`, un second `-i` : tous font
  exécuter autre chose que ce que le commit d'audit dit. Le produit vend « ce qui
  s'applique est ce qui est audité » ; un drapeau qui déplace la source échappe à
  cette phrase. C'est aussi pourquoi la valeur de `--limit` refuse la forme
  `@fichier`, et **seulement** celle-là — elle ferait lire la liste des machines
  dans un fichier du pod plutôt que dans l'inventaire audité.
- **Le périmètre.** Ce qui descend vers le domaine hostile de l'ADR-0011 doit
  avoir une forme connue avant de partir.

L'allowlist **refuse au lieu de filtrer**, et cette distinction porte une
propriété : une requête portant un drapeau non accepté est rejetée entière, donc
ce qui est enregistré dans `run.flags` est toujours exactement ce qui a été
demandé. C'est ce qui permet à `core.NewRun.Flags` de continuer à dire que
demandé et effectif coïncident — pour une raison différente de celle de
l'ADR-0021, qu'il fallait réécrire.

Elle est appliquée **deux fois** : à la création du run, et à la relecture de
`run.flags` au moment de composer l'ordre. La seconde n'est pas décorative — elle
est le dernier point avant que ces chaînes deviennent l'`argv` d'un processus du
pod, et une ligne écrite par un serveur antérieur à ce contrôle n'a traversé
aucune allowlist.

Elle vit **côté serveur** et ne pourra jamais vivre ailleurs : une allowlist
appliquée par le runner est une allowlist appliquée par le code qu'elle est
censée contraindre (ADR-0011).

### Pourquoi trois et pas sept

Le §5.5 en nomme sept, et le schéma de l'ADR-0021 en annonçait quatre
(« `flags` — `--check`, `--diff`, `--limit`, `--tags` tels qu'exécutés »).
`--tags`, `--skip-tags` et `-v` sont absents faute de demande, et s'ajouteront par une ligne dans la table — c'est la forme retenue
pour que l'ajout suivant soit assez peu coûteux pour être fait.

`-e/--extra-vars` est différent, et son absence est une **décision** : il
déplacerait la source de vérité hors du commit d'audit, ce que le produit vend
précisément de ne pas faire. Il demande sa propre ADR, pas une ligne de plus.

> **Suite (2026-09-03)** : cette ADR est écrite, c'est
> l'[ADR-0039](0039-extra-vars-declarees-par-le-commit.md), et elle **lève**
> l'objection au lieu de la contourner — les noms autorisés sont déclarés dans le
> commit, donc la source de vérité n'en sort pas. La forme LIBRE de `-e`, elle,
> reste refusée : `-e @fichier` et `-e '{"json":…}'` ne passent pas.

### Amendement (2026-09-05) : `--become`, qui réparait un demi-mécanisme

`-b/--become` et `--become-user` s'ajoutent à la table. Deux lignes, comme
annoncé — mais celui-là ne comblait pas seulement un manque de confort, il
**réparait une incohérence** que ni le §5.5 ni cette ADR n'avaient vue.

Le produit savait depuis toujours transporter le **mot de passe** d'élévation :
`-K`, une consigne éphémère, `env/extravars` dans le pod (ADR-0018). Il ne savait
pas **demander l'élévation**. Le mot de passe ne servait donc à rien tant que le
playbook ne déclarait pas `become: true` de son côté — un demi-mécanisme, dont
l'autre moitié n'était nommée nulle part. Un opérateur qui tapait `-K` en croyant
faire ce que fait `ansible-playbook -K` obtenait un run sans élévation.

**Ce n'est pas une élévation de privilège nouvelle.** Elle exige toujours que le
compte y ait droit sur la machine cible, et le playbook y exécute déjà du code
arbitraire (§2). Ce qu'elle change est le PÉRIMÈTRE : `--become` s'applique à
*toutes* les tâches, y compris celles que le playbook avait délibérément laissées
non élevées. C'est enregistré dans `run.flags`, donc lisible en audit, et c'est
précisément la raison pour laquelle ça peut être offert.

`--become-user` **seul est refusé**. Ansible n'élève que si l'élévation est
demandée : le laisser passer produirait un run tournant sous le mauvais compte
sans qu'aucun message ne le dise, et l'audit enregistrerait un drapeau sans
effet — pire que rien. Le refus est symétrique des deux côtés, CLI et serveur.

`--become-method` n'est **pas** ajouté, et c'est une décision : `sudo`, `su` ou
`doas` est une propriété de la MACHINE, pas du run. Sa place est l'inventaire,
qui est audité, et non un drapeau qui varie d'une invocation à l'autre.

Le cadrage est celui que l'ADR-0034 a déjà tranché pour le fichier de projet :
une liste fixe câblée dans le produit est un choix de **périmètre** — « la
compatibilité exhaustive avec `ansible-playbook` est un gouffre » — et non un
contrôle de sécurité.

### Pourquoi ces flags voyagent dans la requête sans contredire l'ADR-0009

L'ADR-0009 interdit qu'un champ de **configuration** transite par la requête :
ce qui s'exécute est résolu depuis le commit. Ces trois-là ne disent pas *quoi*
jouer mais *comment* jouer ce commit-là — en simulation, en montrant les diffs,
sur un sous-ensemble de machines. Ils sont de la même famille que `--env` et
`--inventory`, que la composition accepte déjà comme flags explicites.

## Solutions envisagées

### Option A — Passer les flags sans allowlist ✅ rejetée
Le plus court : la CLI compose, le serveur recopie.
- **Rejetée parce que** : c'est l'état actuel, et il laisse `flags` accepter
  n'importe quelle forme JSON. Il fallait de toute façon vérifier que c'est un
  tableau de chaînes avant de le poser sur une ligne de commande ; une fois cette
  vérification écrite, refuser ce qu'on ne sait pas nommer coûte quinze lignes.

### Option B — Une allowlist par ENVIRONNEMENT, en base
Ce que le §5.5 décrit : `prod` interdit ce que `staging` permet.
- **Rejetée parce que** : elle demande une table, une surface d'administration et
  une commande pour l'éditer, pour un produit qui n'a pas encore d'utilisateur
  ayant demandé à restreindre. Une liste fixe se remplace par une liste
  configurable sans que rien de ce qui est écrit ici change de forme — le point
  d'application reste `core.ValiderFlags`.

### Option C — Une liste fixe câblée, refusant le reste ✅ (retenue)
Trois entrées, une table, un message qui les publie.

## Conséquences

- `warden submit --check --diff --limit web-01` fonctionne, et c'était la
  demande. Le dry-run — premier réflexe avant de toucher à la prod — devient
  atteignable.
- `GET /runs/{run_ref}` publie `flags`. Un `--check` change la **lecture** du
  reste de la page : zéro changement sous `--check` ne dit pas la même chose que
  zéro changement sans.
- La CLI refuse un `--limit` mal formé **sur le poste**, avant la capture de
  l'arbre et le push. Sa regex est le jumeau de celle du serveur, et la
  duplication est délibérée — le serveur reste l'autorité, celle du poste
  n'existe que pour ne pas payer une capture avant d'apprendre la faute (§5.7).
- Le commentaire d'`exec.Config.Flags`, qui disait « ils ne sont PAS filtrés »,
  devient faux et a été réécrit. C'était vrai quand il a été écrit.
- Le seul flag hors allowlist qu'un déploiement existant pourrait perdre est
  **aucun** : personne n'en envoyait, puisque la CLI n'en émettait pas et que
  l'ordre ne les transportait pas.

## Ce que ça n'achète pas

`--limit` **n'est pas un contrôle de sécurité**, et ne le devient pas en passant
par warden. Un run limité à `web-01` reste un run dont le pod a un egress ouvert
(§10.2) et une clé d'accès valable sur tout l'environnement. Il restreint ce
qu'Ansible fait, pas ce que le pod peut joindre.

`--check` **n'est pas une garantie d'innocuité** : c'est Ansible qui décide de le
respecter, module par module, et un `command` ou un `shell` sans `check_mode:`
s'exécute quand même. Warden le transmet, il ne le fait pas appliquer.
