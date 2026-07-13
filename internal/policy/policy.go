package policy

import (
	"math"
	"os"
	"strconv"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Policy decides whether and how much to hedge based on a target hedge ratio
// and a hard cap on unhedged (open) exposure in USD-equivalent.
type Policy struct {
	HedgeRatioTarget  float64
	MaxOpenExposureUSD float64
}

// New reads HEDGE_RATIO_TARGET and MAX_OPEN_EXPOSURE_USD from env, with
// defaults 0.90 and 1_000_000.
func New() *Policy {
	p := &Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000}
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
	return p
}

// ShouldHedge reports whether a hedge is required for the given currency and
// exposure, and the notional (in the exposure currency) to hedge.
//
// The target is to cover HedgeRatioTarget of the net exposure. If the open
// (unhedged) amount is already within the cap and below the target, no hedge
// is needed. If the open amount exceeds the cap, hedge the full open amount
// to bring it back under the cap. The notional returned is non-negative and
// zero when no hedge is required.
func (p *Policy) ShouldHedge(currency string, exp *domain.Exposure) (bool, float64) {
	_ = currency
	if exp == nil {
		return false, 0
	}
	net := exp.NetAmount
	coverage := exp.HedgeCoverage
	open := net - coverage

	// No exposure to hedge.
	if math.Abs(net) < 1e-9 {
		return false, 0
	}

	// Target coverage amount (signed same direction as net).
	targetCoverage := net * p.HedgeRatioTarget
	// How much more coverage is needed to reach the target.
	needed := targetCoverage - coverage
	if net > 0 {
		if needed <= 0 {
			return false, 0
		}
	} else {
		if needed >= 0 {
			return false, 0
		}
	}

	// Enforce cap on absolute open exposure (USD-equivalent for simplicity).
	absOpen := math.Abs(open)
	if absOpen > p.MaxOpenExposureUSD {
		// Hedge the full open amount to get under the cap.
		needed = open
	}

	notional := math.Abs(needed)
	if notional < 1e-9 {
		return false, 0
	}
	return true, notional
}