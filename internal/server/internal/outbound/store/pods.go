package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// ErrPodUnknown est renvoyée quand un jeton d'amorçage ne correspond à aucun pod
// vivant.
//
// Elle ne distingue pas « jeton inconnu » de « pod libéré », et l'appelant ne
// doit pas non plus : la réponse HTTP est la même dans les deux cas, sans quoi
// elle dirait à un porteur de jeton si son pod a existé.
var ErrPodUnknown = errors.New("pod inconnu")

// ErrNoWarmPod est renvoyée quand aucun pod chaud n'est disponible pour le pool
// demandé.
//
// Ce n'est PAS une condition d'erreur : c'est le cas nominal du §8.4, où la
// seconde commande lancée en parallèle « doit tomber en cold start proprement,
// ni attendre ni recevoir le même pod ». L'appelant crée un pod et paie le
// démarrage.
var ErrNoWarmPod = errors.New("aucun pod chaud disponible")

// NewPod décrit un pod chaud au moment où le contrôleur l'enregistre.
//
// L'enregistrement précède la création du pod : le Secret qui porte le jeton
// doit exister avant que le kubelet monte le volume, faute de quoi le pod attend
// au montage — et l'attente au démarrage est ce que le pré-chauffage supprime
// (§8.1).
type NewPod struct {
	PodName string
	Pool    core.PodPool
	// Token n'est pas stocké : seule son empreinte l'est (ADR-0023).
	Token core.PodToken
}

// RegisterPod enregistre un pod chaud et son identité d'amorçage.
func (s *Store) RegisterPod(ctx context.Context, n NewPod) (int64, error) {
	if n.PodName == "" {
		return 0, errors.New("nom de pod vide")
	}
	if n.Token.Zero() {
		return 0, errors.New("pod enregistré sans jeton d'amorçage")
	}

	const q = `
INSERT INTO runner_pod (pod_name, user_id, repo, environment, token_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING id`

	var id int64
	err := s.pool.QueryRow(ctx, q,
		n.PodName, n.Pool.UserID, n.Pool.Repo, n.Pool.Environment, n.Token.Hash(),
	).Scan(&id)
	if err != nil {
		// Le jeton n'apparaît dans aucun message d'erreur, y compris en cas de
		// collision d'empreinte.
		return 0, fmt.Errorf("enregistrement du pod %s: %w", n.PodName, err)
	}
	return id, nil
}

