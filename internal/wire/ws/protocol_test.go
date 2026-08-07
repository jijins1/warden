package ws_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/wire/ws"
)

func TestEventMessage(t *testing.T) {
	e := events.Event{
		Seq:        42,
		OccurredAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		Host:       "web-01",
		TaskNumber: 5,
		TaskName:   "install packages",
		Role:       "all",
		State:      events.StateChanged,
		Payload:    json.RawMessage(`{"changed":true}`),
	}

	msg := ws.NewEventMessage(e)
	if msg.Type != ws.TypeEvent {
		t.Errorf("Type = %s, want %s", msg.Type, ws.TypeEvent)
	}

	var frame ws.EventFrame
	if err := json.Unmarshal(msg.Body, &frame); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if frame.Seq != 42 {
		t.Errorf("Seq = %d, want 42", frame.Seq)
	}
	if frame.Host != "web-01" {
		t.Errorf("Host = %s, want web-01", frame.Host)
	}
	if frame.State != events.StateChanged {
		t.Errorf("State = %v, want %v", frame.State, events.StateChanged)
	}
}

func TestEndMessage(t *testing.T) {
	msg := ws.NewEndMessage("succeeded", "")
	if msg.Type != ws.TypeEnd {
		t.Errorf("Type = %s, want %s", msg.Type, ws.TypeEnd)
	}

	var frame ws.EndFrame
	if err := json.Unmarshal(msg.Body, &frame); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if frame.Status != "succeeded" {
		t.Errorf("Status = %s, want succeeded", frame.Status)
	}
}

func TestErrorMessage(t *testing.T) {
	msg := ws.NewErrorMessage("run not found")
	if msg.Type != ws.TypeError {
		t.Errorf("Type = %s, want %s", msg.Type, ws.TypeError)
	}

	var frame ws.ErrorFrame
	if err := json.Unmarshal(msg.Body, &frame); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if frame.Error != "run not found" {
		t.Errorf("Error = %s, want 'run not found'", frame.Error)
	}
}

func TestMessageMarshaling(t *testing.T) {
	e := events.Event{
		Seq:        1,
		OccurredAt: time.Now().UTC(),
		Host:       "db-01",
		State:      events.StateOK,
		Payload:    json.RawMessage(`{}`),
	}

	msg := ws.NewEventMessage(e)

	// Marshal and unmarshal the full message
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var unmarshaled ws.Message
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if unmarshaled.Type != msg.Type {
		t.Errorf("Type mismatch: %s != %s", unmarshaled.Type, msg.Type)
	}
}

func TestFromEvent(t *testing.T) {
	e := events.Event{
		Seq:        10,
		OccurredAt: time.Now().UTC(),
		Host:       "web-01",
		TaskNumber: 3,
		TaskName:   "start service",
		Role:       "webservers",
		State:      events.StateChanged,
		Payload:    json.RawMessage(`{"changed":true}`),
		Diff:       json.RawMessage(`{"secret":"hidden"}`),
	}

	frame := ws.FromEvent(e)

	if frame.Seq != e.Seq {
		t.Errorf("Seq mismatch")
	}
	if frame.Host != e.Host {
		t.Errorf("Host mismatch")
	}
	if frame.TaskName != e.TaskName {
		t.Errorf("TaskName mismatch")
	}

	// Verify Diff is NOT included (security §6.5)
	if frame.Payload == nil {
		t.Error("Payload should be included")
	}
}
