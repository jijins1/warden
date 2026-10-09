//go:build integration

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
)

// runRef builds a syntactically valid run_ref: 26 characters drawn from the
// Crockford base32 alphabet, as enforced by the run_run_ref_check constraint.
// A malformed ref is rejected by the database, so the helper validates the
// length here rather than letting every caller produce a silent INSERT failure.
func runRef(t *testing.T, suffix string) string {
	t.Helper()
	const prefix = "01JZQ"
	pad := 26 - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("suffix %q too long for a 26-char ref", suffix)
	}
	ref := prefix + fmt.Sprintf("%0*d", pad, 0) + suffix
	if len(ref) != 26 {
		t.Fatalf("built ref %q has %d chars, want 26", ref, len(ref))
	}
	return ref
}

// mustCreateRun creates a run and fails the test on error. Ignoring the error
// here would let the assertions below run against an empty table.
func mustCreateRun(t *testing.T, ctx context.Context, s *store.Store, ref string, userID int64, env, inventory, playbook string) int64 {
	t.Helper()
	id, err := s.CreateRun(ctx, store.NewRun{
		RunRef:      ref,
		UserID:      userID,
		Repo:        "warden",
		Environment: env,
		Inventory:   inventory,
		Playbook:    playbook,
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("creating run %s: %v", ref, err)
	}
	return id
}

func mustUser(t *testing.T, ctx context.Context, s *store.Store, subject string) store.User {
	t.Helper()
	u, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: subject, DisplayName: subject,
	})
	if err != nil {
		t.Fatalf("UpsertUser %s: %v", subject, err)
	}
	return u
}

// TestListRunsBasic tests basic run listing and filtering.
func TestListRunsBasic(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")

	// A run is born 'interrupted'/'unknown' (§6.4): the row is written before the
	// pod exists, so a crash in between leaves an honest line. MarkRunning is what
	// moves it to 'running'.
	runID1 := mustCreateRun(t, ctx, s, runRef(t, "001"), user.ID, "production", "inv/prod", "deploy.yml")
	runID2 := mustCreateRun(t, ctx, s, runRef(t, "002"), user.ID, "staging", "inv/staging", "deploy.yml")
	runID3 := mustCreateRun(t, ctx, s, runRef(t, "003"), user.ID, "test", "inv/test", "test.yml")

	for _, id := range []int64{runID1, runID2, runID3} {
		if err := s.MarkRunning(ctx, id, "pod-test"); err != nil {
			t.Fatalf("MarkRunning %d: %v", id, err)
		}
	}

	// Close one run to test state filtering
	if err := s.CloseRun(ctx, runID3, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	// Test 1: List all runs
	result, err := s.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns all: %v", err)
	}
	if len(result.Runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(result.Runs))
	}

	// Test 2: Filter by environment
	result, err = s.ListRuns(ctx, store.RunFilter{Environment: "production"})
	if err != nil {
		t.Fatalf("ListRuns prod: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("expected 1 production run, got %d", len(result.Runs))
	}
	if result.Runs[0].Environment != "production" {
		t.Errorf("expected production env, got %s", result.Runs[0].Environment)
	}

	// Test 3: Filter by state (succeeded)
	result, err = s.ListRuns(ctx, store.RunFilter{State: string(core.RunSucceeded)})
	if err != nil {
		t.Fatalf("ListRuns succeeded: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Errorf("expected 1 succeeded run, got %d", len(result.Runs))
	}

	// Test 4: Filter by state (running)
	result, err = s.ListRuns(ctx, store.RunFilter{State: string(core.RunRunning)})
	if err != nil {
		t.Fatalf("ListRuns running: %v", err)
	}
	if len(result.Runs) != 2 {
		t.Errorf("expected 2 running runs, got %d", len(result.Runs))
	}
}

// TestListRunsPagination walks every page and asserts that each run is seen
// exactly once. A cursor that fails to decode re-serves page 1 forever, so this
// also guards against the loop that bug produces.
func TestListRunsPagination(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")

	const total = 7
	for i := range total {
		mustCreateRun(t, ctx, s, runRef(t, fmt.Sprintf("%03d", i)), user.ID, "prod", "inv", "p.yml")
	}

	seen := map[string]int{}
	cursor := ""
	for page := 0; ; page++ {
		if page > total+2 {
			t.Fatalf("pagination did not terminate after %d pages (cursor not honored?)", page)
		}
		res, err := s.ListRuns(ctx, store.RunFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListRuns page %d: %v", page, err)
		}
		for _, r := range res.Runs {
			seen[r.RunRef]++
		}
		if res.NextCursor == "" {
			break
		}
		if res.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %q", cursor)
		}
		cursor = res.NextCursor
	}

	if len(seen) != total {
		t.Errorf("expected %d distinct runs across pages, got %d", total, len(seen))
	}
	for ref, n := range seen {
		if n != 1 {
			t.Errorf("run %s returned %d times, want exactly 1", ref, n)
		}
	}
}

