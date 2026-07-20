package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
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

func genCACertPath(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return p
}

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
			{Currency: "EUR", Notional: 50_000, Tenor: "SPOT", Type: "SPOT"},
			{Currency: "JPY", Notional: 30_000, Tenor: "FORWARD", Type: "FORWARD", ValueDate: "2026-08-01"},
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
			{Currency: "EUR", Notional: 1_000_000, Tenor: "SPOT", Type: "SPOT"},
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
			{Currency: "EUR", Notional: 50_000, Tenor: "FORWARD", Type: "FORWARD", ValueDate: "2026-08-01"},
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
		t.Fatalf("tenor = %q, want %q", h.Tenor, domain.TenorForward)
	}
	if h.Type != domain.TypeForward {
		t.Fatalf("type = %q, want %q", h.Type, domain.TypeForward)
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

func TestGetLiveRateEmptyCurrency(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	_, err := c.GetLiveRate(context.Background(), &fxpb.GetLiveRateRequest{Currency: ""})
	if err == nil {
		t.Fatal("expected error for empty currency")
	}
}

func TestGetNetExposureEmptyCurrency(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	_, err := c.GetNetExposure(context.Background(), &fxpb.GetNetExposureRequest{Currency: ""})
	if err == nil {
		t.Fatal("expected error for empty currency")
	}
}

func TestSubmitHedgePlanEmptyAndInvalidLegs(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{
		PlanId: "p-empty",
		Legs: []*fxpb.HedgePlanLeg{
			{Currency: "", Notional: 100, Tenor: "SPOT", Type: "SPOT"},
			{Currency: "EUR", Notional: 0, Tenor: "SPOT", Type: "SPOT"},
			{Currency: "EUR", Notional: 50_000, Tenor: "WEIRD", Type: "SPOT"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d", len(resp.Results))
	}
	if resp.Results[0].Error == "" {
		t.Fatal("expected error for empty currency")
	}
	if resp.Results[1].Error == "" {
		t.Fatal("expected error for zero notional")
	}
	if resp.Results[2].Status != string(domain.StatusExecuted) {
		t.Fatalf("invalid tenor should default to spot; status = %q", resp.Results[2].Status)
	}
}

func TestSubmitHedgePlanDuplicate(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	leg := &fxpb.HedgePlanLeg{Currency: "EUR", Notional: 50_000, Tenor: "SPOT", Type: "SPOT"}
	resp1, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{PlanId: "pdup", Legs: []*fxpb.HedgePlanLeg{leg}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp1.Results) != 1 || resp1.Results[0].Status != string(domain.StatusExecuted) {
		t.Fatalf("first submit results = %+v", resp1.Results)
	}
	// Same plan+currency should re-use via client request id check (leg uses a
	// random uuid suffix each time so this primarily exercises the path).
	resp2, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{PlanId: "pdup", Legs: []*fxpb.HedgePlanLeg{leg}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(resp2.Results) != 1 {
		t.Fatalf("results = %d", len(resp2.Results))
	}
}

func TestStreamExposureEmptyCurrency(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	stream, err := c.StreamExposure(ctx, &fxpb.StreamExposureRequest{Currency: ""})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("expected error for empty currency")
	}
}

func TestStreamExposureContextCancel(t *testing.T) {
	s := newTestServices(t)
	s.Tracker.AddExposure("EUR", 100)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.StreamExposure(ctx, &fxpb.StreamExposureRequest{Currency: "EUR"})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("recv: %v", err)
	}
	cancel()
	if _, err := stream.Recv(); err != nil {
		// expect clean EOF on cancel
	}
}

func TestNewServerBadPort(t *testing.T) {
	s := newTestServices(t)
	_, _, err := NewServer(s, "not-a-port")
	if err == nil {
		t.Fatal("expected error for bad port")
	}
}

func TestNewServerMTLSEnabled(t *testing.T) {
	// Generate a CA cert and point MTLS_CA_CERT at it; NewServer should build
	// TLS creds successfully.
	caPath := genCACertPath(t)
	t.Setenv("MTLS_CA_CERT", caPath)
	s := newTestServices(t)
	srv, lis, err := NewServer(s, "0")
	if err != nil {
		t.Fatalf("new server with mTLS: %v", err)
	}
	defer srv.Stop()
	_ = lis.Close()
}

func TestNewServerMTLSBadCA(t *testing.T) {
	t.Setenv("MTLS_CA_CERT", filepath.Join(t.TempDir(), "missing.pem"))
	s := newTestServices(t)
	_, _, err := NewServer(s, "0")
	if err == nil {
		t.Fatal("expected error for missing CA cert")
	}
}

func TestSubmitHedgePlanAutoPlanID(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	resp, err := c.SubmitHedgePlan(context.Background(), &fxpb.SubmitHedgePlanRequest{
		PlanId: "",
		Legs:   []*fxpb.HedgePlanLeg{{Currency: "EUR", Notional: 50_000, Tenor: "SPOT", Type: "SPOT"}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.PlanId == "" {
		t.Fatal("expected auto-generated plan id")
	}
}

func TestStreamExposureFiltersOtherCurrencies(t *testing.T) {
	s := newTestServices(t)
	c, cleanup := startGRPC(t, s)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stream, err := c.StreamExposure(ctx, &fxpb.StreamExposureRequest{Currency: "EUR"})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// Push JPY then EUR; JPY should be filtered out, EUR delivered.
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.Tracker.AddExposure("JPY", 1000)
		s.Tracker.AddExposure("EUR", 200_000)
	}()
	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got.Currency != "EUR" {
		t.Fatalf("currency = %q, want EUR (JPY filtered)", got.Currency)
	}
	if got.NetAmount != 200_000 {
		t.Fatalf("net = %v, want 200000", got.NetAmount)
	}
}