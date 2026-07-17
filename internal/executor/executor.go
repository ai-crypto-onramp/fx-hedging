// Package executor defines the common hedge execution interface and
// adapters for the bank FX API (REST) and external FX venues. A Router
// selects the best executor by price/liquidity/cost and supports
// execution splits with per-venue fill tracking.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/google/uuid"
)

// Quote is a priced quote for hedging notional of a currency.
type Quote struct {
	Venue      string
	Rate       float64
	Liquidity  float64 // available notional at this rate
	CostBPS    float64 // explicit cost in bps (commission/fee)
	Tenor      string
	ExpiresAt  time.Time
}

// Executor is the common execution interface for bank FX APIs and external
// FX venues. Implementations must be safe for concurrent use.
type Executor interface {
	// Name returns the venue identifier (e.g. "bank", "venue-1").
	Name() string
	// Quote returns a rate for hedging notional of currency with tenor.
	Quote(ctx context.Context, currency string, notional float64, tenor string) (Quote, error)
	// Submit executes the hedge and returns fills.
	Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error)
	// Cancel attempts to cancel a previously submitted hedge. May be a
	// no-op for venues that fill immediately.
	Cancel(ctx context.Context, hedgeID string) error
}

// ErrQuoteFailed is returned when a venue cannot quote.
var ErrQuoteFailed = errors.New("executor: quote failed")

// ErrSubmitFailed is returned when a venue cannot submit.
var ErrSubmitFailed = errors.New("executor: submit failed")

// --- Bank FX API adapter (REST) ---

// BankAdapter is a REST adapter for the bank FX API. It uses BANK_API_URL
// and BANK_API_KEY from the environment. Requests are authenticated with an
// Authorization: Bearer <key> header; the key is never logged.
//
// In the absence of a reachable bank API (e.g. in tests/local dev), the
// adapter falls back to a deterministic reference rate so the service
// remains functional. Set BANK_API_URL to enable live calls.
type BankAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
	rate    float64
}

// NewBankAdapter reads BANK_API_URL and BANK_API_KEY and returns a
// BankAdapter. fallbackRate is used when no live URL is configured.
func NewBankAdapter(fallbackRate float64) *BankAdapter {
	return &BankAdapter{
		baseURL: os.Getenv("BANK_API_URL"),
		apiKey:  os.Getenv("BANK_API_KEY"),
		client:  &http.Client{Timeout: 2 * time.Second},
		rate:    fallbackRate,
	}
}

func (b *BankAdapter) Name() string { return "bank" }

func (b *BankAdapter) Quote(ctx context.Context, currency string, notional float64, tenor string) (Quote, error) {
	if b.baseURL == "" {
		return Quote{Venue: b.Name(), Rate: b.rate, Liquidity: notional, CostBPS: 0.5, Tenor: tenor, ExpiresAt: time.Now().Add(5 * time.Second)}, nil
	}
	return b.liveQuote(ctx, currency, notional, tenor)
}

