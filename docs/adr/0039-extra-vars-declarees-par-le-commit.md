# ADR-0039 — `-e` : les noms viennent du commit, les valeurs sont auditées

**Date** : 2026-09-03
**Statut** : Accepted
**Complète** : [ADR-0038](0038-flags-ansible-check-diff-limit.md)
**S'appuie sur** : [ADR-0009](0009-configuration-resolue-depuis-le-commit.md), [ADR-0034](0034-schema-du-fichier-de-projet.md)

## Contexte

L'ADR-0038 a ouvert `--check`, `--diff` et `--limit`, et a laissé
`-e/--extra-vars` dehors avec cette raison :

> Il déplacerait la source de vérité hors du commit d'audit, ce que le produit
> vend précisément de ne pas faire. Il demande sa propre ADR, pas une ligne de
> plus.

Le retour d'usage a répondu à l'objection au lieu de la contourner : *« pour le
`-e` on peut faire comme les variables d'environnement et mettre une liste
blanche des paramètres autorisés »*.

C'est juste, et ça referme exactement le trou nommé. `forward_env` a déjà résolu
le même problème pour les variables du shell (§5.4, ADR-0034) : **le fichier
versionné déclare les NOMS, la requête n'apporte que des VALEURS**. Un `-e` dont
le nom est déclaré dans le commit ne déplace plus rien — le commit décrit
toujours l'espace de ce qui peut varier, et le run dit ce qui a varié.

## Ce qui est décidé

Un champ `extra_vars:` dans `.ansibleproxy.yml`, liste de noms, relu par le
serveur depuis le commit d'audit. `warden submit -e nom=valeur`, répétable, ne
pose que des noms qui y figurent. Un nom absent est **refusé**, en nommant ceux
que le projet déclare.

```yaml
# .ansibleproxy.yml, dans le commit
extra_vars:
  - version
  - couleur
```

```sh
warden submit --playbook deploy.yml --env prod -e version=1.2.3 -e couleur=bleu
```

### Les valeurs sont AUDITÉES, et c'est la décision qui compte

C'est le point où cette ADR **diverge** de `forward_env`, dont elle copie tout le
reste. Les valeurs de `forward_env` sont sensibles : consigne éphémère, consommée
à la livraison, jamais en base, jamais journalisées, et passées au rédacteur de
l'ADR-0017 pour être masquées dans les events.

Les valeurs d'`extra_vars` font l'inverse : elles sont **enregistrées dans
`run.flags`**, servies par `GET /runs/{run_ref}`, et visibles dans les diffs.

Parce que c'est ce à quoi elles servent. *« Quelle version a été déployée le
14 »* est précisément la question qu'un outil d'audit existe pour répondre. Un
run enregistré comme « `deploy.yml` sur `prod` » sans dire `version=1.2.3` ne
répond pas à la question qu'on lui pose. Et si la valeur était masquée, le
`--diff` d'un fichier contenant cette version afficherait `[supprimé]` à
l'endroit exact où l'auditeur regarde.

**La conséquence doit être dite sans détour, et elle l'est dans l'aide, dans le
schéma et dans la prise en main : ne mettez pas de secret dans `-e`.** Le chemin
des secrets est `forward_env`, dont les valeurs ne sont jamais journalisées, et
le mot de passe d'élévation a le sien. Deux mécanismes, deux promesses nettes,
plutôt qu'un seul à la promesse floue.

### Le préfixe `ansible_` est réservé

`extra_vars:` ne peut pas déclarer un nom commençant par `ansible_`, et le
refus est bruyant — comme celui d'un `forward_env` qui déclare un nom réservé.

Tout le préfixe et pas une liste de noms : les variables `ansible_*` décrivent
**comment le run se connecte** — `ansible_host`, `ansible_user`,
`ansible_ssh_private_key_file`, `ansible_python_interpreter`. Les laisser poser
par la requête lui rendrait le pouvoir de redéfinir la connexion, c'est-à-dire de
sortir de ce que l'inventaire audité décrit.

`ansible_become_password` mérite d'être nommé à part, parce qu'il croise la
décision précédente : les valeurs d'`-e` étant auditées, l'autoriser écrirait un
mot de passe **en clair dans la base**. Le mot de passe d'élévation a son propre
chemin (`env/extravars` dans le pod), qui ne persiste rien.

### Où chaque moitié du contrôle vit

Il y en a deux, et elles ne peuvent pas vivre au même endroit :

