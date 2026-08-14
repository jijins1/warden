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
