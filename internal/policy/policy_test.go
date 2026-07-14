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

func TestPerCurrencyOverrideRatio(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000, Overrides: map[string]Override{"TRY": {Ratio: 1.0, CapUSD: 50_000}}}
	if r := p.EffectiveRatio("TRY"); r != 1.0 {
		t.Fatalf("TRY ratio = %v, want 1.0", r)
	}
	if c := p.EffectiveCap("TRY"); c != 50_000 {
		t.Fatalf("TRY cap = %v, want 50000", c)
	}
	if r := p.EffectiveRatio("EUR"); r != 0.90 {
		t.Fatalf("EUR ratio = %v, want 0.90 (default)", r)
	}
}

func TestDecideBlockedByCap(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	exp := &domain.Exposure{Currency: "EUR", NetAmount: 2_000_000, HedgeCoverage: 0}
	dec := p.Decide("EUR", exp)
	if !dec.BlockedByCap {
		t.Fatal("expected BlockedByCap true")
	}
	if math.Abs(dec.Notional-2_000_000) > 1e-6 {
		t.Fatalf("notional = %v, want 2000000", dec.Notional)
	}
	if dec.TenorHint != domain.TenorSpot {
		t.Fatalf("tenor = %v, want spot", dec.TenorHint)
	}
}

func TestBreachDetection(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	if !p.Breach("EUR", &domain.Exposure{Currency: "EUR", NetAmount: 200_000, HedgeCoverage: 0}) {
		t.Fatal("expected breach")
	}
	if p.Breach("EUR", &domain.Exposure{Currency: "EUR", NetAmount: 50_000, HedgeCoverage: 0}) {
		t.Fatal("expected no breach")
	}
	if p.Breach("EUR", nil) {
		t.Fatal("nil exposure should not breach")
	}
}

func TestSlippageTuningWidensRatio(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000, SlippageAlertBPS: 5, WideningStep: 0.05}
	if r := p.EffectiveRatio("EUR"); r != 0.90 {
		t.Fatalf("baseline ratio = %v, want 0.90", r)
	}
	p.ApplySlippageTuning("EUR", 10) // above alert threshold
	if r := p.EffectiveRatio("EUR"); math.Abs(r-0.95) > 1e-9 {
		t.Fatalf("tuned ratio = %v, want 0.95", r)
	}
	p.ApplySlippageTuning("EUR", 2) // below threshold
	if r := p.EffectiveRatio("EUR"); math.Abs(r-0.90) > 1e-9 {
		t.Fatalf("untuned ratio = %v, want 0.90", r)
	}
}

func TestParseOverridesEnv(t *testing.T) {
	t.Setenv("HEDGE_OVERRIDES", "TRY:1.0:50000; BRL:0.95:100000 ; bad entry")
	p := New()
	if p.EffectiveRatio("TRY") != 1.0 {
		t.Fatalf("TRY ratio = %v, want 1.0", p.EffectiveRatio("TRY"))
	}
	if p.EffectiveCap("BRL") != 100_000 {
		t.Fatalf("BRL cap = %v, want 100000", p.EffectiveCap("BRL"))
	}
}