// SetPodUID renseigne l'UID Kubernetes, connu seulement après création.
//
// Purement corrélatif : l'identité du pod est son nom et son jeton. L'UID sert à
// distinguer deux pods homonymes dans les logs d'infrastructure, le jour où ça
// arrive.
func (s *Store) SetPodUID(ctx context.Context, podID int64, uid string) error {
	const q = `UPDATE runner_pod SET pod_uid = NULLIF($2, '') WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, podID, uid)
	if err != nil {
		return fmt.Errorf("mise à jour de l'UID du pod %d: %w", podID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pod %d: %w", podID, ErrNotFound)
	}
	return nil
}

// AuthenticatePod résout un jeton d'amorçage en identité de pod.
//
// C'est le point où le lien `pod -> run` est établi CÔTÉ SERVEUR (ADR-0023). Un
// `run_id` présent dans le body d'une requête d'ingestion n'a pas à être
// consulté : la source est ici, et nulle part ailleurs.
//
// La recherche se fait par empreinte : le jeton présenté n'est jamais comparé au
// jeton d'origine, lequel n'existe pas côté serveur.
func (s *Store) AuthenticatePod(ctx context.Context, token core.PodToken) (core.PodIdentity, error) {
	if token.Zero() {
		return core.PodIdentity{}, ErrPodUnknown
	}

	const q = `
SELECT id, coalesce(run_id, 0)
  FROM runner_pod
 WHERE token_hash = $1 AND released_at IS NULL`

	var p core.PodIdentity
	err := s.pool.QueryRow(ctx, q, token.Hash()).Scan(&p.PodID, &p.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.PodIdentity{}, ErrPodUnknown
	}
	if err != nil {
		// Ni le jeton ni son empreinte dans le message : un log d'erreur ne doit
		// pas devenir un dictionnaire d'identités de pods.
		return core.PodIdentity{}, fmt.Errorf("authentification du pod: %w", err)
	}
	return p, nil
}

// SetPodReady marque le pod joignable, ou cesse de le marquer.
//
// `ready` traduit l'existence d'une connexion de dispatch tenue par CE réplica.
// C'est le prédicat « connexion oisive » de l'ADR-0018 : on ne réclame que des
// pods effectivement joignables.
//
// Marquer un pod libéré comme joignable est refusé — un pod mort ne redevient
// pas réclamable, la contrainte du schéma le dit aussi.
func (s *Store) SetPodReady(ctx context.Context, podID int64, ready bool) error {
	const q = `
UPDATE runner_pod
   SET ready_at = CASE WHEN $2 THEN now() ELSE NULL END
 WHERE id = $1 AND (released_at IS NULL OR NOT $2)`

	tag, err := s.pool.Exec(ctx, q, podID, ready)
	if err != nil {
		return fmt.Errorf("mise à jour de la disponibilité du pod %d: %w", podID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pod %d: %w", podID, ErrNotFound)
	}
	return nil
}

// Claim est le résultat d'une attribution réussie.
type Claim struct {
	PodID   int64
	PodName string
}

// ClaimPod attribue un pod chaud à un run, en une instruction.
//
// C'est le « claim atomique » du §8.4, et le `SKIP LOCKED` en est la partie
// essentielle : deux commandes concurrentes du même utilisateur ne peuvent ni
// recevoir le même pod, ni S'ATTENDRE. La seconde ne voit aucun pod libre et
// reçoit ErrNoWarmPod, ce qui est exactement le comportement spécifié — tomber
// en cold start proprement.
//
// L'unicité de `run_id` dans le schéma en est le filet : même si ce prédicat
// était contourné, un run ne pourrait pas être attribué à deux pods.
func (s *Store) ClaimPod(ctx context.Context, pool core.PodPool, runID int64) (Claim, error) {
	const q = `
UPDATE runner_pod
   SET run_id = $4, claimed_at = now()
 WHERE id = (
       SELECT id
         FROM runner_pod
        WHERE user_id = $1 AND repo = $2 AND environment = $3
          AND run_id IS NULL
          AND released_at IS NULL
          AND ready_at IS NOT NULL
        ORDER BY created_at
        LIMIT 1
        FOR UPDATE SKIP LOCKED)
RETURNING id, pod_name`

	var c Claim
	err := s.pool.QueryRow(ctx, q, pool.UserID, pool.Repo, pool.Environment, runID).
		Scan(&c.PodID, &c.PodName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, ErrNoWarmPod
	}
	if err != nil {
		return Claim{}, fmt.Errorf("attribution d'un pod au run %d: %w", runID, err)
	}
	return c, nil
}

// RequestPodCancel demande l'annulation du run (§5.6).
//
// Une colonne et non un message : le réplica qui reçoit le Ctrl-C n'est pas
// forcément celui qui tient la connexion du pod. Le signal doit donc survivre au
// routage, et c'est la notification qui va réveiller le bon réplica.
//
// Idempotente — `coalesce` fige la première demande, donc une seconde demande ne
// déplace pas l'horodatage et ne renotifie pas.
func (s *Store) RequestPodCancel(ctx context.Context, runID int64) error {
	const q = `
UPDATE runner_pod
   SET cancel_requested_at = coalesce(cancel_requested_at, now())
 WHERE run_id = $1 AND released_at IS NULL`

	tag, err := s.pool.Exec(ctx, q, runID)
	if err != nil {
		return fmt.Errorf("demande d'annulation du run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		// Le pod est déjà libéré, ou aucun ne porte ce run. Dans les deux cas il
		// n'y a personne à annuler, et le levier restant est la suppression du
		// pod par le contrôleur (ADR-0018).
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// ReleasePod met un pod définitivement hors service.
//
// Définitif au sens du §8.2 : un pod n'est jamais réutilisé pour deux runs. Son
// jeton d'amorçage est donc refusé à partir de cet instant — c'est ce qui
// dispense ce secret de la procédure de rotation dont dépendent les ADR-0017
// et 0020.
//
// Idempotente : `coalesce` conserve la première libération, et libérer deux fois
// n'est pas une erreur. Les appelants sont des chemins de nettoyage, où une
// erreur sur un pod déjà mort ne serait que du bruit.
func (s *Store) ReleasePod(ctx context.Context, podID int64) error {
	const q = `
UPDATE runner_pod
   SET released_at = coalesce(released_at, now()), ready_at = NULL
 WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, podID)
	if err != nil {
		return fmt.Errorf("libération du pod %d: %w", podID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("pod %d: %w", podID, ErrNotFound)
	}
	return nil
}

// PodStates lit l'état des pods dont ce réplica tient la connexion.
//
// C'est le « pointeur » de l'ADR-0018 amendée résolu : la notification dit quel
// pod regarder, cette lecture est ce qu'on trouve en regardant. Elle ne rapporte
// AUCUN credential — ils sont lus au moment de la livraison, et ne sont donc
// jamais persistés.
//
// En lot, parce que le poll de secours interroge tous les pods du réplica :
// une requête par pod ferait payer au filet un coût proportionnel au nombre de
// pods chauds, alors que c'est précisément le chemin qui doit rester bon marché.
func (s *Store) PodStates(ctx context.Context, podIDs []int64) ([]core.PodState, error) {
	if len(podIDs) == 0 {
		return nil, nil
	}

	const q = `
SELECT id, coalesce(run_id, 0), cancel_requested_at, released_at
  FROM runner_pod
 WHERE id = ANY($1)`

	rows, err := s.pool.Query(ctx, q, podIDs)
	if err != nil {
		return nil, fmt.Errorf("lecture de l'état des pods: %w", err)
	}
	defer rows.Close()

	out := make([]core.PodState, 0, len(podIDs))
	for rows.Next() {
		var st core.PodState
		var cancel, released *time.Time
		if err := rows.Scan(&st.PodID, &st.RunID, &cancel, &released); err != nil {
			return nil, fmt.Errorf("lecture de l'état des pods: %w", err)
		}
		if cancel != nil {
			st.CancelRequestedAt = *cancel
		}
		if released != nil {
			st.ReleasedAt = *released
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lecture de l'état des pods: %w", err)
	}
	return out, nil
}