// TestListRunsRejectsBadCursor verifies that a malformed cursor is reported
// rather than silently ignored (which would quietly restart at page 1).
func TestListRunsRejectsBadCursor(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := s.ListRuns(ctx, store.RunFilter{Cursor: "not-a-cursor"}); err == nil {
		t.Error("expected an error for a malformed cursor, got nil")
	}
}

// TestListRunsSecureEnumeration verifies that list exposes ULID, not sequential ID.
func TestListRunsSecureEnumeration(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	ref := runRef(t, "001")
	mustCreateRun(t, ctx, s, ref, user.ID, "test", "inv/test", "test.yml")

	result, err := s.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(result.Runs))
	}

	run := result.Runs[0]
	if run.RunRef != ref {
		t.Errorf("expected RunRef %q, got %s", ref, run.RunRef)
	}
	if run.ID <= 0 {
		t.Errorf("expected ID > 0, got %d", run.ID)
	}
}

// TestDiffReaderUsesPartitionKey checks that a diff is read back through its
// primary key (run_id, run_day, seq), and that a wrong run_day finds nothing.
// The second half is what pins the partition key as load-bearing: a query that
// ignores it would still return the row, while scanning every partition.
func TestDiffReaderUsesPartitionKey(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	dr, err := store.OpenDiffReader(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenDiffReader: %v", err)
	}
	defer dr.Close()

	user := mustUser(t, ctx, s, "test")
	ref := runRef(t, "D1")
	runID := mustCreateRun(t, ctx, s, ref, user.ID, "prod", "inv", "p.yml")

	now := time.Now().UTC()
	if err := in.AppendEvents(ctx, runID, []events.Event{
		{
			Seq: 0, OccurredAt: now, Host: "h1", TaskNumber: 1,
			State: events.StateChanged, Payload: json.RawMessage(`{}`),
			Diff: json.RawMessage(`{"before":"a","after":"b"}`),
		},
		{
			Seq: 1, OccurredAt: now, Host: "h1", TaskNumber: 2,
			State: events.StateOK, Payload: json.RawMessage(`{}`),
		},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	run, err := s.RunByRef(ctx, ref)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}
	if run.RunDay.IsZero() {
		t.Fatal("RunByRef did not return the partition key")
	}

	// The changed event carries its diff.
	diff, err := dr.DiffForRun(ctx, run, 0)
	if err != nil {
		t.Fatalf("DiffForRun: %v", err)
	}
	if !strings.Contains(string(diff), "before") {
		t.Errorf("expected the stored diff, got %q", diff)
	}

	// An event without a diff is absence, not an error.
	none, err := dr.DiffForRun(ctx, run, 1)
	if err != nil {
		t.Fatalf("DiffForRun on an event without diff: %v", err)
	}
	if none != nil {
		t.Errorf("expected no diff, got %q", none)
	}

	// The partition key participates in the lookup.
	otherDay, err := dr.Diff(ctx, run.ID, run.RunDay.AddDate(0, 0, -1), 0)
	if err != nil {
		t.Fatalf("Diff on another run_day: %v", err)
	}
	if otherDay != nil {
		t.Errorf("run_day is not part of the lookup: got %q", otherDay)
	}

	// A missing partition key is refused rather than silently scanning.
	if _, err := dr.Diff(ctx, run.ID, time.Time{}, 0); err == nil {
		t.Error("expected an error when run_day is missing")
	}
}

// TestPreviousRun couvre « le run précédent sur le même inventaire » du §6.5.
func TestPreviousRun(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")

	// Trois runs sur le même triplet, plus deux leurres : un autre inventaire,
	// et surtout un autre DÉPÔT — deux SHA de dépôts différents n'ont aucun
	// ancêtre commun, les comparer échouerait.
	creer := func(suffixe, repo, env, inv string) (int64, string) {
		ref := runRef(t, suffixe)
		id, err := s.CreateRun(ctx, store.NewRun{
			RunRef: ref, UserID: user.ID, Repo: repo,
			Environment: env, Inventory: inv, Playbook: "p.yml",
			AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MainlineRef: "refs/heads/main",
			MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
		})
		if err != nil {
			t.Fatalf("CreateRun %s: %v", ref, err)
		}
		return id, ref
	}

	_, ref1 := creer("P01", "warden", "prod", "inv")
	_, ref2 := creer("P02", "warden", "prod", "inv")
	_, ref3 := creer("P03", "warden", "prod", "inv")
	creer("P04", "warden", "prod", "autre") // autre inventaire
	creer("P05", "autre", "prod", "inv")    // autre dépôt

	troisieme, err := s.RunByRef(ctx, ref3)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	prec, err := s.PreviousRun(ctx, troisieme)
	if err != nil {
		t.Fatalf("PreviousRun: %v", err)
	}
	if prec.RunRef != ref2 {
		t.Errorf("précédent de %s = %s, attendu %s", ref3, prec.RunRef, ref2)
	}

	// Le deuxième a bien le premier pour précédent.
	deuxieme, _ := s.RunByRef(ctx, ref2)
	prec, err = s.PreviousRun(ctx, deuxieme)
	if err != nil {
		t.Fatalf("PreviousRun sur le deuxième: %v", err)
	}
	if prec.RunRef != ref1 {
		t.Errorf("précédent de %s = %s, attendu %s", ref2, prec.RunRef, ref1)
	}

	// Le premier n'en a pas : c'est une absence, pas une panne.
	premier, _ := s.RunByRef(ctx, ref1)
	if _, err := s.PreviousRun(ctx, premier); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("erreur = %v, attendu ErrNotFound", err)
	}
}

