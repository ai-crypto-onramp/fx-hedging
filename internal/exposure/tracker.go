package exposure

import (
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Tracker is a thread-safe in-memory per-currency exposure aggregator.
// Hedge coverage is the sum of executed hedge notionals for a currency;
// open amount is net exposure minus hedge coverage.
//
// Ingestion is idempotent on event id: AddEvent with a previously seen id
// is a no-op and reports applied=false, preventing double counting on
// replay. Subscribers registered via Subscribe receive a copy of every
// exposure snapshot produced by an update.
type Tracker struct {
	mu        sync.RWMutex
	net       map[string]float64
	coverage  map[string]float64
	updatedAt map[string]time.Time
	seen      map[string]struct{}
	subs      map[chan *domain.Exposure]struct{}
}

// New returns an empty in-memory exposure tracker.
func New() *Tracker {
	return &Tracker{
		net:       make(map[string]float64),
		coverage:  make(map[string]float64),
		updatedAt: make(map[string]time.Time),
		seen:      make(map[string]struct{}),
		subs:      make(map[chan *domain.Exposure]struct{}),
	}
}

// AddExposure applies a signed delta to the net exposure for currency.
// Positive amounts increase a long position; negative amounts decrease it
// or create a short position.
func (t *Tracker) AddExposure(currency string, amount float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.applyDelta(currency, amount)
}

// AddEvent applies an idempotent exposure event. If the event id has been
// seen before, it is a no-op and applied is false. Otherwise the delta is
// applied and applied is true. Empty event id is always applied (no
// idempotency guard).
func (t *Tracker) AddEvent(e domain.ExposureEvent) (applied bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e.EventID != "" {
		if _, ok := t.seen[e.EventID]; ok {
			return false
		}
		t.seen[e.EventID] = struct{}{}
	}
	t.applyDelta(e.Currency, e.Amount)
	return true
}

// applyDelta mutates net/updatedAt and notifies subscribers. Caller holds
// the write lock.
func (t *Tracker) applyDelta(currency string, amount float64) {
	t.net[currency] += amount
	if t.updatedAt == nil {
		t.updatedAt = map[string]time.Time{}
	}
	t.updatedAt[currency] = time.Now().UTC()
	snap := t.snapshotLocked(currency)
	for ch := range t.subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// AddCoverage applies a signed delta to the hedge coverage for currency.
// Executed hedges add coverage; failed/cancelled hedges should not.
func (t *Tracker) AddCoverage(currency string, amount float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.coverage[currency] += amount
	t.updatedAt[currency] = time.Now().UTC()
	snap := t.snapshotLocked(currency)
	for ch := range t.subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// Subscribe registers a channel that receives a copy of every exposure
// snapshot produced by an update. The returned channel is buffered; slow
// consumers drop snapshots (non-blocking). The caller owns the channel
// lifecycle; call Unsubscribe to stop sending.
func (t *Tracker) Subscribe(ch chan *domain.Exposure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.subs[ch] = struct{}{}
}

// Unsubscribe removes a previously registered channel.
func (t *Tracker) Unsubscribe(ch chan *domain.Exposure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.subs, ch)
}

// Seen reports whether eventID has been ingested already.
func (t *Tracker) Seen(eventID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.seen[eventID]
	return ok
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

// snapshotLocked builds an exposure snapshot for currency. Caller holds lock.
func (t *Tracker) snapshotLocked(currency string) *domain.Exposure {
	updated := t.updatedAt[currency]
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return &domain.Exposure{
		Currency:      currency,
		NetAmount:     t.net[currency],
		HedgeCoverage: t.coverage[currency],
		OpenAmount:    t.net[currency] - t.coverage[currency],
		UpdatedAt:     updated,
	}
}