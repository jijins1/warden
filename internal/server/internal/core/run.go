package core

import (
	"context"
	"errors"
	"fmt"
)

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
	// plantage entre l'insertion et le dispatch laisse alors une line exacte
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

// Closure est la clôture d'un run telle que le runner la rapporte (§6.4).
//
// Elle vit dans le cœur et non dans un adaptateur parce que deux adaptateurs en
// dépendent — `inbound/ingest` la reçoit du fil, `outbound/store` l'écrit — et
// que l'ADR-0019 leur interdit de se connaître. Même cas d'école que
// `PodIdentity`.
//
// Elle ne porte PAS le `run_id` : celui-ci est résolu côté serveur depuis le
// jeton présenté, jamais accepté du corps d'une requête (ADR-0021).
type Closure struct {
	State RunState
	// Cause est exigée si et seulement si State vaut RunInterrupted.
	Cause TerminationCause
	// SecretsUnmasked porte le MOTIF de l'ADR-0017, vide quand la rédaction
	// s'est appliquée normalement. Le motif et non un booléen : savoir qu'un
	// masquage a échoué ne dit pas s'il faut faire tourner un secret.
	SecretsUnmasked string
}

// Validate applique les invariants que le schéma impose de toute façon.
//
// Au bord plutôt qu'au fond : une violation de contrainte PostgreSQL ne dit pas
// ce qui manque, alors qu'ici on peut le nommer. Et le contenu vient d'un pod
// hostile — le contrat de fil l'a déjà validé de son côté, ce qui ne vaut rien.
func (c Closure) Validate() error {
	if !c.State.Valid() {
		return fmt.Errorf("statut de run %q inconnu", c.State)
	}
	if c.State == RunRunning {
		// Une clôture qui ne clôt pas laisserait `ended_at` posé sur un run
		// toujours en vol, donc une durée mesurée sur un run qui continue.
		return errors.New("une clôture ne peut pas porter l'état `running`")
	}
	if c.State == RunInterrupted {
		if !c.Cause.Valid() {
			return fmt.Errorf("cause de terminaison %q inconnue", c.Cause)
		}
	} else if c.Cause != "" {
		return fmt.Errorf("cause de terminaison %q sur un run %s", c.Cause, c.State)
	}
	return nil
}

// RunStateWriter marque un run en cours d'exécution.
//
// Le port existe pour que l'adaptateur entrant puisse enregistrer ce fait sans
// connaître PostgreSQL. Il est appelé quand le control plane a créé le pod du
// run — c'est-à-dire sur un fait qu'IL constate, pas sur la parole du pod, qui
// est du code hostile.
type RunStateWriter interface {
	MarkRunning(ctx context.Context, runID int64, pod string) error
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
