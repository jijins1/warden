// Package gitclient est le client du rôle `api` vers le rôle `git` : ancestry,
// diffs, quarantaine, promotion, lecture d'un blob de configuration.
//
// C'est LA SEULE ARÊTE AUTORISÉE entre deux rôles (ADR-0006). Tout le reste
// transite par PostgreSQL. Le rôle `api` n'a jamais accès au filesystem git.
//
// Adaptateur SORTANT du cœur (ADR-0019), et non différable : l'ADR-0007 place la
// création de la ref d'espace de noms sur le chemin du claim.
//
// L'API interne appelée ici est exposée sur un PORT distinct de celui du smart
// HTTP servi aux runners (ADR-0024). L'ADR-0006 demandait un chemin distinct ;
// séparer par port satisfait cette exigence et va plus loin, ce que fait déjà le
// reste du produit — `inbound/ingest` et `outbound/dispatch` ont chacun le leur,
// avec leur règle d'ingress. Sur un service joignable depuis du code arbitraire
// (ADR-0007), l'isolation ne doit pas reposer sur le seul routage applicatif.
//
// ÉTAT : seuls les diffs sont implémentés. L'ancestry, la quarantaine, la
// promotion et la lecture d'un blob de configuration restent à écrire.
//
// Le client n'envoie jamais de run_ref : il transmet un dépôt et deux SHA, que
// l'appelant a résolus ET autorisés. C'est ce qui garde la politique d'accès du
// côté qui la détient — le run précédent d'un inventaire appartient souvent à un
// autre utilisateur, et son arbre est son code.
package gitclient
