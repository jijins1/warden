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
	"strings"
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
		Repo:        "warden",
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
// d'ingestion ait le droit d'écrire cette colonne — la migration 00006 lui a
// ouvert les cinq colonnes de statut, et `changed_count` n'en fait pas partie.
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

		// Les colonnes que la migration 00006 a délibérément LAISSÉES hors du
		// GRANT de clôture. Ce sont celles qui disent qui a exécuté quoi : un
		// pod hostile qui pourrait les toucher réécrirait l'audit dont il est
		// l'objet.
		{"réécrire le commit exécuté", `UPDATE run SET audit_sha = 'dead' WHERE id = $1`, []any{runID}},
		{"changer d'auteur", `UPDATE run SET user_id = 1 WHERE id = $1`, []any{runID}},
		{"antidater le démarrage", `UPDATE run SET started_at = now() WHERE id = $1`, []any{runID}},
		{"déplacer le run de partition", `UPDATE run SET run_day = '2000-01-01' WHERE id = $1`, []any{runID}},
	}
	// Ce que ce GRANT ne borne PAS, et il faut le dire : `UPDATE run SET state`
	// sans clause WHERE est permis. Ce qui enferme un pod dans SON run n'est pas
	// le GRANT, c'est que le `run_id` est résolu côté serveur depuis le jeton
	// présenté et jamais accepté d'un corps de requête (ADR-0021, ADR-0023). Le
	// GRANT est la seconde couche, celle qui limite les dégâts si la première
	// cédait — et sa portée est exactement les colonnes de statut.
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

// La clôture par le runner (§6.4, marche 2), sous le rôle RESTREINT et non sous
// le rôle applicatif. C'est le seul montage qui prouve que le GRANT de la
// migration 00006 est suffisant : le vérifier avec le rôle applicatif ne
// dirait rien, puisqu'il peut déjà tout.
func TestClotureParLeRoleDIngestion(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)
	for _, q := range []string{
		`CREATE ROLE ingest_login LOGIN PASSWORD 'ingest'`,
		`GRANT warden_ingest TO ingest_login`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("préparation du rôle: %v", err)
		}
	}

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	runID := seedRun(t, s)

	in, err := store.OpenIngest(ctx, swapUser(dsn, "ingest_login", "ingest"))
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	// Le dernier lot ET la clôture, en une seule requête : c'est la forme que
	// le contrat de fil impose, et celle que le §6.3 exige.
	if err := in.AppendAndClose(ctx, runID, batch(), core.Closure{State: core.RunSucceeded}); err != nil {
		t.Fatalf("AppendAndClose: %v", err)
	}

	fin := readClosure(t, dsn, runID)
	if fin.state != string(core.RunSucceeded) {
		t.Errorf("état = %q, attendu %q", fin.state, core.RunSucceeded)
	}
	if fin.cause != nil {
		t.Errorf("cause = %q sur un run réussi, attendu NULL", *fin.cause)
	}
	if fin.endedAt == nil {
		t.Fatal("ended_at non posé : la durée du run est inconnue")
	}
	assertCounts(t, dsn, runID, len(batch()), 1, 2)

	// Réémission : le runner rejoue son dernier lot après une coupure réseau.
	// Ni les events ni la date de fin ne doivent bouger — un `ended_at` redaté
	// à chaque reprise ferait dériver la durée mesurée du run.
	if err := in.AppendAndClose(ctx, runID, batch(), core.Closure{State: core.RunSucceeded}); err != nil {
		t.Fatalf("réémission: %v", err)
	}
	apres := readClosure(t, dsn, runID)
	if apres.endedAt == nil || !apres.endedAt.Equal(*fin.endedAt) {
		t.Errorf("ended_at redaté par une réémission : %v puis %v", fin.endedAt, apres.endedAt)
	}
	assertCounts(t, dsn, runID, len(batch()), 1, 2)
}

