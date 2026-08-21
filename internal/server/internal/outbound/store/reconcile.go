package store

import (
	"context"
	"fmt"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// RunsEnCours rend les runs que la base croit encore en vol.
//
// « En vol » se lit sur DEUX colonnes et pas une : `state = 'running'` dit que
// `MarkRunning` a eu lieu, `ended_at IS NULL` dit que personne n'a encore clos.
// Un run inséré mais jamais démarré ne remonte pas ici — `CreateRun` l'écrit
// d'emblée `interrupted` / `unknown` (§6.4), donc il dit déjà la vérité et il
// n'y a rien à réconcilier.
//
// `runner_pod` peut être NULL : le pod n'a alors jamais été enregistré, et c'est
// au cœur de décider ce qu'on peut en dire.
func (s *Store) RunsEnCours(ctx context.Context) ([]core.RunEnCours, error) {
	const q = `
SELECT id, run_ref, coalesce(runner_pod, ''), started_at
  FROM run
 WHERE state = $1
   AND ended_at IS NULL
 ORDER BY started_at`

	lignes, err := s.pool.Query(ctx, q, string(core.RunRunning))
	if err != nil {
		return nil, fmt.Errorf("lecture des runs en vol: %w", err)
	}
	defer lignes.Close()

	var out []core.RunEnCours
	for lignes.Next() {
		var r core.RunEnCours
		if err := lignes.Scan(&r.ID, &r.RunRef, &r.PodName, &r.StartedAt); err != nil {
			return nil, fmt.Errorf("lecture d'un run en vol: %w", err)
		}
		out = append(out, r)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("lecture des runs en vol: %w", err)
	}
	return out, nil
}

// CloseRunSiOuvert clôt un run SANS écraser une clôture déjà faite.
//
// La garde `ended_at IS NULL` est ce qui rend le réconciliateur utilisable : le
// runner clôt son propre run par l'ingestion (§6.4, marche 2) et son pod se
// termine juste après, donc la course est PERMANENTE. Sans elle, un passage de
// réconciliation remplacerait « succeeded » par « interrupted / pod_evicted » sur
// un run qui s'est parfaitement terminé — c'est-à-dire qu'il inscrirait un
// mensonge dans le journal d'audit, précisément ce qu'il existe pour éviter.
//
// Elle porte aussi l'IDEMPOTENCE que le §8.7 exige : la leader election par
// Lease ne garantit pas l'unicité, et deux contrôleurs qui agissent en même
// temps ne doivent pas produire deux clôtures différentes.
//
// L'état est toujours `interrupted` : si le réconciliateur en est réduit à
// clore, c'est que le runner n'a rien rapporté, donc rien ne permet de dire que
// le playbook a réussi ou échoué. `succeeded` ici serait une invention.
//
// Rend `true` si la clôture a effectivement eu lieu.
func (s *Store) CloseRunSiOuvert(ctx context.Context, runID int64, cause core.TerminationCause) (bool, error) {
	if !cause.Valid() {
		return false, fmt.Errorf("cause de terminaison %q inconnue", cause)
	}

	const q = `
UPDATE run
   SET state = $2, termination_cause = $3, ended_at = now()
 WHERE id = $1
   AND ended_at IS NULL`

	tag, err := s.pool.Exec(ctx, q, runID, string(core.RunInterrupted), string(cause))
	if err != nil {
		return false, fmt.Errorf("clôture du run %d par réconciliation: %w", runID, err)
	}
	return tag.RowsAffected() == 1, nil
}
