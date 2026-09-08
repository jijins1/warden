# ADR-0034 — Schéma de `.ansibleproxy.yml` : substitution fermée, noms réservés, et ce que l'allowlist protège vraiment

**Date** : 2026-08-26
**Statut** : Accepted

## Contexte

L'[ADR-0009](0009-configuration-resolue-depuis-le-commit.md) a tranché **d'où**
vient la configuration effective d'un run : de l'arbre du commit, lu par le
serveur, pour que ce qui s'applique et ce qui est audité soient le même octet.
Elle n'a pas dit **ce que le fichier contient**, ni comment il se lit.

Trois questions restaient ouvertes, et deux d'entre elles se sont révélées plus
piégeuses que prévu :

1. Un chemin d'inventaire qui varie avec l'environnement est le besoin le plus
   banal qui soit (`inventories/staging`, `inventories/prod`). Faut-il un moteur
   de templates, et si oui lequel ?
2. Le §5.4.1 exige une **allowlist stricte** des variables d'environnement, et
   nomme `ANSIBLE_STDOUT_CALLBACK` « la variable qui tue le produit ». Cette
   affirmation n'avait jamais été mesurée.
3. L'ADR-0009 note en dette que « le versionnage du schéma n'est pas traité ».

La deuxième question a aussi un problème de **justification**, que
`docs/reste-a-faire.md` avait déjà repéré : le §5.5 adosse l'allowlist à une
politique serveur, écartée en v1 par l'[ADR-0021](0021-modele-devent-normalise.md).
Sans politique, une liste écrite par celui qu'elle contraint ne contrôle rien —
et l'utilisateur exécute de toute façon du Python arbitraire par son playbook.
Il fallait donc soit abandonner l'allowlist, soit lui trouver une justification
qui tienne réellement.

## Ce que la mesure a dit

Menée le 2026-08-25 sur l'image `warden-runner:dev` (ansible-core + ansible-runner
2.4.3), une variable à la fois, en reproduisant l'invocation exacte de
`exec.go` — `ansible-runner worker --private-data-dir=<pdd>`.

**Un `ansible.cfg` commité dans l'arbre uploadé ne coupe PAS l'audit.** Testé
deux fois, dont une avec un faux `awx_display.py` présent dans le dossier qu'il
désigne. L'environnement l'emporte sur `ansible.cfg` dans la précédence Ansible.

**`env/envvars`, en revanche, le coupe** — et c'est le fichier même que
`forward_env` écrit :

| Variable posée seule | Effet sur le flux d'events |
|---|---|
| **`ANSIBLE_CALLBACK_PLUGINS`** | **audit tué** — zéro event de playbook |
| `ANSIBLE_STDOUT_CALLBACK` | survit |
| `ANSIBLE_CALLBACKS_ENABLED` | survit |
| `ANSIBLE_LOAD_CALLBACK_PLUGINS` | survit |
| `ANSIBLE_CONFIG` | survit |
| `PYTHONPATH` | survit |

Le mécanisme tient en deux lignes d'`ansible_runner/config/_base.py` :

```python
:322  self.env['ANSIBLE_STDOUT_CALLBACK'] = 'awx_display'          # forcé, sans condition
:303  self.env['ANSIBLE_CALLBACK_PLUGINS'] = ':'.join(filter(None,
          (self.env.get('ANSIBLE_CALLBACK_PLUGINS'), callback_dir)))  # utilisateur EN TÊTE
```

`ANSIBLE_STDOUT_CALLBACK` est écrasée sans condition — d'où son innocuité, et
d'où le fait que **le §5.4.1 se trompe de coupable**. Mais
`ANSIBLE_CALLBACK_PLUGINS` **préfixe** la valeur de l'utilisateur devant le
dossier d'ansible-runner. Ansible résout les plugins par nom le long de ce
chemin, premier trouvé gagne : un `awx_display.py` déposé dans l'arbre audité est
donc chargé **sous le nom même qu'ansible-runner s'apprête à utiliser**. Le
dossier vide, lui, survit — c'est bien l'ombrage qui tue, pas la variable.

## Solutions envisagées

### Templatisation de l'inventaire

**Option A — `text/template`.** Rejetée : l'entrée vient d'un commit, donc d'une
source non fiable, et on y gagnerait les appels de méthode, `{{template}}` et
l'amplification de sortie pour un besoin qui est une substitution de chaîne.

**Option B — un vrai moteur Jinja.** Rejetée pour la même raison, aggravée d'une
dépendance et d'une surface bien plus large.

**Option C — substitution d'un ensemble FERMÉ de placeholders ✅ (retenue).**
Un seul nom reconnu, `{{ env }}`, remplacé littéralement. Tout le reste est
refusé en nommant la construction.