// Une trame terminale voyage sans event : c'est le cas d'un run qui n'a plus
// rien à dire mais doit être clos.
func TestClotureSansEvent(t *testing.T) {
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

	err = in.AppendAndClose(ctx, runID, nil, core.Closure{
		State:           core.RunInterrupted,
		Cause:           core.CauseUserInterrupt,
		SecretsUnmasked: "jeu de secrets indisponible",
	})
	if err != nil {
		t.Fatalf("AppendAndClose: %v", err)
	}

	c := readClosure(t, dsn, runID)
	if c.state != string(core.RunInterrupted) ||
		c.cause == nil || *c.cause != string(core.CauseUserInterrupt) {
		t.Errorf("état/cause = %q/%v, attendu %q/%q",
			c.state, c.cause, core.RunInterrupted, core.CauseUserInterrupt)
	}
	// L'ADR-0017 exige de pouvoir répondre à « N runs contiennent des secrets
	// non masqués ». Sans le motif, l'alerte est inexploitable : savoir qu'un
	// masquage a échoué ne dit pas s'il faut faire tourner un secret.
	if !c.unmasked || c.reason == nil || *c.reason != "jeu de secrets indisponible" {
		t.Errorf("secrets non masqués = %v / %v, le motif doit accompagner le drapeau",
			c.unmasked, c.reason)
	}
}

// Une clôture invalide doit être nommée ici, pas renvoyée comme une violation
// de contrainte PostgreSQL dont le message ne dit pas ce qui manque.
func TestUneClotureInvalideEstRefuseeAvantLaBase(t *testing.T) {
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

	cas := map[string]core.Closure{
		"interruption sans cause": {State: core.RunInterrupted},
		"cause sur un succès":     {State: core.RunSucceeded, Cause: core.CauseUserInterrupt},
		"clôture sur `running`":   {State: core.RunRunning},
		"statut inconnu":          {State: "presque"},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			if err := in.AppendAndClose(ctx, runID, nil, c); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}

	// Et le run n'a pas bougé : un refus ne doit rien écrire.
	if readClosure(t, dsn, runID).endedAt != nil {
		t.Error("un refus de clôture a quand même posé ended_at")
	}
}

// closureRow est ce que la clôture écrit, lu directement en SQL.
//
// Directement, parce que `core.Run` ne porte ni `ended_at` ni le motif de
// masquage : l'élargir pour un test changerait la forme de la réponse d'audit,
// ce qui n'est pas l'objet de ce lot.
type closureRow struct {
	state    string
	cause    *string
	endedAt  *time.Time
	unmasked bool
	reason   *string
}

func readClosure(t *testing.T, dsn string, runID int64) closureRow {
	t.Helper()
	ctx := context.Background()

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	var r closureRow
	err := conn.QueryRow(ctx, `
SELECT state, termination_cause, ended_at, secrets_unmasked, secrets_unmasked_reason
  FROM run WHERE id = $1`, runID).
		Scan(&r.state, &r.cause, &r.endedAt, &r.unmasked, &r.reason)
	if err != nil {
		t.Fatalf("lecture de la clôture: %v", err)
	}
	return r
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

// --- les clés d'environnement (ADR-0020, migration 00007) ---

func descripteur(env, empreinte string) core.KeyDescriptor {
	return core.KeyDescriptor{
		Environment: env,
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple warden-" + env,
		Fingerprint: empreinte,
		SecretName:  core.SecretNameFor(env),
		Origin:      core.KeyGenerated,
	}
}

func TestUneCleEstEnregistreeEtRelue(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveKey(ctx, descripteur("prod", "SHA256:aaaa")); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}

	d, err := s.KeyByEnvironment(ctx, "prod")
	if err != nil {
		t.Fatalf("KeyByEnvironment: %v", err)
	}
	if d.Fingerprint != "SHA256:aaaa" || d.SecretName != core.SecretNameFor("prod") {
		t.Errorf("descripteur relu = %+v", d)
	}
	// Une clé qui n'a jamais tourné ne porte PAS de date de rotation : elle
	// ferait croire à un évènement qui n'a pas eu lieu.
	if !d.RotatedAt.IsZero() {
		t.Errorf("rotated_at = %v sur une clé jamais remplacée", d.RotatedAt)
	}

	if _, err := s.KeyByEnvironment(ctx, "jamais-configure"); !errors.Is(err, core.ErrNoKey) {
		t.Errorf("erreur = %v, attendu core.ErrNoKey", err)
	}
}

