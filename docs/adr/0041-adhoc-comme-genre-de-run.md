# ADR-0041 — L'ad-hoc comme troisième genre de run, sans allowlist de modules

**Date** : 2026-09-13
**Statut** : Accepted
**S'appuie sur** : [ADR-0018](0018-canal-de-dispatch-sse-vers-le-runner.md), [ADR-0030](0030-enrolement-des-machines.md), [ADR-0038](0038-flags-ansible-check-diff-limit.md)

## Contexte

Un utilisateur veut vérifier l'état d'un service, lire un `uptime`, ou
regarder le contenu d'un fichier sur une machine — le genre de geste pour
lequel Ansible a `ansible <motif> -m <module> -a <args>`, distinct
d'`ansible-playbook`. Warden n'exécutait que des playbooks entiers : déboguer
demandait d'écrire un mini-playbook jetable rien que pour ça.

`ansible-runner worker` — déjà le seul moteur piloté par `internal/runner`
(§11, décision #16) — sait nativement composer ce mode : des kwargs
`module`/`module_args`/`host_pattern` au lieu de `playbook`. Le pilotage
(trame JSON sur stdin, lecture du flux JSONL, traduction en events) n'a
aucune dépendance à la sémantique playbook.

## Ce qui est décidé

### 1. Un troisième `RunKind`, pas une commande séparée du domaine

`core.RunKind` portait déjà `playbook` et `enrollment`, avec ce raisonnement :
« les deux sont des runs parce que les deux sont des actions auditées sur la
production ». L'ad-hoc en est un troisième — pod, canal de dispatch, flux
d'events, clôture, tout est commun — et `RunAdhoc` s'ajoute à l'énumération
plutôt que de justifier une table ou un pipeline parallèle.

Contrairement à l'enrôlement, un ad-hoc reste **plus proche du playbook** :
il a toujours besoin du dépôt audité et de l'inventaire pour résoudre son
motif de machines. Seule la colonne `playbook` devient facultative, remplacée
par une cible `adhoc_target` (JSONB, symétrique d'`enroll_target` — migration
`00013`).

### 2. Extension d'`Order`, pas un quatrième `Kind` de dispatch

`wire/dispatch.Kind` distingue déjà `order` d'`enroll`, avec une justification
explicite : les deux « n'ont RIEN en commun » — l'un ne matérialise aucun
commit ni ne tire de jeton git, l'autre si. L'ad-hoc est l'inverse exact :
il partage avec `Order` le jeton git, l'inventaire, la clé SSH, le mot de
passe d'élévation, les variables transmises. Seul « quoi exécuter » diffère.

`Module`, `ModuleArgs` et `HostPattern` sont donc trois champs optionnels de
plus sur `Order` (mutuellement exclusifs avec `Playbook`), et non un nouveau
message. `exec.Config` suit le même principe côté runner : `New()` refuse que
les deux modes soient renseignés à la fois, ou ni l'un ni l'autre.

### 3. Aucune allowlist de modules

Une liste fermée de modules autorisés (`ping`, `setup`, `stat`… en excluant
`shell`/`command`) a été envisagée et rejetée : un compte qui a accès à un
environnement exécute **déjà** du code arbitraire via un playbook. Un
`shell`/`command` ad-hoc n'ouvre rien que ce compte ne pouvait déjà faire.
Restreindre les modules ajouterait une liste à maintenir sans fermer de porte
réelle — une allowlist de confort et non de sécurité, ce que ce dépôt refuse
ailleurs (voir le commentaire de `core.ValiderFlags` sur ce que l'allowlist de
flags protège *réellement*).

Seule la **forme** est vérifiée côté serveur (`core.ValiderModule`,
`core.ValiderModuleArgs`) — nom de module borné à un identifiant ou une FQCN
pointée, arguments bornés en longueur et en caractères de contrôle — pour la
même raison que `--extra-vars` : ce qui descend vers le domaine hostile de
l'ADR-0011 doit avoir une forme connue avant de partir, pas une politique.

### 4. Le motif de machines remplace `--limit`, positionnellement

Un ad-hoc n'a pas de `--limit` : son motif de machines est LE paramètre
principal, porté en position — `warden run <motif> -m <module>` — pour ne pas
surprendre quelqu'un qui connaît déjà `ansible`. Sa syntaxe et sa validation
(`verifierLimit`) sont réutilisées telles quelles : c'est le même langage de
sélection, seulement porté ailleurs sur la ligne de commande.

Les autres flags Ansible restent partagés avec `submit` sans distinction :
`--check`, `--diff`, `--become`, `--become-user`, `--extra-vars` gardent un
sens pour un module ad-hoc (beaucoup implémentent `check_mode`), et
`core.ValiderFlags` n'a donc pas eu besoin d'une variante par genre.

### 5. `warden run`, pas `warden adhoc`

Le mot qu'un utilisateur tape doit se comprendre sans connaître le jargon
Ansible : « ad-hoc » ne se devine pas, « run » si. Le genre interne
(`core.RunKind` = `"adhoc"`, colonnes `run.kind`/`run.adhoc_target`) garde son
nom technique ; seule la commande CLI porte le nom court.

## Solutions envisagées

### Option A — Allowlist de modules ❌ rejetée
- **Rejetée parce que** : n'importe quel debug utile inclut `shell`/`command`,
  qui égalent déjà les pouvoirs d'un playbook. La liste fermerait une porte
  déjà ouverte ailleurs, au prix d'une maintenance perpétuelle.

### Option B — Un quatrième `Kind` de dispatch (`KindAdhoc`) ❌ rejetée
- **Rejetée parce que** : dupliquerait GitURL/GitToken/SSHKey/BecomePassword/
  ForwardEnv sur une structure jumelle d'`Order`, pour ne faire varier que
  trois champs. `Enroll` existe séparément précisément parce qu'il ne partage
  RIEN de tout ça ; l'ad-hoc partage tout.

### Option C — Extension d'`Order` par des champs optionnels ✅ (retenue)

## Conséquences

- L'audit d'un ad-hoc est aussi complet que celui d'un playbook : le module,
  ses arguments et le motif traversent le même commit d'audit, la même
  consigne éphémère de mot de passe d'élévation, le même flux d'events. Rien
  n'échappe à `warden logs`.
- Une ligne `run` peut désormais porter TROIS formes de « quoi exécuter » :
  `playbook`, `enroll_target`, ou `adhoc_target` — mutuellement exclusives par
  construction (contraintes CHECK de la migration `00013`, comme `00009` pour
  l'enrôlement).
- `--limit` reste sans effet pour un ad-hoc (il n'apparaît pas dans `fsRun()`) :
  le motif positionnel en tient lieu, et l'exposer en plus aurait ouvert deux
  façons contradictoires de désigner les mêmes machines.

## Ce que ça n'achète pas

Aucun second niveau d'autorisation entre « lire l'état d'une machine » et
« la modifier » : un compte autorisé sur un environnement peut lancer
`ansible run <all> -m shell -a "..."` sans distinction de privilège
supplémentaire, exactement comme il pouvait déjà le faire par playbook.
