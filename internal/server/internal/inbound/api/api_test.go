package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	wsproto "github.com/jijins1/warden/internal/wire/ws"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// harness sets up a test API server with PostgreSQL backend.
type harness struct {
	server   *api.Server
	store    *store.Store
	ingest   *store.Ingest
	flux     *flux.Flux
	httpSrv  *httptest.Server
	userID   int64
	dsn      string
}

// setup creates a test harness with database.
func setup(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	// Start PostgreSQL
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("warden"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("starting PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	// Migrate
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Create API server
	cfg := &api.Config{
		Addr:        "127.0.0.1:0",
		DatabaseURL: dsn,
	}
	server, err := api.New(cfg)
	if err != nil {
		t.Fatalf("creating API server: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	// Create HTTP server with proper routing
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runs/{run_ref}/stream", server.HandleStream)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	// Open flux separately (the server opens it internally too)
	f, err := flux.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("opening flux: %v", err)
	}
	t.Cleanup(f.Close)

	// Start LISTEN goroutine
	go func() { _ = f.Listen(context.WithoutCancel(ctx)) }()
	time.Sleep(100 * time.Millisecond) // Let LISTEN establish

	// Open store for creating test data
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(st.Close)

	// Create test user
	user, err := st.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "test", DisplayName: "Test User",
	})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	// Create ingest for pushing events
	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("opening ingest: %v", err)
	}
	t.Cleanup(in.Close)

	return &harness{
		server:  server,
		store:   st,
		ingest:  in,
		flux:    f,
		httpSrv: httpSrv,
		userID:  user.ID,
		dsn:     dsn,
	}
}

// createRun creates a test run and returns its reference (ULID).
func (h *harness) createRun(t *testing.T, ctx context.Context) string {
	t.Helper()
	runRef := "01JZQ00000000000000000ABCD"
	_, err := h.store.CreateRun(ctx, store.NewRun{
		RunRef:      runRef,
		UserID:      h.userID,
		Environment: "test",
		Inventory:   "inv/test",
		Playbook:    "test.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("creating run: %v", err)
	}
	return runRef
}

// pushEvents sends events to the ingest.
func (h *harness) pushEvents(t *testing.T, runID int64, count int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	evs := make([]events.Event, count)
	for i := range evs {
		evs[i] = events.Event{
			Seq:        int64(i),
			OccurredAt: now,
			Host:       "test-01",
			TaskNumber: i + 1,
			TaskName:   "test task",
			State:      events.StateOK,
			Payload:    json.RawMessage(`{"ok":true}`),
		}
	}
	if err := h.ingest.AppendEvents(ctx, runID, evs); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
}

// dialWS connects to the WebSocket endpoint.
func (h *harness) dialWS(t *testing.T, runRef string, fromOffset int64) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(h.httpSrv.URL, "http")
	path := "/runs/" + runRef + "/stream"
	if fromOffset > 0 {
		path += "?from_offset=" + strconv.FormatInt(fromOffset, 10)
	}

	ws, _, err := websocket.DefaultDialer.Dial(wsURL+path, nil)
	if err != nil {
		t.Fatalf("WebSocket dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	return ws
}

// readWSMessage reads a WebSocket message.
func readWSMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) *wsproto.Message {
	t.Helper()

	conn.SetReadDeadline(time.Now().Add(timeout))
	var msg wsproto.Message
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading message: %v", err)
	}
	return &msg
}

// TestWebSocketUpgrade verifies WebSocket upgrade works.
func TestWebSocketUpgrade(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	runRef := h.createRun(t, ctx)
	run, err := h.store.RunByRef(ctx, runRef)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	// Send an event
	h.pushEvents(t, run.ID, 1)

	// Connect via WebSocket
	ws := h.dialWS(t, runRef, 0)

	// Should receive the event
	msg := readWSMessage(t, ws, 5*time.Second)
	if msg.Type != wsproto.TypeEvent {
		t.Fatalf("expected event, got %s", msg.Type)
	}

	var frame wsproto.EventFrame
	if err := json.Unmarshal(msg.Body, &frame); err != nil {
		t.Fatalf("unmarshaling frame: %v", err)
	}

	if frame.Seq != 0 {
		t.Errorf("seq = %d, want 0", frame.Seq)
	}
	if frame.Host != "test-01" {
		t.Errorf("host = %s, want test-01", frame.Host)
	}
}

