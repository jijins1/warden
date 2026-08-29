package api

import (
	"net/http"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// runDetail est un run tel qu'une interface le peint.
//
// Un type de FIL explicite, avec des tags `json`, et non `core.Run` sérialisé
// tel quel. Deux raisons, et la seconde est la vraie :
//
//   - la convention de fil du produit est le snake_case (`loginResponse`,
//     `ws.EventFrame`, `NewRunRequest` la suivent tous) ;
//   - `core.Run` porte `ID` et `UserID`, deux entiers séquentiels internes.
//     L'ADR-0021 a retiré la propriété d'énumération en exposant des ULID ;
//     les republier ici la rendrait au premier appel.
//
// `GET /runs` sérialise encore `core.RunListItem` directement, donc en
// PascalCase et avec l'`ID`. C'est une incohérence ANTÉRIEURE, et la corriger
// casserait la CLI qui lit déjà cette forme — elle est signalée, pas héritée.
type runDetail struct {
	RunRef      string `json:"run_ref"`
	Repo        string `json:"repo"`
	Environment string `json:"environment"`
	Inventory   string `json:"inventory,omitempty"`
	Playbook    string `json:"playbook,omitempty"`

	// Kind vaut `playbook` ou `enrollment`. Normalisé ici : en base, le vide
	// vaut `playbook` — c'est ce que sont toutes les lignes antérieures — et
	// laisser filtrer ce vide obligerait chaque client à connaître l'histoire
	// du schéma.
	Kind string `json:"kind"`

	AuditSHA    string `json:"audit_sha,omitempty"`
	BaseSHA     string `json:"base_sha,omitempty"`
	MainlineRef string `json:"mainline_ref,omitempty"`
	MainlineSHA string `json:"mainline_sha,omitempty"`

	State string `json:"state"`

	// TerminationCause est vide sauf sur un run `interrupted`, où elle est
	// toujours renseignée. Le client ne doit JAMAIS afficher l'état sans elle.
	TerminationCause string `json:"termination_cause,omitempty"`

	ChangedCount int `json:"changed_count"`

	// SecretsUnmasked dit que la rédaction de l'ADR-0017 a échoué : des valeurs
	// injectées peuvent être en clair dans les diffs de ce run. Un client qui
	// l'ignore montre des secrets sans le dire.
	SecretsUnmasked bool `json:"secrets_unmasked"`

	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`

	// ArtifactPruned dit que le code du run a été purgé (ADR-0013). Le run a
	// bien existé — ce sont ses diffs git qui sont devenus incalculables. Un
	// booléen EN PLUS de la date, pour que le cas se traite sans avoir à
	// interpréter un champ nul.
	ArtifactPruned   bool    `json:"artifact_pruned"`
	ArtifactPrunedAt *string `json:"artifact_pruned_at,omitempty"`
}

// HandleGetRun rend un run seul.
//
// GET /runs/{run_ref} — la route qui manquait, et son absence cassait la
// fonction première d'une interface web : un lien profond `/runs/01J…` rechargé
// n'avait aucun moyen de retrouver son run. Passer par `GET /runs` et filtrer
// ne marche pas, la liste étant paginée par curseur.
//
// L'accès passe par `requireRunAccess`, donc un run qu'on n'a pas le droit de
// voir répond 404 et non 403 : distinguer les deux ferait de l'endpoint un
// oracle des `run_ref` qui existent.
func (a *Server) HandleGetRun(w http.ResponseWriter, r *http.Request) {
	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}

	ecrireJSON(w, versRunDetail(run))
}

func versRunDetail(run core.Run) runDetail {
	genre := string(run.Kind)
	if genre == "" {
		genre = string(core.RunPlaybook)
	}

	d := runDetail{
		RunRef:           run.RunRef,
		Repo:             run.Repo,
		Environment:      run.Environment,
		Inventory:        run.Inventory,
		Playbook:         run.Playbook,
		Kind:             genre,
		AuditSHA:         run.AuditSHA,
		BaseSHA:          run.BaseSHA,
		MainlineRef:      run.MainlineRef,
		MainlineSHA:      run.MainlineSHA,
		State:            string(run.State),
		TerminationCause: string(run.TerminationCause),
		ChangedCount:     run.ChangedCount,
		SecretsUnmasked:  run.SecretsUnmasked,
		StartedAt:        run.StartedAt.UTC().Format(time.RFC3339),
		ArtifactPruned:   run.ArtifactPruned(),
	}
	if run.EndedAt != nil {
		fin := run.EndedAt.UTC().Format(time.RFC3339)
		d.EndedAt = &fin
	}
	if run.ArtifactPrunedAt != nil {
		purge := run.ArtifactPrunedAt.UTC().Format(time.RFC3339)
		d.ArtifactPrunedAt = &purge
	}
	return d
}
