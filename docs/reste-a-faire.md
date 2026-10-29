# Reste à faire

Inventaire de ce qui manque, tenu à jour à la main. Établi le 2026-08-24 en
relisant le code, pas la mémoire : chaque case cochable renvoie à un fichier et
une ligne, et les affirmations vérifiées sont distinguées de celles qui restent
à mesurer.

Ce fichier n'est pas une feuille de route — il ne dit pas dans quel ordre, il dit
**ce qui est ouvert**. La spécification (`specs/initial.md`) reste la référence
de ce qu'on veut ; les ADR restent la référence de ce qu'on a tranché.

## État du plan d'implémentation (§12)

| Phase | État |
|---|---|
| 0 — mesure du cold start | ✅ ADR-0015 |
| 1 — streaming reconnectable | 🟡 le streaming marche et `submit` s'y attache enfin (ADR-0040) ; le Ctrl-C (§5.6) et les hôtes en vol (§5.8) manquent |
| 2 — transport git + audit | ✅ vérifié sur le cluster (ADR-0025, ADR-0031) |
| 3 — pré-chauffage | ❌ rien |
| 4 — auth, authz, isolation | 🟡 comptes locaux, droits, enrôlement, durcissement du pod ✅ — allowlists, périmètre réseau, OIDC, Vault ❌ |
| 5 — mode PROD | ❌ |
| 6 — UI d'audit | ❌ |

---

## 1. Contrats faux — ✅ FAIT (2026-08-24)

Le plus petit lot du fichier, et celui traité en premier : un commentaire qui
ment coûte plus qu'un manque déclaré, parce qu'on ne le relit pas.

- [x] **`exec.Config.Flags` prétendait être filtré et ne l'était pas.**
  `exec.go` énonçait « les flags EFFECTIFS, déjà filtrés par la politique
  serveur (§5.5) ». Suivi du chemin : `api/run_handler.go:27` accepte un
  `json.RawMessage`, `:206` le recopie tel quel, et il ressort sur l'`argv`.
  Aucun filtrage nulle part. Le commentaire disait alors que ce sont les flags
  **demandés**, renvoyait à l'écart assumé par l'ADR-0021, et ajoutait ce qui
  manquait : le filtre **ne pourra jamais vivre là**, puisque ce paquet tourne
  dans le domaine hostile de l'ADR-0011 — une allowlist appliquée par le runner
  est une allowlist appliquée par le code qu'elle est censée contraindre.

  Mis à jour le 2026-09-03 : l'allowlist existe (ADR-0038), côté serveur comme
  ce commentaire l'exigeait. Il a donc été réécrit une seconde fois — il était
  devenu faux dans l'autre sens, ce qui est le prix d'un commentaire exact.

- [x] **`gitd/doc.go` et `gitclient/client.go` disaient le claim « à écrire ».**
  Faux depuis la phase 2. La relecture a trouvé une **seconde** erreur dans la
  même phrase, qu'on ne cherchait pas : le claim ne vit pas dans le contrôleur
  comme le §8.4 le laissait attendre, mais dans le rôle **`api`** — c'est
  `POST /runs` qui démarre un run. `CreateRunNamespace` est appelée par
  `core.RunStarter` (`core/start.go:155`), `IssueFetchGrant` par
  `core.OrderBuilder` (`core/order.go:151`), donc à la livraison de l'ordre et
  jamais avant, ce qui est la propriété que l'ADR-0018 achète. Ces deux
  commentaires portent l'avertissement de l'ADR-0026 sur l'imbrication des
  espaces de noms : les laisser faux, c'était risquer qu'on cesse de les croire.

- [x] **`api/auth.go` — `TODO: users who own the hosts a run touched.`** Un TODO
  nu ne dit pas si c'est un manque ou une décision. Remplacé par la règle réelle
  — propriété du run, et rien d'autre, pas même l'administrateur — et par ce que
  son absence tient : c'est **précisément pourquoi** `HandleListByHost` filtre
  son résultat sur l'appelant au lieu de servir l'historique complet de l'hôte.
  L'élargir suppose une notion de propriétaire d'hôte, qui n'existe pas.
  Vérifié au passage, puisque c'était l'inquiétude naturelle : `CanViewHost`
  rend `user != nil`, mais le filtrage a bien lieu ensuite
  (`runs_handler.go:112-118`) — **pas de fuite**.