| Contrôle | Où | Pourquoi là |
|---|---|---|
| **Forme** — `nom=valeur`, nom = identifiant Ansible, pas de Jinja, pas de caractère de contrôle | `core.ValiderFlags`, à l'entrée de `POST /runs` | Ne dépend d'aucun contexte, donc peut refuser **avant** que le rôle `git` travaille |
| **Appartenance** — le nom est-il dans `extra_vars:` | `core.Composer`, après lecture du commit | Le fichier qui fait autorité est celui du commit, qui n'est pas encore résolu à l'entrée |

La CLI duplique le contrôle de **forme** et pas celui d'appartenance. C'est
délibéré : la forme est vérifiable sur le poste, donc autant refuser avant de
payer une capture d'arbre et un push (§5.7). L'appartenance ne l'est pas — le
worktree local peut différer du commit — et un contrôle client qui accepterait ce
que le serveur refuse, ou l'inverse, serait pire que pas de contrôle.

### Le refus n'est pas silencieux, contrairement à `forward_env`

`composerForwardEnv` **jette en silence** une valeur dont le nom n'est pas
déclaré. `composerFlags` **refuse** la requête entière.

L'asymétrie tient à la nature de la source. `forward_env` puise dans un shell
ambiant, où des variables existent sans que personne les ait voulues pour ce
run : en écarter une n'a rien à signaler. Un `-e version=1.2.3` est au contraire
un acte explicite, tapé pour ce run-là. L'ignorer ferait partir un déploiement
**sans** la version demandée, et l'échec arriverait de la machine cible plutôt
que du serveur.

## Solutions envisagées

### Option A — `-e` libre, sans allowlist ✅ rejetée
Ce que fait `ansible-playbook`.
- **Rejetée parce que** : `-e @fichier` et `-e '{"json":…}'` font lire les
  variables ailleurs que dans le commit. C'est l'objection de l'ADR-0038, et elle
  tient toujours pour cette forme-là.

### Option B — `-e` dont les valeurs sont traitées comme des secrets
Copier `forward_env` en entier, consigne éphémère comprise.
- **Rejetée parce que** : elle rendrait l'audit muet sur le paramètre le plus
  intéressant du run. Et elle masquerait la valeur dans les `--diff`, c'est-à-dire
  à l'endroit précis où on la cherche. Un produit d'audit qui cache ce qui a été
  déployé n'a pas d'objet.

### Option C — Noms déclarés par le commit, valeurs auditées ✅ (retenue)
La proposition du retour d'usage, plus la décision d'auditer les valeurs et la
réserve du préfixe `ansible_`.

## Conséquences

- `warden submit -e version=1.2.3 -e couleur=bleu` fonctionne, sous les deux noms
  qu'`ansible-playbook` donne au drapeau (`-e` et `--extra-vars`).
- `.ansibleproxy.yml` gagne un champ. **Même précaution de déploiement que pour
  `repo:`** : `config.Parse` refuse les champs inconnus et le SERVEUR relit ce
  fichier depuis le commit. Un dépôt qui commite `extra_vars:` contre un serveur
  non mis à jour verra **tous ses runs refusés**. Déployer le serveur d'abord.
- `run.flags` porte désormais des valeurs saisies par l'utilisateur. Elles sont
  bornées en longueur, sans caractère de contrôle et sans Jinja, mais elles sont
  **lisibles par quiconque peut voir le run**. C'est voulu ; c'est écrit partout
  où quelqu'un est susceptible d'y mettre un mot de passe.
- Le drapeau est **répétable**, ce que `flag` ne sait pas faire seul :
  `submit.Repetable` implémente `flag.Value`, comme `prompt.Source` le fait déjà
  dans ce dépôt.
- Deux occurrences de la **même** variable sont refusées, des deux côtés : Ansible
  garderait la dernière, l'audit garderait les deux, et le run cesserait de dire
  ce qu'il a fait.

## Ce que ça n'achète pas

L'allowlist ne protège **pas** les machines cibles. Le playbook exécute déjà du
code arbitraire (§2), et déclarer `version` ne rend pas `version` inoffensive :
un playbook qui fait `shell: rm -rf {{ version }}` reste un playbook qui fait ça.
Elle protège **l'audit** — la garantie que ce qui s'est exécuté est décrit par ce
qui a été committé — et rien d'autre.

Elle ne remplace pas non plus la validation dans le playbook : c'est à lui de
vérifier que `version` ressemble à une version. Warden garantit qu'elle a été
déclarée, transmise sans altération, et écrite dans le journal.
