//go:build integration

package api_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// setupServer creates a test API server with testcontainers PostgreSQL.
func setupServer(t *testing.T) (*api.Server, *store.Ingest, int64) {
	t.Helper()
	ctx := context.Background()

	// Start PostgreSQL container
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

	// Migrate schema
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Create API server
	cfg := &api.Config{
		Addr:         "127.0.0.1:0",
		DatabaseURL:  dsn,
	}
	server, err := api.New(cfg)
	if err != nil {
		t.Fatalf("creating server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	// Create ingest client for pushing events
	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("opening ingest: %v", err)
	}
	t.Cleanup(in.Close)

	// Create a test run
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer st.Close()

	user, err := st.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}

	runID, err := st.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ00000000000000000TEST",
		UserID:      user.ID,
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

	return server, in, runID
}

// connectWS dials the WebSocket endpoint and returns the connection.
func connectWS(t *testing.T, baseURL string, runID int64, fromOffset int64) *websocket.Conn {
	t.Helper()

	// Convert http:// to ws:// URL
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1)
	path := "/runs/" + strconv.FormatInt(runID, 10) + "/stream"
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

// readMessage reads and parses a WebSocket message.
// TODO: Implement proper message type for streaming
func readMessage(t *testing.T, wsConn *websocket.Conn) map[string]interface{} {
	t.Helper()

	var msg map[string]interface{}
	wsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := wsConn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading message: %v", err)
	}
	return msg
}

// TestStreamBasicFlow tests the basic streaming flow.
func TestStreamBasicFlow(t *testing.T) {
	server, in, runID := setupServer(t)

	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Run(ctx) }()

	// Give server time to start listening
	time.Sleep(100 * time.Millisecond)

	// Get server URL (this is tricky with httptest)
	// For now, we'll skip this test and come back to it
	// after we wire the server to an actual listener.
	t.Skip("WebSocket server routing needs full HTTP server setup")

	_ = in
	_ = runID
}

// TestStreamEventOrdering tests that events arrive in sequence order.
func TestStreamEventOrdering(t *testing.T) {
	t.Skip("WebSocket server routing needs full HTTP server setup")
}

// TestStreamReplayFromOffset tests offset-based replay.
func TestStreamReplayFromOffset(t *testing.T) {
	t.Skip("WebSocket server routing needs full HTTP server setup")
}

// TestStreamClientDisconnect tests graceful client disconnection.
func TestStreamClientDisconnect(t *testing.T) {
	t.Skip("WebSocket server routing needs full HTTP server setup")
}
