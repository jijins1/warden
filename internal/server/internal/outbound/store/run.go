package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// NewRun porte ce qu'on connaît d'un run au moment de l'insérer, c'est-à-dire
// AVANT que le pod existe.
//
// On y trouve ce que git ne conserve PAS (ADR-0021) : le contenu à un SHA est
// figé par git et n'a pas à être recopié ici, mais la position d'une branche à
// une date ne l'est pas — les reflogs expirent et sont locaux au miroir.
type NewRun struct {
	// RunRef est un ULID, sûr comme nom de ref git (ADR-0007) et dans une URL.
	// C'est l'identifiant qui sort du produit ; l'id bigint reste interne.
	RunRef string
	UserID int64

	// Repo est le dépôt d'où vient le run. Le rôle `git` en déduit le bare repo
	// à ouvrir : l'ADR-0006 veut tous les runs d'un dépôt dans le même, et les
	// diffs du §6.5 en dépendent.
	Repo string

	Environment string
	Inventory   string
	Playbook    string

	AuditSHA string // le commit d'audit poussé par la CLI (§5.2)
	BaseSHA  string // son parent (ADR-0003)

	// MainlineRef et MainlineSHA figent le point de comparaison. Sans eux, le
	// diff « run contre branche principale » du §6.5 se calcule contre la
	// branche telle qu'elle est AUJOURD'HUI, et change donc à chaque
	// consultation. Le nom de la ref accompagne le SHA parce que la branche de
	// référence est une configuration par environnement (§3.2) : un SHA seul
	// cesse d'être interprétable si elle change.
	MainlineRef string
	MainlineSHA string

	// Flags tels qu'exécutés (§5.5). Il n'y a pas de colonne « demandé » en
	// face : la politique serveur étant mise de côté, demandé et effectif
	// coïncident (ADR-0021).
	Flags json.RawMessage

	RunnerImage       string // ADR-0014
	RunnerImageDigest string
}

// CreateRun insère la line de run et renvoie son identifiant technique.
//
// Le statut initial est `interrupted` / `unknown`, écrit AVANT la création du
// pod (§6.4). Ce n'est pas un artifice : entre cette insertion et le dispatch,
// un plantage du control plane laisse une line qui dit la vérité, là qu'un
// statut « en attente » aurait laissé un run indéfiniment en vol.
//
// `run_day` n'est pas passé : la valeur par défaut de la colonne en est la
// seule source, et c'est elle qui déterminera la partition de tous les events
// du run.
func (s *Store) CreateRun(ctx context.Context, n NewRun) (int64, error) {
	// Contrepartie du DROP DEFAULT de la migration 00004 : un run sans dépôt
	// s'insérerait sans bruit et son artefact deviendrait introuvable des mois
	// plus tard, au moment précis où quelqu'un en a besoin. L'échec est ici.
	if n.Repo == "" {
		return 0, errors.New("run créé sans dépôt : ses diffs d'audit seraient incalculables (§6.5)")
	}

	flags := n.Flags
	if len(flags) == 0 {
		flags = json.RawMessage(`{}`)
	}

	const q = `
INSERT INTO run (run_ref, user_id, repo, environment, inventory, playbook,
                 audit_sha, base_sha, mainline_ref, mainline_sha, flags,
                 runner_image, runner_image_digest, state, termination_cause)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
        NULLIF($12, ''), NULLIF($13, ''), $14, $15)
RETURNING id`

	var id int64
	err := s.pool.QueryRow(ctx, q,
		n.RunRef, n.UserID, n.Repo, n.Environment, n.Inventory, n.Playbook,
		n.AuditSHA, n.BaseSHA, n.MainlineRef, n.MainlineSHA, flags,
		n.RunnerImage, n.RunnerImageDigest,
		string(core.RunInterrupted), string(core.CauseUnknown),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("création du run %s: %w", n.RunRef, err)
	}
	return id, nil
}

