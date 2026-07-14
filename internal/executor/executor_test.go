package executor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestBankAdapterFallbackQuote(t *testing.T) {
	b := NewBankAdapter(1.10)
	q, err := b.Quote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Rate != 1.10 {
		t.Fatalf("rate = %v, want 1.10", q.Rate)
	}
	if q.Venue != "bank" {
		t.Fatalf("venue = %q", q.Venue)
	}
}

func TestBankAdapterFallbackSubmit(t *testing.T) {
	b := NewBankAdapter(1.10)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, QuotedRate: 1.10, Tenor: domain.TenorSpot}
	fills, err := b.Submit(context.Background(), h)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills = %d", len(fills))
	}
	if fills[0].Venue != "bank" {
		t.Fatalf("venue = %q", fills[0].Venue)
	}
	if fills[0].VenueTradeID == "" {
		t.Fatal("venue trade id should be set")
	}
}

func TestVenueAdapterFallbackSubmit(t *testing.T) {
	v := NewVenueAdapter(1.10, 5) // 5 bps slippage
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, QuotedRate: 1.10, Tenor: domain.TenorSpot}
	fills, err := v.Submit(context.Background(), h)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := 1.10 * (1 + 5.0/10_000.0)
	if absDiffFloat(fills[0].Price, want) > 1e-9 {
		t.Fatalf("price = %v, want %v", fills[0].Price, want)
	}
	if fills[0].Venue != "venue" {
		t.Fatalf("venue = %q", fills[0].Venue)
	}
}

// fakeExec is a test Executor with controllable quotes and submits.
type fakeExec struct {
	name      string
	rate      float64
	liquidity float64
	costBPS   float64
	fail      bool
	submits   atomic.Int64
}

func (f *fakeExec) Name() string { return f.name }
func (f *fakeExec) Quote(ctx context.Context, ccy string, n float64, tenor string) (Quote, error) {
	if f.fail {
		return Quote{}, ErrQuoteFailed
	}
	return Quote{Venue: f.name, Rate: f.rate, Liquidity: f.liquidity, CostBPS: f.costBPS, Tenor: tenor, ExpiresAt: time.Now().Add(time.Second)}, nil
}
func (f *fakeExec) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	if f.fail {
		return nil, ErrSubmitFailed
	}
	f.submits.Add(1)
	amount := h.Notional
	if f.liquidity > 0 && f.liquidity < amount {
		amount = f.liquidity
	}
	return []domain.Fill{{
		HedgeID:      h.ID,
		Venue:        f.name,
		VenueTradeID: f.name + "-trade-" + h.ID,
		Price:        f.rate,
		Amount:       amount,
		Timestamp:    time.Now().UTC(),
	}}, nil
}
func (f *fakeExec) Cancel(ctx context.Context, id string) error { return nil }

func TestRouterBestQuoteByEffectiveRate(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000, costBPS: 1}
	b := &fakeExec{name: "b", rate: 1.10, liquidity: 1_000_000, costBPS: 0.2} // lower cost -> better
	r := NewRouter(a, b)
	q, ex, err := r.BestQuote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("bestquote: %v", err)
	}
	if ex.Name() != "b" {
		t.Fatalf("best venue = %q, want b (lower cost)", ex.Name())
	}
	if q.Venue != "b" {
		t.Fatalf("quote venue = %q", q.Venue)
	}
}

func TestRouterBestQuoteTieBreakByLiquidity(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 500_000, costBPS: 0}
	b := &fakeExec{name: "b", rate: 1.10, liquidity: 1_000_000, costBPS: 0}
	r := NewRouter(a, b)
	_, ex, err := r.BestQuote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("bestquote: %v", err)
	}
	if ex.Name() != "b" {
		t.Fatalf("best venue = %q, want b (higher liquidity)", ex.Name())
	}
}

func TestRouterAllFail(t *testing.T) {
	a := &fakeExec{name: "a", fail: true}
	r := NewRouter(a)
	_, _, err := r.BestQuote(context.Background(), "EUR", 100, "spot")
	if !errors.Is(err, ErrQuoteFailed) {
		t.Fatalf("err = %v, want ErrQuoteFailed", err)
	}
}

func TestRouterRouteAndExecuteSingle(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000, costBPS: 0}
	r := NewRouter(a)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, Tenor: domain.TenorSpot}
	fills, err := r.RouteAndExecute(context.Background(), h)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills = %d", len(fills))
	}
	if h.QuotedRate != 1.10 {
		t.Fatalf("quoted rate = %v, want 1.10", h.QuotedRate)
	}
	if fills[0].Venue != "a" {
		t.Fatalf("venue = %q", fills[0].Venue)
	}
}

func TestRouterRouteAndExecuteSplit(t *testing.T) {
	// Two venues: each can only fill 60k of a 100k request -> must split.
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 60_000, costBPS: 0.1}
	b := &fakeExec{name: "b", rate: 1.11, liquidity: 60_000, costBPS: 0}
	r := NewRouter(a, b)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, Tenor: domain.TenorSpot}
	fills, err := r.RouteAndExecute(context.Background(), h)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(fills) < 2 {
		t.Fatalf("expected split into >=2 fills, got %d", len(fills))
	}
	venues := map[string]bool{}
	var totalAmt float64
	for _, f := range fills {
		venues[f.Venue] = true
		totalAmt += f.Amount
	}
	if len(venues) < 2 {
		t.Fatalf("expected >=2 venues, got %v", venues)
	}
	if totalAmt != 100_000 {
		t.Fatalf("total filled = %v, want 100000", totalAmt)
	}
}

func TestLatencyExecutorSLO(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000}
	le := NewLatencyExecutor(a, 500*time.Millisecond)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, QuotedRate: 1.10, Tenor: domain.TenorSpot}
	if _, err := le.Submit(context.Background(), h); err != nil {
		t.Fatalf("submit: %v", err)
	}
	count, exceeded, max := le.Stats()
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	if exceeded != 0 {
		t.Fatalf("exceeded = %d, want 0", exceeded)
	}
	if max <= 0 {
		t.Fatalf("max = %v", max)
	}
}

func TestLatencyExecutorDetectsExceed(t *testing.T) {
	// A fake executor that sleeps 20ms; SLO target 5ms -> exceeded.
	a := &slowExec{inner: &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000}, delay: 20 * time.Millisecond}
	le := NewLatencyExecutor(a, 5*time.Millisecond)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100, QuotedRate: 1.10, Tenor: domain.TenorSpot}
	if _, err := le.Submit(context.Background(), h); err != nil {
		t.Fatalf("submit: %v", err)
	}
	_, exceeded, _ := le.Stats()
	if exceeded != 1 {
		t.Fatalf("exceeded = %d, want 1", exceeded)
	}
}

type slowExec struct {
	inner Executor
	delay time.Duration
}

func (s *slowExec) Name() string { return s.inner.Name() }
func (s *slowExec) Quote(ctx context.Context, ccy string, n float64, tenor string) (Quote, error) {
	return s.inner.Quote(ctx, ccy, n, tenor)
}
func (s *slowExec) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	time.Sleep(s.delay)
	return s.inner.Submit(ctx, h)
}
func (s *slowExec) Cancel(ctx context.Context, id string) error { return s.inner.Cancel(ctx, id) }

func absDiffFloat(a, b float64) float64 {
	if a < b {
		return b - a
	}
	return a - b
}