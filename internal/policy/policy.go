package policy

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Override is a per-currency hedge ratio and open-exposure cap override.
type Override struct {
	Ratio  float64 // 0..1; 0 means inherit default
	CapUSD float64 // 0 means inherit default
}

// Policy decides whether and how much to hedge based on a target hedge ratio
// and a hard cap on unhedged (open) exposure in USD-equivalent. Per-currency
// overrides take precedence over the default ratio/cap for emerging-market
// or low-liquidity currencies.
//
// SlippageTuning feeds aggregate slippage back into policy: currencies
// whose mean slippage exceeds SlippageAlertBPS have their effective hedge
// ratio widened (increased) by WideningStep, capped at 1.0, so the service
// hedges more of the exposure for high-slippage currencies (reducing
// repeated slippage cost on smaller, more frequent hedges).
type Policy struct {
	mu                 sync.RWMutex
	HedgeRatioTarget   float64
	MaxOpenExposureUSD float64
	Overrides          map[string]Override
	SlippageAlertBPS   float64
	WideningStep       float64
	tuning             map[string]float64 // currency -> mean slippage bps
}

// New reads HEDGE_RATIO_TARGET, MAX_OPEN_EXPOSURE_USD, SLIPPAGE_ALERT_BPS,
// and HEDGE_OVERRIDES from env, with defaults 0.90, 1_000_000, 5 bps, and no
// overrides.
//
// HEDGE_OVERRIDES is a semicolon-separated list of
// `CCY:ratio:capUSD` entries, e.g. `TRY:1.0:50000;BRL:0.95:100000`.
func New() *Policy {
	p := &Policy{
		HedgeRatioTarget:   0.90,
		MaxOpenExposureUSD: 1_000_000,
		Overrides:          map[string]Override{},
		SlippageAlertBPS:   5,
		WideningStep:       0.05,
		tuning:             map[string]float64{},
	}
	if v := os.Getenv("HEDGE_RATIO_TARGET"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			p.HedgeRatioTarget = f
		}
	}
	if v := os.Getenv("MAX_OPEN_EXPOSURE_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			p.MaxOpenExposureUSD = f
		}
	}
	if v := os.Getenv("SLIPPAGE_ALERT_BPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			p.SlippageAlertBPS = f
		}
	}
	if v := os.Getenv("HEDGE_OVERRIDES"); v != "" {
		p.Overrides = parseOverrides(v)
	}
	return p
}

func parseOverrides(v string) map[string]Override {
	out := map[string]Override{}
	for _, entry := range strings.Split(v, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) < 2 {
			continue
		}
		ccy := strings.ToUpper(strings.TrimSpace(parts[0]))
		ratio, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		cap := 0.0
		if len(parts) >= 3 {
			cap, _ = strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		}
		if ccy != "" && (ratio > 0 || cap > 0) {
			out[ccy] = Override{Ratio: ratio, CapUSD: cap}
		}
	}
	return out
}

// SetOverride installs or replaces a per-currency override.
func (p *Policy) SetOverride(currency string, o Override) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Overrides == nil {
		p.Overrides = map[string]Override{}
	}
	p.Overrides[strings.ToUpper(currency)] = o
}

// EffectiveRatio returns the hedge ratio that applies to currency, after
// per-currency overrides and slippage tuning.
func (p *Policy) EffectiveRatio(currency string) float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ratio := p.HedgeRatioTarget
	if ov, ok := p.Overrides[strings.ToUpper(currency)]; ok && ov.Ratio > 0 {
		ratio = ov.Ratio
	}
	if meanSlip, ok := p.tuning[strings.ToUpper(currency)]; ok && p.SlippageAlertBPS > 0 && meanSlip > p.SlippageAlertBPS {
		ratio += p.WideningStep
	}
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	return ratio
}

// EffectiveCap returns the open-exposure cap (USD) that applies to currency.
func (p *Policy) EffectiveCap(currency string) float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if ov, ok := p.Overrides[strings.ToUpper(currency)]; ok && ov.CapUSD > 0 {
		return ov.CapUSD
	}
	return p.MaxOpenExposureUSD
}

// ApplySlippageTuning updates the rolling mean slippage per currency used to
// widen the hedge ratio for high-slippage currencies.
func (p *Policy) ApplySlippageTuning(currency string, meanSlippageBPS float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tuning == nil {
		p.tuning = map[string]float64{}
	}
	p.tuning[strings.ToUpper(currency)] = meanSlippageBPS
}

// ShouldHedge reports whether a hedge is required for the given currency and
// exposure, and the notional (in the exposure currency) to hedge.
//
// The target is to cover EffectiveRatio(currency) of the net exposure. If
// the open (unhedged) amount is already within the cap and below the
// target, no hedge is needed. If the open amount exceeds the cap, hedge the
// full open amount to bring it back under the cap. The notional returned is
// non-negative and zero when no hedge is required.
func (p *Policy) ShouldHedge(currency string, exp *domain.Exposure) (bool, float64) {
	dec := p.Decide(currency, exp)
	if dec.BlockedByCap || dec.Notional > 0 {
		return dec.Notional > 0, dec.Notional
	}
	return false, 0
}

// Decide returns a full HedgeDecision for the given currency and exposure,
// including the target notional, a tenor hint (spot for the default case),
// and whether the decision is blocked by the open-exposure cap.
//
// "Blocked by cap" means the current open (unhedged) amount already exceeds
// the cap; the policy still requests a hedge to reduce it, but flags the
// breach so the caller can emit an alert and block new exposure-increasing
// flow. A nil exposure yields a zero decision.
func (p *Policy) Decide(currency string, exp *domain.Exposure) domain.HedgeDecision {
	if exp == nil {
		return domain.HedgeDecision{Currency: currency}
	}
	net := exp.NetAmount
	coverage := exp.HedgeCoverage
	open := net - coverage

	if math.Abs(net) < 1e-9 {
		return domain.HedgeDecision{Currency: currency, TenorHint: domain.TenorSpot}
	}

	ratio := p.EffectiveRatio(currency)
	cap := p.EffectiveCap(currency)

	targetCoverage := net * ratio
	needed := targetCoverage - coverage
	if net > 0 {
		if needed <= 0 {
			needed = 0
		}
	} else {
		if needed >= 0 {
			needed = 0
		}
	}

	absOpen := math.Abs(open)
	breached := absOpen > cap
	if breached {
		needed = open
	}

	notional := math.Abs(needed)
	if notional < 1e-9 {
		return domain.HedgeDecision{Currency: currency, TenorHint: domain.TenorSpot, BlockedByCap: breached, Reason: "no hedge needed"}
	}
	reason := ""
	if breached {
		reason = "open exposure exceeds cap; hedging full open amount"
	}
	return domain.HedgeDecision{
		Currency:     currency,
		Notional:     notional,
		TenorHint:    domain.TenorSpot,
		BlockedByCap: breached,
		Reason:       reason,
	}
}

// Breach reports whether the current open exposure for currency exceeds the
// effective cap. Used by the service to emit an alertable event and block
// new exposure-increasing flow.
func (p *Policy) Breach(currency string, exp *domain.Exposure) bool {
	if exp == nil {
		return false
	}
	cap := p.EffectiveCap(currency)
	open := exp.NetAmount - exp.HedgeCoverage
	if exp.OpenAmount != 0 {
		open = exp.OpenAmount
	}
	return math.Abs(open) > cap
}