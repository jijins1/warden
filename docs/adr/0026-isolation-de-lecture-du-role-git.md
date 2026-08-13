# ADR-0026 — L'isolation de lecture du rôle `git` est une conjonction, pas un espace de noms

**Date** : 2026-08-13
**Statut** : Accepted
**Amende** : [ADR-0007](0007-acces-du-runner-au-code.md)

## Contexte

L'ADR-0007 fait reposer toute la séparation des runs sur `GIT_NAMESPACE`, et
énonce la propriété dans des termes qui ne laissent aucune marge :

> Ce point est le cœur de la décision : la restriction ne dépend ni d'un filtrage
> de `hideRefs`, ni d'une inspection des lignes `want`, ni de la discipline du
> client. Elle est appliquée par git lui-même, à l'extérieur de la négociation.
> **Un runner qui demande autre chose ne reçoit rien.**

L'écriture du smart HTTP (`internal/server/internal/gitd/smarthttp.go`) a mis
cette phrase à l'épreuve d'un vrai client git. Elle est fausse telle quelle, pour
deux raisons indépendantes, et l'ADR contient par ailleurs un chemin de ref qui
n'existe pas. Aucune des trois ne se voit sans exécuter le protocole : les trois
produisent un silence, pas une erreur.

Le contexte de menace n'a pas changé et c'est ce qui rend l'écart sérieux : le
rôle `git` est joignable depuis les pods runner, donc depuis du code arbitraire.
Le §2 pose qu'un playbook arbitraire équivaut à une exécution de code dans le
runner. Le pair, ici, est hostile par construction.

## Constat 1 — le chemin de ref de l'ADR-0007 n'existe pas

L'ADR écrit ces deux lignes comme si elles décrivaient la même chose :

```
refs/namespaces/runs/<run-id>/refs/heads/run
GIT_NAMESPACE=runs/<run-id>
```

Git **imbrique** chaque composant séparé par une barre oblique. Mesuré sur
git 2.43, `GIT_NAMESPACE=runs/ABC` résout :

```
refs/namespaces/runs/refs/namespaces/ABC/refs/heads/run
```

Une ref écrite à l'adresse que l'ADR indique n'est donc jamais annoncée. Le
symptôme est une annonce **vide** et un code de sortie zéro — pas une erreur, ce
qui est le pire des deux mondes : un runner sans code, et rien dans les logs.

## Constat 2 — les espaces de noms ne sont pas un contrôle d'accès en lecture

La documentation de git l'énonce elle-même, dans la section SECURITY de
`gitnamespaces(7)` :

> The fetch and push protocols are not designed to prevent one side from stealing
> data from the other repository that was not intended to be shared. […] In
> particular, **namespaces on a server are not effective for read access
> control**; you should only grant read access to a namespace to clients that you
> would trust with read access to the entire repository.

C'est exactement l'inverse de l'usage que l'ADR-0007 en fait. La phrase suivante
de la doc décrit le pair qui abuse des lignes `have` — c'est-à-dire le runner.

## Constat 3 — sous le protocole v2, le refus des `want` non annoncés ne s'applique pas

Mesuré sur git 2.43, à travers le transport réel (`git http-backend` en CGI),
avec `uploadpack.allowAnySHA1InWant=false`, `allowReachableSHA1InWant=false`,
`allowTipSHA1InWant=false`, et — pour lever tout doute — `transfer.hideRefs=refs/`
qui masque absolument tout :

| Protocole demandé par le client | `git fetch <url> <sha d'un autre run>` |
|---|---|
| v0 | refusé : `Server does not allow request for unadvertised object` |
| **v2 (défaut depuis git 2.26)** | **l'objet est servi** |

La configuration est bien appliquée — vérifié séparément, `hideRefs` prend effet
sur le même canal. C'est le chemin v2 qui ne consulte pas le réglage.

Conséquence brute : un SHA suffit à tirer l'arbre d'un autre run. Et un SHA n'est
pas un secret — il transite par un diff d'audit, un message d'erreur, un log.

## Solutions envisagées

