# ADR-0057 — Runs planifiés : une expression cron, un propriétaire, la principale telle qu'elle est

**Date** : 2026-11-05
**Statut** : Proposed
**S'appuie sur** : [ADR-0042](0042-commit-daudit-frappe-par-le-serveur.md), [ADR-0033](0033-droits-par-environnement.md), [ADR-0044](0044-environnements-proteges.md), [ADR-0053](0053-connexion-active-directory-et-droits-par-groupe.md), [ADR-0009](0009-configuration-resolue-depuis-le-commit.md)

## Contexte

Certains playbooks doivent tourner seuls, à heure fixe : une sauvegarde chaque
nuit, une vérification de conformité chaque matin. Aujourd'hui il faut un
humain devant `warden submit` ou devant le bouton « Launch a run », ou bien un
cron système qui appelle la CLI avec un jeton de session longue durée posé sur
une machine. Ce jeton contourne précisément ce que le produit contrôle.

Un run planifié n'a rien à capturer. Comme un run lancé depuis le navigateur,
il exécute **la branche protégée telle qu'elle est** au moment du déclenchement.
L'ADR-0042 a déjà tout le chemin : la ref d'audit pointe le commit de la
principale, sans push et sans nouvel objet. Il manque deux choses : une horloge,
et une identité au nom de laquelle le run part alors que personne n'est
connecté.

## Solutions envisagées

### Où vit la déclaration

#### Option A — Une clé `schedules:` dans `.warden.yml` ❌ rejetée
- Attirante parce que versionnée avec le code (ADR-0009).
- **Rejetée parce que** : il faudrait un champ « au nom de qui », et ce nom
  viendrait du commit. N'importe quel auteur d'un merge sur la principale
  pourrait écrire `run_as: alice` et faire tourner un run sous les droits
  d'Alice, sans qu'Alice l'ait demandé. Le commit dirait *quoi* exécuter et
  usurperait *qui* l'exécute. On pourrait l'éviter avec un compte de service
  unique, mais ce compte aurait alors les droits de tous les crons de tous les
  dépôts.

#### Option B — Un document GitOps du dépôt de déploiement (`schedules.yaml`) ❌ rejetée
- **Rejetée parce que** : la planification d'un playbook est une décision de
  l'équipe qui le possède, pas de l'exploitant de warden. Les documents GitOps
  existants (`grants.yaml`, `protected.yaml`) décrivent la **politique**, que
  seul l'exploitant fixe. Un cron n'est pas de la politique, c'est un usage.

#### Option C — En base, créé par un utilisateur authentifié qui en devient le propriétaire ✅ retenue
- Le propriétaire est **l'identité authentifiée de la requête de création**,
  jamais un champ du corps de la requête. C'est la même règle que la ref
  d'audit (`core.AuditRefFor(user.ID, …)`).
- **Ce que ça coûte** : la déclaration n'est pas versionnée dans git. La table
  `schedule` garde `created_by` et `created_at`, et chaque run porte
  `schedule_id`. On sait donc toujours *quel* cron a lancé *quel* run.

### Au nom de qui

#### Option D — Un compte de service `scheduler` ❌ rejetée
- **Rejetée parce que** : il concentrerait l'union des droits de tous les
  crons. Et l'audit dirait « le cron », jamais *qui* a voulu que ce playbook
  tourne en prod toutes les nuits.

#### Option E — Le propriétaire, revérifié à chaque déclenchement ✅ retenue
- Le run est enregistré **au nom du propriétaire** : `run.user_id`, ref
  d'audit `refs/audit/<owner.ID>/<run_ref>`. Il passe par les **mêmes
  contrôles** qu'un lancement depuis le navigateur, au moment où il part :
  compte non désactivé, droit vivant sur l'environnement (ADR-0033), gel de
  l'environnement (ADR-0044).
- Retirer un droit, désactiver un compte ou geler `prod` **arrête donc les
  crons concernés** au déclenchement suivant, sans autre action. Le refus est
  consigné sur le cron (`last_error`) ; aucune ligne de run n'est créée.

