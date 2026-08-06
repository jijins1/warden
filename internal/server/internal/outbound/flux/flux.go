package flux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Noms des canaux de notification.
//
// Ils sont définis par les migrations, qui vivent dans `outbound/store` — et
// que ce paquet ne peut pas importer, deux adaptateurs sortants n'ayant pas le
// droit de se connaître (ADR-0019). La duplication est donc voulue, mais elle
// n'est pas gratuite : un renommage côté migration casserait le direct EN
// SILENCE, puisque le poll continuerait de tout servir correctement. C'est le
// test d'intégration qui tient cette arête, pas le compilateur.
const (
	channelEvents   = "warden_events"
	channelRunEnded = "warden_run_ended"
)

// pollInterval est le filet.
//
// La correction ne dépend pas de NOTIFY : elle vient de `seq`. Un réplica qui
// ne recevrait aucune notification — pooler en mode transaction, connexion
// LISTEN tombée, trigger absent — sert quand même tout, avec cette latence.
// C'est la valeur qui définit à quel point une panne du direct est bénigne.
const pollInterval = 2 * time.Second

// reconnectDelay sépare deux tentatives de rétablissement de la connexion
// d'écoute.
const reconnectDelay = 3 * time.Second

// Flux suit un run en temps réel.
//
// Séparé de `store` bien que reposant aujourd'hui sur la même base : si le
// volume d'events dépasse ce que PostgreSQL absorbe, l'ADR-0008 prévoit de
// déporter la table d'events vers un store dédié. Séparés, ce repli ne touche
// qu'un adaptateur ; fusionnés, il traverserait le cœur.
type Flux struct {
	pool *pgxpool.Pool
	dsn  string

	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{}
}

// Open ouvre le pool de lecture.
//
// N'ouvre PAS la connexion d'écoute : c'est Listen qui s'en charge, et un Flux
// sur lequel Listen n'a jamais été appelé reste fonctionnel — simplement servi
// par le poll. C'est la même propriété que celle qui rend une panne de NOTIFY
// bénigne, et elle vaut la peine d'être vraie dès la construction.
func Open(ctx context.Context, dsn string) (*Flux, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Jamais le DSN dans l'erreur : il porte le mot de passe.
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "warden-flux"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("ouverture du pool de suivi: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connexion à PostgreSQL: %w", err)
	}

	return &Flux{
		pool: pool,
		dsn:  dsn,
		subs: make(map[int64]map[chan struct{}]struct{}),
	}, nil
}

// Close relâche le pool de lecture.
func (f *Flux) Close() { f.pool.Close() }

// Listen tient la connexion d'écoute jusqu'à l'annulation du contexte.
//
// UNE connexion par réplica, jamais par client : une connexion LISTEN par
// client abonné épuiserait PostgreSQL en sessions oisives. Le fan-out vers les
// abonnés se fait en mémoire.
//
// Cette connexion est prise HORS du pool, et c'est structurel : `LISTEN` n'est
// pas une requête, c'est une session qui reste ouverte et reçoit des messages
// asynchrones. Rendue au pool, elle cesserait de recevoir sans qu'aucune erreur
// ne le signale.
//
// Une rupture n'est pas fatale : on se reconnecte, et entre-temps les abonnés
// sont servis par le poll. Les notifications manquées ne se rattrapent pas et
// n'ont pas à l'être — le rattrapage se fait par `seq`, au même titre qu'une
// reconnexion client.
func (f *Flux) Listen(ctx context.Context) error {
	for {
		if err := f.listenOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Volontairement pas une erreur remontée : la boucle d'écoute qui
			// s'arrête au premier incident réseau transformerait une dégradation
			// en panne du direct.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reconnectDelay):
			}
		}
	}
}

// listenerAppName identifie la connexion d'écoute dans pg_stat_activity.
//
// Elle est hors du pool, donc invisible dans ses métriques : sans ce nom, une
// connexion d'écoute morte ou dupliquée ne se voit nulle part.
const listenerAppName = "warden-flux-listen"

func (f *Flux) listenOnce(ctx context.Context) error {
	cfg, err := pgx.ParseConfig(f.dsn)
	if err != nil {
		return fmt.Errorf("DSN PostgreSQL invalide: %w", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = listenerAppName

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connexion d'écoute: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	for _, ch := range []string{channelEvents, channelRunEnded} {
		if _, err := conn.Exec(ctx, "LISTEN "+ch); err != nil {
			return fmt.Errorf("LISTEN %s: %w", ch, err)
		}
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("attente de notification: %w", err)
		}
		if runID, ok := parseNotification(n.Payload); ok {
			f.wake(runID)
		}
	}
}

// parseNotification extrait le run d'une charge utile `run_id` ou `run_id:seq`.
//
// Le `seq` n'est pas utilisé pour décider quoi lire — l'abonné relit la table
// dans tous les cas, puisque c'est de là que vient la correction. Il est dans
// la charge utile parce que l'ADR-0008 le spécifie et parce que déboguer le
// chemin de streaming sans lui revient à regarder un canal muet.
func parseNotification(payload string) (int64, bool) {
	if i := strings.IndexByte(payload, ':'); i >= 0 {
		payload = payload[:i]
	}
	runID, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return 0, false
	}
	return runID, true
}

// subscribe enregistre un réveil pour un run.
func (f *Flux) subscribe(runID int64) chan struct{} {
	wake := make(chan struct{}, 1)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs[runID] == nil {
		f.subs[runID] = make(map[chan struct{}]struct{})
	}
	f.subs[runID][wake] = struct{}{}
	return wake
}

func (f *Flux) unsubscribe(runID int64, wake chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs[runID], wake)
	if len(f.subs[runID]) == 0 {
		delete(f.subs, runID)
	}
}

// wake signale les abonnés d'un run, sans jamais bloquer.
//
// Le canal de réveil a une case : un abonné en retard n'a pas besoin de N
// réveils, il a besoin d'UN réveil et relira tout ce qui s'est accumulé. Un
// envoi bloquant ferait dépendre la connexion d'écoute — partagée par tous les
// runs du réplica — de la vitesse du client le plus lent.
func (f *Flux) wake(runID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for wake := range f.subs[runID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