### Option A — Un dépôt bare par run
La recommandation littérale de la doc de git : « store it in another repository ».
- **Avantage** : c'est la seule frontière que git garantit.
- **Rejetée parce que** : elle supprime la déduplication par blobs, qui est la
  raison n°3 des décisions actées (« git sert de transport **et** d'audit ») ; elle
  multiplie les dépôts par le nombre de runs sur un PVC RWO tenu par une instance
  unique (ADR-0006) ; et elle rend le `prune` de l'ADR-0004 et le cycle de vie du
  reconciler beaucoup plus lourds. Le coût est structurel, la menace est bornée.

### Option B — Inspecter les lignes `want` dans un proxy maison
Interposer un analyseur du protocole entre le runner et `upload-pack`.
- **Rejetée parce que** : l'ADR-0007 refuse déjà explicitement cette voie
  (« ni d'une inspection des lignes `want` »), et à raison — réimplémenter
  l'analyse d'un protocole binaire versionné pour en faire un contrôle de
  sécurité est précisément la classe de code où les contournements se logent.

### Option C — Épingler le protocole en v0 et assumer la conjonction ✅ (retenue)
Le serveur retire l'en-tête `Git-Protocol` de la requête avant de la passer à
`git http-backend`, qui en dérive `GIT_PROTOCOL`. La négociation retombe en v0,
où le refus des `want` non annoncés fonctionne.
- **Choisie parce que** : le contrôle est appliqué **côté serveur** et le client
  ne peut pas y échapper — il peut réclamer v2, `http-backend` n'apprendra jamais
  qu'il l'a fait. Elle ne coûte qu'une négociation moins efficace, sans effet ici :
  le runner tire une seule ref en `--depth=1` (ADR-0007).

## Décision

**L'isolation de lecture d'un run est la conjonction de trois contrôles, et aucun
des trois ne suffit seul.** L'ADR-0007 est amendée sur ce point : la formule
« un runner qui demande autre chose ne reçoit rien » n'est vraie que sous ces
trois conditions réunies.

1. **`GIT_NAMESPACE`**, qui borne l'annonce de refs. Il empêche la découverte,
   pas l'accès.
2. **Le refus de `receive-pack`** sur ce transport. Il ferme le vecteur n°1 de
   `gitnamespaces(7)` : sans droit d'écrire une ref dans son propre espace de
   noms, le runner ne peut pas faire croire au serveur qu'il détient un objet
   qu'il convoite.
3. **L'épinglage du protocole en v0**, sans lequel le premier contrôle est
   contournable par un simple `want` sur un SHA.

**Le chemin de ref est celui que git impose, pas celui que l'ADR-0007 écrivait.**
Pour un espace de noms `runs/<run-id>` :

```
refs/namespaces/runs/refs/namespaces/<run-id>/refs/heads/run
```

Quiconque crée ou supprime ces refs — le claim aujourd'hui, le reconciler
demain — compose ce chemin en insérant `refs/namespaces/` devant **chaque**
composant.

**Ce que le produit ne prétend pas.** L'isolation obtenue est bonne contre un
runner qui négocie, y compris malhonnêtement. Elle n'est pas une garantie
cryptographique de confinement, et la doc de git décrit deux attaques par lignes
`have` et par delta que le point 2 rend impraticables mais ne rend pas
impossibles dans l'absolu. Un run qui exigerait un confinement strict de son code
relève de l'option A, dépôt dédié — porte laissée ouverte, non empruntée.

## Conséquences

- **Avantages** :
  - la propriété de sécurité est énoncée en termes exacts, donc vérifiable ;
  - les trois contrôles sont épinglés par des tests d'étage 1 qui font échouer
    la suite si l'un disparaît, dont `TestProtocolIsPinnedToV0` ;
  - le correctif tient en une ligne et ne coûte rien en exploitation.

- **Contraintes** :
  - **La suppression de `inner.Header.Del("Git-Protocol")` rouvre la fuite en
    silence.** C'est une ligne qui ressemble à du nettoyage. Elle est commentée
    sur place et couverte par un test dédié pour cette seule raison.
  - Le constat 3 est une mesure sur git 2.43, pas une garantie contractuelle de
    git. Une version future pourrait appliquer le réglage en v2 — ce qui ne ferait
    que rendre l'épinglage redondant — ou modifier v0. À revérifier lors d'une
    montée de version du moteur, au même titre que le plancher de l'ADR-0005.
  - Le protocole v0 est le seul servi aux runners. Toute optimisation future qui
    supposerait v2 sur cette arête est à traiter comme un changement de sécurité.

- **Dette technique éventuelle** :
  - L'autorisation par ref n'existe pas, donc `receive-pack` est refusé
    entièrement. Le point 2 de la conjonction est aujourd'hui obtenu par
    l'absence de la fonctionnalité. Quand la quarantaine l'ouvrira, il faudra
    qu'un runner reste incapable d'écrire une ref dans son espace de noms —
    sans quoi le vecteur n°1 se rouvre.