## Ce qui est décidé

### 1. Le lancement devient un cas d'usage du cœur

`resoudreEffectif` et `enregistrerEtDemarrer` vivaient dans l'adaptateur HTTP et
écrivaient leur refus directement dans `http.ResponseWriter`. Le planificateur
est un second adaptateur entrant : il ne peut pas les appeler. Ils deviennent
`core.Lanceur` (`ResoudreEffectif`, `EnregistrerEtDemarrer`,
`LancerSurLaPrincipale`), qui rend un `*core.RefusDeLancement` typé (forme,
interdit, introuvable, indisponible, amont, conflit, interne).

Les handlers HTTP traduisent ce refus en statut, **avec les mêmes messages et
les mêmes codes qu'avant**. Les deux chemins HTTP et le planificateur
partagent ainsi une seule séquence de contrôles, et les trois ne peuvent pas
diverger.

### 2. Expression cron à cinq champs, avec un fuseau

- `minute heure jour-du-mois mois jour-de-semaine`, avec `*`, `a-b`, `*/n`,
  `a-b/n` et les listes. Le jour de semaine va de 0 à 7, 0 et 7 valant tous deux
  dimanche. Les raccourcis `@hourly`, `@daily`, `@weekly`, `@monthly` et
  `@yearly` sont acceptés. Quand le jour du mois ET le jour de semaine sont
  restreints, l'un **ou** l'autre suffit, comme dans cron.
- Le parseur est écrit dans `core` (`core/cron.go`) plutôt qu'importé : une
  centaine de lignes testées, contre une dépendance de plus pour un besoin
  borné.
- Le fuseau est **obligatoire** et nommé (`Europe/Paris`), jamais déduit du
  serveur. Les données de fuseau sont embarquées dans le binaire
  (`time/tzdata`), ce qui ne fait plus dépendre le calcul de l'image.
- Changement d'heure : l'heure murale avance strictement. Une occurrence qui
  tombe dans le trou de mars (2 h 30 à Paris n'existe pas) est **sautée ce
  jour-là**. Une occurrence dans l'heure doublée d'octobre ne tire **qu'une
  fois**. Évitez la plage 2 h–3 h pour un cron qui ne doit jamais manquer.
- Une expression qui ne se réalise jamais (`0 0 30 2 *`) est refusée à la
  création.

### 3. Au plus une fois, sans rattrapage au-delà d'une fenêtre de grâce

- Le planificateur tourne **dans le rôle `api`**, qui détient déjà tous les
  ports du lancement. Il tourne sur **chaque réplica**. L'exclusion mutuelle
  vient de PostgreSQL : chaque tour réclame un cron échu par
  `SELECT … FOR UPDATE SKIP LOCKED`, calcule et écrit son `next_fire_at`,
  valide la transaction, et **seulement ensuite** lance le run. Il n'y a donc
  ni leader election, ni Lease, ni second processus à déployer.
- Ordre réclamer → valider → lancer, donc **au plus une fois**. Un plantage
  entre la validation et le lancement perd ce déclenchement plutôt que de le
  doubler. Pour un playbook d'exploitation, ne pas tourner une fois vaut mieux
  que tourner deux fois.
- Si l'échéance est dépassée de plus de `WARDEN_SCHEDULE_GRACE_SECONDS` (une heure
  par défaut), par exemple parce que l'API était arrêtée, le déclenchement est
  **consigné comme manqué**, sans run. Le déploiement de 3 h ne part pas à 14 h
  parce que l'API vient de redémarrer. Dans tous les cas, on ne rattrape qu'au
  plus **une** échéance, jamais une par occurrence manquée.
- L'intervalle de scrutation est `WARDEN_SCHEDULE_TICK_SECONDS` (30 s par défaut). La
  précision d'un cron est donc de l'ordre de cet intervalle.