// Reposer une clé EST la rotation : l'ADR-0020 note que c'est le seul mécanisme
// de révocation. La refuser obligerait à supprimer d'abord, donc à passer par un
// état où la production n'a plus de clé du tout.
func TestReposerUneCleEstUneRotation(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveKey(ctx, descripteur("prod", "SHA256:aaaa")); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	nouvelle := descripteur("prod", "SHA256:bbbb")
	nouvelle.Origin = core.KeyImported
	if err := s.SaveKey(ctx, nouvelle); err != nil {
		t.Fatalf("SaveKey (rotation): %v", err)
	}

	d, err := s.KeyByEnvironment(ctx, "prod")
	if err != nil {
		t.Fatalf("KeyByEnvironment: %v", err)
	}
	if d.Fingerprint != "SHA256:bbbb" || d.Origin != core.KeyImported {
		t.Errorf("la rotation n'a pas remplacé : %+v", d)
	}
	if d.RotatedAt.IsZero() {
		t.Error("rotated_at non posé : impossible de dire depuis quand la clé est en place")
	}

	// Une ligne par environnement, toujours.
	toutes, err := s.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(toutes) != 1 {
		t.Errorf("%d clés pour un environnement", len(toutes))
	}
}

// La contrainte de forme du schéma. Cette valeur devient un composant de nom de
// Secret Kubernetes : une violation découverte par l'API se présenterait à la
// livraison de l'ordre, très loin d'ici.
func TestUnEnvironnementMalFormeEstRefuseParLeSchema(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Directement en SQL : la validation Go refuse déjà ces valeurs, et ce
	// qu'on vérifie ici est que la BASE les refuserait aussi — une défense de
	// moins à oublier le jour où un autre chemin d'écriture apparaîtra.
	conn := connect(t, dsn)
	defer conn.Close(ctx)

	for _, env := range []string{"PROD", "prod.eu", "prod/eu", "-prod", ""} {
		_, err := conn.Exec(ctx, `
INSERT INTO ssh_key (environment, public_key, fingerprint, secret_name, origin)
VALUES ($1, 'ssh-ed25519 AAAA x', 'SHA256:aaaa', 'warden-key-x', 'generated')`, env)
		if err == nil {
			t.Errorf("l'environnement %q a été accepté par le schéma", env)
		}
	}
}

// L'ingestion écrit des events. Elle n'a AUCUNE raison de connaître les clés
// d'accès de la flotte, et le GRANT ne lui en donne pas.
func TestLIngestionNeVoitPasLesCles(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)
	for _, q := range []string{
		`CREATE ROLE ingest_login LOGIN PASSWORD 'ingest'`,
		`GRANT warden_ingest TO ingest_login`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("préparation du rôle: %v", err)
		}
	}

	restreint := connect(t, swapUser(dsn, "ingest_login", "ingest"))
	defer restreint.Close(ctx)

	interdits := map[string]string{
		"lire les clés":     `SELECT count(*) FROM ssh_key`,
		"poser une clé":     `INSERT INTO ssh_key (environment, public_key, fingerprint, secret_name, origin) VALUES ('x', 'ssh-ed25519 A x', 'SHA256:a', 'w', 'generated')`,
		"effacer une clé":   `DELETE FROM ssh_key`,
		"détourner une clé": `UPDATE ssh_key SET secret_name = 'ailleurs'`,
	}
	for nom, q := range interdits {
		if _, err := restreint.Exec(ctx, q); err == nil {
			t.Errorf("le rôle d'ingestion peut %s — le GRANT est trop large", nom)
		}
	}
}

// --- l'inventaire des machines (§10.2, migration 00008) ---

func machine(env, nom, adresse string) core.ManagedHost {
	return core.ManagedHost{
		Name: nom, Environment: env,
		Address: adresse,
		Port:    core.PortSSHParDefaut, Login: "ansible",
		Mode: core.HostDeclared,
	}
}

