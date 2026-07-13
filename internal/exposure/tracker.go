package exposure

import (
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Tracker is a thread-safe in-memory per-currency exposure aggregator.
// Hedge coverage is the sum of executed hedge notionals for a currency;
// open amount is net exposure minus hedge coverage.
type Tracker struct {
	mu          sync.RWMutex
	net         map[string]float64
	coverage    map[string]float64
	updatedAt   map[string]time.Time
}

// New returns an empty in-memory exposure tracker.
func New() *Tracker {
	return &Tracker{
		net:       make(map[string]float64),
		coverage:  make(map[string]float64),
		updatedAt: make(map[string]time.Time),
	}
}

// AddExposure applies a signed delta to the net exposure for currency.
// Positive amounts increase a long position; negative amounts decrease it
// or create a short position.
func (t *Tracker) AddExposure(currency string, amount float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.net[currency] += amount
	t.updatedAt[currency] = time.Now().UTC()
}

// AddCoverage applies a signed delta to the hedge coverage for currency.
// Executed hedges add coverage; failed/cancelled hedges should not.
func (t *Tracker) AddCoverage(currency string, amount float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.coverage[currency] += amount
	t.updatedAt[currency] = time.Now().UTC()
}

// GetExposure returns the current exposure view for currency, or nil if no
// exposure has ever been recorded for that currency.
func (t *Tracker) GetExposure(currency string) *domain.Exposure {
	t.mu.RLock()
	defer t.mu.RUnlock()
	net, ok := t.net[currency]
	if !ok {
		return nil
	}
	coverage := t.coverage[currency]
	open := net - coverage
	updated := t.updatedAt[currency]
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return &domain.Exposure{
		Currency:      currency,
		NetAmount:     net,
		HedgeCoverage: coverage,
		OpenAmount:    open,
		UpdatedAt:     updated,
	}
}

// AllExposures returns a snapshot of the exposure for every currency that has
// a non-zero net or non-zero coverage record.
func (t *Tracker) AllExposures() []*domain.Exposure {
	t.mu.RLock()
	defer t.mu.RUnlock()
	seen := make(map[string]struct{})
	out := make([]*domain.Exposure, 0)
	for ccy := range t.net {
		seen[ccy] = struct{}{}
		open := t.net[ccy] - t.coverage[ccy]
		out = append(out, &domain.Exposure{
			Currency:      ccy,
			NetAmount:     t.net[ccy],
			HedgeCoverage: t.coverage[ccy],
			OpenAmount:    open,
			UpdatedAt:     t.updatedAt[ccy],
		})
	}
	for ccy := range t.coverage {
		if _, ok := seen[ccy]; ok {
			continue
		}
		seen[ccy] = struct{}{}
		out = append(out, &domain.Exposure{
			Currency:      ccy,
			NetAmount:     t.net[ccy],
			HedgeCoverage: t.coverage[ccy],
			OpenAmount:    t.net[ccy] - t.coverage[ccy],
			UpdatedAt:     t.updatedAt[ccy],
		})
	}
	return out
}