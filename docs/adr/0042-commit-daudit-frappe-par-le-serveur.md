# ADR-0042 — Le commit d'audit d'un run lancé depuis le navigateur est frappé par le serveur

**Date** : 2026-09-13
**Statut** : Accepted
**S'appuie sur** : [ADR-0025](0025-perimetre-de-la-capture-daudit.md), [ADR-0027](0027-ecriture-du-role-git.md), [ADR-0028](0028-jeton-de-push-auto-porteur.md), [ADR-0009](0009-configuration-resolue-depuis-le-commit.md)

## Contexte

`POST /runs` (`HandleCreateRun`) enregistre un run APRÈS que son commit
d'audit a été poussé : le serveur résout `refs/audit/<user.ID>/<run_ref>` et
refuse la requête si cette ref n'existe pas. Produire ce commit est un travail
de CLI — `warden submit` capture l'arbre de travail local (modifié ou non) et
le pousse sous un jeton scopé au namespace d'audit de son porteur (ADR-0028).

Un navigateur n'a ni checkout local, ni credential git, et surtout **rien à
capturer** : la seule chose qu'une page peut vouloir lancer, c'est la branche
protégée telle qu'elle est. C'est exactement le cas « zéro modification
locale » que produit déjà `warden submit` — un commit dont l'arbre est
identique à celui de sa base.

## Ce qui est décidé

### 1. `gitd` pointe une ref d'audit sur un commit EXISTANT — aucun nouveau commit

Un premier brouillon de cette conception faisait frapper par `gitd` un
nouveau commit (même arbre que la base, chaîné dessus, avec un auteur et une
date dérivée du ULID du run). C'est inutile : git admet nativement plusieurs
refs sur un même objet, et rien en aval — le lecteur de diff, `CanViewRun`,
l'audit — ne distingue « commit fraîchement frappé » de « commit existant
référencé une seconde fois ». Les deux propriétés qui comptent réellement —
un artefact permanent et une ref composée depuis l'identité authentifiée —
sont des propriétés de la REF, pas du commit.

Le nouvel endpoint interne, `POST /internal/v1/audit-ref`
(`gitwire.AuditRefRequest{Repo, Ref, CommitSHA}`), fait donc une seule chose :
`git update-ref -- <ref> <commit> ""` sur un commit qui existe déjà dans le
dépôt — celui que `MirrorPath` vient de résoudre. Aucun objet n'est transmis,
aucune capture n'a lieu.

### 2. Écriture par CAS, parce que `pre-receive` ne s'applique pas ici

ADR-0027 fait des refs `refs/audit/**` des refs write-once, appliquées par le
hook `pre-receive` — mais ce hook ne s'exécute que sous `receive-pack`, sur un
PUSH. Un `git update-ref` déclenché directement par `gitd` ne le traverse
jamais. **C'est le piège** : sans réaction, ce nouveau chemin réintroduirait la
possibilité de réécrire un enregistrement d'audit, exactement ce que
l'ADR-0027 existe pour fermer.

La parade est la primitive native de git : `update-ref <ref> <new> ""` — la
chaîne vide comme ancienne valeur signifie « cette ref ne doit pas exister » et
échoue atomiquement sinon. Sur un échec, `gitd` résout la ref existante :
- même commit que celui demandé → succès idempotent (`created: false`) ;
- commit différent → conflit (`409`, `gitwire.CodeRefExists`).

### 3. Aucun contrôle de fraîcheur requis

Le contrôle de fraîcheur de l'ADR-0027 protège contre un push qui référence
par SHA l'arbre convoité d'un autre sans l'avoir réellement transmis. Il ne
s'applique pas ici : il n'y a ni push, ni objet transmis, et surtout **aucune
valeur de la requête ne vient d'un client** — `CommitSHA` est la réponse de
`RefreshMirror`, appelée par `api` lui-même juste avant. Le seul appelant de
ce chemin est `api`, sur le listener interne, jamais un pod runner.

### 4. `api` compose la ref, le message n'existe pas, `gitd` reste domaine-nul

Même principe que `BlobRequest` pour `.ansibleproxy.yml` : `gitd` ne sait pas
ce qu'est un utilisateur ni un run, il reçoit un nom de ref littéral. La
composition (`core.AuditRefFor(user.ID, runRef)`) reste dans `core`, au même
endroit que pour `POST /runs`.

### 5. Un handler séparé, pas une branche dans `HandleCreateRun`