func TestUneMachineEstEnregistreeEtRelue(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	h := machine("prod", "db-prod-02", "10.20.0.42")
	h.HostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExemple"
	if err := s.SaveHost(ctx, h); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}

	relue, err := s.HostByName(ctx, "prod", "db-prod-02")
	if err != nil {
		t.Fatalf("HostByName: %v", err)
	}
	// L'adresse fait l'aller-retour telle quelle. Elle était jadis un `inet`,
	// qui rendait `10.20.0.42/32` — une valeur qui ne se reconnecte à rien ; la
	// migration 00012 l'a passée en `text` et la question ne se pose plus.
	if relue.Address != "10.20.0.42" {
		t.Errorf("adresse relue = %q", relue.Address)
	}
	if relue.HostKey != h.HostKey || relue.Login != "ansible" || relue.Port != 22 {
		t.Errorf("machine relue = %+v", relue)
	}
	if relue.EnrolledAt.IsZero() {
		t.Error("enrolled_at non posé")
	}

	if _, err := s.HostByName(ctx, "prod", "inconnue"); !errors.Is(err, core.ErrNoHost) {
		t.Errorf("erreur = %v, attendu core.ErrNoHost", err)
	}
}

// IPv6 aussi. Le cas comptait doublement du temps du type `inet` ; il reste
// vérifié parce que la contrainte de forme de la migration 00012 doit accepter
// les deux-points, y compris en tête d'une adresse abrégée.
func TestUneAdresseIPv6TraverseIntacte(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveHost(ctx, machine("prod", "v6", "2001:db8::42")); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	relue, err := s.HostByName(ctx, "prod", "v6")
	if err != nil {
		t.Fatalf("HostByName: %v", err)
	}
	if relue.Address != "2001:db8::42" {
		t.Errorf("adresse relue = %q", relue.Address)
	}
}

// Un NOM d'hôte est enregistré et relu tel quel (ADR-0036).
//
// C'est le test de la migration 00012 : la colonne était un `inet`, qui aurait
// refusé cette ligne. Ce qu'on vérifie n'est pas que le nom résout — la base
// n'en sait rien et le pod tranchera — mais qu'il traverse sans être réécrit.
func TestUnNomDHoteEstEnregistreEtRelu(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveHost(ctx, machine("prod", "kodi", "kodi.lan")); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	relue, err := s.HostByName(ctx, "prod", "kodi")
	if err != nil {
		t.Fatalf("HostByName: %v", err)
	}
	if relue.Address != "kodi.lan" {
		t.Errorf("adresse relue = %q", relue.Address)
	}
}