### Justification de l'allowlist

**Option A — abandonner l'allowlist.** Défendable après la mesure de
`ansible.cfg` : l'utilisateur exécute déjà du Python arbitraire. Rejetée parce
que la mesure d'`env/envvars` montre que le produit s'apprêtait à **ouvrir
lui-même** le seul vecteur qui coupe l'audit.

**Option B — allowlist adossée à une politique serveur.** Rejetée : la politique
n'existe pas (ADR-0021), et prétendre le contraire dans un ADR serait pire que
son absence.

**Option C — le fichier EST l'allowlist, plus des noms réservés par le produit
✅ (retenue).**

## Décision

### Le fichier est l'allowlist

Rien qui ne soit **nommé dans `forward_env`** ne peut être transmis. La liste
vit dans un fichier versionné, donc lisible dans l'audit, et le serveur
n'intersecte que ce que le commit déclare avec ce que le shell fournit.

Une valeur envoyée par la CLI pour un nom que le commit ne déclare pas est
**jetée sans bruit** : elle n'a jamais été autorisée, il n'y a rien à signaler.

#### Amendement (2026-09-03) : `extra_vars`, le même mécanisme pour `-e`

L'[ADR-0039](0039-extra-vars-declarees-par-le-commit.md) ajoute `extra_vars:`,
qui reprend ce mécanisme mot pour mot : le fichier versionné déclare les NOMS,
la requête n'apporte que les VALEURS. C'est ce qui rend `-e/--extra-vars`
compatible avec l'ADR-0009, là où sa forme libre ne l'était pas.

Deux différences, toutes deux voulues et argumentées là-bas :

- un nom non déclaré fait **échouer** au lieu d'être jeté en silence — un `-e`
  est un acte explicite de l'opérateur, pas une variable d'un shell ambiant ;
- les **valeurs sont auditées**, enregistrées dans `run.flags` et servies par
  l'API, au lieu d'être consignées puis détruites. `forward_env` reste le seul
  chemin dont les valeurs ne sont jamais journalisées.

### Les noms réservés protègent l'audit, pas l'utilisateur

`core.NomsReserves` refuse `ANSIBLE_CALLBACK_PLUGINS`, `PYTHONPATH` et
`LD_PRELOAD`. Le refus est **bruyant** — le run échoue en nommant la variable —
et non silencieux, pour la même raison que l'ADR-0009 refuse les champs inconnus
plutôt que de les ignorer.

**Il faut lire cette liste pour ce qu'elle est.** Elle ne protège pas
l'utilisateur de lui-même : son playbook exécute du Python arbitraire, et aucune
liste n'y changera rien. Elle protège **l'audit**, c'est-à-dire la seule chose
que l'utilisateur ne doit pas pouvoir couper, parce que c'est ce que le produit
vend.

La première entrée est **mesurée**. Les deux autres sont de la **prudence
assumée** : elles ont survécu à l'attaque menée contre elles, mais l'ombrage par
paquet Python n'a pas été testé pour `PYTHONPATH`, et `LD_PRELOAD` pas du tout.
Les réserver est gratuit ; affirmer qu'elles sont sûres demanderait une preuve
qu'on n'a pas.

### La substitution est fermée, et refuse bruyamment

`{{ env }}` est le seul substituable. Les trois orthographes qu'Ansible accepte
— `{{env}}`, `{{ env }}`, `{{  env  }}` — le sont aussi, parce que les gens
taperont les trois.

En empruntant la syntaxe de Jinja on **hérite de ses attentes** : quelqu'un
écrira `{{ env | default('staging') }}`. Traité littéralement, ça produirait un
répertoire portant ce nom et une erreur « fichier introuvable » trois couches
plus bas, très loin de la cause. Les filtres et les placeholders inconnus sont
donc refusés en nommant la construction.

Un template référençant `env` **sans environnement** est refusé, et ce n'est pas
du zèle : `inventories/` est un inventaire de type répertoire qu'Ansible accepte
sans broncher — donc ça marcherait, en désignant autre chose que ce qui était
voulu.

**La substitution a lieu côté serveur, jamais dans la CLI.** Sinon le template
audité et le chemin appliqué pourraient diverger, ce que l'ADR-0009 existe pour
empêcher. Le commit garde le template, `run.inventory` garde le chemin résolu :
« quelle convention » et « qu'a-t-elle donné ce jour-là » se lisent séparément.

### Deux exceptions structurelles, et non une

L'ADR-0009 écrit qu'il existe « une exception, structurelle : le champ
`server` ». Il y en a **deux**.

`audit_include` décide de ce qui **entre** dans le commit ; le lire depuis le
commit serait circulaire. Il est donc lu par la CLI, nécessairement.

