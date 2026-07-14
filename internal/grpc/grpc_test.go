package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/executor"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/ratecache"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	fxpb "github.com/ai-crypto-onramp/fx-hedging/proto/fx/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type fakeExec struct {
	name string
	rate float64
	liq  float64
}

func (f *fakeExec) Name() string { return f.name }
func (f *fakeExec) Quote(ctx context.Context, ccy string, n float64, tenor string) (executor.Quote, error) {
	return executor.Quote{Venue: f.name, Rate: f.rate, Liquidity: f.liq, CostBPS: 0, Tenor: tenor, ExpiresAt: time.Now().Add(time.Second)}, nil
}
func (f *fakeExec) Submit(ctx context.Context, h *domain.Hedge) ([]domain.Fill, error) {
	return []domain.Fill{{HedgeID: h.ID, Venue: f.name, VenueTradeID: f.name + "-" + h.ID, Price: f.rate, Amount: h.Notional, Timestamp: time.Now().UTC()}}, nil
}
func (f *fakeExec) Cancel(ctx context.Context, id string) error { return nil }

func newTestServices(t *testing.T) *Services {
	t.Helper()
	tr := exposure.New()
	st := store.New()
	pol := &policy.Policy{HedgeRatioTarget: 0.90, MaxOpenExposureUSD: 1_000_000, Overrides: map[string]policy.Override{}, SlippageAlertBPS: 5, WideningStep: 0.05}
	cache := ratecache.New(time.Second)
	rtr := executor.NewRouter(&fakeExec{name: "bank", rate: 1.10, liq: 1_000_000})
	return &Services{Tracker: tr, Cache: cache, Policy: pol, Router: rtr, Store: st}
}

func startGRPC(t *testing.T, s *Services) (fxpb.FXClient, func()) {
	t.Helper()
	srv, lis, err := NewServer(s, "0")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return fxpb.NewFXClient(conn), func() { conn.Close(); srv.Stop() }
}

func TestGetLiveRate(t *testing.T) {
	s := newTestServices(t)
	s.Cache.Update("EUR", 1.10, "bank")
	s.Cache.SetRevaluationRate("EUR", 1.10)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.GetLiveRate(context.Background(), &fxpb.GetLiveRateRequest{Currency: "EUR"})
	if err != nil {
		t.Fatalf("get live rate: %v", err)
	}
	if resp.Rate != 1.10 {
		t.Fatalf("rate = %v", resp.Rate)
	}
	if resp.Stale {
		t.Fatal("rate should not be stale")
	}
}

func TestGetLiveRateStale(t *testing.T) {
	s := newTestServices(t)
	s.Cache = ratecache.New(20 * time.Millisecond)
	s.Cache.Update("EUR", 1.10, "bank")
	time.Sleep(40 * time.Millisecond)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	_, err := c.GetLiveRate(context.Background(), &fxpb.GetLiveRateRequest{Currency: "EUR"})
	if err == nil {
		t.Fatal("expected stale error")
	}
}

func TestGetLiveRateNotFound(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	_, err := c.GetLiveRate(context.Background(), &fxpb.GetLiveRateRequest{Currency: "EUR"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

func TestGetNetExposure(t *testing.T) {
	s := newTestServices(t)
	s.Tracker.AddExposure("EUR", 100_000)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.GetNetExposure(context.Background(), &fxpb.GetNetExposureRequest{Currency: "EUR"})
	if err != nil {
		t.Fatalf("get net exposure: %v", err)
	}
	if resp.NetAmount != 100_000 {
		t.Fatalf("net = %v", resp.NetAmount)
	}
	if resp.OpenAmount != 100_000 {
		t.Fatalf("open = %v", resp.OpenAmount)
	}
}

func TestGetNetExposureMissing(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.GetNetExposure(context.Background(), &fxpb.GetNetExposureRequest{Currency: "USD"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.NetAmount != 0 {
		t.Fatalf("net = %v, want 0", resp.NetAmount)
	}
}

func TestStreamExposure(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := c.StreamExposure(ctx, &fxpb.StreamExposureRequest{Currency: "EUR"})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// First snapshot is empty (no exposure yet); push an exposure to get one.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Tracker.AddExposure("EUR", 100_000)
	}()
	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got.Currency != "EUR" {
		t.Fatalf("currency = %q", got.Currency)
	}
	if got.NetAmount != 100_000 {
		t.Fatalf("net = %v, want 100000", got.NetAmount)
	}
}

func TestSubmitHedgePlan(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{
		PlanId: "p1",
		Legs: []*fxpb.HedgePlanLeg{
			{Currency: "EUR", Notional: 50_000, Tenor: "spot", Type: "spot"},
			{Currency: "JPY", Notional: 30_000, Tenor: "forward", Type: "forward", ValueDate: "2026-08-01"},
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if resp.Rejected {
		t.Fatalf("unexpected rejection: %q", resp.RejectReason)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	for _, r := range resp.Results {
		if r.Status != string(domain.StatusExecuted) {
			t.Fatalf("leg status = %q, want executed", r.Status)
		}
		if r.HedgeId == "" {
			t.Fatal("hedge id should be set")
		}
	}
}

func TestSubmitHedgePlanRejectsCapBreach(t *testing.T) {
	s := newTestServices(t)
	s.Policy.MaxOpenExposureUSD = 10_000
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{
		PlanId: "p2",
		Legs: []*fxpb.HedgePlanLeg{
			{Currency: "EUR", Notional: 1_000_000, Tenor: "spot", Type: "spot"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.Rejected {
		t.Fatal("expected rejection for cap breach")
	}
	if resp.RejectReason == "" {
		t.Fatal("reject reason should be set")
	}
	if len(resp.Results) != 0 {
		t.Fatalf("expected no legs executed, got %d", len(resp.Results))
	}
}

func TestSubmitHedgePlanForwardValueDate(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, _ := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{
		PlanId: "p3",
		Legs: []*fxpb.HedgePlanLeg{
			{Currency: "EUR", Notional: 50_000, Tenor: "forward", Type: "forward", ValueDate: "2026-08-01"},
		},
	})
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d", len(resp.Results))
	}
	h := s.Store.GetHedge(resp.Results[0].HedgeId)
	if h == nil {
		t.Fatal("hedge not in store")
	}
	if h.Tenor != domain.TenorForward {
		t.Fatalf("tenor = %q, want forward", h.Tenor)
	}
	if h.Type != domain.TypeForward {
		t.Fatalf("type = %q, want forward", h.Type)
	}
	wantVD := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !h.ValueDate.Equal(wantVD) {
		t.Fatalf("value date = %v, want %v", h.ValueDate, wantVD)
	}
}

func TestGetLiveRateCrossCheckDiverge(t *testing.T) {
	s := newTestServices(t)
	s.Cache.Update("EUR", 1.20, "bank")     // live
	s.Cache.SetRevaluationRate("EUR", 1.10) // diverges ~900 bps
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	_, err := c.GetLiveRate(context.Background(), &fxpb.GetLiveRateRequest{Currency: "EUR"})
	if err == nil {
		t.Fatal("expected divergence error so callers fall back")
	}
}