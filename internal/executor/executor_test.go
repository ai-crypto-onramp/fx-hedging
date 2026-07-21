package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func dInt(n int64) decimal.Decimal     { return decimal.NewFromInt(n) }
func dFloat(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

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
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), QuotedRate: dFloat(1.10), Tenor: domain.TenorSpot}
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
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), QuotedRate: dFloat(1.10), Tenor: domain.TenorSpot}
	fills, err := v.Submit(context.Background(), h)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := dFloat(1.10 * (1 + 5.0/10_000.0))
	if absDiffDecimal(fills[0].Price, want) > 1e-9 {
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
	if f.liquidity > 0 && dFloat(f.liquidity).LessThan(amount) {
		amount = dFloat(f.liquidity)
	}
	return []domain.Fill{{
		HedgeID:      h.ID,
		Venue:        f.name,
		VenueTradeID: f.name + "-trade-" + h.ID,
		Price:        dFloat(f.rate),
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
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot}
	fills, err := r.RouteAndExecute(context.Background(), h)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills = %d", len(fills))
	}
	if !h.QuotedRate.Equal(dFloat(1.10)) {
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
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot}
	fills, err := r.RouteAndExecute(context.Background(), h)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(fills) < 2 {
		t.Fatalf("expected split into >=2 fills, got %d", len(fills))
	}
	venues := map[string]bool{}
	totalAmt := decimal.Zero
	for _, f := range fills {
		venues[f.Venue] = true
		totalAmt = totalAmt.Add(f.Amount)
	}
	if len(venues) < 2 {
		t.Fatalf("expected >=2 venues, got %v", venues)
	}
	if !totalAmt.Equal(dInt(100_000)) {
		t.Fatalf("total filled = %v, want 100000", totalAmt)
	}
}

func TestLatencyExecutorSLO(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000}
	le := NewLatencyExecutor(a, 500*time.Millisecond)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), QuotedRate: dFloat(1.10), Tenor: domain.TenorSpot}
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
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), QuotedRate: dFloat(1.10), Tenor: domain.TenorSpot}
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

func absDiffDecimal(a, b decimal.Decimal) float64 {
	return a.Sub(b).Abs().InexactFloat64()
}

func TestBankAdapterLiveQuote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/quote" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("auth header = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate":      1.20,
			"liquidity": 500_000.0,
			"cost_bps":  0.3,
		})
	}))
	defer srv.Close()
	t.Setenv("BANK_API_URL", srv.URL)
	t.Setenv("BANK_API_KEY", "secret")
	b := NewBankAdapter(1.10)
	q, err := b.Quote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Rate != 1.20 {
		t.Fatalf("rate = %v, want 1.20", q.Rate)
	}
	if q.Liquidity != 500_000 {
		t.Fatalf("liquidity = %v", q.Liquidity)
	}
	if q.CostBPS != 0.3 {
		t.Fatalf("cost_bps = %v", q.CostBPS)
	}
}

func TestBankAdapterLiveQuoteErrors(t *testing.T) {
	t.Run("bad status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		t.Setenv("BANK_API_URL", srv.URL)
		b := NewBankAdapter(1.10)
		_, err := b.Quote(context.Background(), "EUR", 100, "spot")
		if !errors.Is(err, ErrQuoteFailed) {
			t.Fatalf("err = %v, want ErrQuoteFailed", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not-json")
		}))
		defer srv.Close()
		t.Setenv("BANK_API_URL", srv.URL)
		b := NewBankAdapter(1.10)
		_, err := b.Quote(context.Background(), "EUR", 100, "spot")
		if !errors.Is(err, ErrQuoteFailed) {
			t.Fatalf("err = %v, want ErrQuoteFailed", err)
		}
	})
}

