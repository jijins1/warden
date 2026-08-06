package flux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/events"
)

// ErrNotFound est retournée quand le run demandé n'existe pas.
var ErrNotFound = errors.New("introuvable")

// batchLimit borne ce qu'une lecture rapporte d'un coup.
//
// Un client qui se rattache après une longue coupure peut avoir des milliers
// d'events de retard (§9.1, replay depuis un offset) : les charger d'un bloc
// ferait dépendre la mémoire du réplica de l'ancienneté de la reconnexion.
const batchLimit = 500

// Stream est un suivi en cours.
//
// Le canal est fermé quand le run est terminé, quand le contexte est annulé ou
// quand une erreur survient. Err() doit être consultée APRÈS la fermeture du
// canal : c'est elle qui distingue « le run est fini » de « le suivi s'est
// interrompu », et les confondre afficherait un run tronqué comme un run réussi.
type Stream struct {
	events chan events.Event

	mu  sync.Mutex
	err error
}

// Events est le canal des events, dans l'ordre de `seq`.
//
// Il ne porte PAS les payloads --diff. Ils vivent dans une table à contrôle
// d'accès distinct parce qu'ils contiennent les secrets rendus (§6.5) ; les
// faire passer par le flux de suivi annulerait cette séparation au premier
// client connecté. Les consulter est une action distincte, et autorisée
// séparément.
func (s *Stream) Events() <-chan events.Event { return s.events }

// Err rend la cause d'arrêt, ou nil si le run s'est terminé normalement.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Stream) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Follow suit un run à partir de fromSeq, historique compris.
//
// Il n'y a pas deux chemins « rattrapage » et « direct » : il n'y en a qu'un,
// qui lit la table. NOTIFY ne fait que dire quand relire. C'est ce qui rend le
// detach/reattach, le rolling update et le multi-réplica couverts par le même
// mécanisme (ADR-0008), et c'est aussi ce qui garantit qu'on ne livre jamais ce
// qui n'est pas journalisé (§6.3).
func (f *Flux) Follow(ctx context.Context, runID, fromSeq int64) (*Stream, error) {
	runDay, err := f.runDay(ctx, runID)
	if err != nil {
		return nil, err
	}

	// L'abonnement est pris AVANT la première lecture : entre les deux, une
	// notification serait perdue et l'abonné attendrait le poll pour rien.
	wake := f.subscribe(runID)

	s := &Stream{events: make(chan events.Event, batchLimit)}
	go func() {
		defer close(s.events)
		defer f.unsubscribe(runID, wake)
		f.pump(ctx, s, runID, runDay, fromSeq, wake)
	}()
	return s, nil
}

func (f *Flux) pump(ctx context.Context, s *Stream, runID int64, runDay time.Time, seq int64, wake <-chan struct{}) {
	for {
		// L'ordre compte. On lit la fin du run AVANT les events : si le run
		// était fini à l'instant T, tout ce qui a été validé avant T est visible
		// dans la lecture qui suit. Lire les events d'abord laisserait une
		// fenêtre où un dernier lot arrive entre les deux lectures et n'est
		// jamais servi.
		//
		// Cela repose sur une propriété du système, pas de la base : le run est
		// clos APRÈS l'acquittement du dernier lot du runner. Si cet ordre
		// s'inversait, la fin du flux redeviendrait une course.
		ended, err := f.runEnded(ctx, runID)
		if err != nil {
			if ctx.Err() == nil {
				s.fail(err)
			}
			return
		}

		batch, err := f.since(ctx, runID, runDay, seq)
		if err != nil {
			if ctx.Err() == nil {
				s.fail(err)
			}
			return
		}

		for _, e := range batch {
			select {
			case s.events <- e:
				seq = e.Seq + 1
			case <-ctx.Done():
				return
			}
		}

		// Lot plein : il reste très probablement à lire, on enchaîne sans
		// attendre.
		if len(batch) == batchLimit {
			continue
		}
		if ended {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(pollInterval):
		}
	}
}

// History sert un intervalle d'events sans suivre le direct.
//
// C'est le même chemin de lecture que Follow, exposé seul pour l'interface
// d'audit : consulter un run de mars ne doit pas ouvrir un abonnement.
func (f *Flux) History(ctx context.Context, runID, fromSeq int64) ([]events.Event, error) {
	runDay, err := f.runDay(ctx, runID)
	if err != nil {
		return nil, err
	}
	return f.since(ctx, runID, runDay, fromSeq)
}

// runDay lit la partition du run.
//
// Elle est passée à chaque lecture d'events pour que PostgreSQL élimine les
// autres partitions : sans elle, chaque lot lu balaierait toutes les partitions
// de la table, dont le nombre ne fait que croître puisqu'on ne purge jamais
// (ADR-0013). Les events d'un run tenant dans une seule partition, la
// restriction est exacte.
func (f *Flux) runDay(ctx context.Context, runID int64) (time.Time, error) {
	var day time.Time
	err := f.pool.QueryRow(ctx, `SELECT run_day FROM run WHERE id = $1`, runID).Scan(&day)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("lecture du run %d: %w", runID, err)
	}
	return day, nil
}

// runEnded dit si le run est clos.
//
// Le critère est `ended_at`, et NON un statut terminal : un run naît
// `interrupted` / `unknown`, écrit avant même que le pod existe (§6.4). Se fier
// au statut fermerait le flux à la seconde où il s'ouvre.
func (f *Flux) runEnded(ctx context.Context, runID int64) (bool, error) {
	var ended bool
	err := f.pool.QueryRow(ctx,
		`SELECT ended_at IS NOT NULL FROM run WHERE id = $1`, runID).Scan(&ended)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("lecture de l'état du run %d: %w", runID, err)
	}
	return ended, nil
}

func (f *Flux) since(ctx context.Context, runID int64, runDay time.Time, fromSeq int64) ([]events.Event, error) {
	const q = `
SELECT seq, occurred_at,
       coalesce(host, ''), coalesce(task_number, 0), coalesce(task_name, ''), coalesce(role, ''),
       state, payload
  FROM event
 WHERE run_id = $1 AND run_day = $2 AND seq >= $3
 ORDER BY seq
 LIMIT $4`

	rows, err := f.pool.Query(ctx, q, runID, runDay, fromSeq, batchLimit)
	if err != nil {
		return nil, fmt.Errorf("lecture des events du run %d: %w", runID, err)
	}
	defer rows.Close()

	var out []events.Event
	for rows.Next() {
		var e events.Event
		var payload []byte
		if err := rows.Scan(&e.Seq, &e.OccurredAt, &e.Host, &e.TaskNumber,
			&e.TaskName, &e.Role, &e.State, &payload); err != nil {
			return nil, fmt.Errorf("lecture des events du run %d: %w", runID, err)
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lecture des events du run %d: %w", runID, err)
	}
	return out, nil
}
