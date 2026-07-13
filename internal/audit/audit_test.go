package audit

import "testing"

func TestRecorderEmitAndEvents(t *testing.T) {
	r := NewRecorder()
	r.Emit(Event{Type: EventHedgeCreated, HedgeID: "h1", Currency: "EUR"})
	r.Emit(Event{Type: EventHedgeExecuted, HedgeID: "h1", Detail: "filled"})

	got := r.Events()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Type != EventHedgeCreated {
		t.Fatalf("first type = %q", got[0].Type)
	}
	if got[1].Detail != "filled" {
		t.Fatalf("second detail = %q", got[1].Detail)
	}
	if got[1].At.IsZero() {
		t.Fatal("At should be set")
	}

	got[0].HedgeID = "mutated"
	again := r.Events()
	if again[0].HedgeID == "mutated" {
		t.Fatal("Events should return a copy")
	}
}

func TestNopSink(t *testing.T) {
	NopSink{}.Emit(Event{})
}