func TestBankAdapterLiveSubmit(t *testing.T) {
	var gotBody string
	var idem, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hedges" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		auth = r.Header.Get("Authorization")
		idem = r.Header.Get("Idempotency-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"venue_trade_id": "bank-trade-1",
			"price":          1.10,
			"amount":         100_000.0,
		})
	}))
	defer srv.Close()
	t.Setenv("BANK_API_URL", srv.URL)
	t.Setenv("BANK_API_KEY", "secret")
	b := NewBankAdapter(1.10)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot, ClientRequestID: "req-1", QuotedRate: dFloat(1.10)}
	fills, err := b.Submit(context.Background(), h)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(fills) != 1 || fills[0].VenueTradeID != "bank-trade-1" {
		t.Fatalf("fills = %+v", fills)
	}
	if auth != "Bearer secret" {
		t.Fatalf("auth = %q", auth)
	}
	if idem != "req-1" {
		t.Fatalf("idempotency key = %q", idem)
	}
	if !strings.Contains(gotBody, "EUR") {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestBankAdapterLiveSubmitErrors(t *testing.T) {
	t.Run("bad status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		t.Setenv("BANK_API_URL", srv.URL)
		b := NewBankAdapter(1.10)
		_, err := b.Submit(context.Background(), &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), Tenor: domain.TenorSpot, QuotedRate: dFloat(1.10)})
		if !errors.Is(err, ErrSubmitFailed) {
			t.Fatalf("err = %v, want ErrSubmitFailed", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not-json")
		}))
		defer srv.Close()
		t.Setenv("BANK_API_URL", srv.URL)
		b := NewBankAdapter(1.10)
		_, err := b.Submit(context.Background(), &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), Tenor: domain.TenorSpot, QuotedRate: dFloat(1.10)})
		if !errors.Is(err, ErrSubmitFailed) {
			t.Fatalf("err = %v, want ErrSubmitFailed", err)
		}
	})
}

func TestBankAdapterCancel(t *testing.T) {
	t.Run("fallback no-op", func(t *testing.T) {
		b := NewBankAdapter(1.10)
		if err := b.Cancel(context.Background(), "h1"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	})
	t.Run("live", func(t *testing.T) {
		var path, auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		t.Setenv("BANK_API_URL", srv.URL)
		t.Setenv("BANK_API_KEY", "secret")
		b := NewBankAdapter(1.10)
		if err := b.Cancel(context.Background(), "h1"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if path != "/v1/hedges/h1" {
			t.Fatalf("path = %q", path)
		}
		if auth != "Bearer secret" {
			t.Fatalf("auth = %q", auth)
		}
	})
}

func TestVenueAdapterFallbackQuote(t *testing.T) {
	v := NewVenueAdapter(1.10, 5)
	q, err := v.Quote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Rate != 1.10 {
		t.Fatalf("rate = %v, want 1.10", q.Rate)
	}
	if q.Venue != "venue" {
		t.Fatalf("venue = %q", q.Venue)
	}
}

func TestVenueAdapterLiveQuote(t *testing.T) {
	var path, apikey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path + "?" + r.URL.RawQuery
		apikey = r.Header.Get("X-API-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rate":      1.20,
			"liquidity": 200_000.0,
			"cost_bps":  0.2,
		})
	}))
	defer srv.Close()
	t.Setenv("FX_VENUE_URL", srv.URL)
	t.Setenv("FX_VENUE_API_KEY", "k")
	v := NewVenueAdapter(1.10, 5)
	q, err := v.Quote(context.Background(), "EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if q.Rate != 1.20 {
		t.Fatalf("rate = %v", q.Rate)
	}
	if !strings.Contains(path, "EURUSD") {
		t.Fatalf("path = %q", path)
	}
	if apikey != "k" {
		t.Fatalf("api key = %q", apikey)
	}
}

func TestVenueAdapterLiveQuoteErrors(t *testing.T) {
	t.Run("bad status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		t.Setenv("FX_VENUE_URL", srv.URL)
		v := NewVenueAdapter(1.10, 5)
		_, err := v.Quote(context.Background(), "EUR", 100, "spot")
		if !errors.Is(err, ErrQuoteFailed) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not-json")
		}))
		defer srv.Close()
		t.Setenv("FX_VENUE_URL", srv.URL)
		v := NewVenueAdapter(1.10, 5)
		_, err := v.Quote(context.Background(), "EUR", 100, "spot")
		if !errors.Is(err, ErrQuoteFailed) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVenueAdapterLiveSubmit(t *testing.T) {
	var path, idem, apikey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		idem = r.Header.Get("Idempotency-Key")
		apikey = r.Header.Get("X-API-Key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"venue_trade_id": "v-trade-1",
			"price":          1.11,
			"amount":         100_000.0,
		})
	}))
	defer srv.Close()
	t.Setenv("FX_VENUE_URL", srv.URL)
	t.Setenv("FX_VENUE_API_KEY", "k")
	v := NewVenueAdapter(1.10, 5)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot, ClientRequestID: "req-9", QuotedRate: dFloat(1.10)}
	fills, err := v.Submit(context.Background(), h)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(fills) != 1 || fills[0].VenueTradeID != "v-trade-1" {
		t.Fatalf("fills = %+v", fills)
	}
	if path != "/orders" {
		t.Fatalf("path = %q", path)
	}
	if idem != "req-9" {
		t.Fatalf("idem = %q", idem)
	}
	if apikey != "k" {
		t.Fatalf("api key = %q", apikey)
	}
}