### 4. Ce qu'un cron peut porter, et ce qu'il ne peut pas

- Il porte : dépôt, playbook, environnement (**obligatoire**, comme pour
  l'ADR-0042 et pour la même raison), inventaire, et flags, `-e` compris. Les
  valeurs des `-e` sont auditées en clair dans `run.flags` (ADR-0039) : les
  stocker dans le cron ne change rien à leur exposition.
- Il **ne porte pas** de secret : ni mot de passe d'élévation, ni mot de passe
  de connexion, ni valeur de `forward_env`. Ces valeurs ne sont jamais écrites
  en base (§5.4) et un cron n'a personne pour les taper. Un playbook qui en a
  besoin doit les tirer d'Ansible Vault (ADR-0045).
- Seul le genre `playbook` est planifiable, comme pour l'ADR-0042.
- La configuration du commit (`.warden.yml`, allowlist des `-e`) est lue **au
  déclenchement**, sur la principale du moment. Un `-e` accepté à la création
  peut donc être refusé plus tard si le projet le retire. Ce refus est consigné
  sur le cron.

### 5. Le run garde la trace de son cron

`run.schedule_id` (nullable, clé étrangère vers `schedule`) distingue un run
planifié d'un run lancé à la main. On répond ainsi à la question que pose un
run de 3 h du matin, ce qu'un SHA seul ne permettait pas.

### 6. Surface

| Route | Qui |
|---|---|
| `GET /schedules` | ses propres crons ; un administrateur les voit tous |
| `POST /repos/{repo}/schedules` | tout utilisateur qui a un droit vivant sur l'environnement visé |
| `POST /schedules/{id}/disable`, `/enable` | le propriétaire, ou un administrateur |
| `DELETE /schedules/{id}` | le propriétaire, ou un administrateur |

Un administrateur peut couper n'importe quel cron : c'est le bouton d'arrêt
de l'exploitant. Il ne peut pas en **créer** au nom d'un autre. Côté CLI, ce
sont les commandes `warden schedule list|create|disable|enable|delete`.

## Conséquences

- Un propriétaire dont le droit sur l'environnement vient **seulement d'un
  groupe de l'annuaire** (ADR-0053) voit son cron refusé dès qu'il n'a plus de
  session vivante, car un droit de groupe n'existe que par une session. C'est
  voulu : le droit de groupe borne un retrait dans l'AD à la durée d'une
  session, et le planificateur ne doit pas l'étendre. Un cron demande donc un
  **droit nominatif** sur l'environnement visé. La création ne le détecte pas
  encore : le premier déclenchement sans session le dira dans `last_error`.
- `HandleCreateRun` et `HandleLaunchRun` ne contiennent plus la séquence de
  contrôles, seulement le décodage et la traduction des refus en HTTP. Toute
  évolution de la séquence se fait dans `core/lancement.go`.
- Les crons sont planifiés en base mais exécutés par le rôle `api`. Un
  déploiement sans `api` démarré n'exécute aucun cron.

## Ce que ça n'achète pas

- **Pas de garde contre le chevauchement** : si l'occurrence précédente tourne
  encore, la suivante part quand même (ADR-0016, pas de verrou de
  concurrence). Pour un cron quotidien, le cas est théorique.
- **Pas d'écran dans l'UI** dans ce premier jet : API et CLI seulement.
  `run.schedule_id` est écrit en base mais n'est pas encore rendu par
  `GET /runs/{run_ref}` : il se lit en SQL pour l'instant.
- **Le planificateur ne tourne qu'avec le chemin d'écriture monté** (client du
  rôle `git`). Sans lui, `api` le dit au démarrage et aucun cron ne tire.
- **Pas de déclenchement manuel d'un cron** (« lancer maintenant ») : le bouton
  « Launch a run » fait déjà la même chose.
- **Pas de notification** d'un déclenchement refusé ou d'un run en échec,
  au-delà de `last_error` et des journaux. C'est le premier manque qui se
  fera sentir.
