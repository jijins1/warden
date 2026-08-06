//go:build integration

// Étage 2 de l'ADR-0010 : PostgreSQL réel, fourni par testcontainers plutôt que
// par un service déclaré dans le workflow — le même chemin de code sert en
// local et en CI.
//
// Ce que ce fichier vérifie n'est pas « le SQL compile » mais les propriétés
// dont d'autres décisions dépendent : l'idempotence de l'ADR-0008, la
// séparation des rôles qu'elle exige, et le refus bruyant d'une partition
// absente sur lequel l'ADR-0012 fonde une alerte de premier niveau.
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const testULID = "01JZQ00000000000000000ABCD"

// startPostgres démarre une base migrée et renvoie le DSN du rôle applicatif.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	// BasicWaitStrategies, et surtout PAS une attente sur le port ouvert :
	// l'entrypoint de l'image démarre un serveur temporaire pour initdb, puis
	// l'arrête et redémarre le vrai. Le port est donc ouvert pendant
	// l'initialisation, et s'y connecter donne un « connection reset by peer »
	// au moment où le serveur temporaire s'arrête. La stratégie du module
	// attend le message de démarrage DEUX fois, pour cette raison précise.
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("warden"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("démarrage de PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("chaîne de connexion: %v", err)
	}
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return dsn
}

