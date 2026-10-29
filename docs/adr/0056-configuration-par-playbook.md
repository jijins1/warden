# ADR-0056 — Configuration par playbook, et `.ansibleproxy.yml` devient `.warden.yml`

**Date** : 2026-10-29
**Statut** : Proposed
**Complète** : [ADR-0034](0034-schema-du-fichier-de-projet.md), [ADR-0039](0039-extra-vars-declarees-par-le-commit.md)
**S'appuie sur** : [ADR-0009](0009-configuration-resolue-depuis-le-commit.md)

## Contexte

Le fichier de projet a été pensé pour un dépôt qui porte UN playbook. À l'usage,
un dépôt en porte plusieurs (`deploy.yml`, `backup.yml`, `rollback.yml`…), et ce
qu'ils attendent diffère : l'`extra_vars:` de l'un autorise des `-e` qui n'ont
aucun sens pour l'autre, et l'image du runner peut ne pas être la même.

Seule réponse possible jusqu'ici : déclarer à la racine l'union de tout ce dont
chaque playbook a besoin. L'allowlist s'en trouve élargie pour tous, et le
fichier ne dit plus quel playbook attend quoi.

Le nom du fichier, enfin, est celui du projet avant qu'il s'appelle warden.

## Solutions envisagées

### Option A — Un fichier par playbook (`deploy.warden.yml`)
- **Rejetée parce que** : `server`, `repo` et `audit_include` sont lus par la CLI
  avant qu'un playbook soit en jeu ; il faudrait soit les recopier dans chaque
  fichier, soit garder un fichier racine en plus. Deux sources pour une même
  configuration finissent par diverger.

### Option B — Une clé `playbooks:` qui remplace entièrement la racine
- **Rejetée parce que** : chaque playbook devrait redéclarer ce que tous
  partagent, et un oubli retirerait silencieusement une variable à un seul
  playbook.

### Option C — Une clé `playbooks:` qui complète la racine ✅ (retenue)
```yaml
extra_vars:
  - version
  - colour
playbooks:
  mon_playbook.yaml:
    extra_vars:
      - other
```
- **Choisie parce que** : la racine dit ce que le dépôt partage, la surcharge ce
  qui n'est qu'à un playbook, dans le même fichier du même commit.

## Ce qui est décidé

### La surcharge

- La clé est le chemin du playbook **relatif à la racine du dépôt**, normalisé
  (`./site.yml` et `site.yml` sont le même). Un chemin absolu ou qui sort du
  dépôt est refusé : il ne correspondrait à aucun run, et la déclaration serait
  ignorée sans bruit.
- Champs admis : `default_env`, `inventory`, `runner_image`, `forward_env`,
  `extra_vars` — la configuration de RUN. `server`, `repo` et `audit_include`
  sont refusés comme champs inconnus : ils ne dépendent pas du playbook.
- Les valeurs seules **remplacent** la racine, les listes **s'y ajoutent**.
- Un nom déclaré à la fois à la racine et dans une surcharge est **refusé**.
  Il ne change rien aujourd'hui ; le jour où une déclaration portera plus qu'un
  nom ([ADR-0054](0054-extra-vars-typees-et-formulaire-de-lancement.md)), il
  faudra décider laquelle l'emporte. Relâcher la règle ce jour-là est facile, la
  resserrer une fois des dépôts écrits ne l'est pas.

### Où elle s'applique

Côté serveur, dans `core.Composer`, depuis le playbook de la requête et le
fichier lu dans l'arbre du commit (ADR-0009). La CLI ne l'applique que pour ce
qu'elle seule peut faire : chercher dans le shell les valeurs des
`forward_env` du playbook soumis. Un run ad-hoc n'a pas de playbook et reçoit la
racine.

### Le renommage

Le fichier devient `.warden.yml`. L'ancien `.ansibleproxy.yml` reste lu **en
repli**, quand `.warden.yml` est absent du commit — par le serveur comme par la
CLI, dans le même ordre (`config.Files`). Les dépôts existants n'ont rien à
changer, et `playbooks:` est accepté sous les deux noms.

Quand les deux fichiers sont présents, `.warden.yml` fait foi et l'ancien n'est
pas ouvert. C'est ce qui permet de garder l'ancien le temps que tous les
serveurs soient mis à jour : un serveur plus ancien ne lit que lui. Les deux
peuvent alors diverger, et un auditeur doit savoir que seul le nouveau
s'applique — d'où une règle fixe plutôt qu'un refus.

Les ADR antérieures gardent l'ancien nom, qui était le bon à leur date.

## Conséquences

- **Avantages** :
  - l'allowlist d'un playbook ne s'élargit plus de ce dont les autres ont besoin ;
  - un auditeur lit dans le commit ce que CE playbook admettait.

- **Contraintes** :
  - le repli coûte un aller-retour de plus vers le rôle `git` pour les dépôts
    qui n'ont pas renommé leur fichier ;
  - même précaution que pour `repo:` : un dépôt qui commite `playbooks:` contre
    un serveur plus ancien voit ses runs refusés ;
  - le formulaire de lancement de l'ADR-0054 devra lire les déclarations du
    playbook choisi, et non de la seule racine.

- **Dette** :
  - le repli est destiné à disparaître une fois les dépôts renommés, comme
    l'interrupteur `WARDEN_EXIGER_CONFIG` de l'ADR-0034.

- **Hors périmètre** :
  - `warden host` lit l'inventaire depuis la racine seulement ; un `--playbook`
    y sera ajouté si un dépôt surcharge `inventory` par playbook.
