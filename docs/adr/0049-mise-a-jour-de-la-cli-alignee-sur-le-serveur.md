# ADR-0049 — Mise à jour de la CLI alignée sur le serveur, binaires signés par minisign

**Date** : 2026-10-28
**Statut** : Proposed
**Touche** : [ADR-0006](0006-un-binaire-trois-roles.md),
[ADR-0011](0011-un-binaire-par-domaine-de-confiance.md)

## Contexte

La CI publie la CLI à chaque merge sur `main`, en GitHub Release `v0.0.N`, sous
la MÊME version que les images et le chart. Rien, en revanche, ne relie la CLI
d'un poste au serveur qu'elle appelle : aucun binaire ne porte sa version, le
serveur n'annonce pas la sienne, et la CLI s'installe depuis les sources
(`make install`) ou par un téléchargement fait à la main.

Le décalage n'est pas cosmétique. Les flags Ansible, les champs d'un run, le
schéma du fichier de projet évoluent ensemble des deux côtés ; une CLI en retard
produit des refus du serveur dont la cause réelle — « votre CLI est ancienne » —
n'apparaît nulle part.

La demande : que la CLI se mette à la version du serveur.

Deux questions en découlent, de natures différentes :

1. **Le geste** — quand et comment remplacer le binaire.
2. **La confiance** — un binaire qui se remplace exécute, au lancement suivant,
   ce qu'il a téléchargé. Le mécanisme ouvre donc un chemin d'exécution de code
   sur chaque poste utilisateur.

## Solutions envisagées

### Geste A — Mise à jour silencieuse à chaque commande ❌ rejetée

- **Rejetée parce que** : remplacer le binaire au milieu d'un `warden submit`
  est surprenant, et un téléchargement qui échoue ferait échouer une commande
  sans rapport. Un outil d'audit ne change pas sous les pieds de son
  utilisateur sans le lui dire.

### Geste B — Simple avertissement ❌ rejetée

- **Rejetée parce que** : un avertissement se lit une fois, puis s'ignore. Le
  décalage reste, et le run part avec.

### Geste C — Le run est bloqué, la mise à jour est proposée ✅ retenue

`warden submit` et `warden run` — les deux commandes qui soumettent un run —
demandent sa version au serveur avant toute chose. En cas d'écart :

```
La CLI (v0.0.41) ne correspond pas au serveur (v0.0.42).
Mettre à jour maintenant ? [O/n]
```