// seedRun crée un utilisateur et un run, et renvoie l'id du run.
func seedRun(t *testing.T, s *store.Store) int64 {
	t.Helper()
	ctx := context.Background()

	u, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	id, err := s.CreateRun(ctx, store.NewRun{
		RunRef: testULID, UserID: u.ID,
		Environment: "staging", Inventory: "inv/staging", Playbook: "site.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return id
}

func batch() []events.Event {
	now := time.Now().UTC()
	return []events.Event{
		{Seq: 0, OccurredAt: now, Host: "db-01", TaskNumber: 1, State: events.StateOK, Payload: json.RawMessage(`{}`)},
		{Seq: 1, OccurredAt: now, Host: "db-01", TaskNumber: 2, State: events.StateChanged, Payload: json.RawMessage(`{}`),
			Diff: json.RawMessage(`{"before":"x","after":"y"}`)},
		{Seq: 2, OccurredAt: now, Host: "db-02", TaskNumber: 2, State: events.StateChanged, Payload: json.RawMessage(`{}`)},
		{Seq: 3, OccurredAt: now, Host: "db-02", TaskNumber: 3, State: events.StateSkipped, Payload: json.RawMessage(`{}`)},
	}
}

// Le run est créé AVANT le pod, donc dans un état déjà terminal : interrompu,
// cause inconnue (§6.4). Un plantage du control plane entre l'insertion et le
// dispatch laisse ainsi une ligne exacte, et non un run éternellement en vol.
func TestRunNaitInterrompu(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	seedRun(t, s)

	r, err := s.RunByRef(ctx, testULID)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}
	if r.State != core.RunInterrupted {
		t.Errorf("statut initial = %q, attendu %q", r.State, core.RunInterrupted)
	}
	if r.TerminationCause != core.CauseUnknown {
		t.Errorf("cause initiale = %q, attendu %q", r.TerminationCause, core.CauseUnknown)
	}

	if err := s.MarkRunning(ctx, r.ID, "warden-runner-abc"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := s.CloseRun(ctx, r.ID, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	// Une cause sur un run réussi n'a pas de sens, et le schéma l'interdit :
	// la méthode doit le dire avant d'aller le découvrir en base.
	if err := s.CloseRun(ctx, r.ID, core.RunSucceeded, core.CauseUserInterrupt); err == nil {
		t.Error("CloseRun a accepté une cause de terminaison sur un run réussi")
	}
	// Et un run interrompu SANS cause est ce que le §6.4 juge « presque
	// inutile ».
	if err := s.CloseRun(ctx, r.ID, core.RunInterrupted, ""); err == nil {
		t.Error("CloseRun a accepté un run interrompu sans cause")
	}
}

// L'offset étant assigné par le runner, une réémission doit être sans danger
// (ADR-0008) — y compris, et c'est le cas qui compte, quand l'horodatage change
// entre les deux envois.
func TestIngestionIdempotente(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	if err := in.AppendEvents(ctx, runID, batch()); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	assertCounts(t, dsn, runID, 4, 1, 2)

	// Réémission à l'identique.
	if err := in.AppendEvents(ctx, runID, batch()); err != nil {
		t.Fatalf("AppendEvents (réémission): %v", err)
	}
	assertCounts(t, dsn, runID, 4, 1, 2)

	// Réémission avec un horodatage différent. C'est ce cas qui justifie de
	// partitionner sur run_day et non sur occurred_at : avec une partition par
	// date d'event, PostgreSQL aurait exigé occurred_at dans la clé primaire,
	// et cette ligne serait passée.
	decale := batch()
	for i := range decale {
		decale[i].OccurredAt = decale[i].OccurredAt.Add(time.Hour)
	}
	if err := in.AppendEvents(ctx, runID, decale); err != nil {
		t.Fatalf("AppendEvents (horodatage décalé): %v", err)
	}
	assertCounts(t, dsn, runID, 4, 1, 2)
}

// Le compteur `changed` est dénormalisé (ADR-0008) : c'est la métrique d'audit
// principale, elle ne doit pas coûter une agrégation. Il est tenu par un
// trigger SECURITY DEFINER, ce qui lui permet d'être exact SANS que le rôle
// d'ingestion ait le droit d'écrire sur `run`.
func TestCompteurChangedSurPlusieursLots(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	for lot := 0; lot < 3; lot++ {
		evs := batch()
		for i := range evs {
			evs[i].Seq += int64(lot * len(evs))
		}
		if err := in.AppendEvents(ctx, runID, evs); err != nil {
			t.Fatalf("AppendEvents (lot %d): %v", lot, err)
		}
	}
	assertCounts(t, dsn, runID, 12, 3, 6)
}

// Le plafond par instruction ne doit pas être une limite fonctionnelle : un lot
// plus grand se découpe, et le compteur reste exact.
func TestLotPlusGrandQueLePlafond(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	const n = 1200
	now := time.Now().UTC()
	evs := make([]events.Event, n)
	for i := range evs {
		st := events.StateOK
		if i%2 == 0 {
			st = events.StateChanged
		}
		evs[i] = events.Event{
			Seq: int64(i), OccurredAt: now, Host: "db-01",
			State: st, Payload: json.RawMessage(`{"x":1}`),
		}
	}
	if err := in.AppendEvents(ctx, runID, evs); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	assertCounts(t, dsn, runID, n, 0, n/2)
}

// Un type d'event inconnu est stocké tel quel, payload d'origine intact
// (ADR-0021). C'est la soupape qui rend acceptable qu'un traducteur soit en
// retard sur une version d'ansible-core déployée par un projet : il ne doit
// jamais pouvoir faire échouer un run.
func TestEventInconnuAccepte(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	brut := `{"event":"v2_runner_on_something_new","data":{"quoi":"?"}}`
	err = in.AppendEvents(ctx, runID, []events.Event{{
		Seq: 0, OccurredAt: time.Now().UTC(), State: events.StateUnknown,
		Payload: json.RawMessage(brut),
	}})
	if err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	var got string
	if err := conn.QueryRow(ctx,
		`SELECT payload::text FROM event WHERE run_id = $1 AND seq = 0`, runID).Scan(&got); err != nil {
		t.Fatalf("relecture du payload: %v", err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(brut), &a)
	if err := json.Unmarshal([]byte(got), &b); err != nil {
		t.Fatalf("payload relu illisible: %v", err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("payload altéré:\n  écrit = %s\n  relu  = %s", brut, got)
	}
}

// L'ADR-0012 range l'absence de partition utilisable au rang d'une
// indisponibilité de la base. Ce test fige le comportement qui rend cette
// alerte possible : l'insertion échoue, elle n'est pas absorbée en silence.
// C'est pourquoi il n'y a pas de partition DEFAULT.
func TestPartitionAbsenteRefuseeBruyamment(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	_, err = conn.Exec(ctx, `
		INSERT INTO event (run_id, run_day, seq, occurred_at, state, payload)
		VALUES ($1, (timezone('UTC', now()) + interval '1 year')::date, 0, now(), 'ok', '{}')`, runID)
	if err == nil {
		t.Fatal("une insertion hors partition a été acceptée")
	}

	// Et la création anticipée doit être idempotente : c'est une tâche
	// récurrente (ADR-0012), pas une opération à jouer une seule fois.
	first, err := s.EnsurePartitions(ctx, 14)
	if err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("EnsurePartitions n'a créé aucune partition")
	}
	again, err := s.EnsurePartitions(ctx, 14)
	if err != nil {
		t.Fatalf("EnsurePartitions (2e passage): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("EnsurePartitions n'est pas idempotente, a recréé %v", again)
	}
}

// Test NÉGATIF exigé par l'ADR-0008 : l'isolation de l'ingestion repose sur une
// séparation par port et par rôle PostgreSQL, non par pod. Elle ne vaut donc
// que ce que vaut le GRANT — et un GRANT non testé est une intention.
func TestRoleIngestionNePeutQueInserer(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)

	if _, err := admin.Exec(ctx, `CREATE ROLE ingest_login LOGIN PASSWORD 'ingest'`); err != nil {
		t.Fatalf("création du rôle de connexion: %v", err)
	}
	if _, err := admin.Exec(ctx, `GRANT warden_ingest TO ingest_login`); err != nil {
		t.Fatalf("GRANT: %v", err)
	}

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	restreint := connect(t, swapUser(dsn, "ingest_login", "ingest"))
	defer restreint.Close(ctx)

	// Ce qu'il DOIT pouvoir faire : insérer, et lire la partition où écrire.
	if _, err := restreint.Exec(ctx, `
		INSERT INTO event (run_id, run_day, seq, occurred_at, host, state, payload)
		SELECT id, run_day, 0, now(), 'db-01', 'changed', '{}' FROM run WHERE id = $1`, runID); err != nil {
		t.Fatalf("le rôle d'ingestion ne peut pas insérer: %v", err)
	}

	interdits := []struct {
		nom  string
		q    string
		args []any
	}{
		{"lire une ligne de run entière", `SELECT * FROM run WHERE id = $1`, []any{runID}},
		{"lire les events", `SELECT count(*) FROM event`, nil},
		{"lire les diffs", `SELECT count(*) FROM event_diff`, nil},
		{"lire les utilisateurs", `SELECT count(*) FROM users`, nil},
		{"falsifier le compteur changed", `UPDATE run SET changed_count = 9999 WHERE id = $1`, []any{runID}},
		{"effacer de l'audit", `DELETE FROM event WHERE run_id = $1`, []any{runID}},
		{"modifier un event", `UPDATE event SET state = 'ok' WHERE run_id = $1`, []any{runID}},
		{"créer une table", `CREATE TABLE evasion (x int)`, nil},
	}
	for _, c := range interdits {
		if _, err := restreint.Exec(ctx, c.q, c.args...); err == nil {
			t.Errorf("le rôle d'ingestion peut %s — le GRANT est trop large", c.nom)
		}
	}

	// Le compteur a pourtant bien bougé : le trigger est SECURITY DEFINER,
	// c'est ce qui permet au GRANT de rester au plus juste sans que le compteur
	// puisse dériver.
	r, err := s.RunByRef(ctx, testULID)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}
	if r.ChangedCount != 1 {
		t.Errorf("changed_count = %d, attendu 1", r.ChangedCount)
	}
}

// Un diff est un secret rendu (§6.5). La table séparée n'a de sens que si un
// lecteur d'audit ordinaire ne peut effectivement pas l'atteindre.
func TestLecteurDAuditNeVoitPasLesDiffs(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)

	for _, q := range []string{
		`CREATE ROLE reader_login LOGIN PASSWORD 'reader'`,
		`GRANT warden_reader TO reader_login`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()
	if err := in.AppendEvents(ctx, runID, batch()); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	lecteur := connect(t, swapUser(dsn, "reader_login", "reader"))
	defer lecteur.Close(ctx)

	var n int
	if err := lecteur.QueryRow(ctx, `SELECT count(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("le lecteur d'audit ne peut pas lire les events: %v", err)
	}
	if n != 4 {
		t.Errorf("events visibles = %d, attendu 4", n)
	}
	if err := lecteur.QueryRow(ctx, `SELECT count(*) FROM event_diff`).Scan(&n); err == nil {
		t.Error("le lecteur d'audit voit les diffs — la séparation de event_diff ne vaut rien")
	}
}

// Un run introuvable doit se dire, pas se traduire en zéro ligne insérée.
func TestIngestionSurRunInconnu(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	err = in.AppendEvents(ctx, 424242, batch())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("erreur = %v, attendu store.ErrNotFound", err)
	}
}

// --- utilitaires ---

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	return c
}

// swapUser remplace l'identité dans un DSN postgres://user:pass@host/...
func swapUser(dsn, user, pass string) string {
	const scheme = "postgres://"
	at := 0
	for i := len(scheme); i < len(dsn); i++ {
		if dsn[i] == '@' {
			at = i
			break
		}
	}
	if at == 0 {
		panic("DSN inattendu: " + dsn)
	}
	return scheme + user + ":" + pass + dsn[at:]
}

func assertCounts(t *testing.T, dsn string, runID int64, wantEvents, wantDiffs, wantChanged int) {
	t.Helper()
	ctx := context.Background()

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	var gotEvents, gotDiffs, gotChanged int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM event WHERE run_id = $1`, runID).Scan(&gotEvents); err != nil {
		t.Fatalf("comptage des events: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM event_diff WHERE run_id = $1`, runID).Scan(&gotDiffs); err != nil {
		t.Fatalf("comptage des diffs: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT changed_count FROM run WHERE id = $1`, runID).Scan(&gotChanged); err != nil {
		t.Fatalf("lecture du compteur: %v", err)
	}

	if gotEvents != wantEvents {
		t.Errorf("events = %d, attendu %d", gotEvents, wantEvents)
	}
	if gotDiffs != wantDiffs {
		t.Errorf("diffs = %d, attendu %d", gotDiffs, wantDiffs)
	}
	if gotChanged != wantChanged {
		t.Errorf("changed_count = %d, attendu %d", gotChanged, wantChanged)
	}
}
