package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jijins1/warden/internal/events"
)

// maxEventsPerStatement borne le nombre d'events par instruction INSERT.
//
// PostgreSQL plafonne à 65535 le nombre de paramètres liés d'une instruction ;
// à dix colonnes par event, la limite dure est vers 6500. Le plafond réel est
// choisi bien en dessous parce que le but n'est pas de s'en approcher : c'est
// de garder UNE instruction par lot, donc un seul déclenchement du trigger qui
// tient le compteur `changed`.
const maxEventsPerStatement = 500

// Ingest est l'accès d'ingestion. Il n'expose que l'ajout d'events.
//
// La restriction réelle est le rôle PostgreSQL, restreint à INSERT sur les
// tables d'events (ADR-0008) ; ce type la rend visible dans le code. Les deux
// comptent : le type empêche d'écrire par distraction du code qui ferait autre
// chose, le rôle empêche que ce code ait le moindre effet s'il finissait par
// exister.
type Ingest struct {
	pool *pgxpool.Pool
}

// OpenIngest ouvre le pool d'ingestion.
//
// Le DSN doit porter le rôle restreint, pas le rôle applicatif. Rien ici ne
// peut le vérifier — c'est une propriété du déploiement, couverte par le test
// négatif de l'ADR-0008.
func OpenIngest(ctx context.Context, dsn string) (*Ingest, error) {
	pool, err := openPool(ctx, dsn, "warden-ingest")
	if err != nil {
		return nil, err
	}
	return &Ingest{pool: pool}, nil
}

// Close relâche les connexions.
func (i *Ingest) Close() { i.pool.Close() }

// AppendEvents insère un lot d'events et leurs diffs, en une transaction.
//
// L'insertion est IDEMPOTENTE sur (run_id, seq) : l'offset étant assigné par le
// runner, une réémission est sans danger et l'ingestion se parallélise sur N
// réplicas sans contrainte d'ordre (ADR-0008). L'ordre est reconstitué à la
// lecture.
//
// `run_day` est lu depuis la line de run, jamais accepté de l'appelant. C'est
// la clé de partition : une valeur choisie par un pod hostile ferait écrire les
// events d'un run dans la partition d'un autre mois, et casserait l'unicité qui
// fonde l'idempotence.
func (i *Ingest) AppendEvents(ctx context.Context, runID int64, evs []events.Event) error {
	if len(evs) == 0 {
		return nil
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("run %d: %w", runID, err)
		}
	}

	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ouverture de transaction d'ingestion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Le rôle d'ingestion n'a le SELECT que sur ces deux colonnes. Cette
	// lecture vaut aussi contrôle d'existence : sans elle, un run inconnu
	// donnerait zéro line insérée sans rien signaler.
	var runDay any
	err = tx.QueryRow(ctx, `SELECT run_day FROM run WHERE id = $1`, runID).Scan(&runDay)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lecture de la partition du run %d: %w", runID, err)
	}

	for chunk := range chunks(evs, maxEventsPerStatement) {
		if err := insertEvents(ctx, tx, runID, runDay, chunk); err != nil {
			return err
		}
		if err := insertDiffs(ctx, tx, runID, runDay, chunk); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("validation du lot d'events du run %d: %w", runID, err)
	}
	return nil
}

// insertEvents écrit un lot en UNE instruction.
//
// Une instruction par event serait plus simple à lire, mais le compteur
// `changed` est tenu par un trigger STATEMENT à table de transition : autant
// d'instructions, autant d'UPDATE sur la line de run.
func insertEvents(ctx context.Context, tx pgx.Tx, runID int64, runDay any, evs []events.Event) error {
	const cols = 10

	var b strings.Builder
	b.WriteString(`INSERT INTO event (run_id, run_day, seq, occurred_at, host, ` +
		`task_number, task_name, role, state, payload) VALUES `)

	args := make([]any, 0, len(evs)*cols)
	for n, e := range evs {
		if n > 0 {
			b.WriteByte(',')
		}
		writePlaceholders(&b, n*cols, cols)
		args = append(args,
			runID, runDay, e.Seq, e.OccurredAt,
			nullIfEmpty(e.Host), nullIfZero(e.TaskNumber),
			nullIfEmpty(e.TaskName), nullIfEmpty(e.Role),
			string(e.State), []byte(e.Payload),
		)
	}
	// L'idempotence est ici. Les lignes écartées n'entrent pas dans la table de
	// transition du trigger : une réémission ne double pas le compteur.
	b.WriteString(` ON CONFLICT DO NOTHING`)

	if _, err := tx.Exec(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("insertion de %d events du run %d: %w", len(evs), runID, err)
	}
	return nil
}

// insertDiffs écrit les payloads --diff dans leur table séparée.
//
// Après les events du même lot, dans la même transaction : la clé étrangère
// l'exige, et c'est elle qui interdit un diff orphelin — donc un secret rendu
// qui survivrait à l'event auquel il se rapporte.
func insertDiffs(ctx context.Context, tx pgx.Tx, runID int64, runDay any, evs []events.Event) error {
	const cols = 4

	var b strings.Builder
	b.WriteString(`INSERT INTO event_diff (run_id, run_day, seq, diff) VALUES `)

	args := make([]any, 0, cols)
	n := 0
	for _, e := range evs {
		if e.Diff == nil {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		writePlaceholders(&b, n*cols, cols)
		args = append(args, runID, runDay, e.Seq, []byte(e.Diff))
		n++
	}
	if n == 0 {
		return nil
	}
	b.WriteString(` ON CONFLICT DO NOTHING`)

	if _, err := tx.Exec(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("insertion de %d diffs du run %d: %w", n, runID, err)
	}
	return nil
}

// writePlaceholders écrit `($k+1,$k+2,...,$k+n)`.
//
// Seuls des numéros de paramètres sont assemblés dans la requête. Aucune donnée
// n'y entre : les valeurs passent toutes par les paramètres liés.
func writePlaceholders(b *strings.Builder, offset, n int) {
	b.WriteByte('(')
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(offset + i))
	}
	b.WriteByte(')')
}

// chunks découpe une tranche en morceaux d'au plus size éléments.
func chunks[T any](s []T, size int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for start := 0; start < len(s); start += size {
			end := min(start+size, len(s))
			if !yield(s[start:end]) {
				return
			}
		}
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
