package policy

import (
	"math"
	"testing"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.HedgeRatioTarget != 0.90 {
		t.Fatalf("ratio = %v, want 0.90", p.HedgeRatioTarget)
	}
	if p.MaxOpenExposureUSD != 1_000_000 {
		t.Fatalf("cap = %v, want 1000000", p.MaxOpenExposureUSD)
	}
}

func TestShouldHedgeNilExposure(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	ok, n := p.ShouldHedge("EUR", nil)
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeZeroExposure(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	ok, n := p.ShouldHedge("EUR", &domain.Exposure{Currency: "EUR", NetAmount: 0})
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeUnderTarget(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 90k already covered -> at target, no hedge needed.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 100_000, HedgeCoverage: 90_000}
	ok, n := p.ShouldHedge("EUR", exp)
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeBelowTarget(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, no coverage -> target 90k.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 100_000, HedgeCoverage: 0}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n-90_000) > 1e-6 {
		t.Fatalf("notional = %v, want 90000", n)
	}
}

func TestShouldHedgePartialCoverage(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 50k coverage -> need 40k more to reach 90k target.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 100_000, HedgeCoverage: 50_000}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n-40_000) > 1e-6 {
		t.Fatalf("notional = %v, want 40000", n)
	}
}

func TestShouldHedgeShortExposure(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// -100k net -> target -90k coverage.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: -100_000, HedgeCoverage: 0}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n-90_000) > 1e-6 {
		t.Fatalf("notional = %v, want 90000", n)
	}
}

func TestShouldHedgeCapBreach(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	// 2M net, no coverage -> open 2M > cap 100k, hedge full open.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 2_000_000, HedgeCoverage: 0}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n-2_000_000) > 1e-6 {
		t.Fatalf("notional = %v, want 2000000", n)
	}
}

func TestShouldHedgeOverCovered(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 95k coverage -> beyond target, no hedge.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 100_000, HedgeCoverage: 95_000}
	ok, n := p.ShouldHedge("EUR", exp)
	if ok || n != 0 {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}