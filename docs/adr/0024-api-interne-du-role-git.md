# ADR-0024 — API interne du rôle `git` : HTTP sur port distinct, jeton porteur partagé

**Date** : 2026-08-08
**Statut** : Accepted

## Contexte

L'ADR-0006 pose une règle de couplage et une exception :

> Aucun rôle n'appelle un autre rôle, **sauf `api` → `git`**. Tout le reste
> transite par PostgreSQL.

et précise que « le rôle `api` n'a jamais accès au filesystem git. Ancestry,
diffs, quarantaine et promotion passent par une **API interne exposée par le rôle
`git`, sur un chemin distinct de celui du smart HTTP** ».

Cette API n'a jamais été conçue. Ni son transport, ni son contrôle d'accès, ni
l'emplacement de ses types ne sont décidés nulle part — les trois ADR et les deux
`doc.go` qui la mentionnent affirment son existence sans la définir.

La dette s'est payée. La phase 2D.5 a livré les diffs d'audit du §6.5 en faisant
lire un dépôt local au rôle `api` par une variable `WARDEN_REPO_ROOT`, c'est-à-dire
en faisant exactement l'inverse de la règle. Le paquet fautif a été supprimé et
l'endpoint laissé en 501, en attendant cette décision.

Le besoin immédiat est étroit : deux diffs, `mainline` et `previous`. Le décider
maintenant plutôt qu'au moment du smart HTTP évite de le décider trois fois.

## Solutions envisagées

### Transport

**Retenu — HTTP, corps de requête JSON, corps de réponse brut.** Le produit sert
déjà du HTTP dans trois rôles ; en ajouter un n'ajoute aucune dépendance. Le diff
sort en `text/plain` et non encapsulé dans une chaîne JSON : l'échapper et le
recopier des deux côtés coûterait sur plusieurs mégaoctets pour aucun bénéfice.

**Écarté — gRPC.** Une dépendance et un générateur pour une arête à un endpoint.
La question se reposera si l'arête grossit.

**Écarté — appel en process.** L'ADR-0006 permet de lancer les trois rôles dans
un seul process en développement, mais l'arête doit exister comme arête en
production : la coder comme un appel de fonction rendrait le déploiement séparé
non testable.

### Surface

**Retenu — un port distinct**, `WARDEN_GIT_INTERNAL_ADDR`, séparé de celui du
smart HTTP (`WARDEN_GIT_ADDR`, pas encore servi).

L'ADR-0006 demandait un chemin distinct. Un port satisfait cette exigence et va
plus loin, et c'est déjà ce que fait le reste du produit : `inbound/ingest` et
`outbound/dispatch` ont chacun le leur, avec sa propre règle d'ingress. Le §7 y
insiste — « quand l'ADR-0007 dit que le runner ne joint jamais l'API, cela se lit
jamais le **port** de l'API ».

La raison est spécifique à ce rôle : `gitd` est joignable depuis les pods runner,
donc depuis du code arbitraire (ADR-0007). Faire reposer l'isolation de l'API
interne sur le seul routage applicatif, dans le même processus qui sert du git à
un adversaire, est une frontière plus mince que nécessaire. Les deux adresses
existent dès maintenant pour que la NetworkPolicy s'écrive sans changement de
code le jour où le smart HTTP arrivera.

### Authentification

**Retenu — jeton porteur partagé**, lu par les deux rôles dans
`WARDEN_GIT_INTERNAL_TOKEN`, présenté en `Authorization: Bearer`.

La comparaison porte sur les **empreintes SHA-256** et non sur les octets : à
longueur fixe, sa durée ne dépend ni de la valeur présentée ni de sa taille.
L'ADR-0023 pose déjà qu'aucun secret ne se compare octet à octet.

Le refus est nu : 401, corps vide, pas de `WWW-Authenticate`, aucune distinction
entre jeton absent et jeton faux. Chacune de ces différences est une information
offerte à qui sonde.

**Écarté pour l'instant — mTLS.** C'est la cible, et il faut le dire clairement :
le jeton retenu **n'est pas une identité**. Il n'a ni expiration, ni audience, ni
porteur nommé, et sa rotation est un redéploiement des deux rôles. Il ferme
l'arête, il ne l'authentifie pas finement. Aucune PKI n'existe dans le projet et
en introduire une déborde de cette tranche.

**Écarté — NetworkPolicy seule.** Le `doc.go` de `gitd` et l'ADR-0007 exigent que
l'autorisation soit couverte par un test négatif. Sans credential applicatif, ce
test n'est pas écrivable à l'étage 1.

### Emplacement des types du contrat

**Retenu — `internal/server/internal/gitwire`**, partagé par les deux extrémités,
avec trois entrées `allow` dans `.golangci.yml`.

**Écarté — types dupliqués de chaque côté.** Deux définitions jumelles du même
message dérivent en silence, et la dérive ne se voit qu'en production.

