package core

// RunState est le statut d'un run (§6.4).
type RunState string

const (
	RunRunning     RunState = "running"
	RunSucceeded   RunState = "succeeded"
	RunFailed      RunState = "failed"
	RunInterrupted RunState = "interrupted"
)

// TerminationCause qualifie un run interrompu.
//
// « Interrompu » sans la cause est presque inutile (§6.4) : un Ctrl-C de
// l'utilisateur est un signal fort — il a vu quelque chose de travers — là où
// un client_disconnect_timeout est accidentel et ne signale rien. Les
// confondre revient à jeter l'information qui compte.
type TerminationCause string

const (
	// CauseUserInterrupt : l'utilisateur a interrompu. Signal fort.
	CauseUserInterrupt TerminationCause = "user_interrupt"
	// CauseClientDisconnectTimeout : accidentel, aucun signal.
	CauseClientDisconnectTimeout TerminationCause = "client_disconnect_timeout"
	// CauseServerTaskTimeout : configuration ou hôte bloqué.
	CauseServerTaskTimeout TerminationCause = "server_task_timeout"
	// CausePodOOMKilled : ressources.
	CausePodOOMKilled TerminationCause = "pod_oom_killed"
	// CausePodEvicted : infrastructure.
	CausePodEvicted TerminationCause = "pod_evicted"
	// CauseNodeLost : infrastructure.
	CauseNodeLost TerminationCause = "node_lost"
	// CauseAdminKilled : reste à dire qui, et pourquoi.
	CauseAdminKilled TerminationCause = "admin_killed"
	// CauseUnknown est délibérément conservée. Ce n'est pas une lacune de
	// conception, c'est un aveu honnête : la supprimer par principe conduirait
	// à inventer une cause plausible, bien pire en post-mortem. L'objectif est
	// de la rendre rare, pas de l'éliminer.
	//
	// C'est aussi la valeur INITIALE, écrite avant la création du pod : un
	// plantage entre l'insertion et le dispatch laisse alors une ligne exacte
	// plutôt qu'un run éternellement « en cours ».
	CauseUnknown TerminationCause = "unknown"
)

// Valid dit si le statut fait partie de ceux que le schéma accepte.
func (s RunState) Valid() bool {
	switch s {
	case RunRunning, RunSucceeded, RunFailed, RunInterrupted:
		return true
	}
	return false
}

// Valid dit si la cause fait partie de celles que le schéma accepte.
func (c TerminationCause) Valid() bool {
	switch c {
	case CauseUserInterrupt, CauseClientDisconnectTimeout, CauseServerTaskTimeout,
		CausePodOOMKilled, CausePodEvicted, CauseNodeLost, CauseAdminKilled, CauseUnknown:
		return true
	}
	return false
}

// UserType est le fournisseur d'identité par lequel un utilisateur est arrivé
// (ADR-0021).
type UserType string

const (
	UserOIDC UserType = "oidc"
	UserLDAP UserType = "ldap"
	// UserLocal existe pour le POC, afin de ne pas exiger un IdP pour
	// démarrer. Il doit être désactivable par configuration et désactivé hors
	// développement : un backend d'authentification interne dans un produit
	// d'audit est utile trois mois et gênant ensuite.
	UserLocal UserType = "local"
)

// Valid dit si le type fait partie de ceux que le schéma accepte.
func (t UserType) Valid() bool {
	switch t {
	case UserOIDC, UserLDAP, UserLocal:
		return true
	}
	return false
}