---

## 2. Flags, variables et fichier projet — §5.4, §5.4.1, §5.5

Le fichier projet et le forward de variables sont **FAITS** (2026-08-26,
[ADR-0034](adr/0034-schema-du-fichier-de-projet.md)). Les flags Ansible le sont
pour six d'entre eux (2026-09-03 et 2026-09-05,
[ADR-0038](adr/0038-flags-ansible-check-diff-limit.md) et
[ADR-0039](adr/0039-extra-vars-declarees-par-le-commit.md)) ; ce qui reste ouvert
est le PÉRIMÈTRE de la liste, plus son absence.

- [x] **`internal/config` porte le schéma strict de `.ansibleproxy.yml`.** Six
  champs, plafond de taille, champs inconnus refusés, clés dupliquées refusées,
  **ancres et alias refusés**. Ce dernier point a demandé un parcours de
  `yaml.Node` : `KnownFields` ne voit PAS un alias dont l'ancre porte sur un
  champ connu — mesuré, pas déduit.
- [x] **La configuration est lue depuis le commit** (ADR-0009), par un endpoint
  `gitwire.BlobPath` sur le listener interne. `CodeUnknownPath` distingue
  « commit absent » de « fichier absent » : le premier est une incohérence, le
  second un état ordinaire.
- [x] **`forward_env` fonctionne de bout en bout.** Noms du commit ∩ valeurs du
  shell, consigne éphémère consommée à la livraison, `env/envvars` en 0600.