// TestOffsetReplay verifies that clients can replay from an offset.
func TestOffsetReplay(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	runRef := h.createRun(t, ctx)
	run, err := h.store.RunByRef(ctx, runRef)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	// Send 10 events
	h.pushEvents(t, run.ID, 10)

	// Connect from offset 5
	ws := h.dialWS(t, runRef, 5)

	// Should receive events 5-9
	for i := 5; i < 10; i++ {
		msg := readWSMessage(t, ws, 5*time.Second)
		if msg.Type != wsproto.TypeEvent {
			t.Fatalf("event %d: expected event, got %s", i, msg.Type)
		}

		var frame wsproto.EventFrame
		if err := json.Unmarshal(msg.Body, &frame); err != nil {
			t.Fatalf("unmarshaling frame: %v", err)
		}

		if frame.Seq != int64(i) {
			t.Errorf("event %d: seq = %d, want %d", i, frame.Seq, i)
		}
	}

	// No more events yet
	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var msg wsproto.Message
	if err := ws.ReadJSON(&msg); err == nil {
		t.Errorf("unexpected message: %+v", msg)
	}
}

// TestKeepAlive verifies that the server sends keep-alive frames.
func TestKeepAlive(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	runRef := h.createRun(t, ctx)

	// Don't send any events, just connect
	ws := h.dialWS(t, runRef, 0)

	// Should receive keep-alive within 25 seconds (20s interval + buffer)
	msg := readWSMessage(t, ws, 30*time.Second)
	if msg.Type != wsproto.TypeKeepAlive {
		t.Fatalf("expected keepalive, got %s", msg.Type)
	}
}

// TestStreamEnd verifies that the stream closes when the run ends.
func TestStreamEnd(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	runRef := h.createRun(t, ctx)
	run, err := h.store.RunByRef(ctx, runRef)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	// Send one event
	h.pushEvents(t, run.ID, 1)

	// Connect
	ws := h.dialWS(t, runRef, 0)

	// Receive the event
	_ = readWSMessage(t, ws, 5*time.Second)

	// Close the run
	if err := h.store.CloseRun(ctx, run.ID, core.RunSucceeded, ""); err != nil {
		t.Fatalf("closing run: %v", err)
	}

	// Should receive end message
	msg := readWSMessage(t, ws, 5*time.Second)
	if msg.Type != wsproto.TypeEnd {
		t.Fatalf("expected end, got %s", msg.Type)
	}

	var frame wsproto.EndFrame
	if err := json.Unmarshal(msg.Body, &frame); err != nil {
		t.Fatalf("unmarshaling end frame: %v", err)
	}

	if frame.Status != "succeeded" {
		t.Errorf("status = %s, want succeeded", frame.Status)
	}

	// Connection should close
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var nextMsg wsproto.Message
	if err := ws.ReadJSON(&nextMsg); err == nil {
		t.Errorf("unexpected message after end: %+v", nextMsg)
	}
}

// TestMultipleClients verifies that multiple clients can stream the same run.
func TestMultipleClients(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	runRef := h.createRun(t, ctx)
	run, err := h.store.RunByRef(ctx, runRef)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	// Connect two clients
	ws1 := h.dialWS(t, runRef, 0)
	ws2 := h.dialWS(t, runRef, 0)

	// Send events
	h.pushEvents(t, run.ID, 3)

	// Both clients should receive all events
	for i := 0; i < 3; i++ {
		msg1 := readWSMessage(t, ws1, 5*time.Second)
		msg2 := readWSMessage(t, ws2, 5*time.Second)

		if msg1.Type != wsproto.TypeEvent || msg2.Type != wsproto.TypeEvent {
			t.Fatalf("event %d: expected events, got %s and %s", i, msg1.Type, msg2.Type)
		}

		var f1, f2 wsproto.EventFrame
		_ = json.Unmarshal(msg1.Body, &f1)
		_ = json.Unmarshal(msg2.Body, &f2)

		if f1.Seq != f2.Seq || f1.Seq != int64(i) {
			t.Errorf("event %d: mismatch or wrong seq", i)
		}
	}
}

// TestReconnectWithOffset verifies that clients can reconnect from a saved offset.
// The offset is managed by the client, not the server.
func TestReconnectWithOffset(t *testing.T) {
	t.Skip("Client-managed offset reconnection - tested as part of CLI upload flow")
}