// Une adresse déjà en base traverse la migration 00012 SANS CHANGER.
//
// C'est la question la plus chère de l'ADR-0036 : une flotte est déjà enrôlée,
// et une conversion de type qui réécrit des adresses ferait perdre l'accès à des
// machines de production. Ce qui est vérifié est l'EXPRESSION de conversion —
// `host(address)` — sur les deux familles d'adresses, plus le fait que ce
// qu'elle produit satisfait la nouvelle contrainte de forme.
//
// La table est un décalque jetable de `managed_host` d'avant la migration, et
// les deux instructions sont recopiées du fichier 00012. La duplication est
// délibérée : goose ne rejoue pas une migration déjà appliquée, et sans décalque
// il n'y aurait aucun moyen d'exercer le chemin de MONTÉE sur une base qui a
// déjà des lignes.
func TestUneAdresseInetTraverseLaMigrationSansChanger(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	conn := connect(t, dsn)
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE TABLE decalque_00012 (name text, address inet NOT NULL)`); err != nil {
		t.Fatalf("création du décalque: %v", err)
	}
	avant := map[string]string{
		"v4":          "10.20.0.42",
		"v6":          "2001:db8::42",
		"v6 abrégée":  "::1",
		"v4 en /8":    "10.0.0.1",
		"hors privée": "203.0.113.7",
	}
	for nom, adresse := range avant {
		if _, err := conn.Exec(ctx,
			`INSERT INTO decalque_00012 (name, address) VALUES ($1, $2::inet)`, nom, adresse); err != nil {
			t.Fatalf("insertion de %s: %v", nom, err)
		}
	}

	// Les deux instructions de la migration, dans l'ordre.
	if _, err := conn.Exec(ctx,
		`ALTER TABLE decalque_00012 ALTER COLUMN address TYPE text USING host(address)`); err != nil {
		t.Fatalf("conversion: %v", err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE decalque_00012 ADD CONSTRAINT decalque_adresse_forme
	       CHECK (address ~ '^[A-Za-z0-9:][A-Za-z0-9.:_-]{0,252}$')`); err != nil {
		// Une contrainte refusée ici signifie qu'elle refuse des lignes
		// EXISTANTES : la migration échouerait en production.
		t.Fatalf("contrainte de forme refusée sur des lignes déjà en base: %v", err)
	}

	for nom, attendue := range avant {
		var relue string
		if err := conn.QueryRow(ctx,
			`SELECT address FROM decalque_00012 WHERE name = $1`, nom).Scan(&relue); err != nil {
			t.Fatalf("relecture de %s: %v", nom, err)
		}
		if relue != attendue {
			t.Errorf("%s : adresse %q après migration, %q avant", nom, relue, attendue)
		}
		// Et ce que la base rend est ce que le cœur accepte : sans ça, une
		// machine migrée deviendrait irrécupérable au premier `SaveHost`.
		if err := core.ValiderAdresse(relue); err != nil {
			t.Errorf("%s : adresse migrée %q refusée par le cœur : %v", nom, relue, err)
		}
	}
}

// La contrainte de forme de la migration 00012 écarte ce qui ne peut être ni une
// adresse ni un nom. Elle double `core.ValiderAdresse` volontairement : le cœur
// peut nommer la machine fautive, la base fait autorité.
func TestLaBaseRefuseUneAdresseInformeMemeSansLeCoeur(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Directement en SQL : la validation Go refuse déjà ces valeurs, et ce qu'on
	// vérifie ici est que la BASE les refuserait aussi.
	conn := connect(t, dsn)
	defer conn.Close(ctx)

	const q = `INSERT INTO managed_host (name, environment, address, login, enrollment_mode)
	           VALUES ('db', 'prod', $1, 'ansible', 'declared')`
	for nom, adresse := range map[string]string{
		"avec un espace":   "db prod 02",
		"avec un saut":     "db\n02",
		"une URL":          "https://db",
		"un tiret en tête": "-db",
		"trop longue":      strings.Repeat("a", 254),
	} {
		t.Run(nom, func(t *testing.T) {
			if _, err := conn.Exec(ctx, q, adresse); err == nil {
				t.Fatalf("adresse %q acceptée à tort", adresse)
			}
		})
	}
}

// Redéclarer une machine est le geste NORMAL — on corrige une adresse, on
// réenrôle après réinstallation. Mais `enrolled_at` ne bouge pas : la question
// posée en incident est « depuis quand est-elle dans le périmètre », pas
// « quand a-t-on corrigé sa fiche ».
func TestRedeclarerUneMachineNeRedatePasSonEnrolement(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveHost(ctx, machine("prod", "db", "10.0.0.1")); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	avant, _ := s.HostByName(ctx, "prod", "db")

	corrigee := machine("prod", "db", "10.0.0.99")
	corrigee.Login = "deploy"
	if err := s.SaveHost(ctx, corrigee); err != nil {
		t.Fatalf("SaveHost (correction): %v", err)
	}

	apres, err := s.HostByName(ctx, "prod", "db")
	if err != nil {
		t.Fatalf("HostByName: %v", err)
	}
	if apres.Address != "10.0.0.99" || apres.Login != "deploy" {
		t.Errorf("la correction n'a pas pris : %+v", apres)
	}
	if !apres.EnrolledAt.Equal(avant.EnrolledAt) {
		t.Errorf("enrolled_at redaté : %v puis %v", avant.EnrolledAt, apres.EnrolledAt)
	}

	toutes, err := s.ListHosts(ctx, "prod")
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(toutes) != 1 {
		t.Errorf("%d machines, attendu 1 : la redéclaration a créé un doublon", len(toutes))
	}
}