`HandleCreateRun` porte un commentaire qui dit que l'ordre de ses opérations
« vaut d'être lu en entier ». Y ajouter un `if lance-depuis-le-navigateur`
rendrait chacune de ses affirmations conditionnelle. `HandleLaunchRun`
(`POST /repos/{repo}/runs`) est un fichier séparé qui partage seulement ce qui
doit l'être — `resoudreEffectif` (configuration du commit → composition →
autorisation de l'environnement effectif) et `enregistrerEtDemarrer` (consigne
des secrets → création → démarrage) — extraits de `HandleCreateRun` dans un
commit à part, sans changement de comportement, avant que le nouveau handler
n'existe.

### 6. `environment` est obligatoire dans le payload de lancement

`core.Composer` peut tirer l'environnement du `default_env` du commit
(ADR-0009), ce qui est pourquoi `HandleCreateRun` autorise en deux temps. Sur
le chemin navigateur, lire cette configuration exige déjà la principale — donc
accepter un `default_env` obligerait à rafraîchir le miroir AVANT toute
autorisation, l'inverse de l'invariant que `POST /runs` tient depuis toujours
(« un compte sans droit ne fait jamais travailler le rôle `git` »). Rendre le
champ obligatoire coûte une entrée de formulaire et préserve l'invariant tel
quel.

### 7. Autorisation par droits d'environnement, pas par administration

Lancer un run depuis le navigateur est un second TRANSPORT pour l'action que
`warden submit` effectue déjà, pas une action d'administrateur. La route vit
sous `/repos/{repo}/runs`, jamais sous `/admin/`, et appelle
`autoriserLEnvironnement` (les mêmes droits par environnement, ADR-0033) — pas
`adminOuRien`. Un administrateur sans droit sur `prod` ne peut pas y lancer de
run ; un utilisateur ordinaire avec ce droit le peut.

### 8. Ad-hoc, `default_env`, et vue détail de dépôt : hors de ce premier jet

`HandleLaunchRun` n'accepte que `Kind = playbook` — `DisallowUnknownFields`
refuse un `"kind"` dans le corps plutôt que de l'ignorer, ce qui fait de cette
restriction un contrat vérifié, pas une habitude. Il n'y a pas non plus de
route `/repos/:name` de détail : l'UI n'a pas de moyen non-admin d'énumérer
les dépôts, et le formulaire de lancement se pré-remplit depuis les paramètres
de requête (bouton « Launch a run » sur la liste admin, bouton « Relaunch »
sur le détail d'un run déjà consulté par l'utilisateur).

## Solutions envisagées

### Option A — `gitd` frappe un nouveau commit (même arbre, nouveau SHA) ❌ rejetée
- **Rejetée parce que** : aucune propriété n'en dépend. Un commit
  supplémentaire par run lancé depuis l'UI n'apporte rien que la ref seule
  n'apporte déjà, au prix de primitives (`TreeOf`, `CommitTree`, signature
  déterministe) qui n'ont aucun autre usage.

### Option B — Le client (navigateur) pousse quand même ❌ rejetée
- **Rejetée parce que** : impossible sans checkout local ni credential git
  dans le navigateur — c'est le problème que cette ADR résout, pas une
  contrainte à contourner.

### Option C — `gitd` pointe la ref d'audit directement sur le commit résolu par le serveur ✅ (retenue)

## Conséquences

- Pour tout run lancé par ce chemin, `run.audit_sha == run.base_sha ==
  run.mainline_sha` : le même commit, référencé sous trois noms. Ce n'est pas
  un défaut — c'est la définition même de « lancer la branche protégée telle
  quelle » — mais ça mérite d'être lu ici plutôt que découvert en base.
- Les refs d'audit ont désormais DEUX producteurs : la CLI (un commit propre à
  chaque run, éventuellement porteur de modifications locales) et le serveur
  (une seconde référence vers un commit qui existe déjà). Rien dans l'objet
  commit ne distingue les deux — l'auteur `warden <warden@invalid>` est
  identique dans les deux cas. Le fait distinctif est `audit_sha ==
  mainline_sha`, pas quelque chose que porte le commit lui-même.
- `resoudreEffectif` et `enregistrerEtDemarrer` (`run_pipeline.go`) sont
  désormais un point de passage obligé pour toute évolution de la
  configuration effective ou de la séquence d'enregistrement — les modifier
  affecte les deux chemins de création de run à la fois, ce qui est le but.

## Ce que ça n'achète pas

- Pas de lancement ad-hoc depuis le navigateur : seul un playbook peut être
  lancé par ce chemin pour l'instant. Un bouton `ansible -m shell` depuis une
  page pose des questions de confirmation d'UX distinctes, non traitées ici.
- Pas de trace, dans le commit d'audit lui-même, de la distinction
  CLI/navigateur : envisager plus tard un trailer de commit ou une colonne
  `runs.origin` si cette distinction devient nécessaire ailleurs qu'en
  comparant des SHA.
