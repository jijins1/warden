package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// dispatchChannel est défini par la migration 00003. La duplication est voulue —
// deux adaptateurs sortants n'ont pas le droit de se connaître (ADR-0019) — mais
// elle n'est pas gratuite : un renommage côté migration casserait le routage EN
// SILENCE, puisque le poll du dispatcher continuerait de tout servir. C'est le
// test d'intégration qui tient cette arête, pas le compilateur.
const dispatchChannel = "warden_pod_dispatch"

// dispatchListenerAppName identifie la connexion d'écoute dans pg_stat_activity.
//
// Elle est hors du pool, donc invisible dans ses métriques : sans ce nom, une
// connexion d'écoute morte ou dupliquée ne se voit nulle part.
const dispatchListenerAppName = "warden-dispatch-listen"

// dispatchReconnectDelay sépare deux tentatives de rétablissement.
const dispatchReconnectDelay = 3 * time.Second

// WatchPodDispatch appelle wake à chaque changement d'état d'un pod, jusqu'à
// l'annulation du contexte.
//
// C'est le sens inverse du flux d'events : le claim est gagné par un réplica
// quelconque, la connexion SSE du pod réclamé est tenue par un autre
// (ADR-0018 amendée). La notification porte un POINTEUR — l'id du pod — et le
// réplica qui détient la connexion va chercher l'ordre et les credentials
// lui-même. C'est ce qui garantit qu'aucun credential n'est jamais persisté.
//
// Cette méthode vit dans `store` et non dans `dispatch` pour que ce dernier
// ignore complètement PostgreSQL : il n'est alors qu'une logique de routage et
// un handler HTTP, testable sans base (ADR-0010, étage 1).
//
// Une rupture n'est pas fatale : on se reconnecte, et entre-temps le dispatcher
// est servi par son poll. Les notifications manquées ne se rattrapent pas et
// n'ont pas à l'être — l'état est en base, la notification ne dit que « va
// regarder ».
func (s *Store) WatchPodDispatch(ctx context.Context, wake func(podID int64)) error {
	for {
		if err := s.watchOnce(ctx, wake); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Volontairement pas une erreur remontée : une boucle d'écoute qui
			// s'arrête au premier incident réseau transformerait une dégradation
			// — le claim arrive en deux secondes au lieu d'instantanément — en
			// panne du routage.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(dispatchReconnectDelay):
			}
		}
	}
}

func (s *Store) watchOnce(ctx context.Context, wake func(podID int64)) error {
	cfg, err := pgx.ParseConfig(s.dsn)
	if err != nil {
		// Jamais le DSN dans l'erreur : il porte le mot de passe.
		return fmt.Errorf("DSN PostgreSQL invalide: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = dispatchListenerAppName

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connexion d'écoute du dispatch: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	if _, err := conn.Exec(ctx, "LISTEN "+dispatchChannel); err != nil {
		return fmt.Errorf("LISTEN %s: %w", dispatchChannel, err)
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("attente de notification: %w", err)
		}
		if podID, convErr := strconv.ParseInt(n.Payload, 10, 64); convErr == nil {
			wake(podID)
		}
	}
}
