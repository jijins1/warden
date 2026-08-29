package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jijins1/warden/internal/server/internal/core"
	wire "github.com/jijins1/warden/internal/wire/ws"
)

// streamHandler manages a single WebSocket client streaming run events.
type streamHandler struct {
	// runRef sert à RELIRE l'issue du run à la fermeture du flux : l'annoncer
	// sans la lire ferait passer un échec pour un succès.
	runRef string
	runID  int64
	offset int64
	events core.EventFollower
	ws     *websocket.Conn

	mu           sync.Mutex
	lastAckSeq   int64
	clientClosed bool
}

// HandleStream upgrades HTTP to WebSocket and streams run events.
// Path parameter {run_ref} is a ULID (immutable, not enumerable).
func (a *Server) HandleStream(w http.ResponseWriter, r *http.Request) {
	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	// Resolve run_ref to internal run ID and check read access
	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}
	runID := run.ID

	fromOffset := int64(0)
	if offsetStr := r.URL.Query().Get("from_offset"); offsetStr != "" {
		if o, err := strconv.ParseInt(offsetStr, 10, 64); err == nil && o >= 0 {
			fromOffset = o
		}
	}

	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("WebSocket upgrade failed", "run_id", runID, "error", err)
		return
	}
	defer func() { _ = ws.Close() }()

	h := &streamHandler{
		runID:  runID,
		runRef: runRef,
		offset: fromOffset,
		events: a.deps.Events,
		ws:     ws,
	}

	// Set read/write deadlines to detect dead connections
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
		return nil
	})

	// Start reading client messages (for ACKs and detach signals)
	go h.readLoop(a)

	// Stream events
	if err := h.streamEvents(r.Context(), a); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("Stream error", "run_id", runID, "error", err)
		_ = h.sendError(fmt.Sprintf("stream error: %v", err))
	}
}

// streamEvents reads from flux and sends to the client.
func (h *streamHandler) streamEvents(ctx context.Context, server *Server) error {
	stream, err := h.events.Follow(ctx, h.runID, h.offset)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Events()

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-keepAlive.C:
			if err := h.send(wire.NewKeepAliveMessage()); err != nil {
				return err
			}

		case e, ok := <-stream.Events():
			if !ok {
				// Stream closed
				if err := stream.Err(); err != nil {
					return err
				}
				// Le flux d'events est terminé : le run est clos. Son ISSUE est
				// relue en base, jamais supposée.
				//
				// Cette ligne rendait `succeeded` en dur, ce qui annonçait
				// « réussi » à qui regardait un playbook en échec. C'est le pire
				// mensonge possible d'un produit d'audit — et il est devenu
				// load-bearing quand `warden enroll` s'est mis à décider, sur
				// cette valeur, s'il fallait déclarer la machine.
				return h.sendEnd(server.issueDuRun(ctx, h.runRef))
			}

			// Send event to client
			if err := h.send(wire.NewEventMessage(e)); err != nil {
				return err
			}

			// Update our tracking
			h.mu.Lock()
			h.lastAckSeq = e.Seq
			h.mu.Unlock()
		}
	}
}

// readLoop processes incoming client messages and detects disconnection.
func (h *streamHandler) readLoop(server *Server) {
	for {
		var msg wire.Message
		if err := h.ws.ReadJSON(&msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				h.mu.Lock()
				h.clientClosed = true
				h.mu.Unlock()
			}
			return
		}

		switch msg.Type {
		case "ack":
			// Client acknowledging receipt up to an offset.
			// Reserved for future use.

		default:
			// Ignore unknown message types (forward compatibility)
		}
	}
}

// send encodes and sends a message to the client.
func (h *streamHandler) send(msg wire.Message) error {
	h.mu.Lock()
	closed := h.clientClosed
	h.mu.Unlock()

	if closed {
		return fmt.Errorf("client disconnected")
	}

	return h.ws.WriteJSON(msg)
}

// sendEnd signals that the run has ended.
func (h *streamHandler) sendEnd(status core.RunState, reason string) error {
	return h.send(wire.NewEndMessage(string(status), reason))
}

// issueDuRun relit l'état final d'un run.
//
// Une relecture qui échoue rend `interrupted` / `unknown` plutôt que
// `succeeded` : ne pas savoir comment un run s'est terminé est exactement ce que
// `unknown` existe pour dire (§6.4), et supposer la réussite serait le mode de
// défaillance le plus coûteux d'un produit d'audit.
func (a *Server) issueDuRun(ctx context.Context, runRef string) (core.RunState, string) {
	run, err := a.deps.Runs.RunByRef(ctx, runRef)
	if err != nil {
		slog.Warn("issue du run illisible",
			slog.String("run_ref", runRef), slog.String("error", err.Error()))
		return core.RunInterrupted, string(core.CauseUnknown)
	}
	return run.State, string(run.TerminationCause)
}

// sendError sends an error frame and closes the connection.
func (h *streamHandler) sendError(errMsg string) error {
	_ = h.send(wire.NewErrorMessage(errMsg))
	return h.ws.Close()
}
