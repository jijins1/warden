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
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	wire "github.com/jijins1/warden/internal/wire/ws"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// streamHandler manages a single WebSocket client streaming run events.
type streamHandler struct {
	runID  int64
	offset int64
	flux   *flux.Flux
	ws     *websocket.Conn

	mu           sync.Mutex
	lastAckSeq   int64
	clientClosed bool
}

// HandleStream upgrades HTTP to WebSocket and streams run events.
func (a *Server) HandleStream(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid run ID", http.StatusBadRequest)
		return
	}

	fromOffset := int64(0)
	if offsetStr := r.URL.Query().Get("from_offset"); offsetStr != "" {
		if o, err := strconv.ParseInt(offsetStr, 10, 64); err == nil && o >= 0 {
			fromOffset = o
		}
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("WebSocket upgrade failed", "run_id", runID, "error", err)
		return
	}
	defer ws.Close()

	h := &streamHandler{
		runID:  runID,
		offset: fromOffset,
		flux:   a.flux,
		ws:     ws,
	}

	// Set read/write deadlines to detect dead connections
	ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(5 * time.Minute))
		return nil
	})

	// Start reading client messages (for ACKs and detach signals)
	go h.readLoop(a)

	// Stream events
	if err := h.streamEvents(r.Context(), a); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("Stream error", "run_id", runID, "error", err)
		h.sendError(fmt.Sprintf("stream error: %v", err))
	}
}

// streamEvents reads from flux and sends to the client.
func (h *streamHandler) streamEvents(ctx context.Context, server *Server) error {
	stream, err := h.flux.Follow(ctx, h.runID, h.offset)
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
				// Run ended normally
				return h.sendEnd(core.RunSucceeded, "")
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

// sendError sends an error frame and closes the connection.
func (h *streamHandler) sendError(errMsg string) error {
	_ = h.send(wire.NewErrorMessage(errMsg))
	return h.ws.Close()
}

