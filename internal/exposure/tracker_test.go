package exposure

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestGetExposureMissing(t *testing.T) {
	tr := New()
	if tr.GetExposure("EUR") != nil {
		t.Fatal("expected nil for unknown currency")
	}
}

func TestAddExposureLongShort(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", 100_000)
	tr.AddExposure("EUR", -30_000)

	got := tr.GetExposure("EUR")
	if got == nil {
		t.Fatal("expected exposure")
	}
	if got.NetAmount != 70_000 {
		t.Fatalf("net = %v, want 70000", got.NetAmount)
	}
	if got.HedgeCoverage != 0 {
		t.Fatalf("coverage = %v, want 0", got.HedgeCoverage)
	}
	if got.OpenAmount != 70_000 {
		t.Fatalf("open = %v, want 70000", got.OpenAmount)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set")
	}
}

func TestCoverageReducesOpen(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", 100_000)
	tr.AddCoverage("EUR", 90_000)

	got := tr.GetExposure("EUR")
	if got.HedgeCoverage != 90_000 {
		t.Fatalf("coverage = %v, want 90000", got.HedgeCoverage)
	}
	if got.OpenAmount != 10_000 {
		t.Fatalf("open = %v, want 10000", got.OpenAmount)
	}
}

func TestShortExposure(t *testing.T) {
	tr := New()
	tr.AddExposure("JPY", -50_000)
	got := tr.GetExposure("JPY")
	if got.NetAmount != -50_000 {
		t.Fatalf("net = %v, want -50000", got.NetAmount)
	}
	if got.OpenAmount != -50_000 {
		t.Fatalf("open = %v, want -50000", got.OpenAmount)
	}
}

func TestAllExposures(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", 100_000)
	tr.AddExposure("JPY", -50_000)
	tr.AddCoverage("GBP", 10_000) // coverage only currency

	all := tr.AllExposures()
	seen := make(map[string]*domain.Exposure)
	for _, e := range all {
		seen[e.Currency] = e
	}
	if _, ok := seen["EUR"]; !ok {
		t.Fatal("EUR missing")
	}
	if _, ok := seen["JPY"]; !ok {
		t.Fatal("JPY missing")
	}
	if _, ok := seen["GBP"]; !ok {
		t.Fatal("GBP missing (coverage only)")
	}
	if seen["EUR"].OpenAmount != 100_000 {
		t.Fatalf("EUR open = %v, want 100000", seen["EUR"].OpenAmount)
	}
}

func TestTrackerConcurrent(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tr.AddExposure("EUR", float64(n))
			_ = tr.GetExposure("EUR")
		}(i)
	}
	wg.Wait()

	got := tr.GetExposure("EUR")
	wantSum := 0.0
	for i := 0; i < 50; i++ {
		wantSum += float64(i)
	}
	if got.NetAmount != wantSum {
		t.Fatalf("net = %v, want %v", got.NetAmount, wantSum)
	}
}

func TestAddEventIdempotent(t *testing.T) {
	tr := New()
	ev := domain.ExposureEvent{EventID: "e1", Currency: "EUR", Amount: 100_000}
	if !tr.AddEvent(ev) {
		t.Fatal("first AddEvent should apply")
	}
	if tr.AddEvent(ev) {
		t.Fatal("duplicate AddEvent should not apply")
	}
	got := tr.GetExposure("EUR")
	if got.NetAmount != 100_000 {
		t.Fatalf("net = %v, want 100000 (no double count)", got.NetAmount)
	}
	if !tr.Seen("e1") {
		t.Fatal("Seen should report e1")
	}
	if tr.Seen("missing") {
		t.Fatal("Seen should not report missing")
	}
}

func TestAddEventEmptyIDAlwaysApplied(t *testing.T) {
	tr := New()
	if !tr.AddEvent(domain.ExposureEvent{Currency: "EUR", Amount: 50}) {
		t.Fatal("empty id should apply")
	}
	if !tr.AddEvent(domain.ExposureEvent{Currency: "EUR", Amount: 50}) {
		t.Fatal("empty id should apply again (no guard)")
	}
	if got := tr.GetExposure("EUR"); got.NetAmount != 100 {
		t.Fatalf("net = %v, want 100", got.NetAmount)
	}
}

type captureSink struct {
	mu   sync.Mutex
	snaps []*domain.Exposure
}

func (c *captureSink) AppendExposureSnapshot(e *domain.Exposure) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snaps = append(c.snaps, e)
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.snaps)
}

func TestSnapshotterPersistsOnChange(t *testing.T) {
	tr := New()
	sink := &captureSink{}
	snap := NewSnapshotter(tr, sink, 10*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go snap.Run(ctx)

	tr.AddExposure("EUR", 100_000)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if sink.count() < 1 {
		t.Fatalf("expected at least 1 snapshot, got %d", sink.count())
	}
}

func TestSnapshotterPersistsOnTick(t *testing.T) {
	tr := New()
	sink := &captureSink{}
	snap := NewSnapshotter(tr, sink, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go snap.Run(ctx)

	tr.AddExposure("EUR", 100)
	time.Sleep(100 * time.Millisecond)
	cancel()
	if sink.count() < 2 {
		t.Fatalf("expected at least 2 snapshots (change + tick), got %d", sink.count())
	}
}