func (b *BankAdapter) liveQuote(ctx context.Context, currency string, notional float64, tenor string) (Quote, error) {
	url := fmt.Sprintf("%s/v1/quote?ccy=%s&notional=%g&tenor=%s", b.baseURL, strings.ToUpper(currency), notional, tenor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Quote{}, fmt.Errorf("%w: status %d", ErrQuoteFailed, resp.StatusCode)
	}
	var body struct {
		Rate      float64 `json:"rate"`
		Liquidity float64 `json:"liquidity"`
		CostBPS   float64 `json:"cost_bps"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	return Quote{Venue: b.Name(), Rate: body.Rate, Liquidity: body.Liquidity, CostBPS: body.CostBPS, Tenor: tenor, ExpiresAt: time.Now().Add(5 * time.Second)}, nil
}

func (b *BankAdapter) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	if b.baseURL == "" {
		return []domain.Fill{{
			HedgeID:      h.ID,
			Venue:        b.Name(),
			VenueTradeID: func() string { v, _ := uuid.NewV7(); return "bank-" + v.String() }(),
			Price:        h.QuotedRate,
			Amount:       h.Notional,
			Timestamp:    time.Now().UTC(),
		}}, nil
	}
	return b.liveSubmit(ctx, h)
}

func (b *BankAdapter) liveSubmit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	body := fmt.Sprintf(`{"currency":"%s","notional":%g,"tenor":"%s","type":"%s","client_request_id":"%s","quoted_rate":%g}`,
		h.Currency, h.Notional, h.Tenor, h.Type, h.ClientRequestID, h.QuotedRate)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/hedges", strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	if h.ClientRequestID != "" {
		req.Header.Set("Idempotency-Key", h.ClientRequestID)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrSubmitFailed, resp.StatusCode)
	}
	var out struct {
		VenueTradeID string  `json:"venue_trade_id"`
		Price        float64 `json:"price"`
		Amount       float64 `json:"amount"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	return []domain.Fill{{
		HedgeID:      h.ID,
		Venue:        b.Name(),
		VenueTradeID: out.VenueTradeID,
		Price:        out.Price,
		Amount:       out.Amount,
		Timestamp:    time.Now().UTC(),
	}}, nil
}

func (b *BankAdapter) Cancel(ctx context.Context, hedgeID string) error {
	if b.baseURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.baseURL+"/v1/hedges/"+hedgeID, nil)
	if err != nil {
		return err
	}
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- External FX venue adapter ---

// VenueAdapter is a REST adapter for an external FX venue. It uses
// FX_VENUE_URL and FX_VENUE_API_KEY from the environment.
type VenueAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
	rate    float64
	slip    float64
}

// NewVenueAdapter reads FX_VENUE_URL and FX_VENUE_API_KEY and returns a
// VenueAdapter. fallbackRate is used when no live URL is configured.
// fallbackSlipBPS is applied to the executed rate vs the quoted rate when
// running in fallback mode.
func NewVenueAdapter(fallbackRate, fallbackSlipBPS float64) *VenueAdapter {
	return &VenueAdapter{
		baseURL: os.Getenv("FX_VENUE_URL"),
		apiKey:  os.Getenv("FX_VENUE_API_KEY"),
		client:  &http.Client{Timeout: time.Second},
		rate:    fallbackRate,
		slip:    fallbackSlipBPS,
	}
}

func (v *VenueAdapter) Name() string { return "venue" }

func (v *VenueAdapter) Quote(ctx context.Context, currency string, notional float64, tenor string) (Quote, error) {
	if v.baseURL == "" {
		return Quote{Venue: v.Name(), Rate: v.rate, Liquidity: notional, CostBPS: 0.2, Tenor: tenor, ExpiresAt: time.Now().Add(2 * time.Second)}, nil
	}
	url := fmt.Sprintf("%s/quote?pair=%sUSD&amount=%g&tenor=%s", v.baseURL, strings.ToUpper(currency), notional, tenor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	if v.apiKey != "" {
		req.Header.Set("X-API-Key", v.apiKey)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Quote{}, fmt.Errorf("%w: status %d", ErrQuoteFailed, resp.StatusCode)
	}
	var body struct {
		Rate      float64 `json:"rate"`
		Liquidity float64 `json:"liquidity"`
		CostBPS   float64 `json:"cost_bps"`
	}
	if err := decodeJSON(resp, &body); err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrQuoteFailed, err)
	}
	return Quote{Venue: v.Name(), Rate: body.Rate, Liquidity: body.Liquidity, CostBPS: body.CostBPS, Tenor: tenor, ExpiresAt: time.Now().Add(2 * time.Second)}, nil
}

