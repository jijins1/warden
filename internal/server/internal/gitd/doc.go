// Package gitd est le rôle `git` : le seul détenteur du volume des dépôts bare.
//
// Il sert le smart HTTP (receive-pack, upload-pack), tient la quarantaine et le
// scan de secrets, la promotion, la vérification d'ancestry en mode PROD (§3.2),
// les diffs d'audit (§6.5), la maintenance planifiée (ADR-0004) et le cycle de
// vie des espaces de noms de run (ADR-0007).
//
// Une seule instance, PVC RWO, redéploiement en Recreate : c'est un point de
// défaillance unique assumé (ADR-0006). Pendant son indisponibilité, aucun run
// ne démarre dans l'organisation.
//
// Il est joignable depuis les pods runner, donc depuis du code arbitraire : sa
// surface exposée doit rester minimale et son autorisation couverte par un test
// négatif — un run qui tente de lire la ref d'un autre run doit échouer.
//
// Ce paquet n'est ni un adaptateur entrant ni un adaptateur sortant du cœur : le
// rôle `git` est un SERVICE DISTINCT, qui sert le protocole git aux runners et à
// la CLI sans passer par le cœur, et que le cœur atteint par
// `outbound/gitclient`. Il vit donc à côté de la décomposition hexagonale de
// l'ADR-0019, pas dedans — l'ADR le rangeait à tort côté entrant.
//
// Arêtes autorisées : `outbound/store` et le socle `internal/git`, plus le
// contrat `gitwire` qu'il partage avec `outbound/gitclient`. Ce paquet n'importe
// ni `core`, ni `inbound`, ni les autres adaptateurs.
//
// ÉTAT : l'API interne de diff (ADR-0024) et le smart HTTP EN LECTURE sont
// implémentés. La quarantaine, le scan de secrets, la promotion, l'ancestry et
// le cycle de vie des espaces de noms restent à écrire.
//
// Le smart HTTP ne sert que `upload-pack`. L'écriture est refusée par un 403
// explicite tant que l'autorisation par REF n'existe pas : un push autorisé au
// seul niveau du dépôt laisserait n'importe quel porteur de jeton écrire sur la
// ref d'audit d'autrui, ce qui est précisément ce qu'un journal d'audit ne doit
// pas permettre. Elle viendra avec la quarantaine.
//
// L'autorisation est un PORT (`Authorizer`) et n'a pas d'implémentation ici. Le
// jeton de run de l'ADR-0007 n'existe encore nulle part — la migration 00003 dit
// explicitement qu'elle ne porte jamais « le jeton git de l'ADR-0007 » — et où
// vit son état est une décision ouverte, contrainte par le fait que ce paquet ne
// parle pas à PostgreSQL.
//
// AVERTISSEMENT sur la propriété d'isolation, tranché par l'ADR-0026 qui amende
// l'ADR-0007 sur ce point. L'ADR-0007 fait reposer la
// séparation des runs sur GIT_NAMESPACE en affirmant qu'elle est « appliquée par
// git lui-même, à l'extérieur de la négociation ». C'est plus fragile que
// l'ADR ne le dit, et deux écarts mesurés sont à connaître avant de toucher à
// `smarthttp.go` :
//
//  1. La documentation de git énonce que « namespaces on a server are not
//     effective for read access control » (gitnamespaces(7), section SECURITY).
//     L'isolation obtenue ici tient à la CONJONCTION du namespace, du refus de
//     `receive-pack` et de l'épinglage du protocole ci-dessous — pas au namespace
//     seul.
//  2. Sous le protocole v2, `uploadpack.allowAnySHA1InWant` n'est pas appliqué :
//     un client qui nomme un objet non annoncé par son SHA le reçoit, namespace
//     ou non. Le transport retire donc l'en-tête `Git-Protocol` pour figer la
//     négociation en v0, où le refus fonctionne.
//
// Il ne parle pas à PostgreSQL, et c'est délibéré. L'endpoint de diff reçoit
// deux objets et le dépôt où les trouver, jamais un run : « quel est le run
// précédent » est une règle de run, tranchée par le rôle `api` qui détient la
// politique d'accès. Un `gitd` qui la trancherait lui-même court-circuiterait
// cette politique — le run précédent appartient souvent à quelqu'un d'autre, et
// son arbre est son code. Accessoirement, rester sans base garde tout ce paquet
// à l'étage 1 de l'ADR-0010.
package gitd
