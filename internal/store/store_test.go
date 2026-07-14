package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestCreateAndGetHedge(t *testing.T) {
	s := New()
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100, Status: domain.StatusExecuted}
	s.CreateHedge(h)

	got := s.GetHedge("h1")
	if got == nil {
		t.Fatal("expected hedge")
	}
	if got.ID != "h1" {
		t.Fatalf("id = %q", got.ID)
	}
	if s.GetHedge("missing") != nil {
		t.Fatal("expected nil for missing")
	}
}

func TestGetHedgeReturnsCopy(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{
		ID:     "h1",
		Status: domain.StatusPending,
		Fills:  []domain.Fill{{HedgeID: "h1", Price: 1.0}},
	})
	got := s.GetHedge("h1")
	got.Status = domain.StatusFailed
	got.Fills[0].Price = 9.9

	again := s.GetHedge("h1")
	if again.Status != domain.StatusPending {
		t.Fatalf("stored status mutated to %q", again.Status)
	}
	if again.Fills[0].Price != 1.0 {
		t.Fatalf("stored fill mutated to %v", again.Fills[0].Price)
	}
}

func TestUpdateHedgeNotFound(t *testing.T) {
	s := New()
	_, err := s.UpdateHedge("missing", func(*domain.Hedge) error { return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateHedgePropagatesError(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Status: domain.StatusPending})
	custom := errors.New("boom")
	_, err := s.UpdateHedge("h1", func(*domain.Hedge) error { return custom })
	if !errors.Is(err, custom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestHedgesByCurrency(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR", CreatedAt: time.UnixMilli(200)})
	s.CreateHedge(&domain.Hedge{ID: "h2", Currency: "EUR", CreatedAt: time.UnixMilli(100)})
	s.CreateHedge(&domain.Hedge{ID: "h3", Currency: "JPY", CreatedAt: time.UnixMilli(50)})

	got := s.HedgesByCurrency("EUR")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "h2" {
		t.Fatalf("first id = %q, want h2 (sorted by created_at)", got[0].ID)
	}
	if got[1].ID != "h1" {
		t.Fatalf("second id = %q, want h1", got[1].ID)
	}
	if len(s.HedgesByCurrency("USD")) != 0 {
		t.Fatal("USD should have no hedges")
	}
}

func TestAllHedges(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR", CreatedAt: time.UnixMilli(200)})
	s.CreateHedge(&domain.Hedge{ID: "h2", Currency: "JPY", CreatedAt: time.UnixMilli(100)})
	all := s.AllHedges()
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	if all[0].ID != "h2" {
		t.Fatalf("first = %q, want h2", all[0].ID)
	}
}

func TestSlippageSamples(t *testing.T) {
	s := New()
	t0 := time.UnixMilli(1000)
	t1 := time.UnixMilli(2000)
	t2 := time.UnixMilli(3000)
	s.AddSlippageSample(domain.SlippageSample{Pair: "EURUSD", SlippageBPS: 1, Timestamp: t0})
	s.AddSlippageSample(domain.SlippageSample{Pair: "JPYUSD", SlippageBPS: 2, Timestamp: t1})
	s.AddSlippageSample(domain.SlippageSample{Pair: "EURUSD", SlippageBPS: 3, Timestamp: t2})

	if got := s.SlippageSamples("EURUSD", time.Time{}, time.Time{}); len(got) != 2 {
		t.Fatalf("EURUSD len = %d, want 2", len(got))
	}
	if got := s.SlippageSamples("", time.Time{}, time.Time{}); len(got) != 3 {
		t.Fatalf("all len = %d, want 3", len(got))
	}
	if got := s.SlippageSamples("EURUSD", t1, time.Time{}); len(got) != 1 {
		t.Fatalf("from-filtered len = %d, want 1", len(got))
	}
	if got := s.SlippageSamples("EURUSD", time.Time{}, t1); len(got) != 1 {
		t.Fatalf("to-filtered len = %d, want 1", len(got))
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR", Status: domain.StatusPending})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.UpdateHedge("h1", func(h *domain.Hedge) error {
				h.Fills = append(h.Fills, domain.Fill{HedgeID: "h1"})
				return nil
			})
			_ = s.GetHedge("h1")
		}()
	}
	wg.Wait()
	if got := len(s.GetHedge("h1").Fills); got != 20 {
		t.Fatalf("fills = %d, want 20", got)
	}
}

func TestGetHedgeByClientRequest(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR", ClientRequestID: "req-1"})
	if got := s.GetHedgeByClientRequest("req-1"); got == nil || got.ID != "h1" {
		t.Fatalf("got = %v, want h1", got)
	}
	if s.GetHedgeByClientRequest("missing") != nil {
		t.Fatal("expected nil for missing request id")
	}
}

func TestHasFillIdempotency(t *testing.T) {
	s := New()
	s.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR"})
	_, _ = s.UpdateHedge("h1", func(h *domain.Hedge) error {
		h.Fills = append(h.Fills, domain.Fill{HedgeID: "h1", Venue: "bank", VenueTradeID: "vt-1"})
		return nil
	})
	if !s.HasFill("bank", "vt-1") {
		t.Fatal("expected HasFill true for recorded fill")
	}
	if s.HasFill("bank", "other") {
		t.Fatal("expected HasFill false for unknown trade id")
	}
	if s.HasFill("", "vt-1") {
		t.Fatal("expected HasFill false for empty venue")
	}
}

func TestExposureSnapshots(t *testing.T) {
	s := New()
	s.AppendExposureSnapshot(&domain.Exposure{Currency: "EUR", NetAmount: 100, UpdatedAt: time.UnixMilli(200)})
	s.AppendExposureSnapshot(&domain.Exposure{Currency: "JPY", NetAmount: 50, UpdatedAt: time.UnixMilli(100)})
	s.AppendExposureSnapshot(&domain.Exposure{Currency: "EUR", NetAmount: 200, UpdatedAt: time.UnixMilli(300)})
	got := s.ExposureSnapshots("EUR")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].UpdatedAt.UnixMilli() != 200 {
		t.Fatalf("first not sorted by UpdatedAt: %v", got[0].UpdatedAt)
	}
}