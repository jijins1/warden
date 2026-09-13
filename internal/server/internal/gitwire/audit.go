package gitwire

// AuditRefPath fait pointer une ref d'audit sur un commit EXISTANT, sur le
// listener INTERNE.
//
// Il existe parce qu'un navigateur ne pousse pas, et surtout n'a RIEN À
// CAPTURER : ce qu'il lance est la branche protégée telle quelle. L'artefact
// d'audit d'un tel run n'est donc pas un nouveau commit — c'est une SECONDE REF
// sur le commit que MirrorPath vient de résoudre. Git admet nativement plusieurs
// refs sur un même objet ; rien en aval ne distingue « commit fraîchement
// frappé » de « commit existant référencé une seconde fois ».
//
// JAMAIS sur le smart HTTP : cet endpoint ÉCRIT une ref. Exposé à un pod runner,
// il donnerait à du code arbitraire le droit de fabriquer des refs d'audit —
// de falsifier l'artefact que le produit existe pour conserver.
const AuditRefPath = "/internal/v1/audit-ref"

// AuditRefRequest est un ordre git PUR, comme ResolveRequest et NamespaceRequest.
//
// Aucun `user_id`, aucun `run_ref` : le rôle `git` reçoit un NOM DE REF littéral,
// composé par `api` à partir de l'identité authentifiée (core.AuditRefFor). Lui
// faire composer la ref ferait de `gitd` un dépositaire de la politique d'audit.
type AuditRefRequest struct {
	Repo string `json:"repo"`

	// Ref est le nom littéral à créer. Sous `refs/audit/` : ce service refuse
	// d'écrire ailleurs, pour que ce chemin ne puisse pas devenir un moyen de
	// déplacer le pointeur de principale ou fabriquer une ref d'espace de noms.
	Ref string `json:"ref"`

	// CommitSHA est le commit EXISTANT à pointer — celui que MirrorPath a rendu.
	// Aucun contrôle de fraîcheur n'est requis : rien n'est transmis, l'objet vit
	// déjà dans le dépôt, et cette valeur vient du serveur, jamais du client.
	CommitSHA string `json:"commit_sha"`
}

// AuditRefResponse dit si l'écriture vient d'avoir lieu.
//
// `Created` n'est pas décoratif : un rejeu rend `false` pour le MÊME commit, et
// l'appelant doit pouvoir journaliser la différence sans la traiter comme une
// erreur.
type AuditRefResponse struct {
	Created bool `json:"created"`
}

// CodeRefExists : la ref existe et pointe AILLEURS que sur le commit demandé.
//
// Distinct d'un rejeu, qui est un succès. C'est un identifiant de run réutilisé
// sur un autre commit — la seule chose qu'un journal d'audit ne doit jamais
// permettre (ADR-0027).
const CodeRefExists Code = "ref_exists"