// MarkRunning fait passer un run réclamé à l'état courant et enregistre le pod
// qui l'exécute.
//
// `runner_pod` sert à corréler avec les logs d'infrastructure, rien de plus :
// l'exit code du pod dit que le processus est mort, pas ce que le playbook a
// fait. La question d'audit qui compte — qu'est-ce qui avait déjà été modifié —
// ne peut venir que du flux d'events (§6.3, §6.4).
func (s *Store) MarkRunning(ctx context.Context, runID int64, pod string) error {
	const q = `
UPDATE run
   SET state = $2, termination_cause = NULL, runner_pod = NULLIF($3, '')
 WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, runID, string(core.RunRunning), pod)
	if err != nil {
		return fmt.Errorf("passage du run %d à l'état courant: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// CloseRun clôt un run.
//
// La cause est exigée pour un run interrompu et refusée sinon : c'est aussi une
// contrainte du schéma, vérifiée ici pour que le message nomme le problème au
// lieu de renvoyer une violation de contrainte.
func (s *Store) CloseRun(ctx context.Context, runID int64, state core.RunState, cause core.TerminationCause) error {
	if !state.Valid() {
		return fmt.Errorf("statut de run %q inconnu", state)
	}
	if state == core.RunRunning {
		return errors.New("CloseRun ne peut pas clore sur l'état `running`")
	}
	if state == core.RunInterrupted {
		if !cause.Valid() {
			return fmt.Errorf("cause de terminaison %q inconnue", cause)
		}
	} else if cause != "" {
		return fmt.Errorf("cause de terminaison %q sur un run %s", cause, state)
	}

	var causeArg *string
	if state == core.RunInterrupted {
		c := string(cause)
		causeArg = &c
	}

	const q = `
UPDATE run
   SET state = $2, termination_cause = $3, ended_at = now()
 WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, runID, string(state), causeArg)
	if err != nil {
		return fmt.Errorf("clôture du run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// MarkSecretsUnmasked signale qu'un run contient des valeurs non masquées, avec
// le motif.
//
// L'ADR-0017 exige de pouvoir répondre à « N runs contiennent des secrets non
// masqués ». Sans le motif, l'alerte est inexploitable : savoir qu'un masquage
// a échoué ne dit pas s'il faut faire tourner un secret.
func (s *Store) MarkSecretsUnmasked(ctx context.Context, runID int64, reason string) error {
	if reason == "" {
		return errors.New("un run marqué « secrets non masqués » doit porter son motif (ADR-0017)")
	}

	const q = `UPDATE run SET secrets_unmasked = true, secrets_unmasked_reason = $2 WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, runID, reason)
	if err != nil {
		return fmt.Errorf("marquage du run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// Run est le type du cœur, réexporté par alias (voir users.go).
type Run = core.Run

// RunByRef lit un run par son identifiant public.
func (s *Store) RunByRef(ctx context.Context, runRef string) (Run, error) {
	const q = `
SELECT id, run_ref, user_id, run_day, repo, environment, inventory, playbook,
       audit_sha, base_sha, mainline_ref, mainline_sha,
       state, coalesce(termination_cause, ''), changed_count, secrets_unmasked,
       artifact_pruned_at
  FROM run
 WHERE run_ref = $1`

	var r Run
	err := s.pool.QueryRow(ctx, q, runRef).Scan(
		&r.ID, &r.RunRef, &r.UserID, &r.RunDay, &r.Repo, &r.Environment, &r.Inventory, &r.Playbook,
		&r.AuditSHA, &r.BaseSHA, &r.MainlineRef, &r.MainlineSHA,
		&r.State, &r.TerminationCause, &r.ChangedCount, &r.SecretsUnmasked,
		&r.ArtifactPrunedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, fmt.Errorf("run %s: %w", runRef, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("lecture du run %s: %w", runRef, err)
	}
	return r, nil
}

// PreviousRun rend le run qui précède celui-ci sur (dépôt, environnement,
// inventaire) — le « qu'est-ce qui a changé depuis que ça marchait » du §6.5.
//
// La position du run courant est relue dans la même instruction plutôt que
// passée en argument : `core.Run` ne porte pas `started_at`, et l'y ajouter pour
// cette seule requête élargirait un type du domaine au bénéfice d'un adaptateur.
// Le surcoût est une recherche sur la clé primaire, dans la même requête.
//
// Le départage se fait sur (started_at, id) DESC, comme la pagination keyset de
// ListRuns : deux runs peuvent partager l'horodatage à la microseconde près.
//
// Aucun filtre sur l'état, et les runs purgés ne sont pas sautés. « Le run
// précédent » est le run précédent : en écarter un répondrait en silence à une
// autre question, ce qu'un outil d'audit ne doit pas faire. Un artefact absent
// se signale, il ne se contourne pas.
func (s *Store) PreviousRun(ctx context.Context, run Run) (Run, error) {
	const q = `
WITH courant AS (
    SELECT repo, environment, inventory, started_at, id FROM run WHERE id = $1
)
SELECT r.id, r.run_ref, r.user_id, r.run_day, r.repo, r.environment, r.inventory,
       r.playbook, r.audit_sha, r.base_sha, r.mainline_ref, r.mainline_sha,
       r.state, coalesce(r.termination_cause, ''), r.changed_count,
       r.secrets_unmasked, r.artifact_pruned_at
  FROM run r, courant c
 WHERE r.repo = c.repo
   AND r.environment = c.environment
   AND r.inventory = c.inventory
   AND (r.started_at, r.id) < (c.started_at, c.id)
 ORDER BY r.started_at DESC, r.id DESC
 LIMIT 1`

	var p Run
	err := s.pool.QueryRow(ctx, q, run.ID).Scan(
		&p.ID, &p.RunRef, &p.UserID, &p.RunDay, &p.Repo, &p.Environment, &p.Inventory,
		&p.Playbook, &p.AuditSHA, &p.BaseSHA, &p.MainlineRef, &p.MainlineSHA,
		&p.State, &p.TerminationCause, &p.ChangedCount, &p.SecretsUnmasked,
		&p.ArtifactPrunedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, fmt.Errorf("run précédant %s: %w", run.RunRef, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("lecture du run précédant %s: %w", run.RunRef, err)
	}
	return p, nil
}