- **oui** : téléchargement, vérification, remplacement du binaire, puis la
  commande est RELANCÉE avec les mêmes arguments (sous Windows, où un
  exécutable en cours ne se remplace pas en place, l'utilisateur la relance) ;
- **non** : la commande échoue. Le run ne part pas.
- **sans terminal** (script, CI) : la commande échoue en nommant
  `warden upgrade`. Personne ne peut répondre, et deviner serait pire.

`warden upgrade` fait le même geste à la demande. Les autres commandes ne
vérifient rien : elles ne déclenchent aucune exécution sur une machine.

Une CLI ou un serveur construits sans version (`dev` — `make install`,
`make images`) ne déclenchent rien : c'est l'environnement d'un développeur, qui
sait ce qu'il a compilé.

L'écart est symétrique : une CLI plus RÉCENTE que le serveur est aussi
ramenée à la version du serveur. Le but est l'accord, pas la nouveauté.

### Source A — Binaires servis par le déploiement (image `ui`) ❌ rejetée

Les cinq binaires embarqués dans l'image nginx, servis à côté de l'interface.

- **Avantage** : fonctionne en réseau fermé, sans accès à GitHub.
- **Rejetée parce que** : la CI publie déjà ces binaires en release. Les
  embarquer ajoute ~75 Mo à une image qui sert du statique, et une seconde
  chaîne de compilation de la CLI qui pourrait dériver de la première. Le besoin
  du réseau fermé est couvert autrement : voir `WARDEN_UPDATE_URL` ci-dessous.

### Source B — La GitHub Release de la version du serveur ✅ retenue

`https://github.com/jijins1/warden/releases/download/v<version>/`, qui porte :

- `warden_<os>_<arch>[.exe]` — les cinq cibles déjà publiées ;
- `SHA256SUMS` — une ligne par binaire ;
- `SHA256SUMS.minisig` — la signature de ce fichier.

`WARDEN_UPDATE_URL` remplace la base (`…/releases/download`) pour un miroir
interne, qui reproduit l'arborescence `v<version>/…`. Le miroir n'a pas besoin
d'être de confiance : la signature est vérifiée quel que soit l'hôte.

### Confiance A — TLS et somme SHA-256 seuls ❌ rejetée

- **Rejetée parce que** : la somme dit que le fichier est arrivé intact, pas
  qu'il est authentique. Quiconque contrôle l'hôte de téléchargement — ou le
  serveur, qui désigne la version — contrôle aussi la somme. La CLI deviendrait
  un moyen d'exécuter du code sur tous les postes pour qui compromet un seul
  de ces points.

### Confiance B — cosign (Sigstore) ❌ rejetée

- **Avantage** : le mode *keyless* supprime la clé à garder.
- **Rejetée parce que** : la vérification en Go passe par `sigstore-go`, un arbre
  de dépendances lourd et une racine TUF à tenir à jour, dans le binaire même
  que l'ADR-0011 veut minimal. Le mode keyless dépend en plus des services
  publics de Sigstore (Fulcio, Rekor) à la vérification, ce qu'un déploiement en
  réseau restreint n'atteint pas.

### Confiance C — minisign ✅ retenue

Une paire Ed25519. La clé **publique** est inscrite en dur dans la CLI
(`internal/cli/internal/miseajour/cle.go`) ; la clé **privée** est un secret de
l'environnement GitHub `release` (`MINISIGN_SECRET_KEY`), ouvert à `main`
seule, et nulle part ailleurs. Pas un secret de dépôt : celui-ci est lisible
par tout workflow de toute branche, donc par un `ci.yml` modifié sur une
branche poussée.

La CI signe `SHA256SUMS` — une signature couvre les cinq binaires — avec le
commentaire de confiance `warden v<version>`. La CLI vérifie, dans cet ordre :

1. la signature de `SHA256SUMS` par la clé embarquée ;
2. le commentaire de confiance, qui doit être exactement `warden v<version
   attendue>`. Le commentaire est couvert par la signature globale : c'est ce
   qui empêche de servir les sommes **signées** d'une autre version — ancienne
   et vulnérable, par exemple — à la place de celle demandée ;
3. la somme du binaire téléchargé contre sa ligne dans `SHA256SUMS`.

La vérification est écrite dans le dépôt (~80 lignes sur `crypto/ed25519` et
`x/crypto/blake2b`, déjà dépendances) plutôt que tirée de `go-minisign` : le
format est court et figé, et la CLI ne prend pas de dépendance pour lui.

## Conséquences

### Positives

- Un run ne part plus d'une CLI décalée, et l'utilisateur sait pourquoi.
- Compromettre le serveur, le registre ou l'hôte de téléchargement ne suffit
  plus à pousser un binaire sur les postes : il faut aussi la clé privée.
- Fonctionne derrière un miroir non fiable.

### Négatives

- **La clé privée devient un actif critique.** Fuie, elle permet de signer un
  binaire que toutes les CLI accepteront. Perdue, les CLI déjà installées ne
  peuvent plus se mettre à jour seules : il faut les réinstaller à la main. Elle
  est donc sauvegardée hors de GitHub (Vault).
- **Changer de clé** demande une release intermédiaire : une CLI qui embarque
  la NOUVELLE clé publique, signée par l'ANCIENNE. Les CLI qui sautent cette
  release se réinstallent à la main.
- **Le serveur choisit toujours la version.** La signature garantit que le
  binaire est une release authentique, pas qu'il est la plus récente : un
  serveur compromis peut ramener les postes vers une release ancienne et
  authentique. C'est le prix de « s'aligner sur le serveur », assumé.
- `GET /version` est anonyme : la CLI doit le joindre avant toute session, et
  `warden upgrade` doit fonctionner pour qui n'est pas connecté. La version est
  donc lisible par quiconque atteint l'API. Le code étant public et les
  releases aussi, l'information ne révèle que l'âge du déploiement.
- Un binaire installé dans un répertoire non inscriptible
  (`/usr/local/bin`) ne se remplace pas : la commande le dit et donne la
  marche à suivre, elle n'élève pas ses droits.

### Mise en place

1. `minisign -G -W -p warden.pub -s warden.key` — `-W` : sans mot de passe, la
   protection est celle du magasin de secrets.
2. Environnement GitHub `release`, déploiement limité à la branche `main` ;
   contenu de `warden.key` → son secret `MINISIGN_SECRET_KEY`. Copie dans
   Vault.
3. Deuxième ligne de `warden.pub` → `ClePublique` dans
   `internal/cli/internal/miseajour/cle.go`.

Tant que l'étape 3 n'est pas faite, `warden upgrade` refuse : une CLI sans clé
ne télécharge rien. Tant que l'étape 2 n'est pas faite, le job `release-cli`
échoue — et `publish` avec lui, qui l'attend.

### Ordre de livraison et retour arrière

- **La release avant le serveur.** Un serveur 0.0.N déployé sans la release
  signée `v0.0.N` bloque toutes les CLI à jour, sans rien à télécharger pour en
  sortir. La CI l'impose : `publish` attend `release-cli`. Un déploiement fait
  à la main doit respecter le même ordre.
- **La première fois, à la main.** Les CLI installées avant cette décision ne
  vérifient rien et n'ont pas `warden upgrade` : chaque poste installe une fois
  la première release qui porte le mécanisme. Le serveur suivant déclenche la
  première mise à jour automatique.
- **Redéployer un serveur antérieur** est sans danger : sans `/version`, il
  répond 404, que la CLI lit comme « pas de version annoncée » — rien n'est
  bloqué.
- **Revenir à un serveur qui porte déjà `/version`** ramène les CLI à sa
  version, à la prochaine soumission : c'est l'alignement voulu.
- **Une CLI ne redescend pas seule sous cette décision** : une release
  antérieure n'a pas le mécanisme, et une fois installée elle ne le retrouvera
  pas. Sortir de là demande une réinstallation à la main.
