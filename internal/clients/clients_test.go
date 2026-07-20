package clients

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func newServer(t *testing.T, status int, failFirst int, handler func(body []byte, headers http.Header)) *httptest.Server {
	t.Helper()
	var attempts atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if handler != nil {
			handler(body, r.Header)
		}
		if int(attempts.Add(1)) <= failFirst {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(status)
	}))
}

func TestAuditClientEmitSuccess(t *testing.T) {
	var got AuditPayload
	var idem string
	srv := newServer(t, http.StatusOK, 0, func(body []byte, h http.Header) {
		_ = json.Unmarshal(body, &got)
		idem = h.Get("Idempotency-Key")
	})
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	c := NewAuditClient(nil)
	err := c.Emit(context.Background(), AuditPayload{EventType: "hedge_created", Source: "fx-hedging", HedgeID: "h1", At: time.Now().UTC().Format(time.RFC3339)}, "evt-1", "h1")
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if got.EventType != "hedge_created" {
		t.Fatalf("event_type = %q", got.EventType)
	}
	if idem != "evt-1" {
		t.Fatalf("idempotency key = %q", idem)
	}
	if c.LastEmitted("h1") != "evt-1" {
		t.Fatalf("last emitted = %q", c.LastEmitted("h1"))
	}
}

func TestAuditClientRetryThenSucceed(t *testing.T) {
	srv := newServer(t, http.StatusOK, 2, nil)
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	c := NewAuditClient(nil)
	err := c.Emit(context.Background(), AuditPayload{EventType: "x", At: time.Now().UTC().Format(time.RFC3339)}, "e1", "")
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
}

func TestAuditClientDeadLetterOnExhaust(t *testing.T) {
	srv := newServer(t, http.StatusBadGateway, 10, nil)
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	dl := NewMemDeadLetter()
	c := NewAuditClient(dl)
	err := c.Emit(context.Background(), AuditPayload{EventType: "x", At: time.Now().UTC().Format(time.RFC3339)}, "e1", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	rows, _ := dl.List(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("dead-letter rows = %d, want 1", len(rows))
	}
	if rows[0].Topic != "audit" {
		t.Fatalf("topic = %q", rows[0].Topic)
	}
	if rows[0].Key != "e1" {
		t.Fatalf("key = %q", rows[0].Key)
	}
}

func TestAuditClientEmptyURLNoop(t *testing.T) {
	t.Setenv("AUDIT_EVENT_LOG_URL", "")
	c := NewAuditClient(nil)
	if err := c.Emit(context.Background(), AuditPayload{}, "e1", ""); err != nil {
		t.Fatalf("emit should be no-op: %v", err)
	}
}

func TestAuditClientOrdering(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p AuditPayload
		_ = json.Unmarshal(body, &p)
		seen = append(seen, p.HedgeID)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	c := NewAuditClient(nil)
	for i := 0; i < 5; i++ {
		_ = c.Emit(context.Background(), AuditPayload{EventType: "e", HedgeID: "h", At: time.Now().UTC().Format(time.RFC3339)}, "evt", "h")
	}
	if len(seen) != 5 {
		t.Fatalf("seen = %d, want 5", len(seen))
	}
}

func TestReconClientPublishExecution(t *testing.T) {
	var got ExecutionRecord
	var idem string
	srv := newServer(t, http.StatusOK, 0, func(body []byte, h http.Header) {
		_ = json.Unmarshal(body, &got)
		idem = h.Get("Idempotency-Key")
	})
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	c := NewReconClient(nil)
	rec := ExecutionRecord{HedgeID: "h1", Currency: "EUR", Venue: "bank", VenueTradeID: "vt-1", Notional: 100, FillPrice: 1.1, QuotedPrice: 1.1, SlippageBPS: 0, ExecutedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := c.PublishExecution(context.Background(), rec); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got.HedgeID != "h1" {
		t.Fatalf("hedge id = %q", got.HedgeID)
	}
	if idem != "h1" {
		t.Fatalf("idempotency key = %q, want h1", idem)
	}
}

func TestReconClientPublishObligation(t *testing.T) {
	var got domain.SettlementObligation
	srv := newServer(t, http.StatusOK, 0, func(body []byte, h http.Header) {
		_ = json.Unmarshal(body, &got)
	})
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	c := NewReconClient(nil)
	ob := domain.SettlementObligation{Currency: "EUR", Amount: 10_000, Legs: 2, At: time.Now().UTC()}
	if err := c.PublishObligation(context.Background(), ob); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got.Currency != "EUR" || got.Amount != 10_000 {
		t.Fatalf("got = %+v", got)
	}
}

func TestReconClientDeadLetterOnExhaust(t *testing.T) {
	srv := newServer(t, http.StatusBadGateway, 10, nil)
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	dl := NewMemDeadLetter()
	c := NewReconClient(dl)
	err := c.PublishExecution(context.Background(), ExecutionRecord{HedgeID: "h1", ExecutedAt: time.Now().UTC().Format(time.RFC3339)})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	rows, _ := dl.List(context.Background(), 10)
	if len(rows) != 1 || rows[0].Topic != "recon" {
		t.Fatalf("dead-letter = %+v", rows)
	}
}

func TestMemDeadLetterList(t *testing.T) {
	m := NewMemDeadLetter()
	for i := 0; i < 3; i++ {
		_ = m.Append(context.Background(), &DeadLetter{Topic: "t", Key: "k"})
	}
	rows, _ := m.List(context.Background(), 2)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	all, _ := m.List(context.Background(), 0)
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
}

func TestAuditClientBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad payload")
	}))
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	c := NewAuditClient(nil)
	err := c.Emit(context.Background(), AuditPayload{EventType: "x", At: time.Now().UTC().Format(time.RFC3339)}, "e1", "")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestReconClientBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad payload")
	}))
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	c := NewReconClient(nil)
	err := c.PublishExecution(context.Background(), ExecutionRecord{HedgeID: "h1", ExecutedAt: time.Now().UTC().Format(time.RFC3339)})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestReconClientEmptyURLNoop(t *testing.T) {
	t.Setenv("RECONCILIATION_URL", "")
	c := NewReconClient(nil)
	if err := c.PublishExecution(context.Background(), ExecutionRecord{}); err != nil {
		t.Fatalf("publish should be no-op: %v", err)
	}
	if err := c.PublishObligation(context.Background(), domain.SettlementObligation{Currency: "EUR", At: time.Now().UTC()}); err != nil {
		t.Fatalf("publish should be no-op: %v", err)
	}
}

func TestAuditClientCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("AUDIT_EVENT_LOG_URL", srv.URL)
	c := NewAuditClient(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Emit(ctx, AuditPayload{EventType: "x", At: time.Now().UTC().Format(time.RFC3339)}, "e1", "")
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestReconClientCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	c := NewReconClient(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.PublishExecution(ctx, ExecutionRecord{HedgeID: "h1", ExecutedAt: time.Now().UTC().Format(time.RFC3339)})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestReconClientRetryThenSucceed(t *testing.T) {
	srv := newServer(t, http.StatusOK, 2, nil)
	defer srv.Close()
	t.Setenv("RECONCILIATION_URL", srv.URL)
	c := NewReconClient(nil)
	if err := c.PublishExecution(context.Background(), ExecutionRecord{HedgeID: "h1", ExecutedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}