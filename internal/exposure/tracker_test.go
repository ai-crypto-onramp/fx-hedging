package exposure

import (
	"sync"
	"testing"

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