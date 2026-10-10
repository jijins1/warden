# ADR-0046 — La sortie de la CLI est celle d'Ansible, couleurs comprises

**Date** : 2026-10-10
**Statut** : Accepted
**Amende** : [ADR-0040](0040-submit-suit-le-run.md) (ses conséquences d'affichage)

## Contexte

`warden submit` et `warden logs` affichaient un rendu **recomposé** par la CLI à
partir des events structurés : une ligne de résumé par tâche (`ok=3
changed=1`), les hôtes en échec marqués `✗` ou `⚠`, un « Bilan » final. Sans
couleur, et dans un format qu'aucun utilisateur d'Ansible ne connaît.

La demande : que la sortie soit colorée comme celle d'Ansible, et s'en écarte
le moins possible.

## Ce qui a été MESURÉ

Sur ansible-runner 2.4.3 (image du runner, alpine 3.22) :

1. Chaque trame d'event porte, dans son `stdout` de premier niveau, **le texte
   exact** que le callback par défaut d'Ansible a écrit pour cet event :
   bannières `PLAY [...] ***` et `TASK [...] ***`, `ok: [hôte]`, `fatal: [hôte]:
   FAILED! => {...}`, `...ignoring`, avertissements, `PLAY RECAP`. C'est le
   travail du callback `awx_display`.
2. Sans terminal — et le `worker` n'en a pas — ce texte est sans couleur.
   `ANSIBLE_FORCE_COLOR=1` y fait apparaître les séquences d'Ansible
   (`\x1b[0;32mok: [localhost]\x1b[0m`, `PLAY RECAP` coloré compteur par
   compteur).
3. `envvars` passé dans la trame `kwargs` se **fusionne** avec
   `<pdd>/env/envvars` (variables transmises, §5.4) : les deux arrivent dans
   l'environnement d'`ansible-playbook`.
4. Le `stdout` d'un `runner_on_file_diff` **est le diff unifié**. Le traducteur
   retirait `event_data.diff` du payload mais laissait ce texte : le contenu
   des diffs — donc des secrets rendus (§6.5) — atterrissait dans la table des
   events ordinaires, et partait vers la CLI par le WebSocket, qui omet
   pourtant `Diff` à dessein.

## Solutions envisagées

### Option A — Garder le rendu recomposé, et le colorer ❌ rejetée

- **Rejetée parce que** : il resterait un format à nous. Colorer un résumé
  n'en fait pas la sortie d'Ansible, et chaque écart est une chose de plus à
  réapprendre en incident — le moment où l'on a le moins envie d'apprendre.

### Option B — Recomposer la sortie d'Ansible à partir des events ❌ rejetée

- **Rejetée parce que** : c'est réécrire le callback par défaut d'Ansible en
  Go — boucles, `delegate_to`, handlers, `-v` qui ajoute le résultat, `[CHECK
  MODE]`, `rescued`/`ignored` que l'event seul ne dit pas. L'imitation finit par
  diverger sur le cas qu'on n'avait pas prévu, et elle diverge en silence.

### Option C — Réafficher le texte qu'Ansible a rendu, couleur forcée ✅ (retenue)

Le texte est déjà dans le payload : l'afficher tel quel est le seul moyen
d'avoir **zéro** écart par construction, y compris sur les versions
d'ansible-core à venir.

## Ce qui est décidé

1. **Le runner force la couleur** : `ANSIBLE_FORCE_COLOR=1` par `envvars` dans
   la trame `kwargs`, jamais dans le fichier des variables transmises.
2. **La CLI écrit le `stdout` de chaque event**, `\r\n` ramenés à `\n`. Rien
   d'autre n'est recomposé, sauf :
   - la ligne d'état (`changed: [hôte]`…) d'un event privé de son texte — voir 5 ;
   - un `PLAY RECAP` de repli, au format d'Ansible, quand le flux s'arrête
     avant celui d'Ansible (coupure, run interrompu) — la propriété « le bilan
     survit à une coupure » de l'ADR-0040 est conservée ;
   - le diagnostic de l'ADR-0043, en `ERROR! …`, et seulement quand Ansible n'a
     rien affiché : sinon il ne fait que répéter les trames `verbose`.
3. **La couleur suit les règles d'Ansible** (`ansible/utils/color.py`) :
   `ANSIBLE_NOCOLOR` ou `NO_COLOR` la coupent, une sortie qui n'est pas un
   terminal la coupe, `ANSIBLE_FORCE_COLOR` l'emporte sur tout. Les mêmes
   variables, pas des drapeaux à nous : qui a réglé Ansible dans son shell n'a
   pas à le refaire pour warden.
4. **Seules les couleurs atteignent le terminal.** Le texte vient du pod
   runner, domaine hostile (ADR-0011) : un playbook y écrit ce qu'il veut. Les
   séquences `ESC [ … m` sont gardées ; tout autre caractère de contrôle (C0 et
   C1, donc l'ESC de toute autre séquence) est retiré, hors saut de ligne et
   tabulation. Sans quoi un dépôt pourrait effacer l'écran, réécrire des lignes
   déjà affichées ou remplir le presse-papiers (OSC 52).
5. **Le texte rendu sort du payload de tout event porteur de diff**, comme
   `event_data.diff`. La CLI retombe alors sur la ligne d'état. Le diff ne
   s'affiche donc pas dans la CLI sous `--diff`, contrairement à Ansible : c'est
   l'écart assumé, et il est celui de l'ADR-0008, pas un nouveau.

## Conséquences

- Les conséquences d'affichage de l'ADR-0040 tombent : plus de regroupement
  par tâche en une ligne, plus de marqueurs `✗`/`⚠`. La distinction
  `unreachable`/`failed` reste visible — c'est Ansible qui la fait
  (`UNREACHABLE!` contre `FAILED!`).
- Les bannières ont la largeur d'Ansible **dans le pod** (79 colonnes), pas
  celle du terminal de l'utilisateur.
- L'interface web retire les séquences de couleur du `stdout` qu'elle affiche
  en repli ; elle ne les rend pas encore en couleur.
- Les runs antérieurs gardent leur `stdout` sans couleur : la CLI les réaffiche
  tels quels. Les diffs déjà déposés dans leurs payloads y restent.
- La rédaction de l'ADR-0017 n'est pas affectée : Ansible colore des messages
  entiers, et un secret n'est jamais coupé par une séquence d'échappement.

## Ce que ça n'achète pas

Le §5.8 — les hôtes en vol — n'est toujours pas livré. Le texte d'Ansible ne
dit, par nature, que ce qui est terminé.
