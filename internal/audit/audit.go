package audit

import (
	"sync"
	"time"
)

// EventType is the category of an FX-hedging audit event.
type EventType string

const (
	EventHedgeCreated  EventType = "hedge_created"
	EventHedgeExecuted EventType = "hedge_executed"
	EventHedgeFailed   EventType = "hedge_failed"
	EventExposureAdded EventType = "exposure_added"
)

// Event is an audit record emitted on each FX-hedging state change.
type Event struct {
	Type     EventType `json:"type"`
	HedgeID  string    `json:"hedge_id,omitempty"`
	Currency string    `json:"currency,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

// Sink receives audit events. Implementations must be safe for concurrent use.
type Sink interface {
	Emit(Event)
}

// Recorder is an in-memory Sink used in tests and local dev.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// NewRecorder returns an empty in-memory audit recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Emit records the event.
func (r *Recorder) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	r.events = append(r.events, e)
}

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// NopSink discards all events.
type NopSink struct{}

func (NopSink) Emit(Event) {}