func TestVenueAdapterLiveSubmitErrors(t *testing.T) {
	t.Run("bad status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		t.Setenv("FX_VENUE_URL", srv.URL)
		v := NewVenueAdapter(1.10, 5)
		_, err := v.Submit(context.Background(), &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), Tenor: domain.TenorSpot, QuotedRate: dFloat(1.10)})
		if !errors.Is(err, ErrSubmitFailed) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not-json")
		}))
		defer srv.Close()
		t.Setenv("FX_VENUE_URL", srv.URL)
		v := NewVenueAdapter(1.10, 5)
		_, err := v.Submit(context.Background(), &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), Tenor: domain.TenorSpot, QuotedRate: dFloat(1.10)})
		if !errors.Is(err, ErrSubmitFailed) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVenueAdapterCancel(t *testing.T) {
	t.Run("fallback no-op", func(t *testing.T) {
		v := NewVenueAdapter(1.10, 5)
		if err := v.Cancel(context.Background(), "h1"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	})
	t.Run("live", func(t *testing.T) {
		var path, apikey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			apikey = r.Header.Get("X-API-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		t.Setenv("FX_VENUE_URL", srv.URL)
		t.Setenv("FX_VENUE_API_KEY", "k")
		v := NewVenueAdapter(1.10, 5)
		if err := v.Cancel(context.Background(), "h1"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if path != "/orders/h1" {
			t.Fatalf("path = %q", path)
		}
		if apikey != "k" {
			t.Fatalf("api key = %q", apikey)
		}
	})
}

func TestNewRouterPanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for no executors")
		}
	}()
	_ = NewRouter()
}

func TestRouterExecutors(t *testing.T) {
	a := &fakeExec{name: "a"}
	r := NewRouter(a)
	if len(r.Executors()) != 1 || r.Executors()[0].Name() != "a" {
		t.Fatalf("executors = %+v", r.Executors())
	}
}

func TestRouterSplitIncomplete(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 10, costBPS: 0}
	r := NewRouter(a)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot}
	_, err := r.RouteAndExecute(context.Background(), h)
	if err == nil {
		t.Fatal("expected incomplete fill error")
	}
}

func TestRouterRouteAndExecuteSubmitError(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000, costBPS: 0, fail: true}
	r := NewRouter(a)
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100_000), Tenor: domain.TenorSpot}
	_, err := r.RouteAndExecute(context.Background(), h)
	if err == nil {
		t.Fatal("expected error when submit fails")
	}
}

func TestLatencyExecutorNameQuoteCancel(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000}
	le := NewLatencyExecutor(a, 0) // zero -> default 500ms
	if le.Name() != "a" {
		t.Fatalf("name = %q", le.Name())
	}
	q, err := le.Quote(context.Background(), "EUR", 100, "spot")
	if err != nil || q.Rate != 1.10 {
		t.Fatalf("quote = %+v err = %v", q, err)
	}
	if err := le.Cancel(context.Background(), "h1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestLatencyExecutorSubmitError(t *testing.T) {
	a := &fakeExec{name: "a", rate: 1.10, liquidity: 1_000_000, fail: true}
	le := NewLatencyExecutor(a, time.Second)
	_, err := le.Submit(context.Background(), &domain.Hedge{ID: "h1", Currency: "EUR", Notional: dInt(100), QuotedRate: dFloat(1.10), Tenor: domain.TenorSpot})
	if err == nil {
		t.Fatal("expected error from inner submit")
	}
	count, _, _ := le.Stats()
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestDecodeJSON(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"x":1}`)),
	}
	var out struct {
		X int `json:"x"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.X != 1 {
		t.Fatalf("x = %v", out.X)
	}
}

func TestDecodeJSONLimit(t *testing.T) {
	big := strings.Repeat("a", 2<<20)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(big))}
	var out map[string]int
	if err := decodeJSON(resp, &out); err == nil {
		t.Fatal("expected error for oversized body")
	}
}