// Le même nom dans DEUX environnements est légitime — `db` en staging et `db`
// en prod sont deux machines. Le même nom DANS un environnement ne l'est pas.
func TestLeMemeNomVitDansDeuxEnvironnements(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	for _, env := range []string{"prod", "staging"} {
		if err := s.SaveHost(ctx, machine(env, "db", "10.0.0.1")); err != nil {
			t.Fatalf("SaveHost (%s): %v", env, err)
		}
	}

	toutes, err := s.ListHosts(ctx, "")
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(toutes) != 2 {
		t.Fatalf("%d machines, attendu 2", len(toutes))
	}
	// Triées par (environnement, nom) : deux exécutions se comparent.
	if toutes[0].Environment != "prod" || toutes[1].Environment != "staging" {
		t.Errorf("ordre inattendu : %s puis %s", toutes[0].Environment, toutes[1].Environment)
	}
}

func TestOublierUneMachine(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.SaveHost(ctx, machine("prod", "db", "10.0.0.1")); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	if err := s.ForgetHost(ctx, "prod", "db"); err != nil {
		t.Fatalf("ForgetHost: %v", err)
	}
	if _, err := s.HostByName(ctx, "prod", "db"); !errors.Is(err, core.ErrNoHost) {
		t.Errorf("la machine est encore là : %v", err)
	}
	// Oublier ce qui n'existe pas le DIT plutôt que de réussir en silence : un
	// script qui boucle sur des noms doit pouvoir voir sa faute de frappe.
	if err := s.ForgetHost(ctx, "prod", "db"); !errors.Is(err, core.ErrNoHost) {
		t.Errorf("erreur = %v, attendu core.ErrNoHost", err)
	}
}

// L'ingestion écrit des events. Elle n'a AUCUNE raison de connaître la flotte.
func TestLIngestionNeVoitPasLesMachines(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	admin := connect(t, dsn)
	defer admin.Close(ctx)
	for _, q := range []string{
		`CREATE ROLE ingest_login LOGIN PASSWORD 'ingest'`,
		`GRANT warden_ingest TO ingest_login`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("préparation du rôle: %v", err)
		}
	}

	restreint := connect(t, swapUser(dsn, "ingest_login", "ingest"))
	defer restreint.Close(ctx)

	interdits := map[string]string{
		"lire la flotte":     `SELECT count(*) FROM managed_host`,
		"enrôler":            `INSERT INTO managed_host (name, environment, address, login, enrollment_mode) VALUES ('x','prod','10.0.0.1','ansible','declared')`,
		"désépingler":        `UPDATE managed_host SET host_key = NULL`,
		"vider l'inventaire": `DELETE FROM managed_host`,
	}
	for nom, q := range interdits {
		if _, err := restreint.Exec(ctx, q); err == nil {
			t.Errorf("le rôle d'ingestion peut %s — le GRANT est trop large", nom)
		}
	}
}

// --- le run d'enrôlement (migration 00009) ---

func cibleValide() *core.EnrollTarget {
	return &core.EnrollTarget{
		Host: "db-prod-02", Address: "10.20.0.42",
		Port: core.PortSSHParDefaut, Login: "ansible",
	}
}

