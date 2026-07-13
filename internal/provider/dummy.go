package provider

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/google/uuid"
)

// FXProvider is the execution interface for quoting and executing hedges.
// In this simplified service there is exactly one implementation: DummyFXProvider.
type FXProvider interface {
	// Quote returns a rate for hedging `notional` of `currency` with the given tenor.
	Quote(currency string, notional float64, tenor string) (rate float64, err error)
	// Execute executes the hedge and returns fills. A returned error marks the
	// hedge as failed by the caller.
	Execute(h *domain.Hedge) (fills []domain.Fill, err error)
}

// Sentinel errors returned by the dummy provider.
var (
	ErrQuoteFailed    = errors.New("fx quote failed")
	ErrExecuteFailed  = errors.New("fx execute failed")
)

// DummyFXProvider is the single dummy FX provider. It succeeds at every
// operation unless FailExecute is set or the DUMMY_FX_FAIL env var is "1".
// The quoted rate is configurable via the DUMMY_FX_RATE env var (default 1.10).
type DummyFXProvider struct {
	mu           sync.Mutex
	Rate         float64
	FailExecute  bool
	SlippageBPS  float64 // applied to executed rate vs quoted rate
	samples      []domain.SlippageSample
}

// NewDummy reads env config and returns a DummyFXProvider. Defaults:
//   DUMMY_FX_RATE      = 1.10
//   DUMMY_FX_FAIL      = "" (no failure)
//   DUMMY_FX_SLIPPAGE  = 0  (bps)
func NewDummy() *DummyFXProvider {
	rate := 1.10
	if v := os.Getenv("DUMMY_FX_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			rate = f
		}
	}
	slip := 0.0
	if v := os.Getenv("DUMMY_FX_SLIPPAGE_BPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			slip = f
		}
	}
	d := &DummyFXProvider{Rate: rate, SlippageBPS: slip}
	if os.Getenv("DUMMY_FX_FAIL") == "1" {
		d.FailExecute = true
	}
	return d
}

// Quote returns the configured dummy rate. The dummy provider never fails to
// quote unless FailQuote is set via FailExecute (treated the same in the dummy).
func (d *DummyFXProvider) Quote(currency string, notional float64, tenor string) (float64, error) {
	if d.FailExecute {
		return 0, ErrQuoteFailed
	}
	_ = currency
	_ = notional
	_ = tenor
	return d.Rate, nil
}

// Execute returns an immediate fill at the quoted rate plus configured slippage.
// If FailExecute is set it returns ErrExecuteFailed.
func (d *DummyFXProvider) Execute(h *domain.Hedge) ([]domain.Fill, error) {
	if d.FailExecute {
		return nil, ErrExecuteFailed
	}
	quoted := h.QuotedRate
	executed := quoted
	if d.SlippageBPS != 0 {
		executed = quoted * (1 + d.SlippageBPS/10_000.0)
	}
	fill := domain.Fill{
		HedgeID:      h.ID,
		VenueTradeID: "venue-" + uuid.NewString(),
		Price:        executed,
		Amount:       h.Notional,
		Timestamp:    time.Now().UTC(),
	}
	d.mu.Lock()
	d.samples = append(d.samples, domain.SlippageSample{
		Pair:         h.Currency + "USD",
		QuotedRate:   quoted,
		ExecutedRate: executed,
		SlippageBPS:  d.SlippageBPS,
		Timestamp:    fill.Timestamp,
	})
	d.mu.Unlock()
	return []domain.Fill{fill}, nil
}

// Samples returns a copy of recorded slippage samples.
func (d *DummyFXProvider) Samples() []domain.SlippageSample {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]domain.SlippageSample, len(d.samples))
	copy(out, d.samples)
	return out
}