- [x] **Le rédacteur de l'ADR-0017 est branché.** Il était `nil` : les valeurs
  transmises seraient ressorties EN CLAIR dans les events, définitivement
  (l'ADR-0013 a supprimé toute purge).
- [x] **L'allowlist de variables (§5.4.1) existe, et sa justification est
  corrigée.** La mesure a montré que le §5.4.1 se trompe de coupable :
  `ANSIBLE_STDOUT_CALLBACK` est écrasée sans condition par ansible-runner, donc
  inoffensive. Le vrai tueur est `ANSIBLE_CALLBACK_PLUGINS`, qui préfixe le
  dossier de l'utilisateur devant celui d'ansible-runner et fait charger un faux
  `awx_display`. Voir l'ADR-0034 pour le mécanisme exact.
- [x] **`ValidateEnvironment` est appelée sur `POST /runs`.** Elle ne l'était
  nulle part sur ce chemin : ce qui fermait la porte était INDIRECT — un nom
  tordu n'a pas de droit associé — et l'environnement compose désormais un
  chemin d'inventaire.
- [x] **`runner_image` ne transite plus par la requête** (ADR-0009, ADR-0014).

- [x] ~~**Aucune commande pour déclarer un dépôt.**~~ Résolu (2026-09-02) :
  `warden repo declare` déduit le descripteur du dépôt courant, l'affiche et le
  fait confirmer. Le `curl` de la prise en main a disparu.
- [x] ~~**L'aide est un mur de cent lignes.**~~ Résolu (2026-09-02) : l'arbre de
  `internal/cli/internal/command` a remplacé `texteUsage`, et deux tests
  vérifient que les drapeaux documentés existent et que les drapeaux réels sont
  documentés.

- [x] ~~**AUCUN flag Ansible n'existe côté CLI.**~~ Résolu pour trois d'entre eux
  (2026-09-03, [ADR-0038](adr/0038-flags-ansible-check-diff-limit.md)) :
  `warden submit --check --diff --limit <motif>`, sous le nom qu'
  `ansible-playbook` leur donne.

  **Cette entrée se trompait sur l'étendue du manque, et l'erreur mérite d'être
  gardée.** Elle disait « le fil est branché partout sauf au premier maillon ».
  Le suivi refait en entier en a trouvé **trois** coupés :

  1. la CLI, seul connu ;
  2. `core.Run` ne portait pas `Flags`, et ni `RunByID` ni `RunByRef` ne lisaient
     la colonne — elle s'écrivait sans jamais se relire ;
  3. `OrderBuilder.OrderFor` ne recopiait pas les flags dans l'ordre remis au
     pod. **Le pire des trois** : un `--check` envoyé par un client tiers aurait
     été enregistré comme demandé puis exécuté pour de vrai. L'audit aurait dit
     « simulation », la machine aurait changé, et rien n'aurait signalé l'écart.

  La leçon est celle de la §1 : un contrat qu'on n'a pas suivi jusqu'au bout se
  raconte plus court qu'il n'est.

  Une **allowlist** existe désormais (`core.ValiderFlags`), appliquée deux fois —
  à la création et à la relecture avant livraison. Elle REFUSE au lieu de
  filtrer, donc `run.flags` dit toujours ce qui a été demandé. Ce qu'elle
  protège est l'audit et la forme, pas les machines cibles : voir l'ADR-0038.

- [x] ~~**`-e/--extra-vars` déplacerait la source de vérité hors du commit.**~~
  Objection levée le 2026-09-03
  ([ADR-0039](adr/0039-extra-vars-declarees-par-le-commit.md)) par le retour
  d'usage, qui a répondu au lieu de contourner : *« on peut faire comme les
  variables d'environnement et mettre une liste blanche »*. Un `extra_vars:` dans
  le fichier de projet déclare les NOMS, la requête n'apporte que les VALEURS —
  le commit décrit toujours l'espace de ce qui peut varier.

  **Divergence assumée avec `forward_env`** : les valeurs d'`extra_vars` sont
  AUDITÉES, pas consignées. « Quelle version a été déployée » est la question
  qu'un audit existe pour répondre, et masquer la valeur la rendrait invisible
  dans le `--diff` où on la cherche. La contrepartie est écrite partout où
  quelqu'un pourrait y mettre un mot de passe.

- [x] ~~**`-K` transportait le mot de passe d'élévation sans pouvoir demander
  l'élévation.**~~ Résolu le 2026-09-05 : `-b/--become` et `--become-user`
  (amendement de l'[ADR-0038](adr/0038-flags-ansible-check-diff-limit.md)).

  Ce n'était pas un manque de confort mais une INCOHÉRENCE, et elle n'était
  nommée nulle part : `warden submit -K` ne faisait pas ce que fait
  `ansible-playbook -K`, puisque rien n'activait `become`. Le mot de passe
  partait, arrivait dans `env/extravars`, et ne servait à rien tant que le
  playbook ne déclarait pas `become: true` lui-même.

- [x] ~~**Les deux derniers flags du §5.5.**~~ Résolu le 2026-10-29
  (amendement de l'[ADR-0038](adr/0038-flags-ansible-check-diff-limit.md)).
  `--tags` et `--skip-tags` sont ouverts côté serveur, dans `warden submit` et
  dans le formulaire du navigateur, et la fiche du run les met en avant. Pas
  dans `warden run` : un module ad-hoc n'a pas de tâches à étiqueter, et le
  serveur les refuse sur un run ad-hoc.

- [x] ~~**`-v` n'était disponible nulle part.**~~ Résolu le 2026-09-19
  (amendement de l'[ADR-0038](adr/0038-flags-ansible-check-diff-limit.md)).
  Ouvert des quatre côtés à la fois : allowlist serveur, `warden submit`,
  `warden run` et le formulaire du navigateur.

  Cette ligne annonçait « une ligne dans la table » et se trompait :
  `ansible-playbook` donne QUATRE noms à un seul réglage, et l'unicité de la
  table portait sur le nom du drapeau. `-v -vvv` serait passé — Ansible
  n'appliquant que le dernier, `run.flags` en gardant deux. D'où le champ
  `famille`, qui n'existe que pour ça.

- [x] ~~**Ansible Vault n'a aucune réponse.**~~ Résolu le 2026-09-26
  ([ADR-0045](adr/0045-vault-inventory-cle-ansible-vault.md)). Composant
  `vault-inventory`, troisième famille de secrets aux côtés des credentials
  d'accès et des variables transmises : une clé par environnement, jamais
  révélée à un humain — le seul chemin d'interaction est
  `warden vault encrypt`, qui chiffre côté serveur et rend un bloc `!vault |`
  à coller dans le dépôt. `--vault-password-file` reste absent de
  l'allowlist de l'ADR-0038 ; il est ajouté par le runner lui-même, à partir
  d'un fichier qu'il vient d'écrire, jamais depuis un flag fourni par
  l'appelant.

- [x] ~~**`warden submit` n'affichait pas le déroulement du run.**~~ Résolu le
  2026-09-08 ([ADR-0040](adr/0040-submit-suit-le-run.md)). Le suivi existait, il
  fonctionnait, et `warden enroll` s'en servait — `submit` ne s'y attachait
  jamais. Le maillon n'avait pas été posé, et TOUT autour supposait qu'il
  l'était : le doc de `stream.New`, le §5.6, le §5.7, et un paquet `render` dont
  le `doc.go` annonçait ce rôle sans une ligne de code.

- [ ] **Le Ctrl-C du §5.6 n'annule rien.** Il DÉTACHE, et le message le dit —
  c'est le minimum honnête. L'annulation demande un endpoint que le serveur
  n'expose pas : les routes de run sont `GET /runs`, `GET /runs/{ref}`,
  `.../stream`, `.../diff` et `POST /runs`, il n'y a rien à appeler. L'escalade à
  trois niveaux du §5.6 suppose en plus de porter le signal jusqu'au processus
  du pod, puis de supprimer le pod : c'est du travail de contrôleur, pas de CLI.

- [ ] **Les hôtes en vol du §5.8.** `render/doc.go` les annonce comme « un
  différenciateur produit, pas un confort : c'est ce qu'Ansible ne montre pas et
  qu'on cherche pendant un incident ». Ce qui est affiché aujourd'hui est ce qui
  est TERMINÉ, tâche par tâche. Savoir sur quelle machine un run BLOQUE reste
  hors de portée.

- [ ] **L'allowlist par ENVIRONNEMENT du §5.5.** Ce qui existe est une liste
  fixe, la même pour tous. `prod` ne peut pas interdire ce que `staging`
  autorise. Le point d'application est déjà le bon — `core.ValiderFlags` — donc
  la rendre configurable ne déplace rien de ce qui est écrit.

- [ ] **Trace du demandé vs effectif.** L'écart que l'ADR-0021 a laissé ouvert.
  Partiellement comblé : les NOMS des variables retenues sont journalisés à la
  création du run, et `run.environment` / `run.inventory` portent les valeurs
  effectives. Ce qui manque est la trace de ce qui a été **écarté**, un refus
  étant lui-même un signal d'audit.

- [ ] **Bascule de `WARDEN_EXIGER_CONFIG`.** Le refus des commits sans
  `.ansibleproxy.yml` (ADR-0009) est écrit et testé, mais le défaut TOLÈRE. La
  bascule demande d'abord que tous les dépôts servis aient committé leur
  fichier. L'interrupteur est destiné à disparaître avec sa migration.

- [ ] **Versionnage du schéma**, explicitement reporté par l'ADR-0034. Le refus
  des champs inconnus en tient lieu en attendant. Obligatoire avant que des
  dépôts tiers dépendent du format.

## 3. Phase 3 — pré-chauffage

L'argument d'adoption du §5.1 : **11–12 s aujourd'hui** contre les **1,1 s**
mesurées par l'ADR-0015, qui conclut que le pré-chauffage est *obligatoire* et
le zygote optionnel.

Le socle est posé et il n'attend qu'un appelant :

- [ ] **`ClaimPod` n'a aucun appelant de production** — `store/pods.go:155`,
  présent uniquement dans `pods_integration_test.go`. Le claim atomique et le
  `ErrNoWarmPod` sont écrits et testés.
- [ ] **Personne ne crée de pod de pool.** `core.NewPod.RunID`
  (`pod_registry.go:12-24`) documente déjà le cas « zéro » qui l'attend, et
  pourquoi le pod à la demande ne passe pas par `ClaimPod`.
- [ ] TTL adaptatif, reaper, métrique de hit rate (§8.3).
- [ ] **Critère de sortie du §12 :** surcoût du 2ᵉ run conforme, **zéro pod
  orphelin après un crash du serveur**.
- [ ] Zygote (§8.5) — reste optionnel, ne pas ouvrir tant que le pré-chauffage
  seul n'a pas été mesuré en place.

---

## 4. Expérience CLI

- [ ] **Ctrl-C, escalade à trois niveaux (§5.6).** Le §5.6 dit « doit être
  irréprochable » et c'est le geste le plus fréquent en usage réel. La moitié
  aval existe : `exec.DelaiApresSignalParDefaut` (30 s) est écrit et commenté
  côté runner. Manquent les trois niveaux SIGINT → SIGTERM → suppression du pod,
  et surtout **l'affichage explicite à chaque étape** — sans lui l'utilisateur
  martèle sans savoir si son signal est parti.
- [ ] **Hôtes en vol (§5.8).** `internal/cli/internal/render/` ne contient qu'un
  `doc.go`, qui énonce pourtant que c'est « un différenciateur produit, pas un
  confort ». Effort faible, valeur très visible sur les gros inventaires — et
  Ansible en CLI ne donne pas l'information.
- [ ] **`task_timeout` par défaut, configurable par environnement (§5.8,
  corollaire serveur).** Il n'existe aujourd'hui qu'une échéance globale de run
  (`WARDEN_RUN_DEADLINE_SECONDS`), qui tue le pod entier. Un hôte bloqué en
  D-state gèle le play sans qu'aucun timeout ne s'applique à la tâche.
- [ ] **Signalement des runs abandonnés ET en cours (§6.6, étendu par
  l'ADR-0016).** Rien dans la CLI ne prévient l'utilisateur suivant sur le même
  inventaire. C'est la contrepartie explicitement promise en échange de l'absence
  de verrou de concurrence : « l'information est donnée avant de partir, sans
  blocage ni confirmation ». Elle n'est pas donnée.
- [ ] **Refus des playbooks interactifs (§9.3).** `vars_prompt`, `pause`,
  prompts `become` demandent un vrai PTY. La spec recommande de les interdire
  avec un message clair côté CLI ; aucune occurrence de `vars_prompt` dans le
  code.

---

## 5. Périmètre réseau — 🚧 BLOQUÉ sur une décision d'infrastructure

**Le vrai trou.** Le §10.2 fait du périmètre réseau le *seul* contrôle
d'autorisation réel de la v1, et l'ADR-0030 le nomme comme le risque principal
de son lot. L'egress du runner est ouvert en grand : `0.0.0.0/0`, tous ports,
dans `charts/warden/templates/networkpolicy.yaml`.

Ce que ça emporte, déjà écrit dans le manifeste : le port d'écriture du rôle
`git` (8082) redevient joignable, l'API (8080) et l'API interne de `git` (8081)
aussi, et l'API Kubernetes n'est plus arrêtée que par
`automountServiceAccountToken: false`. Ce qui tient : `ingress: []`.

- [ ] **Décision attendue : CNI capable d'egress par FQDN (Cilium `toFQDNs`), ou
  énumération de CIDR par run ?** Mesuré sur ce cluster : k3s avec le trio par
  défaut, aucun pod Cilium ni Calico, donc kube-router — qui n'implémente que la
  NetworkPolicy standard, dont aucun des trois sélecteurs de destination ne
  résout un nom.
- [ ] Refermer se fait en **supprimant une seule règle** : les règles étroites
  ont été conservées au-dessus et redeviennent alors le périmètre effectif.
- [ ] `environment_grant` existe et applique déjà l'accès aux **clés** ; c'est ce
  qui reste vrai sans le réseau. Le filtrage côté serveur est du « confort, pas
  de la sécurité » (§10.2), et ça doit rester écrit tel quel.

---

## 6. Exploitation et durcissement

- [ ] **Pas de verrouillage de compte** après des échecs répétés. Il y a le
  plancher de 250 ms et les 4 tentatives simultanées (`core/login.go`), qui
  bornent le débit sans jamais fermer un compte.
- [ ] **Les sessions mortes ne sont jamais purgées.** La table ne fait que
  croître ; aucune correction n'en dépend, seulement la taille.
- [ ] **Pas de leader election par Lease** (ADR-0006). Le contrôleur tourne à
  `replicas: 1` avec `strategy: Recreate`, et toutes ses actions sont
  idempotentes — deux contrôleurs feraient du travail en double, pas du travail
  faux. À ajouter quand le coût du doublon comptera, pas avant.
- [x] ~~**Le document de droits est au mauvais endroit.**~~ Résolu par la chart
  (2026-08-25) : les droits sont devenus `grants.environments` dans
  `values.yaml`. Le produit livre le mécanisme, le dépôt de déploiement porte le
  contenu — ce qui était exactement la séparation manquante. La carte vide ne
  rend AUCUN ConfigMap, parce qu'un document vide serait refusé par le
  planificateur et journalisé en erreur toutes les dix secondes.
- [ ] **Pas de rotation de clé outillée.** Dette déjà nommée par l'ADR-0020, que
  l'ADR-0030 a rendue plus chère en posant des `authorized_keys` sur une flotte.
  Minimum : que `warden key show` donne de quoi la faire à la main.
- [ ] **Pas de désenrôlement.** Retirer une machine retire sa ligne, pas la clé
  publique de son `authorized_keys`.
- [ ] **TOFU à la première prise de contact** (ADR-0030). Le premier enrôlement
  sans `host_key` déclarée fait confiance à ce qui répond. `warden host declare`
  avec `host_key` est le chemin qui l'évite, et devrait être préféré en
  production. **L'ADR-0036 a rendu cette dette plus chère** : une adresse peut
  désormais être un nom, dont la résolution a lieu dans le pod — l'empreinte
  épinglée est ce qui reste pour savoir à quoi on parle.
- [ ] **Rien ne vérifie qu'un nom enrôlé résout encore depuis le cluster**
  (ADR-0036). Ça ne se découvre qu'au run suivant. L'équivalent d'un
  `ansible -m ping` lancé depuis un pod reste à écrire.

---

## 7. Critères de sortie jamais exercés

Des cases que rien n'interdit de cocher aujourd'hui — il suffit de faire l'essai.
Tant qu'elles ne le sont pas, la phase correspondante est **présumée** finie.

- [ ] **Phase 1 :** un run de 10 minutes survit à **trois coupures réseau
  client** et à **un redémarrage du serveur**, sans perte d'event.
- [ ] **Phase 2 :** les cas **OOM** et **node drain**. Le pod tué et le dépassement
  d'échéance ont été vérifiés sur le cluster (ADR-0031) ; les pods runner n'ont
  pas de limite mémoire, donc l'OOM n'a jamais été déclenché.
- [ ] **ADR-0030 :** `warden enroll` vers une VM jetable puis `warden submit`
  d'un `ping` sur cet hôte. C'est le contrôle qui prouve la chaîne, et le genre
  de contrôle qui a trouvé les défauts que les tests n'avaient pas vus.

---

## 8. Phases 5 et 6 — pas ouvertes

Listées pour mémoire, aucune n'est commencée.

**Phase 5 — mode PROD (§3.2, §3.3)**
- [ ] Vérifier que le SHA appartient à la branche protégée
- [ ] Politique par environnement, `allowed_overrides`
- [ ] Approbation, `--check` préalable

**Phase 6 — UI d'audit (§6.5)**
- [ ] Liste filtrable, métrique `changed`
- [ ] **Vue par hôte** — « tout ce qui a touché `db-prod-02` ce mois-ci » est la
  question la plus fréquente en incident
- [ ] Diffs commit ↔ branche et run ↔ run précédent — **la justification du
  choix de git**, ce que ni AWX ni Rundeck ne savent faire
- [ ] Diff de configuration avec redaction (ADR-0017)

**Authentification (§10.1)**
- [ ] OIDC / LDAP. Les comptes locaux de l'ADR-0032 sont assumés comme une
  couche, pas comme une dette — l'OIDC s'ajoutera à côté, il ne les remplacera
  pas.

**Autorisation (§10.2)**
- [ ] **Groupes.** Le §10.2 dit `utilisateur|groupe` ; seul l'utilisateur est
  traité, comme `is_admin` l'a fait avant lui.
- [ ] **Certificats SSH éphémères** (contrôle n°1 du §10.2). Sans implémentation,
  Vault n'existant pas dans l'environnement (ADR-0020).
