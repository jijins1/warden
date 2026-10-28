# ADR-0052 — La liste des machines visibles : bornée par les droits vivants, pas par l'administration

**Date** : 2026-10-28
**Statut** : Proposed
**Complète** : [ADR-0030](0030-enrolement-des-machines.md), [ADR-0033](0033-droits-par-environnement.md)

## Contexte

L'interface gagne un écran « Machines ». La seule lecture de l'inventaire
était `GET /admin/hosts` : réservée aux administrateurs (`adminOuRien`, 404
pour les autres), non paginée, et rendant TOUT l'inventaire — c'est la carte
de l'organisation que sert la CLI d'administration.

Un utilisateur ordinaire qui a un droit sur `staging` a pourtant une raison
légitime de voir les machines de `staging` : ce sont celles que ses runs
touchent, et dont il lit déjà l'historique sous `GET /hosts/{host}/runs`.

## Solutions envisagées

### Option A — Ouvrir `GET /admin/hosts` et le filtrer selon l'appelant
- **Avantage** : une seule route, un seul handler.
- **Rejetée parce que** : la même URL rendrait deux réponses de nature
  différente — l'inventaire complet pour un administrateur, une part pour les
  autres — et sortirait l'administration de `/admin`, que la NetworkPolicy et
  l'ingress doivent pouvoir traiter à part. La CLI, qui compte sur l'inventaire
  complet et non paginé, changerait de comportement sans changer d'appel.

### Option B — Une route distincte, bornée par l'administration OU les droits
L'administrateur voit tout, les autres voient leurs environnements.
- **Avantage** : l'administrateur n'a qu'un écran.
- **Rejetée parce que** : c'est l'inverse de la règle posée par
  `GET /environments` et par la liste des runs (`core.RunsVisibles`) :
  l'administration du produit ne donne pas l'accès aux environnements. Un
  écran qui montrerait à l'administrateur des machines qu'il ne peut pas
  atteindre contredirait les autres écrans de la même interface.

### Option C — `GET /hosts`, bornée EXACTEMENT par `core.EnvironnementsVisibles` ✅ (retenue)
- **Choisie parce que** : la portée est celle qui borne déjà les runs et les
  environnements de l'appelant — une seule règle, lue au même endroit.
  L'inventaire complet reste sous `/admin/hosts`, inchangé.

## Décision

1. **`core.MachinesVisibles`** (`internal/server/internal/core/machines_visibles.go`)
   calcule la portée depuis l'appelant authentifié et ses droits vivants, et
   la remet au magasin dans `FiltreDeMachines.Environnements`. Les critères
   venus de la requête (`CriteresDeMachines` : `env`, `q`, curseur, limite) ne
   font que RÉTRÉCIR : rien venu de l'extérieur ne peut écrire la portée.
   - un `env` hors de la portée rend une liste **vide**, pas un 403 : un refus
     confirmerait que l'environnement existe ;
   - une panne de lecture des droits rend une portée vide, donc une liste
     vide — elle n'ouvre rien ;
   - être administrateur n'élargit rien.

2. **Une portée vide signifie « rien »**, à l'inverse de `ListHosts`, où
   l'environnement vide signifie « tout ». La valeur zéro d'un filtre de
   portée doit fermer : sinon un oubli ouvre l'inventaire entier. Le magasin
   ne lit alors pas la base — mais relit le curseur d'abord, pour qu'un curseur
   illisible reste un 400 quels que soient les droits.

3. **`Store.ListerMachines`** : `environment = ANY($1)`, `name ILIKE` avec
   `%`, `_` et `\` échappés (`q=db_` cherche « db_ »), ordre
   `(environment, name)` — celui de la contrainte `managed_host_unique`, donc
   servi par son index. Pagination par keyset sur ce couple, 50 par défaut,
   500 au plus, une ligne sonde pour détecter la suite, comme `ListRuns`. Le
   curseur est le couple encodé en JSON puis en base64url, opaque et robuste à
   tout caractère d'un nom. Illisible, il produit `core.ErrCurseurIllisible`,
   que le handler traduit en 400.

4. **`GET /hosts?env=&q=&cursor=&limit=`** rend
   `{"hosts":[…],"next_cursor":""}`, chaque machine au formalisme de
   `GET /admin/hosts` (`hostPayload`). `hosts` est toujours un tableau,
   `next_cursor` vide sur la dernière page ; 401 sans appelant. La route
   cohabite avec `GET /hosts/{host}/runs` : le motif sans segment ne capture
   que `/hosts` lui-même.

5. Le port `core.HostCatalog` embarque `core.ListeurDeMachines` plutôt que
   d'ajouter un second port aux dépendances de l'API : c'est le même
   inventaire, servi par le même adaptateur.

## Conséquences

- **Avantages** : un utilisateur voit les machines de ses environnements sans
  rien apprendre du reste de l'organisation ; la règle de visibilité est la
  même pour les runs, les environnements et les machines.
- **Contraintes** : deux lectures de l'inventaire coexistent, avec deux
  sémantiques du filtre vide (`ListHosts` : tout ; `ListerMachines` : rien).
  Un administrateur qui veut tout voir dans l'interface doit s'accorder les
  droits correspondants — c'est voulu, et c'est la même chose pour les runs.
- **Dette technique éventuelle** : `GET /admin/hosts` reste non paginé. Si
  l'inventaire grossit au point de gêner la CLI, il faudra le paginer à son
  tour — sans le fusionner avec `GET /hosts`, pour les raisons de l'option A.
