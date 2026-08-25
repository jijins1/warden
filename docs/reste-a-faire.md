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
| 1 — streaming reconnectable | 🟡 le streaming marche ; le Ctrl-C (§5.6) manque, le critère de sortie n'a jamais été exercé |
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
  Aucun filtrage nulle part. Le commentaire dit maintenant que ce sont les flags
  **demandés**, renvoie à l'écart assumé par l'ADR-0021, et ajoute ce qui
  manquait : le filtre **ne pourra jamais vivre là**, puisque ce paquet tourne
  dans le domaine hostile de l'ADR-0011 — une allowlist appliquée par le runner
  est une allowlist appliquée par le code qu'elle est censée contraindre. Le
  fond reste ouvert au §2.

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

Un seul mécanisme, quatre manques. La leçon d'Atlantis du §5.4.2 — **le fichier
client est une requête, pas une déclaration** — en était la justification
d'origine ; elle ne tient plus telle quelle depuis que la politique serveur a
été écartée (2026-08-25). Ce qui reste est plus modeste et toujours nécessaire :
sans ces morceaux, `warden submit` ne sait pas faire un `--check`.

- [ ] **`internal/config/` ne contient qu'un `doc.go`.** Le schéma strict de
  `.ansibleproxy.yml` (§5.4) n'existe pas : ni lecture, ni validation.
- [ ] **AUCUN flag Ansible n'existe côté CLI, et c'est d'abord un manque de
  FONCTIONNALITÉ.** `warden submit` n'accepte que `--repo`, `--playbook`,
  `--env`, `--inventory`, `--run-ref` et la saisie de `become` — dont `-K`,
  seule orthographe d'Ansible reprise. Aucun des sept du §5.5 —
  `--limit`, `--tags`, `--skip-tags`, `--check`, `--diff`, `-e/--extra-vars`,
  `-v` — n'est atteignable. `--check` en particulier : un dry-run est le premier
  réflexe avant de toucher à la prod, et le produit ne sait pas le faire.

  **Le fil est branché partout sauf au premier maillon**, ce qui rend le travail
  restant plus petit qu'il n'en a l'air : `client.NewRun.Flags` existe et n'est
  jamais rempli, `POST /runs` l'accepte, la colonne `run.flags` est là,
  `dispatch.Order.Flags` la transporte, et `exec.Config.Flags` la pose sur
  l'`argv`. Il manque les drapeaux et leur sérialisation.

  **Attention au piège de justification.** Le §5.5 donne deux raisons à
  l'allowlist, et elles ne tombent PAS ensemble. « Gouffre » — la compatibilité
  exhaustive est un puits de maintenance — reste vrai quoi qu'il arrive.
  « Surface d'attaque » suppose une politique serveur, écartée depuis : sans
  elle, une liste écrite par celui qu'elle contraint ne contrôle rien, et
  l'utilisateur exécute de toute façon du Python arbitraire par son playbook.
  Une liste **fixe, câblée dans le produit** reste donc le bon design — mais
  c'est un choix de PÉRIMÈTRE, pas un contrôle de sécurité, et l'ADR devra le
  dire ainsi plutôt que de laisser croire l'inverse.
- [ ] **Allowlist de variables d'environnement (§5.4.1), jamais une denylist.**
  La plomberie *aval* est déjà écrite et attend une source :
  `wire/dispatch.ForwardEnv` (`dispatch.go:125`), le rédacteur, `report`.
  `core/order.go:199` note que la CLI ne les envoie pas encore.
- [ ] **Trace du demandé vs effectif.** C'est l'écart que l'ADR-0021 a laissé
  ouvert en toutes lettres : « cette section se rouvre le jour où la politique
  existe ». Un refus est lui-même un signal d'audit, donc il se journalise.

> **À MESURER avant d'écrire quoi que ce soit** : est-ce qu'un `ansible.cfg`
> déposé dans le dépôt uploadé peut couper le plugin de callback — le cas que le
> §5.4.1 désigne comme « la variable qui tue le produit » ? `ansible-runner`
> impose son `ANSIBLE_STDOUT_CALLBACK`, et l'environnement l'emporte sur
> `ansible.cfg` dans la précédence Ansible, donc c'est probablement déjà fermé.
> **Probablement n'est pas mesuré.** Si l'audit est incoupable, ce lot change de
> justification et rétrécit beaucoup : l'utilisateur peut déjà faire exécuter du
> Python arbitraire par son playbook, donc l'allowlist ne le protège pas de
> *lui* — elle protège l'audit.

---

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
  production.

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