func (v *VenueAdapter) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	if v.baseURL == "" {
		executed := h.QuotedRate
		if v.slip != 0 {
			executed = h.QuotedRate * (1 + v.slip/10_000.0)
		}
		return []domain.Fill{{
			HedgeID:      h.ID,
			Venue:        v.Name(),
			VenueTradeID: func() string { v, _ := uuid.NewV7(); return "venue-" + v.String() }(),
			Price:        executed,
			Amount:       h.Notional,
			Timestamp:    time.Now().UTC(),
		}}, nil
	}
	body := fmt.Sprintf(`{"pair":"%sUSD","amount":%g,"tenor":"%s","client_request_id":"%s"}`,
		h.Currency, h.Notional, h.Tenor, h.ClientRequestID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+"/orders", strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if v.apiKey != "" {
		req.Header.Set("X-API-Key", v.apiKey)
	}
	if h.ClientRequestID != "" {
		req.Header.Set("Idempotency-Key", h.ClientRequestID)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrSubmitFailed, resp.StatusCode)
	}
	var out struct {
		VenueTradeID string  `json:"venue_trade_id"`
		Price        float64 `json:"price"`
		Amount       float64 `json:"amount"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubmitFailed, err)
	}
	return []domain.Fill{{
		HedgeID:      h.ID,
		Venue:        v.Name(),
		VenueTradeID: out.VenueTradeID,
		Price:        out.Price,
		Amount:       out.Amount,
		Timestamp:    time.Now().UTC(),
	}}, nil
}

func (v *VenueAdapter) Cancel(ctx context.Context, hedgeID string) error {
	if v.baseURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, v.baseURL+"/orders/"+hedgeID, nil)
	if err != nil {
		return err
	}
	if v.apiKey != "" {
		req.Header.Set("X-API-Key", v.apiKey)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- Multi-venue router ---

// Router selects the best executor for a hedge by price/liquidity/cost and
// supports execution splits across venues when a single venue cannot fill
// the full notional. It is safe for concurrent use.
//
// Selection score = rate adjusted by costBPS: effectiveRate = rate * (1 +
// costBPS/10000) for buy-side hedging. The venue with the best (lowest for
// buy / highest for sell) effective rate wins; ties broken by higher
// liquidity. When the winning venue's liquidity is below the requested
// notional, the router splits the order across venues in descending
// liquidity order until the full notional is allocated.
type Router struct {
	executors []Executor
}

// NewRouter returns a Router over the given executors. At least one
// executor is required.
func NewRouter(execs ...Executor) *Router {
	if len(execs) == 0 {
		panic("executor: NewRouter requires at least one executor")
	}
	return &Router{executors: execs}
}

// Executors returns the router's executors (for testing/introspection).
func (r *Router) Executors() []Executor { return r.executors }

// BestQuote returns the best quote across all executors for the request.
// "Best" is the lowest effective rate (rate adjusted by costBPS) for
// buy-side hedging, with ties broken by higher liquidity.
func (r *Router) BestQuote(ctx context.Context, currency string, notional float64, tenor string) (Quote, Executor, error) {
	type res struct {
		q  Quote
		ex Executor
	}
	var results []res
	for _, ex := range r.executors {
		q, err := ex.Quote(ctx, currency, notional, tenor)
		if err != nil {
			continue
		}
		results = append(results, res{q, ex})
	}
	if len(results) == 0 {
		return Quote{}, nil, ErrQuoteFailed
	}
	best := results[0]
	for _, cand := range results[1:] {
		if betterQuote(cand.q, best.q) {
			best = cand
		}
	}
	return best.q, best.ex, nil
}

// betterQuote reports whether a is a better quote than b (lower effective
// rate, ties broken by higher liquidity).
func betterQuote(a, b Quote) bool {
	ea := a.Rate * (1 + a.CostBPS/10_000.0)
	eb := b.Rate * (1 + b.CostBPS/10_000.0)
	if diff := ea - eb; absDiff(diff) > 1e-12 {
		return diff < 0
	}
	return a.Liquidity > b.Liquidity
}

func absDiff(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// RouteAndExecute quotes all venues, selects the best, and submits. If the
// best venue's liquidity is below the notional, the order is split across
// venues (largest liquidity first) and each leg is submitted separately,
// producing per-venue fills with separate trade ids.
//
// The hedge's QuotedRate is set to the best quote's rate before submit.
// Each fill carries its venue and a unique venue trade id.
func (r *Router) RouteAndExecute(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	q, ex, err := r.BestQuote(ctx, h.Currency, h.Notional, string(h.Tenor))
	if err != nil {
		return nil, err
	}
	h.QuotedRate = q.Rate

	if q.Liquidity <= 0 || q.Liquidity >= h.Notional {
		fills, err := ex.Submit(ctx, h)
		if err != nil {
			return nil, err
		}
		for i := range fills {
			if fills[i].Venue == "" {
				fills[i].Venue = ex.Name()
			}
		}
		return fills, nil
	}
	return r.splitAndExecute(ctx, h)
}

// splitAndExecute splits the notional across venues in descending liquidity
// order, submitting each leg to the venue that quoted that leg's amount.
func (r *Router) splitAndExecute(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	type legQuote struct {
		q  Quote
		ex Executor
	}
	var lqs []legQuote
	for _, ex := range r.executors {
		q, err := ex.Quote(ctx, h.Currency, h.Notional, string(h.Tenor))
		if err != nil {
			continue
		}
		lqs = append(lqs, legQuote{q, ex})
	}
	if len(lqs) == 0 {
		return nil, ErrQuoteFailed
	}
	// Sort by liquidity descending.
	for i := 1; i < len(lqs); i++ {
		for j := i; j > 0 && lqs[j].q.Liquidity > lqs[j-1].q.Liquidity; j-- {
			lqs[j], lqs[j-1] = lqs[j-1], lqs[j]
		}
	}

	remaining := h.Notional
	var fills []domain.Fill
	for _, lq := range lqs {
		if remaining <= 1e-9 {
			break
		}
		alloc := lq.q.Liquidity
		if alloc <= 0 {
			continue
		}
		if alloc > remaining {
			alloc = remaining
		}
		leg := *h
		leg.Notional = alloc
		leg.QuotedRate = lq.q.Rate
		legFills, err := lq.ex.Submit(ctx, &leg)
		if err != nil {
			continue
		}
		for i := range legFills {
			if legFills[i].Venue == "" {
				legFills[i].Venue = lq.ex.Name()
			}
		}
		fills = append(fills, legFills...)
		remaining -= alloc
	}
	if remaining > 1e-9 {
		return fills, fmt.Errorf("executor: incomplete fill, %g remaining", remaining)
	}
	return fills, nil
}

// --- Latency-instrumented executor ---

// LatencyExecutor wraps an Executor and records the decision-to-fill
// latency of each Submit. The SLO target for spot orders is 500 ms.
type LatencyExecutor struct {
	inner    Executor
	target   time.Duration
	count    atomic.Int64
	exceeded atomic.Int64
	maxObs   atomic.Int64 // nanoseconds
}

// NewLatencyExecutor wraps ex and instruments Submit against target.
func NewLatencyExecutor(ex Executor, target time.Duration) *LatencyExecutor {
	if target <= 0 {
		target = 500 * time.Millisecond
	}
	return &LatencyExecutor{inner: ex, target: target}
}

func (l *LatencyExecutor) Name() string { return l.inner.Name() }

func (l *LatencyExecutor) Quote(ctx context.Context, currency string, notional float64, tenor string) (Quote, error) {
	return l.inner.Quote(ctx, currency, notional, tenor)
}

func (l *LatencyExecutor) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	start := time.Now()
	fills, err := l.inner.Submit(ctx, h)
	elapsed := time.Since(start)
	ns := elapsed.Nanoseconds()
	l.count.Add(1)
	for {
		cur := l.maxObs.Load()
		if ns <= cur {
			break
		}
		if l.maxObs.CompareAndSwap(cur, ns) {
			break
		}
	}
	if elapsed > l.target {
		l.exceeded.Add(1)
	}
	return fills, err
}

func (l *LatencyExecutor) Cancel(ctx context.Context, hedgeID string) error {
	return l.inner.Cancel(ctx, hedgeID)
}

// Stats returns (count, exceeded, max) latency statistics.
func (l *LatencyExecutor) Stats() (count, exceeded int64, max time.Duration) {
	return l.count.Load(), l.exceeded.Load(), time.Duration(l.maxObs.Load())
}

// decodeJSON decodes the JSON body of resp into out.
func decodeJSON(resp *http.Response, out interface{}) error {
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}