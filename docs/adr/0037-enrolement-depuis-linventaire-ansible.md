# ADR-0037 — Enrôler depuis l'inventaire Ansible, et ne pas devenir Ansible

**Date** : 2026-09-02
**Statut** : Accepted
**Complète** : [ADR-0030](0030-enrolement-des-machines.md), [ADR-0036](0036-adresse-ou-nom-resoluble.md)
**S'appuie sur** : [ADR-0009](0009-configuration-resolue-depuis-le-commit.md), [ADR-0034](0034-schema-du-fichier-de-projet.md)

## Contexte

Premier usage réel, cinq machines, un dépôt Ansible. Le retour tenait en une
phrase : *« enrôler les hosts c'est relou, on doit filer un fichier host pour
enrôler toutes les machines pas encore enrôlées »*.

Ce que le produit demandait alors : lire son propre inventaire Ansible, en
recopier chaque machine dans un fichier de déclaration warden, puis lancer
`warden enroll` autant de fois qu'il y a de machines. **Deux descriptions de la
même flotte**, dont l'une est tenue à la main et ne se met à jour que quand
quelqu'un y pense — et la divergence entre les deux se paie sur une machine de
production.

L'ADR-0030 avait vu venir le coût d'adoption : le §5.7 pose que si l'ingénieur
doit retourner au bastion SSH *pour installer le produit*, il n'y entre jamais.
Le mode assisté répondait à la moitié de la question — poser la clé — et laissait
l'autre entière : **savoir quelles machines existent**.

## Solutions envisagées

### Option A — Enrôlement de masse côté SERVEUR
Un endpoint reçoit N cibles, crée N runs, orchestre.
- **Rejetée parce que** : `run.enroll_target` est une cible unique, et la
  contrainte SQL `run_enrolement_cible` la lie à un run. Ouvrir le lot côté
  serveur demanderait un genre de run supplémentaire, une notion de lot dans le
  journal, et une reprise partielle — pour un gain nul. Le serveur ne fait rien
  de plus vite : les connexions SSH sont sérialisées de toute façon, une par pod.
  **Aucune ligne de serveur n'a été écrite pour ce lot**, et c'est le principal
  résultat du découpage.

### Option B — La CLI lit un fichier de déclaration enrichi
On garde `-f`, mais la commande filtre ce qui est déjà enrôlé.
- **Rejetée parce que** : elle traite le symptôme. La ressaisie reste ; c'est
  elle qui était « relou ».

### Option C — La CLI lit l'inventaire ANSIBLE du dépôt ✅ (retenue)
`warden enroll --all` et `warden host declare --from-inventory` lisent le chemin
déclaré par `inventory` dans le fichier de projet, le comparent à
`warden host list`, et ne traitent que la différence.

- **Choisie parce que** : l'inventaire décrit déjà la flotte, et il est déjà
  versionné, relu et corrigé par l'équipe. En faire la source supprime la seconde
  vérité au lieu de la synchroniser.

## Décision

### On lit trois faits, pas un inventaire

Le paquet `internal/cli/internal/ansible` extrait `ansible_host`,
`ansible_user`, `ansible_port` — et **rien d'autre**. Il ne lit ni `group_vars/`,
ni `host_vars/`, ni les plugins d'inventaire, et il refuse une valeur Jinja en le
disant.

C'est la décision centrale, et c'est un **refus**. Reproduire la précédence de
variables d'Ansible demanderait de reproduire Ansible ; une reproduction *à peu
près* juste enrôlerait des machines sous le mauvais compte sans que rien ne le
signale. La frontière est donc tracée là où elle est vérifiable, et elle est dite
dans l'aide plutôt que laissée à découvrir.

### Le lot est N fois l'unité, pas une primitive nouvelle

`EnrolerLot` appelle `enrolerUne` en série. Un run, un pod, une déclaration par
machine — exactement ce que `warden enroll` fait, N fois. Ce qui est mutualisé
est ce qui pesait sur la personne : **une saisie de mot de passe**, **un plan**,
**une confirmation**, **un récapitulatif**.

Le prix est dit et non caché : le secret vit alors le temps du lot au lieu du
temps d'une requête. `--password-per-host` est le chemin qui ne le paie pas, et
il existe de toute façon pour les flottes dont les comptes diffèrent.

### Ce qui manque est traité ; ce qui diffère est signalé

Seules les machines **absentes** du serveur sont enrôlées. Celles qui y sont déjà
mais dont l'inventaire dit autre chose — adresse, compte, port — sont affichées
et **pas touchées**.

Corriger d'autorité répondrait à la place de l'opérateur à une question qui est
la sienne : la machine a-t-elle déménagé, ou quelqu'un s'est-il trompé de ligne ?
Et réenrôler suppose de reposer une clé.

### La revue d'empreinte reste, par machine

L'ADR-0030 pose que la confirmation d'empreinte est la seule revue humaine du
premier contact. Le lot ne la supprime pas : il la pose N fois.
`--accept-new-hostkeys` la désarme, porte un nom long exprès, et les empreintes
constatées figurent alors au récapitulatif — pour qu'elles restent vérifiables
après coup plutôt que perdues.

### Une lecture cliente de plus, bornée

La CLI lit désormais `inventory` et `default_env` du fichier de projet, ce que le
paquet `projet` s'interdisait (ADR-0009). L'écart est réel et il est borné : on
n'y compose pas un run, on énumère des machines pour une commande
d'administration. Rien de ce qui est lu là ne part dans une requête d'exécution,
et le serveur relira le fichier depuis le commit quand un run sera soumis.

## Conséquences

- **Positives** :
  - L'inventaire Ansible redevient la seule description de la flotte.
  - `warden enroll --all --dry-run` répond à « où en suis-je » sans rien toucher,
    ce qu'aucune commande ne faisait.
  - Zéro changement serveur, donc aucune contrainte d'ordre de déploiement.

- **Négatives / risques acceptés** :
  - **Un `ansible_user` posé dans `group_vars/` est invisible.** Le lot s'arrête
    alors avant toute connexion en nommant la machine — bruyant plutôt que faux,
    mais c'est un cas réel qui demandera de déplacer la variable.
  - **Le lot est séquentiel.** Cinquante machines, cinquante runs à la suite.
    Personne ne l'a encore payé ; le jour où quelqu'un le paiera, la question sera
    la concurrence côté pods, pas la forme de cette commande.
  - **`--accept-new-hostkeys` existe**, et un lot est exactement la situation qui
    invite à l'utiliser. C'est le prix d'une commande qui rend l'enrôlement de
    masse praticable ; le récapitulatif est ce qui garde la vérification possible.

- **Dette technique éventuelle** :
  - Le format INI d'Ansible n'est pas lu. Il est **reconnu pour être refusé**,
    avec le chemin de repli nommé.
  - Rien ne réconcilie dans l'autre sens : une machine enrôlée qui disparaît de
    l'inventaire n'est pas signalée. `warden host forget` reste manuel, et
    l'ADR-0030 note déjà qu'il ne retire pas la clé.
