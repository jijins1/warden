package gitwire

// MirrorPath est servi sur le listener INTERNE du rôle `git`, comme DiffPath.
//
// Jamais sur le smart HTTP : rafraîchir un miroir est une opération que seul le
// rôle `api` déclenche, et l'exposer sur une surface joignable depuis un pod
// runner donnerait à du code arbitraire un moyen de faire sortir des connexions
// du rôle `git` vers une adresse qu'il choisit.
const MirrorPath = "/internal/v1/mirror"

// MirrorRequest demande le rafraîchissement du miroir de la branche protégée.
//
// L'ADR-0027 fait de ce miroir l'ANCRE du contrôle de fraîcheur : c'est le
// `--not` qui décide quels objets un push doit transmettre. Il doit donc être
// écrit par le serveur depuis une source que le client ne contrôle pas — un
// client qui désignerait comme principale un commit portant l'arbre convoité
// l'exclurait du périmètre et échapperait au contrôle comme au scan de secrets.
//
// D'où la répartition des rôles dans ce message. `api` détient la politique et
// lit la configuration du dépôt en base ; il dit QUOI rafraîchir. Le rôle `git`
// détient le volume et le credential de la forge ; il dit COMMENT. Aucun
// credential ne transite dans ce corps : il vivrait alors dans un journal de
// requêtes au premier incident.
type MirrorRequest struct {
	// Repo est l'identifiant de dépôt, résolu par `gitd` sous sa racine.
	Repo string `json:"repo"`

	// Remote est l'URL de la forge, telle que la politique serveur la déclare.
	// `gitd` la revalide : `git.ParseRemote` refuse tout ce qui n'est pas
	// http/https, donc `ext::` et `file://`, les deux transports qui exécutent
	// du code.
	Remote string `json:"remote"`

	// Ref est la branche protégée, côté forge ET côté miroir. Le même nom des
	// deux côtés : une correspondance qui diverge est une question de plus à se
	// poser au moment d'un incident.
	Ref string `json:"ref"`
}

// MirrorResponse rend la position de la branche après rafraîchissement.
type MirrorResponse struct {
	// SHA est le sommet de la branche protégée. C'est lui que le run fige dans
	// `mainline_sha` : sans cela le diff « run contre principale » du §6.5 se
	// recalculerait contre la branche d'aujourd'hui et changerait à chaque
	// consultation.
	SHA string `json:"sha"`
}

const (
	// CodeMirrorFailed : la forge est injoignable, refuse le credential, ou ne
	// connaît pas la branche.
	//
	// Un code distinct parce que la conséquence est distincte : ce n'est ni un
	// défaut de l'appelant ni une panne du moteur git, c'est une dépendance
	// externe. L'ADR-0027 note que le rôle `git` acquiert de ce fait un egress
	// vers la forge, et que « pendant son indisponibilité, aucun run ne démarre »
	// couvre désormais aussi la disponibilité de celle-ci.
	CodeMirrorFailed Code = "mirror_failed"
)

// ResolvePath rend le SHA d'une ref nommée, sur le listener INTERNE.
//
// Il existe pour fermer une fuite qui n'a rien à voir avec le protocole git.
// `audit_sha` était destiné à venir du corps de `POST /runs`, donc du client — et
// le diff d'audit du §6.5 compare `mainline_sha` à `audit_sha` sans se demander à
// qui ce dernier appartient. Un utilisateur déclarant l'`audit_sha` d'un autre
// lisait le code de cet autre à travers le diff de SON PROPRE run, auquel la
// politique d'accès lui donne légitimement droit.
//
// Le serveur ne l'accepte donc plus : il DÉRIVE la ref de (utilisateur, run),
// deux valeurs qu'il détient, et demande ici sa position. Rien à croire sur
// parole.
const ResolvePath = "/internal/v1/resolve"

// ResolveRequest demande la position d'une ref.
//
// Le rôle `git` ne se demande pas si l'appelant a le droit de la connaître : la
// politique d'accès vit chez `api`, qui compose la ref avant d'appeler. Un `gitd`
// qui trancherait lui-même court-circuiterait cette politique — c'est le même
// raisonnement que pour DiffRequest.
type ResolveRequest struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

// ResolveResponse rend la position de la ref.
type ResolveResponse struct {
	SHA string `json:"sha"`
}

// NamespacePath crée la ref d'espace de noms d'un run, sur le listener INTERNE.
//
// C'est ce que l'ADR-0007 appelle la réduction de portée « structurelle » : au
// claim, le rôle `git` crée pour le run une ref unique dans un espace dédié, et
// l'`upload-pack` servi au runner est lancé avec ce `GIT_NAMESPACE`. Le runner ne
// voit alors qu'une seule ref, la sienne.
//
// La ref est créée par le SERVEUR et jamais poussée par un client : c'est le
// point 4 de la conjonction de l'ADR-0027, et le hook refuse d'ailleurs
// catégoriquement toute écriture sous `refs/namespaces/`. Un client qui pourrait
// en créer une ferait croire au serveur qu'il détient un objet qu'il convoite —
// le premier vecteur de gitnamespaces(7).
const NamespacePath = "/internal/v1/namespace"

// NamespaceRequest demande la création de la ref d'un run.
type NamespaceRequest struct {
	Repo string `json:"repo"`

	// Namespace est la valeur de GIT_NAMESPACE, par exemple `runs/<run-id>`. Le
	// rôle `git` compose lui-même le chemin réel, imbrication comprise : la
	// composition naïve produit un chemin qui n'est jamais annoncé, sans erreur
	// ni trace (ADR-0026).
	Namespace string `json:"namespace"`

	// SHA est le commit que le runner doit matérialiser.
	SHA string `json:"sha"`
}

// CreateRepoPath matérialise le dépôt bare d'un dépôt configuré.
//
// Il comble un manque que rien ne signalait : un administrateur déclarait la
// configuration d'un dépôt — sa forge, sa branche protégée — mais PERSONNE ne
// créait `<racine>/<nom>.git` sur le volume du rôle `git`. Le premier run
// échouait alors sur « dépôt inconnu », message exact mais qui décrit un état
// que rien ne permettait d'atteindre.
//
// Sur le listener interne, comme le reste : créer un dépôt appartient à
// l'administration, pas au chemin chaud d'un run, et certainement pas à une
// surface joignable depuis un pod.
const CreateRepoPath = "/internal/v1/repos"

// CreateRepoRequest demande la création du dépôt bare.
//
// Idempotente par contrat : un dépôt qui existe déjà est un succès. La
// configuration est déclarative — un administrateur qui la réécrit ne doit pas
// avoir à se demander si le volume est déjà dans l'état voulu.
type CreateRepoRequest struct {
	Repo string `json:"repo"`
}
