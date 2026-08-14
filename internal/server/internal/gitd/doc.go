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
// ÉTAT : l'API interne de diff (ADR-0024), le smart HTTP en LECTURE et le smart
// HTTP en ÉCRITURE sont implémentés. Le scan de secrets, la promotion,
// l'ancestry et le cycle de vie des espaces de noms restent à écrire.
//
// Trois surfaces, trois ports d'écoute : l'API interne joignable du seul rôle
// `api`, la lecture joignable des pods runner, l'écriture joignable de la CLI.
// La séparation est portée par le PORT et non par le chemin, ce qui la rend
// exprimable en NetworkPolicy — et, pour l'écriture, elle est le contrôle n°3 de
// la conjonction de l'ADR-0027.
//
// L'écriture est servie contre une quarantaine native et un hook `pre-receive`
// qui est CE BINAIRE, atteint par un lien symbolique. C'est le seul endroit du
// produit où les hooks ne sont pas neutralisés ; l'exception est bornée par
// construction, `core.hooksPath` pointant vers un répertoire détenu par le rôle,
// hors de la racine des dépôts, posé par l'environnement — donc insurchargeable
// par la config d'un dépôt.
//
// `PushAuthorizer` a une implémentation depuis l'ADR-0028 : `GrantAuthorizer`,
// qui ne conserve AUCUN état. La portée voyage dans le jeton, signée par le rôle
// `api` avec le secret déjà partagé — rien à stocker, rien à synchroniser, rien
// à perdre au redémarrage d'une instance unique. `Authorizer`, lui, reste sans
// implémentation : le jeton de run de l'ADR-0007 n'existe toujours pas, et la
// migration 00003 dit explicitement qu'elle ne le porte jamais.
//
// Le miroir de la principale est servi par `MirrorPath` sur le listener INTERNE,
// jamais sur le smart HTTP : cet endpoint fait SORTIR une connexion vers une
// adresse figurant dans la requête, et l'exposer à un pod runner donnerait à du
// code arbitraire un moyen d'émettre des requêtes depuis le détenteur du volume.
//
// Le chemin d'écriture n'est TOUJOURS pas câblé, mais la raison a changé : ce
// n'est plus une propriété de sécurité manquante, c'est de la plomberie absente.
// Il manque la configuration de dépôt — quelle forge, quelle branche protégée —
// et la réservation de run, sans laquelle le serveur ne connaît pas la ref
// AVANT le push qu'il doit autoriser.
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
//  3. L'ADR-0027 a découvert une SECONDE forme du vecteur n°1, qui ne demande
//     aucune requête forgée : un commit d'audit neuf portant l'ARBRE d'autrui.
//     La conjonction compte donc cinq termes, dont deux vivent sur l'arête
//     d'ÉCRITURE — l'allowlist de refs et le contrôle de fraîcheur de
//     `prereceive.go`. La propriété d'isolation en LECTURE dépend d'eux.
//
// Il ne parle pas à PostgreSQL, et c'est délibéré. L'endpoint de diff reçoit
// deux objets et le dépôt où les trouver, jamais un run : « quel est le run
// précédent » est une règle de run, tranchée par le rôle `api` qui détient la
// politique d'accès. Un `gitd` qui la trancherait lui-même court-circuiterait
// cette politique — le run précédent appartient souvent à quelqu'un d'autre, et
// son arbre est son code. Accessoirement, rester sans base garde tout ce paquet
// à l'étage 1 de l'ADR-0010.
package gitd
