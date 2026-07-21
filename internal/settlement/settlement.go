// Package settlement implements the per-currency settlement netting
// engine: offsetting settlement obligations across flows and hedges are
// reduced to a single net cash movement per currency.
package settlement

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Leg is a single settlement cash obligation leg for a currency. Positive
// Amount means the service receives that currency; negative means it pays.
type Leg struct {
	Currency string
	Amount   decimal.Decimal
	Source   string // "flow" or "hedge"
	At       time.Time
}

// Engine nets offsetting settlement legs per currency to a single net
// obligation per currency. It is safe for concurrent stateless use.
type Engine struct{}

// New returns an empty netting engine.
func New() *Engine { return &Engine{} }

// Net reduces the given legs to one net obligation per currency. Legs with
// the same currency are summed; currencies whose net is within 1e-9 of
// zero are dropped (fully offset). The returned obligations are sorted by
// currency code for deterministic output.
func (e *Engine) Net(legs []Leg) []domain.SettlementObligation {
	byCcy := map[string]decimal.Decimal{}
	countByCcy := map[string]int{}
	latestByCcy := map[string]time.Time{}
	for _, l := range legs {
		byCcy[l.Currency] = byCcy[l.Currency].Add(l.Amount)
		countByCcy[l.Currency]++
		if l.At.After(latestByCcy[l.Currency]) {
			latestByCcy[l.Currency] = l.At
		}
	}
	epsilon := decimal.NewFromFloat(1e-9)
	out := make([]domain.SettlementObligation, 0, len(byCcy))
	for ccy, amt := range byCcy {
		if amt.Abs().LessThan(epsilon) {
			continue
		}
		out = append(out, domain.SettlementObligation{
			Currency: ccy,
			Amount:   byCcy[ccy],
			Legs:     countByCcy[ccy],
			At:       latestByCcy[ccy],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// NetFromFlowsAndHedges is a convenience that builds legs from per-currency
// flow amounts (signed) and executed hedge notionals (signed opposite to
// the flow direction since hedges neutralize exposure). Hedge legs use
// the hedge's currency and negative notional (the hedge pays out on
// settlement), flow legs use the flow's currency and amount directly.
//
// The net result is the per-currency cash movement to settle.
func (e *Engine) NetFromFlowsAndHedges(flows []domain.Exposure, hedges []*domain.Hedge) []domain.SettlementObligation {
	legs := make([]Leg, 0, len(flows)+len(hedges))
	for _, f := range flows {
		if f.NetAmount.IsZero() {
			continue
		}
		legs = append(legs, Leg{Currency: f.Currency, Amount: f.NetAmount, Source: "flow", At: f.UpdatedAt})
	}
	for _, h := range hedges {
		if h.Status != domain.StatusExecuted {
			continue
		}
		// Hedge settlement is the opposite leg of the exposure: a hedge
		// that neutralizes a long exposure pays out the notional in the
		// exposure currency on settlement.
		amt := h.Notional.Neg()
		legs = append(legs, Leg{Currency: h.Currency, Amount: amt, Source: "hedge", At: h.UpdatedAt})
	}
	return e.Net(legs)
}
