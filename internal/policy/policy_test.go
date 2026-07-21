package policy

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func dInt(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

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
	if ok || !n.IsZero() {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeZeroExposure(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	ok, n := p.ShouldHedge("EUR", &domain.Exposure{Currency: "EUR", NetAmount: decimal.Zero})
	if ok || !n.IsZero() {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeUnderTarget(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 90k already covered -> at target, no hedge needed.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(100_000), HedgeCoverage: dInt(90_000)}
	ok, n := p.ShouldHedge("EUR", exp)
	if ok || !n.IsZero() {
		t.Fatalf("ok=%v n=%v, want false 0", ok, n)
	}
}

func TestShouldHedgeBelowTarget(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, no coverage -> target 90k.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(100_000), HedgeCoverage: decimal.Zero}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n.Sub(dInt(90_000)).InexactFloat64()) > 1e-6 {
		t.Fatalf("notional = %v, want 90000", n)
	}
}

func TestShouldHedgePartialCoverage(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 50k coverage -> need 40k more to reach 90k target.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(100_000), HedgeCoverage: dInt(50_000)}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n.Sub(dInt(40_000)).InexactFloat64()) > 1e-6 {
		t.Fatalf("notional = %v, want 40000", n)
	}
}

func TestShouldHedgeShortExposure(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// -100k net -> target -90k coverage.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(-100_000), HedgeCoverage: decimal.Zero}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n.Sub(dInt(90_000)).InexactFloat64()) > 1e-6 {
		t.Fatalf("notional = %v, want 90000", n)
	}
}

func TestShouldHedgeCapBreach(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	// 2M net, no coverage -> open 2M > cap 100k, hedge full open.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(2_000_000), HedgeCoverage: decimal.Zero}
	ok, n := p.ShouldHedge("EUR", exp)
	if !ok {
		t.Fatal("expected hedge")
	}
	if math.Abs(n.Sub(dInt(2_000_000)).InexactFloat64()) > 1e-6 {
		t.Fatalf("notional = %v, want 2000000", n)
	}
}

func TestShouldHedgeOverCovered(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	// 100k net, 95k coverage -> beyond target, no hedge.
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(100_000), HedgeCoverage: dInt(95_000)}
	ok, n := p.ShouldHedge("EUR", exp)
	if ok || !n.IsZero() {
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
	exp := &domain.Exposure{Currency: "EUR", NetAmount: dInt(2_000_000), HedgeCoverage: decimal.Zero}
	dec := p.Decide("EUR", exp)
	if !dec.BlockedByCap {
		t.Fatal("expected BlockedByCap true")
	}
	if math.Abs(dec.Notional.Sub(dInt(2_000_000)).InexactFloat64()) > 1e-6 {
		t.Fatalf("notional = %v, want 2000000", dec.Notional)
	}
	if dec.TenorHint != domain.TenorSpot {
		t.Fatalf("tenor = %v, want spot", dec.TenorHint)
	}
}

func TestBreachDetection(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	if !p.Breach("EUR", &domain.Exposure{Currency: "EUR", NetAmount: dInt(200_000), HedgeCoverage: decimal.Zero}) {
		t.Fatal("expected breach")
	}
	if p.Breach("EUR", &domain.Exposure{Currency: "EUR", NetAmount: dInt(50_000), HedgeCoverage: decimal.Zero}) {
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

func TestSetOverride(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000, Overrides: map[string]Override{}}
	p.SetOverride("eur", Override{Ratio: 0.75, CapUSD: 50_000})
	if r := p.EffectiveRatio("EUR"); r != 0.75 {
		t.Fatalf("EUR ratio = %v, want 0.75", r)
	}
	if c := p.EffectiveCap("EUR"); c != 50_000 {
		t.Fatalf("EUR cap = %v, want 50000", c)
	}
}

func TestSetOverrideNilMap(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
	p.SetOverride("TRY", Override{Ratio: 1.0, CapUSD: 25_000})
	if r := p.EffectiveRatio("TRY"); r != 1.0 {
		t.Fatalf("TRY ratio = %v", r)
	}
}

func TestNewEnvParsing(t *testing.T) {
	t.Setenv("HEDGE_RATIO_TARGET", "0.5")
	t.Setenv("MAX_OPEN_EXPOSURE_USD", "250000")
	t.Setenv("SLIPPAGE_ALERT_BPS", "12")
	p := New()
	if p.HedgeRatioTarget != 0.5 {
		t.Fatalf("ratio = %v", p.HedgeRatioTarget)
	}
	if p.MaxOpenExposureUSD != 250_000 {
		t.Fatalf("cap = %v", p.MaxOpenExposureUSD)
	}
	if p.SlippageAlertBPS != 12 {
		t.Fatalf("alert = %v", p.SlippageAlertBPS)
	}
}

func TestNewEnvInvalidValues(t *testing.T) {
	t.Setenv("HEDGE_RATIO_TARGET", "not-a-number")
	t.Setenv("MAX_OPEN_EXPOSURE_USD", "-1")
	t.Setenv("SLIPPAGE_ALERT_BPS", "bad")
	p := New()
	if p.HedgeRatioTarget != 0.90 {
		t.Fatalf("ratio = %v, want default 0.90", p.HedgeRatioTarget)
	}
	if p.MaxOpenExposureUSD != 1_000_000 {
		t.Fatalf("cap = %v, want default 1M", p.MaxOpenExposureUSD)
	}
	if p.SlippageAlertBPS != 5 {
		t.Fatalf("alert = %v, want default 5", p.SlippageAlertBPS)
	}
}

func TestEffectiveRatioClamped(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000, SlippageAlertBPS: 5, WideningStep: 0.5}
	p.ApplySlippageTuning("EUR", 100)
	if r := p.EffectiveRatio("EUR"); r != 1.0 {
		t.Fatalf("clamped ratio = %v, want 1.0", r)
	}
}

func TestBreachUsesOpenAmountWhenSet(t *testing.T) {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 100_000}
	// NetAmount and HedgeCoverage would imply small open, but OpenAmount is
	// honored when non-zero.
	if !p.Breach("EUR", &domain.Exposure{Currency: "EUR", NetAmount: dInt(100), HedgeCoverage: decimal.Zero, OpenAmount: dInt(500_000)}) {
		t.Fatal("expected breach using explicit OpenAmount")
	}
}