// TestCreateRunRefuseDepotVide : contrepartie en Go du DROP DEFAULT de la
// migration 00004. Un run sans dépôt s'insérerait sans bruit et son artefact
// deviendrait introuvable des mois plus tard.
func TestCreateRunRefuseDepotVide(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	_, err = s.CreateRun(ctx, store.NewRun{
		RunRef: runRef(t, "X01"), UserID: user.ID, Repo: "",
		Environment: "prod", Inventory: "inv", Playbook: "p.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err == nil {
		t.Error("run créé sans dépôt")
	}
}

// TestListRunsSupporteUnRunSansPlaybookNiInventaire.
//
// UN run d'enrôlement n'a ni playbook ni inventaire : ces colonnes sont NULL.
// Scanner un NULL dans une chaîne nue échoue, et l'échec ne porte pas sur la
// ligne — il fait tomber TOUTE la liste, donc l'écran d'accueil du produit.
//
// C'est arrivé en déploiement : 13 runs sur 29 étaient dans ce cas, `GET /runs`
// rendait un 500, et le handler n'en journalisait rien. Les fixtures des autres
// tests renseignent toujours ces deux colonnes, ce qui rendait le défaut
// invisible ici.
func TestListRunsSupporteUnRunSansPlaybookNiInventaire(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	id := mustCreateRun(t, ctx, s, runRef(t, "004"), user.ID, "prod", "inv", "play.yml")

	// Le run d'enrôlement tel que la base le porte : `kind = 'enrollment'`, une
	// cible, et NI playbook NI inventaire. Les deux contraintes de la migration
	// 00009 tiennent — c'est une ligne légitime, pas une donnée abîmée.
	conn := connect(t, dsn)
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `
		UPDATE run
		   SET kind = 'enrollment', enroll_target = '{"host":"cible-test"}'::jsonb,
		       playbook = NULL, inventory = NULL,
		       audit_sha = NULL, base_sha = NULL,
		       mainline_ref = NULL, mainline_sha = NULL
		 WHERE id = $1`, id); err != nil {
		t.Fatalf("passage en run d'enrôlement : %v", err)
	}

	result, err := s.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns doit supporter un run sans playbook ni inventaire : %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("%d runs, attendu 1", len(result.Runs))
	}
	if result.Runs[0].Playbook != "" || result.Runs[0].Inventory != "" {
		t.Errorf("playbook=%q inventory=%q, attendus vides",
			result.Runs[0].Playbook, result.Runs[0].Inventory)
	}
}

