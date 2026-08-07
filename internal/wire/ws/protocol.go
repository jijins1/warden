package ws

import (
	"encoding/json"
	"time"

	"github.com/jijins1/warden/internal/events"
)

// Message is the framing for all WebSocket communication.
//
// Each frame has a Type that determines how to interpret Body.
// This design allows easy extension without breaking existing clients.
type Message struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

const (
	// TypeEvent carries a run event from the stream.
	TypeEvent = "event"
	// TypeEnd signals that the run has finished.
	TypeEnd = "end"
	// TypeError indicates the stream encountered an error.
	TypeError = "error"
	// TypeKeepAlive maintains the connection during idle periods.
	TypeKeepAlive = "keepalive"
)

// EventFrame wraps a single run event for transmission.
//
// It mirrors events.Event but omits Diff, which lives in a separate
// table with distinct access control (§6.5).
type EventFrame struct {
	Seq        int64           `json:"seq"`
	OccurredAt time.Time       `json:"occurred_at"`
	Host       string          `json:"host,omitempty"`
	TaskNumber int             `json:"task_number,omitempty"`
	TaskName   string          `json:"task_name,omitempty"`
	Role       string          `json:"role,omitempty"`
	State      events.State    `json:"state"`
	Payload    json.RawMessage `json:"payload"`
}

// FromEvent converts an events.Event to an EventFrame.
// Diff is intentionally omitted for security (§6.5).
func FromEvent(e events.Event) EventFrame {
	return EventFrame{
		Seq:        e.Seq,
		OccurredAt: e.OccurredAt,
		Host:       e.Host,
		TaskNumber: e.TaskNumber,
		TaskName:   e.TaskName,
		Role:       e.Role,
		State:      e.State,
		Payload:    e.Payload,
	}
}

// EndFrame signals that a run has terminated.
type EndFrame struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ErrorFrame carries an error that terminated the stream.
type ErrorFrame struct {
	Error string `json:"error"`
}

// KeepAliveFrame maintains the connection.
// Sent periodically during idle periods to prevent proxy/LB timeout.
type KeepAliveFrame struct {
	Timestamp time.Time `json:"timestamp"`
}

// NewEventMessage wraps an event for transmission.
func NewEventMessage(e events.Event) Message {
	frame := FromEvent(e)
	body, _ := json.Marshal(frame)
	return Message{Type: TypeEvent, Body: body}
}

// NewEndMessage signals run completion.
func NewEndMessage(status, reason string) Message {
	frame := EndFrame{Status: status, Reason: reason}
	body, _ := json.Marshal(frame)
	return Message{Type: TypeEnd, Body: body}
}

// NewErrorMessage wraps an error.
func NewErrorMessage(err string) Message {
	frame := ErrorFrame{Error: err}
	body, _ := json.Marshal(frame)
	return Message{Type: TypeError, Body: body}
}

// NewKeepAliveMessage creates a heartbeat frame.
func NewKeepAliveMessage() Message {
	frame := KeepAliveFrame{Timestamp: time.Now().UTC()}
	body, _ := json.Marshal(frame)
	return Message{Type: TypeKeepAlive, Body: body}
}