Ce n'est pas un trou : le fichier étant lui-même dans l'arbre, un auditeur peut
comparer ce qu'`audit_include` réclamait à ce que l'arbre contient. La divergence
est **détectable après coup**, elle n'est simplement pas **empêchable à la
source**. Et elle n'ouvre rien de neuf — un client modifié contrôle déjà
entièrement ce qu'il pousse.

#### Amendement (2026-09-02) : `repo`, troisième lecture cliente

Un troisième champ est lu par la CLI et envoyé dans la requête : `repo`, le nom
du dépôt **côté serveur**, qui rend `warden submit --repo` inutile.

Ce n'en est pas pour autant une troisième brèche, et la distinction porte tout
l'argument : `repo` n'est **pas de la configuration de run**. Il ne décide de
rien de ce qui s'exécute — ni du playbook, ni de l'inventaire, ni de l'image, ni
de l'environnement. Il désigne **à qui l'on parle**, exactement comme `server`,
et il relève de la même exception que lui : on ne peut pas demander au serveur
quel dépôt on lui adresse avant de l'avoir adressé.

Aucun écart entre l'audité et l'appliqué ne peut en naître, parce qu'il ne
s'applique à rien. Le drapeau `--repo` reste prioritaire quand il est donné.

Ce champ est écrit par personne d'autre que l'utilisateur : `warden repo declare`
**affiche** la ligne à coller et n'écrit pas le fichier. Modifier d'autorité un
fichier versionné, dans le dos d'un `git status`, serait la mauvaise surprise que
ce produit existe pour éviter.

### Le versionnage du schéma reste ouvert, délibérément

La dette que l'ADR-0009 nomme n'est **pas** soldée ici. Aucun champ de version
n'est introduit, parce qu'un numéro de version sans politique de compatibilité
écrite ne fait que déplacer la question.

Ce qui tient lieu de protection en attendant est le **refus des champs
inconnus** : un dépôt utilisant une clé plus récente que son serveur échoue en
nommant la clé, plutôt que de la voir ignorée en silence. C'est bruyant, c'est
voulu, et c'est le comportement que l'ADR-0009 exige déjà.

L'ajout de `repo` en a fourni la première démonstration en usage, et sa
contrepartie : **le serveur doit être déployé avant qu'un dépôt commite la
nouvelle clé**, faute de quoi tous ses runs échouent. `warden repo declare` le
dit au moment où il donne la ligne à coller — c'est le seul endroit où
l'information arrive à temps.

Le versionnage devient obligatoire **avant que des dépôts tiers dépendent du
format**.

### L'exigence du fichier est un interrupteur de migration

L'ADR-0009 pose qu'un commit sans configuration doit être refusé. Le refus
existe (`core.ConfigObligatoire`) mais **le défaut tolère**, derrière
`WARDEN_EXIGER_CONFIG`.

Ce n'est pas une hésitation sur la décision : c'est que l'activer d'un coup
arrêterait les runs de tout dépôt n'ayant pas encore adopté le fichier. La
bascule doit être un geste d'exploitant, pas un effet de bord de mise à jour.

**L'interrupteur est destiné à disparaître** avec sa migration. Un interrupteur
de migration qui lui survit devient une option, et une option doit être
supportée.

## Conséquences

- **Avantages** :
  - le vecteur qui coupe l'audit est fermé **par mesure** et non par intuition,
    et le commentaire du code porte le mécanisme exact ;
  - la justification de l'allowlist est honnête : elle protège l'audit, et l'ADR
    le dit au lieu de laisser croire qu'elle protège l'utilisateur ;
  - un projet déclare sa convention d'inventaire une fois, sans qu'aucune
    machinerie de templates n'entre dans le produit.

- **Contraintes** :
  - la mesure vaut pour **ansible-runner 2.4.3**. Une montée de version peut
    déplacer le mécanisme de `_base.py`, et la liste réservée doit alors être
    re-mesurée plutôt que relue ;
  - `{{ env }}` seul frustrera : quelqu'un voudra `{{ repo }}`, puis un filtre.
    Ajouter un placeholder est facile, en retirer un ne l'est pas une fois que
    des dépôts en dépendent — d'où le choix de commencer au minimum.

- **Dette technique** :
  - le versionnage du schéma, explicitement reporté ci-dessus ;
  - `PYTHONPATH` et `LD_PRELOAD` sont réservées sans preuve, dans un sens comme
    dans l'autre. Une mesure les confirmerait ou les libérerait ;
  - le seuil de rédaction de l'[ADR-0017](0017-redaction-des-diffs-par-substitution-exacte.md)
    est une constante du runner alors que cette ADR-là le veut en politique
    serveur. Il devra déménager avec elle, pas être recopié.