func TestUnRunDEnrolementSePasseDeDepotEtDeCommit(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	u, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// Ni repo, ni playbook, ni SHA : c'est tout l'objet du genre.
	id, err := s.CreateRun(ctx, store.NewRun{
		RunRef: testULID, UserID: u.ID, Environment: "prod",
		Kind: core.RunEnrollment, EnrollTarget: cibleValide(),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	r, err := s.RunByID(ctx, id)
	if err != nil {
		t.Fatalf("RunByID: %v", err)
	}
	if r.Kind != core.RunEnrollment {
		t.Errorf("genre = %q, attendu %q", r.Kind, core.RunEnrollment)
	}
	if r.EnrollTarget == nil {
		t.Fatal("la cible n'a pas été relue")
	}
	if r.EnrollTarget.Host != "db-prod-02" || r.EnrollTarget.Address != "10.20.0.42" {
		t.Errorf("cible relue = %+v", *r.EnrollTarget)
	}
	// Le genre est aussi lisible seul : c'est ce que le dispatcher interroge à
	// chaque tour de boucle.
	if genre, err := s.RunKindByID(ctx, id); err != nil || genre != core.RunEnrollment {
		t.Errorf("RunKindByID = %q, %v", genre, err)
	}
}

// Un run de playbook reste COMPLET : la relaxation des NOT NULL ne doit pas
// dégénérer en « tout est facultatif ».
func TestUnRunDePlaybookResteComplet(t *testing.T) {
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
	// Vide vaut `playbook` : c'est ce que toutes les lignes antérieures sont.
	if r.Kind != core.RunPlaybook {
		t.Errorf("genre = %q, attendu %q", r.Kind, core.RunPlaybook)
	}
	if r.EnrollTarget != nil {
		t.Error("un run de playbook porte une cible d'enrôlement")
	}

	// Et la contrainte tient : un playbook privé de son commit est refusé par
	// le SCHÉMA, pas seulement par le code.
	conn := connect(t, dsn)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `
INSERT INTO run (run_ref, user_id, environment, state, termination_cause, kind)
VALUES ('01JZQ00000000000000000ZZZZ', $1, 'prod', 'interrupted', 'unknown', 'playbook')`, r.UserID)
	if err == nil {
		t.Error("un run de playbook sans commit d'audit a été accepté par le schéma")
	}
}

// Les deux contraintes symétriques : un enrôlement sans cible ne saurait pas
// quoi joindre, un playbook avec une cible décrirait deux choses à la fois.
func TestLeGenreEtLaCibleVontEnsemble(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	seedRun(t, s)

	r, _ := s.RunByRef(ctx, testULID)
	conn := connect(t, dsn)
	defer conn.Close(ctx)

	cas := map[string]string{
		"enrôlement sans cible": `
INSERT INTO run (run_ref, user_id, environment, state, termination_cause, kind)
VALUES ('01JZQ00000000000000000YYYY', $1, 'prod', 'interrupted', 'unknown', 'enrollment')`,
		"playbook avec cible": `
INSERT INTO run (run_ref, user_id, environment, inventory, playbook, audit_sha,
                 base_sha, mainline_ref, mainline_sha, state, termination_cause,
                 kind, enroll_target)
VALUES ('01JZQ00000000000000000XXXX', $1, 'prod', 'inv', 'site.yml',
        'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'refs/heads/main',
        'cccccccccccccccccccccccccccccccccccccccc', 'interrupted', 'unknown',
        'playbook', '{"host":"x"}'::jsonb)`,
		"genre inconnu": `
INSERT INTO run (run_ref, user_id, environment, state, termination_cause, kind)
VALUES ('01JZQ00000000000000000WWWW', $1, 'prod', 'interrupted', 'unknown', 'autre')`,
	}
	for nom, q := range cas {
		if _, err := conn.Exec(ctx, q, r.UserID); err == nil {
			t.Errorf("%s : accepté par le schéma", nom)
		}
	}
}

// Le code refuse AVANT la base, et nomme ce qui manque : une violation de
// contrainte PostgreSQL ne dit pas quelle cible est en cause.
func TestUnEnrolementMalFormeEstRefuseParLeCode(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	u, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	base := store.NewRun{
		RunRef: testULID, UserID: u.ID, Environment: "prod", Kind: core.RunEnrollment,
	}
	cas := map[string]*core.EnrollTarget{
		"sans cible":      nil,
		"sans adresse":    {Host: "db", Port: 22, Login: "ansible"},
		"adresse absurde": {Host: "db", Address: "db prod 02", Port: 22, Login: "ansible"},
		"compte refusé":   {Host: "db", Address: "10.0.0.1", Port: 22, Login: "Root!"},
	}
	for nom, cible := range cas {
		t.Run(nom, func(t *testing.T) {
			n := base
			n.EnrollTarget = cible
			if _, err := s.CreateRun(ctx, n); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}