**Écarté — `internal/wire/`.** C'est le seul emplacement que les deux atteignent
sans toucher au lint, mais ce répertoire est lié par les **trois** binaires, donc
par l'image du pod runner, hostile par hypothèse (ADR-0011). Y placer le contrat
interne du control plane le publierait à un adversaire sans qu'aucun runner n'en
ait l'usage — l'inverse exact de la raison pour laquelle `wire/ingest` a été
scindé de `wire/ws`.

L'assouplissement de `depguard` est réel et assumé. Le fichier dit lui-même que
chaque contournement affaiblit la règle qui tient l'ADR-0019 ; c'est pourquoi il
passe par une décision écrite plutôt que par un `//nolint`. Il est borné par une
quatrième règle, `serveur-contrat-git`, qui interdit à `gitwire` d'importer quoi
que ce soit du produit ou `net/http` : sans elle, les `allow` ouvriraient un
chemin de contournement entre `gitd` et `outbound/gitclient`, où il suffirait de
déposer du code.

### Ce que `gitd` sait du modèle de run

**Retenu — rien.** L'endpoint reçoit un dépôt et deux SHA. Il ne connaît ni
`run_ref`, ni `run`, et n'ouvre aucune connexion PostgreSQL.

C'est d'abord une décision de sécurité. Le §6.5 demande le diff « entre ce run et
le précédent sur le même inventaire », et ce run précédent appartient souvent à
**un autre utilisateur** : son arbre est son code. La politique d'accès vit dans
`inbound/api`, que `gitd` ne peut pas atteindre ; s'il résolvait lui-même le run
précédent, il court-circuiterait cette politique. La résolution ET l'autorisation
restent donc côté `api`, qui ne transmet que des SHA déjà autorisés.

Deux corollaires : la surface exposée reste minimale, ce que le `doc.go` de
`gitd` exige d'un service joignable depuis du code arbitraire ; et le paquet
n'ayant pas de base de données, tous ses tests restent à l'étage 1 de l'ADR-0010,
où le moteur git a sa place.

Le coût est un aller-retour supplémentaire pour `vs=previous` : une recherche sur
index, puis un appel HTTP. Négligeable.

## Décision

| Sujet | Décision |
|---|---|
| Transport | HTTP. `POST /internal/v1/diff`, requête JSON, réponse `text/plain` brute. |
| Surface | Port distinct du smart HTTP. |
| Authentification | Jeton porteur partagé par variable d'environnement, comparé sur empreintes, refus nu. |
| Contrat | `internal/server/internal/gitwire`, partagé, gardé par sa propre règle depguard. |
| Modèle de run | Inconnu de `gitd`. Le rôle `api` résout et autorise, puis transmet deux SHA. |
| Taille des réponses | Bornée à 8 Mio, troncature **signalée** par un en-tête. |

Les codes d'erreur du contrat distinguent `unknown_repo` de `unknown_object`, que
le statut HTTP confond en 404. Le second est le cas « code purgé » de l'ADR-0013,
que l'interface d'audit doit traiter en première classe : il devient un 410 et
non un 404, parce que le run a bel et bien existé.

## Conséquences

La borne de 8 Mio est un choix d'intégrité autant que de mémoire. Sans elle,
quelques centaines de mégaoctets de blob binaire dans un dépôt audité tueraient
le pod `git` — en instance unique, donc sans réplique pour absorber la perte — et
un réplica `api` au passage. Mais un diff coupé **en silence** serait un mensonge
dans un journal d'audit : un lecteur en conclurait que rien d'autre n'a changé.
D'où l'en-tête.

Le diff est intégralement en mémoire trois fois : dans `gitd`, dans le client,
dans la réponse. C'est acceptable sous la borne. Si l'interface d'audit se met à
en consulter en masse, il faudra basculer en flux, ce qui suppose de changer le
port `core.RepoDiffer` — il rend une chaîne aujourd'hui. C'est la prochaine étape
connue, pas une surprise à venir.

Le jeton statique est une dette nommée, au même titre que l'était l'identité
d'amorçage avant l'ADR-0023. Tant qu'il n'est pas remplacé, l'arête `api → git`
n'a pas d'identité, seulement une serrure.

L'ADR-0006 rangeait le versionnage d'une API interne parmi les coûts qu'il
cherchait à éviter. Le préfixe `/internal/v1/` l'assume : c'est le premier, et il
n'est utile que si un `v2` peut coexister le jour où le contrat change.

Enfin, il reste une provenance non décidée. `run.repo` et `runner_pod.repo`
doivent dériver identiquement, et rien ne les dérive aujourd'hui : deux
dérivations divergentes feraient servir le mauvais dépôt à un pod chaud. Le
commentaire de colonne de la migration 00004 le dit ; il faudra une décision
quand le premier écrivain existera.