// Le même défaut, sur l'autre chemin de lecture.
//
// `RunsByHost` remplit le MÊME RunListItem que `ListRuns`, et un run
// d'enrôlement touche par définition une machine — il apparaît donc dans cet
// historique. Corriger un seul des deux chemins aurait laissé l'écran « runs par
// machine » tomber exactement de la même façon.
func TestRunsByHostSupporteUnRunDEnrolement(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	id := mustCreateRun(t, ctx, s, runRef(t, "005"), user.ID, "prod", "inv", "play.yml")
	if err := s.MarkRunning(ctx, id, "pod-test"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	conn := connect(t, dsn)
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `
		UPDATE run
		   SET kind = 'enrollment', enroll_target = '{"host":"cible-test"}'::jsonb,
		       playbook = NULL, inventory = NULL,
		       audit_sha = NULL, base_sha = NULL,
		       mainline_ref = NULL, mainline_sha = NULL
		 WHERE id = $1`, id); err != nil {
		t.Fatalf("passage en run d'enrôlement : %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO event (run_id, run_day, seq, occurred_at, host, state, payload)
		VALUES ($1, (SELECT run_day FROM run WHERE id = $1), 1, now(), 'cible-test', 'ok', '{}'::jsonb)`,
		id); err != nil {
		t.Fatalf("event : %v", err)
	}

	runs, err := s.RunsByHost(ctx, "cible-test", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("RunsByHost doit supporter un run d'enrôlement : %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs, attendu 1", len(runs))
	}
}

// insererEventPourRun pose un event minimal reliant un run à un hôte, sans
// passer par l'ingest : ce que PlaybooksByHost et RunsByHost lisent est le
// JOIN sur `event.host`, pas le contenu de l'event.
func insererEventPourRun(t *testing.T, ctx context.Context, conn *pgx.Conn, runID int64, host string) {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		INSERT INTO event (run_id, run_day, seq, occurred_at, host, state, payload)
		VALUES ($1, (SELECT run_day FROM run WHERE id = $1), 1, now(), $2, 'ok', '{}'::jsonb)`,
		runID, host); err != nil {
		t.Fatalf("event sur %s : %v", host, err)
	}
}

// TestPlaybooksByHostDedupliqueParPlaybook couvre la dédup du §6.5 par
// playbook : deux runs du MÊME playbook sur une machine ne doivent rendre
// qu'une ligne, portant le plus RÉCENT des deux — et un run d'un autre
// playbook sur la même machine doit rester une ligne séparée.
func TestPlaybooksByHostDedupliqueParPlaybook(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	const host = "db-01"

	ancien := mustCreateRun(t, ctx, s, runRef(t, "006"), user.ID, "prod", "inv", "deploy.yml")
	recent := mustCreateRun(t, ctx, s, runRef(t, "007"), user.ID, "prod", "inv", "deploy.yml")
	autre := mustCreateRun(t, ctx, s, runRef(t, "008"), user.ID, "prod", "inv", "other.yml")

	conn := connect(t, dsn)
	defer func() { _ = conn.Close(ctx) }()

	// Ordre explicite : sans lui, les trois runs partagent la même seconde et
	// "le plus récent" ne voudrait rien dire.
	if _, err := conn.Exec(ctx,
		`UPDATE run SET started_at = now() - interval '2 hours' WHERE id = $1`, ancien); err != nil {
		t.Fatalf("recul de started_at : %v", err)
	}
	if _, err := conn.Exec(ctx,
		`UPDATE run SET started_at = now() - interval '1 hour' WHERE id = $1`, recent); err != nil {
		t.Fatalf("recul de started_at : %v", err)
	}

	insererEventPourRun(t, ctx, conn, ancien, host)
	insererEventPourRun(t, ctx, conn, recent, host)
	insererEventPourRun(t, ctx, conn, autre, host)

	playbooks, err := s.PlaybooksByHost(ctx, host, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("PlaybooksByHost: %v", err)
	}
	if len(playbooks) != 2 {
		t.Fatalf("%d playbooks, attendu 2 (deploy.yml dédupliqué + other.yml) : %+v", len(playbooks), playbooks)
	}

	var deploy, other *core.PlaybookSummary
	for i := range playbooks {
		switch playbooks[i].Playbook {
		case "deploy.yml":
			deploy = &playbooks[i]
		case "other.yml":
			other = &playbooks[i]
		}
	}
	if deploy == nil || other == nil {
		t.Fatalf("playbooks attendus absents : %+v", playbooks)
	}
	if deploy.LastRunRef != runRef(t, "007") {
		t.Errorf("deploy.yml : dernier run = %q, attendu le plus récent (%q)",
			deploy.LastRunRef, runRef(t, "007"))
	}
	if other.LastRunRef != runRef(t, "008") {
		t.Errorf("other.yml : dernier run = %q, attendu %q", other.LastRunRef, runRef(t, "008"))
	}
}
