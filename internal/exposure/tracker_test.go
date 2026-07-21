package exposure

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func dInt(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func TestGetExposureMissing(t *testing.T) {
	tr := New()
	if tr.GetExposure("EUR") != nil {
		t.Fatal("expected nil for unknown currency")
	}
}

func TestAddExposureLongShort(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", dInt(100_000))
	tr.AddExposure("EUR", dInt(-30_000))

	got := tr.GetExposure("EUR")
	if got == nil {
		t.Fatal("expected exposure")
	}
	if !got.NetAmount.Equal(dInt(70_000)) {
		t.Fatalf("net = %v, want 70000", got.NetAmount)
	}
	if !got.HedgeCoverage.IsZero() {
		t.Fatalf("coverage = %v, want 0", got.HedgeCoverage)
	}
	if !got.OpenAmount.Equal(dInt(70_000)) {
		t.Fatalf("open = %v, want 70000", got.OpenAmount)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set")
	}
}

func TestCoverageReducesOpen(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", dInt(100_000))
	tr.AddCoverage("EUR", dInt(90_000))

	got := tr.GetExposure("EUR")
	if !got.HedgeCoverage.Equal(dInt(90_000)) {
		t.Fatalf("coverage = %v, want 90000", got.HedgeCoverage)
	}
	if !got.OpenAmount.Equal(dInt(10_000)) {
		t.Fatalf("open = %v, want 10000", got.OpenAmount)
	}
}

func TestShortExposure(t *testing.T) {
	tr := New()
	tr.AddExposure("JPY", dInt(-50_000))
	got := tr.GetExposure("JPY")
	if !got.NetAmount.Equal(dInt(-50_000)) {
		t.Fatalf("net = %v, want -50000", got.NetAmount)
	}
	if !got.OpenAmount.Equal(dInt(-50_000)) {
		t.Fatalf("open = %v, want -50000", got.OpenAmount)
	}
}

func TestAllExposures(t *testing.T) {
	tr := New()
	tr.AddExposure("EUR", dInt(100_000))
	tr.AddExposure("JPY", dInt(-50_000))
	tr.AddCoverage("GBP", dInt(10_000)) // coverage only currency

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
	if !seen["EUR"].OpenAmount.Equal(dInt(100_000)) {
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
			tr.AddExposure("EUR", dInt(int64(n)))
			_ = tr.GetExposure("EUR")
		}(i)
	}
	wg.Wait()

	got := tr.GetExposure("EUR")
	wantSum := decimal.Zero
	for i := 0; i < 50; i++ {
		wantSum = wantSum.Add(dInt(int64(i)))
	}
	if !got.NetAmount.Equal(wantSum) {
		t.Fatalf("net = %v, want %v", got.NetAmount, wantSum)
	}
}

func TestAddEventIdempotent(t *testing.T) {
	tr := New()
	ev := domain.ExposureEvent{EventID: "e1", Currency: "EUR", Amount: dInt(100_000)}
	if !tr.AddEvent(ev) {
		t.Fatal("first AddEvent should apply")
	}
	if tr.AddEvent(ev) {
		t.Fatal("duplicate AddEvent should not apply")
	}
	got := tr.GetExposure("EUR")
	if !got.NetAmount.Equal(dInt(100_000)) {
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
	if !tr.AddEvent(domain.ExposureEvent{Currency: "EUR", Amount: dInt(50)}) {
		t.Fatal("empty id should apply")
	}
	if !tr.AddEvent(domain.ExposureEvent{Currency: "EUR", Amount: dInt(50)}) {
		t.Fatal("empty id should apply again (no guard)")
	}
	if got := tr.GetExposure("EUR"); !got.NetAmount.Equal(dInt(100)) {
		t.Fatalf("net = %v, want 100", got.NetAmount)
	}
}

type captureSink struct {
	mu    sync.Mutex
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

	tr.AddExposure("EUR", dInt(100_000))
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

	tr.AddExposure("EUR", dInt(100))
	time.Sleep(100 * time.Millisecond)
	cancel()
	if sink.count() < 2 {
		t.Fatalf("expected at least 2 snapshots (change + tick), got %d", sink.count())
	}
}

func TestSnapshotterStop(t *testing.T) {
	tr := New()
	sink := &captureSink{}
	snap := NewSnapshotter(tr, sink, 10*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	go snap.Run(ctx)
	// Stop the snapshotter: it should drain and return. We must cancel the
	// context to unblock Run, then Stop waits for stoppedCh to close.
	cancel()
	snap.Stop()
	// Stop is idempotent.
	snap.Stop()
}

func TestSnapshotterDefaultInterval(t *testing.T) {
	tr := New()
	sink := &captureSink{}
	snap := NewSnapshotter(tr, sink, 0)
	if snap.interval != time.Second {
		t.Fatalf("interval = %v, want 1s default", snap.interval)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	tr := New()
	ch := make(chan *domain.Exposure, 1)
	tr.Subscribe(ch)
	tr.AddExposure("EUR", dInt(100))
	select {
	case e := <-ch:
		if e.Currency != "EUR" {
			t.Fatalf("currency = %q", e.Currency)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected snapshot on subscribed channel")
	}
	tr.Unsubscribe(ch)
	tr.AddExposure("EUR", dInt(100))
	select {
	case <-ch:
		t.Fatal("should not receive after Unsubscribe")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGetExposureUpdatedZeroFallback(t *testing.T) {
	tr := New()
	// Directly manipulate to simulate a missing updatedAt entry.
	tr.mu.Lock()
	tr.net["EUR"] = dInt(100)
	tr.updatedAt["EUR"] = time.Time{}
	tr.mu.Unlock()
	got := tr.GetExposure("EUR")
	if got == nil {
		t.Fatal("expected exposure")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should fall back to now() when zero")
	}
}
