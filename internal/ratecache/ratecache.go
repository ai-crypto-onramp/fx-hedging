// Package ratecache maintains a thread-safe cache of live FX rates fed by
// venue quote streams and bank API quotes. A staleness guard refuses to
// serve rates older than a configurable TTL.
package ratecache

import (
	"errors"
	"sync"
	"time"
)

// Rate is a cached live FX rate for a currency vs USD.
type Rate struct {
	Currency string
	Rate     float64
	Source   string
	At       time.Time
}

// Cache is a thread-safe in-memory rate cache with a staleness guard.
type Cache struct {
	mu      sync.RWMutex
	rates   map[string]Rate
	ttl     time.Duration
	reval   map[string]float64 // last revaluation rate per currency (cross-check)
}

// ErrStale is returned when the cached rate for a currency is older than TTL.
var ErrStale = errors.New("ratecache: rate stale")

// ErrNotFound is returned when no rate is cached for a currency.
var ErrNotFound = errors.New("ratecache: rate not found")

// New returns a Cache with the given staleness TTL. Zero TTL means no
// staleness guard (rates never expire).
func New(ttl time.Duration) *Cache {
	return &Cache{
		rates: make(map[string]Rate),
		ttl:   ttl,
		reval: make(map[string]float64),
	}
}

// Update records a fresh rate for a currency from a source (e.g. "bank",
// "venue"). Safe for concurrent use.
func (c *Cache) Update(currency string, rate float64, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rates[currency] = Rate{Currency: currency, Rate: rate, Source: source, At: time.Now().UTC()}
}

// Get returns the cached rate for a currency. It returns ErrStale if the
// rate's age exceeds the configured TTL, and ErrNotFound if no rate is
// cached. The returned Rate's Stale flag is set when age > TTL.
func (c *Cache) Get(currency string) (Rate, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rates[currency]
	if !ok {
		return Rate{}, ErrNotFound
	}
	if c.ttl > 0 && time.Since(r.At) > c.ttl {
		return r, ErrStale
	}
	return r, nil
}

// SetRevaluationRate records the rate used for revaluation P&L for a
// currency, so callers (e.g. GetLiveRate) can cross-check the live rate
// against the revaluation rate.
func (c *Cache) SetRevaluationRate(currency string, rate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reval[currency] = rate
}

// RevaluationRate returns the last revaluation rate for a currency, or 0
// if none has been set.
func (c *Cache) RevaluationRate(currency string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reval[currency]
}

// CrossCheck reports whether the live rate for a currency is consistent
// with the revaluation rate within tolerance bps. Returns false if no
// revaluation rate has been set.
func (c *Cache) CrossCheck(currency string, toleranceBPS float64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rv, ok := c.reval[currency]
	if !ok || rv == 0 {
		return false
	}
	r, ok := c.rates[currency]
	if !ok || r.Rate == 0 {
		return false
	}
	diff := r.Rate - rv
	if diff < 0 {
		diff = -diff
	}
	return (diff/rv)*10_000.0 <= toleranceBPS
}

// TTL returns the configured staleness TTL.
func (c *Cache) TTL() time.Duration { return c.ttl }