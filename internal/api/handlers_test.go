package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
)

func newTestService(t *testing.T, p provider.FXProvider) (*Service, *audit.Recorder, store.Store, *exposure.Tracker) {
	t.Helper()
	st := store.New()
	tr := exposure.New()
	pol := policy.New()
	rec := audit.NewRecorder()
	if p == nil {
		p = provider.NewDummy()
	}
	svc := NewService(st, tr, p, pol, rec)
	return svc, rec, st, tr
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("failed to decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestHealthzAndReadyz(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz code = %d", rec.Code)
	}
	if m := decodeBody(t, rec); m["status"] != "ok" {
		t.Fatalf("healthz status = %v", m["status"])
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("readyz code = %d", rec2.Code)
	}
	if m := decodeBody(t, rec2); m["status"] != "ready" {
		t.Fatalf("readyz status = %v", m["status"])
	}
}

func TestAddExposure(t *testing.T) {
	svc, rec, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]float64{"amount": 100_000})
	if rec1.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec1.Code, rec1.Body.String())
	}
	m := decodeBody(t, rec1)
	if m["net_amount"].(float64) != 100_000 {
		t.Fatalf("net = %v, want 100000", m["net_amount"])
	}
	if m["open_amount"].(float64) != 100_000 {
		t.Fatalf("open = %v, want 100000", m["open_amount"])
	}

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/exposure/eur", map[string]float64{"amount": -30_000})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	m2 := decodeBody(t, rec2)
	if m2["net_amount"].(float64) != 70_000 {
		t.Fatalf("net = %v, want 70000 (case-insensitive currency)", m2["net_amount"])
	}

	if len(rec.Events()) != 2 {
		t.Fatalf("audit events = %d, want 2", len(rec.Events()))
	}
}

func TestAddExposureValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	tests := []struct {
		name string
		body interface{}
		want int
	}{
		{"zero amount", map[string]float64{"amount": 0}, http.StatusBadRequest},
		{"invalid json", nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body == nil {
				req = httptest.NewRequest(http.MethodPost, "/v1/exposure/EUR", bytes.NewReader([]byte("bad")))
			} else {
				b, _ := json.Marshal(tt.body)
				req = httptest.NewRequest(http.MethodPost, "/v1/exposure/EUR", bytes.NewReader(b))
			}
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("code = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestGetExposureMissing(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/exposure/USD", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["currency"] != "USD" {
		t.Fatalf("currency = %v", m["currency"])
	}
	if m["net_amount"].(float64) != 0 {
		t.Fatalf("net = %v, want 0", m["net_amount"])
	}
}

func TestCreateHedgeSuccess(t *testing.T) {
	svc, rec, st, tr := newTestService(t, nil)
	mux := NewMux(svc)

	// Set up exposure first.
	tr.AddExposure("EUR", 100_000)

	body := map[string]interface{}{"currency": "EUR", "notional": 90_000, "tenor": "SPOT", "type": "SPOT"}
	r := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r.Code != http.StatusCreated {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	var h domain.Hedge
	if err := json.Unmarshal(r.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Status != domain.StatusExecuted {
		t.Fatalf("status = %q, want executed", h.Status)
	}
	if h.QuotedRate != 1.10 {
		t.Fatalf("quoted rate = %v, want 1.10", h.QuotedRate)
	}
	if len(h.Fills) != 1 {
		t.Fatalf("fills len = %d, want 1", len(h.Fills))
	}
	if h.Fills[0].Price != 1.10 {
		t.Fatalf("fill price = %v, want 1.10", h.Fills[0].Price)
	}
	if h.Fills[0].VenueTradeID == "" {
		t.Fatal("venue trade id should be set")
	}

	// Coverage should be increased.
	exp := tr.GetExposure("EUR")
	if exp.HedgeCoverage != 90_000 {
		t.Fatalf("coverage = %v, want 90000", exp.HedgeCoverage)
	}
	if exp.OpenAmount != 10_000 {
		t.Fatalf("open = %v, want 10000", exp.OpenAmount)
	}

	// Audit events: created + executed + pnl entry.
	evs := rec.Events()
	if len(evs) < 2 {
		t.Fatalf("audit events = %d, want >=2", len(evs))
	}

	// Store has the hedge.
	if st.GetHedge(h.ID) == nil {
		t.Fatal("hedge not in store")
	}
}

func TestCreateHedgeValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	tests := []struct {
		name string
		body map[string]interface{}
		want int
	}{
		{"missing currency", map[string]interface{}{"notional": 100, "tenor": "SPOT", "type": "SPOT"}, http.StatusBadRequest},
		{"zero notional", map[string]interface{}{"currency": "EUR", "notional": 0, "tenor": "SPOT", "type": "SPOT"}, http.StatusBadRequest},
		{"bad tenor", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "swap", "type": "SPOT"}, http.StatusBadRequest},
		{"bad type", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "option"}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := doJSON(t, mux, http.MethodPost, "/v1/hedges", tt.body)
			if r.Code != tt.want {
				t.Fatalf("code = %d, want %d, body=%s", r.Code, tt.want, r.Body.String())
			}
		})
	}
}

func TestCreateHedgeInvalidJSON(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/hedges", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestCreateHedgeProviderFailure(t *testing.T) {
	d := &provider.DummyFXProvider{Rate: 1.0, FailExecute: true}
	svc, rec, st, _ := newTestService(t, d)
	mux := NewMux(svc)

	body := map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT"}
	r := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502, body=%s", r.Code, r.Body.String())
	}
	var h domain.Hedge
	_ = json.Unmarshal(r.Body.Bytes(), &h)
	if h.Status != domain.StatusFailed {
		t.Fatalf("status = %q, want failed", h.Status)
	}
	// store has it
	if st.GetHedge(h.ID) == nil {
		t.Fatal("failed hedge should be stored")
	}
	// audit failure event emitted
	found := false
	for _, e := range rec.Events() {
		if e.Type == audit.EventHedgeFailed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected hedge_failed audit event")
	}
}

func TestCreateHedgeWithSlippage(t *testing.T) {
	d := &provider.DummyFXProvider{Rate: 1.0, SlippageBPS: 5}
	svc, _, _, _ := newTestService(t, d)
	mux := NewMux(svc)

	body := map[string]interface{}{"currency": "EUR", "notional": 100_000, "tenor": "SPOT", "type": "SPOT"}
	r := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r.Code != http.StatusCreated {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	var h domain.Hedge
	_ = json.Unmarshal(r.Body.Bytes(), &h)
	if h.SlippageBPS < 4.99 || h.SlippageBPS > 5.01 {
		t.Fatalf("slippage = %v, want ~5", h.SlippageBPS)
	}
}

func TestGetHedge(t *testing.T) {
	svc, _, st, _ := newTestService(t, nil)
	mux := NewMux(svc)

	// Create one via API.
	r := doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT"})
	var created domain.Hedge
	_ = json.Unmarshal(r.Body.Bytes(), &created)

	got := doJSON(t, mux, http.MethodGet, "/v1/hedges/"+created.ID, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("code = %d", got.Code)
	}
	var h domain.Hedge
	_ = json.Unmarshal(got.Body.Bytes(), &h)
	if h.ID != created.ID {
		t.Fatalf("id = %q, want %q", h.ID, created.ID)
	}

	// Missing.
	r2 := doJSON(t, mux, http.MethodGet, "/v1/hedges/nope", nil)
	if r2.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", r2.Code)
	}

	_ = st
}

func TestPnLEndpoint(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	// Create two hedges for different currencies.
	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 100_000, "tenor": "SPOT", "type": "SPOT"})
	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "JPY", "notional": 50_000, "tenor": "SPOT", "type": "SPOT"})

	r := doJSON(t, mux, http.MethodGet, "/v1/pnl", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	var resp pnlResponse
	if err := json.Unmarshal(r.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.ByCurrency) != 2 {
		t.Fatalf("by_currency len = %d, want 2", len(resp.ByCurrency))
	}
	if resp.Total.Currency != "TOTAL" {
		t.Fatalf("total currency = %q", resp.Total.Currency)
	}
}

func TestPnLInvalidRange(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	r := doJSON(t, mux, http.MethodGet, "/v1/pnl?from=notadate", nil)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", r.Code)
	}
}

func TestPnLDateFilter(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	// Create a hedge.
	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT"})

	// Query a range starting in the future -> no hedges.
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	r := doJSON(t, mux, http.MethodGet, "/v1/pnl?from="+future, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, from=%s body=%s", r.Code, future, r.Body.String())
	}
	var resp pnlResponse
	_ = json.Unmarshal(r.Body.Bytes(), &resp)
	if len(resp.ByCurrency) != 0 {
		t.Fatalf("by_currency len = %d, want 0 (future range)", len(resp.ByCurrency))
	}
}

func TestSlippageEndpoint(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)

	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT"})

	r := doJSON(t, mux, http.MethodGet, "/v1/slippage?pair=EURUSD", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	var resp slippageResponse
	if err := json.Unmarshal(r.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Aggregates.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Aggregates.Count)
	}
	if len(resp.Samples) != 1 {
		t.Fatalf("samples len = %d, want 1", len(resp.Samples))
	}
	if resp.Samples[0].Pair != "EURUSD" {
		t.Fatalf("pair = %q", resp.Samples[0].Pair)
	}
}

func TestSlippageInvalidRange(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	r := doJSON(t, mux, http.MethodGet, "/v1/slippage?to=bad", nil)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", r.Code)
	}
}

func TestParseRange(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/pnl?from=2024-01-01T00:00:00Z&to=2024-02-01T00:00:00Z", nil)
	from, to, err := parseRange(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !from.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from = %v", from)
	}
	if !to.Equal(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("to = %v", to)
	}
}

func TestInRange(t *testing.T) {
	t0 := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	if !inRange(t0, from, to) {
		t.Fatal("should be in range")
	}
	if inRange(t0, to, time.Time{}) {
		t.Fatal("should not be in range (after to)")
	}
	if !inRange(t0, time.Time{}, time.Time{}) {
		t.Fatal("unbounded should include")
	}
}

func TestAddExposureIdempotent(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	body := map[string]interface{}{"amount": 100_000, "event_id": "e1"}
	r1 := doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", body)
	if r1.Code != http.StatusOK {
		t.Fatalf("r1 code = %d", r1.Code)
	}
	r2 := doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", body)
	if r2.Code != http.StatusOK {
		t.Fatalf("r2 code = %d", r2.Code)
	}
	m := decodeBody(t, r2)
	if m["net_amount"].(float64) != 100_000 {
		t.Fatalf("net = %v, want 100000 (no double count)", m["net_amount"])
	}
}

func TestAddExposureCapBreachBlocksFlow(t *testing.T) {
	svc, rec, _, _ := newTestService(t, nil)
	svc.Policy.MaxOpenExposureUSD = 50_000
	mux := NewMux(svc)
	// Bring open to 40k (under cap).
	_ = doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]interface{}{"amount": 40_000})
	// A flow of +20k would bring open to 60k > 50k cap and *increases* open.
	r := doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]interface{}{"amount": 20_000})
	if r.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (cap breach blocks flow)", r.Code)
	}
	found := false
	for _, e := range rec.Events() {
		if e.Type == audit.EventCapBreach {
			found = true
		}
	}
	if !found {
		t.Fatal("expected cap_breach audit event")
	}
}

func TestAddExposureCapBreachAllowsReducingFlow(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	svc.Policy.MaxOpenExposureUSD = 50_000
	mux := NewMux(svc)
	_ = doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]interface{}{"amount": 100_000})
	// A reducing flow (-40k) brings open to 60k still > 50k, but absOpen
	// decreases from 100k to 60k, so it should be allowed.
	r := doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]interface{}{"amount": -40_000})
	if r.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (reducing flow allowed)", r.Code)
	}
}

func TestCreateHedgeIdempotentClientRequestID(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	body := map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT", "client_request_id": "req-1"}
	r1 := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r1.Code != http.StatusCreated {
		t.Fatalf("r1 code = %d", r1.Code)
	}
	var h1 domain.Hedge
	_ = json.Unmarshal(r1.Body.Bytes(), &h1)
	r2 := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r2.Code != http.StatusOK {
		t.Fatalf("r2 code = %d, want 200 (duplicate returns original)", r2.Code)
	}
	var h2 domain.Hedge
	_ = json.Unmarshal(r2.Body.Bytes(), &h2)
	if h1.ID != h2.ID {
		t.Fatalf("duplicate submission returned different hedge: %q vs %q", h1.ID, h2.ID)
	}
}

func TestCreateHedgeSlippageAlert(t *testing.T) {
	d := &provider.DummyFXProvider{Rate: 1.0, SlippageBPS: 20}
	svc, rec, _, _ := newTestService(t, d)
	svc.Policy.SlippageAlertBPS = 5
	mux := NewMux(svc)
	body := map[string]interface{}{"currency": "EUR", "notional": 100_000, "tenor": "SPOT", "type": "SPOT"}
	r := doJSON(t, mux, http.MethodPost, "/v1/hedges", body)
	if r.Code != http.StatusCreated {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	found := false
	for _, e := range rec.Events() {
		if e.Type == audit.EventSlippageAlert {
			found = true
		}
	}
	if !found {
		t.Fatal("expected slippage_alert audit event")
	}
}

func TestSettlementEndpoint(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	_ = doJSON(t, mux, http.MethodPost, "/v1/exposure/EUR", map[string]interface{}{"amount": 100_000})
	_ = doJSON(t, mux, http.MethodPost, "/v1/exposure/JPY", map[string]interface{}{"amount": -30_000})
	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 90_000, "tenor": "SPOT", "type": "SPOT"})
	r := doJSON(t, mux, http.MethodGet, "/v1/settlement", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", r.Code, r.Body.String())
	}
	m := decodeBody(t, r)
	if m["count"].(float64) < 1 {
		t.Fatalf("count = %v, want >=1", m["count"])
	}
}

func TestSlippageTuningFeedsPolicy(t *testing.T) {
	d := &provider.DummyFXProvider{Rate: 1.0, SlippageBPS: 20}
	svc, _, _, _ := newTestService(t, d)
	svc.Policy.SlippageAlertBPS = 5
	svc.Policy.WideningStep = 0.05
	mux := NewMux(svc)
	_ = doJSON(t, mux, http.MethodPost, "/v1/hedges", map[string]interface{}{"currency": "EUR", "notional": 100, "tenor": "SPOT", "type": "SPOT"})
	before := svc.Policy.EffectiveRatio("EUR")
	_ = doJSON(t, mux, http.MethodGet, "/v1/slippage?pair=EURUSD", nil)
	after := svc.Policy.EffectiveRatio("EUR")
	if after <= before {
		t.Fatalf("ratio before=%v after=%v, expected to widen (increase)", before, after)
	}
}

// Integration test using httptest.NewServer.
func TestIntegrationServer(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	mux := NewMux(svc)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Add exposure.
	body, _ := json.Marshal(map[string]float64{"amount": 200_000})
	resp, err := http.Post(srv.URL+"/v1/exposure/EUR", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post exposure: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exposure status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Create hedge.
	hbody, _ := json.Marshal(map[string]interface{}{"currency": "EUR", "notional": 180_000, "tenor": "SPOT", "type": "SPOT"})
	resp, err = http.Post(srv.URL+"/v1/hedges", "application/json", bytes.NewReader(hbody))
	if err != nil {
		t.Fatalf("post hedge: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("hedge status = %d", resp.StatusCode)
	}
	var h domain.Hedge
	_ = json.NewDecoder(resp.Body).Decode(&h)
	resp.Body.Close()

	// Get hedge.
	resp, err = http.Get(srv.URL + "/v1/hedges/" + h.ID)
	if err != nil {
		t.Fatalf("get hedge: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get hedge status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Get exposure.
	resp, err = http.Get(srv.URL + "/v1/exposure/EUR")
	if err != nil {
		t.Fatalf("get exposure: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get exposure status = %d", resp.StatusCode)
	}
	var exp domain.Exposure
	_ = json.NewDecoder(resp.Body).Decode(&exp)
	resp.Body.Close()
	if exp.HedgeCoverage != 180_000 {
		t.Fatalf("coverage = %v, want 180000", exp.HedgeCoverage)
	}
	if exp.OpenAmount != 20_000 {
		t.Fatalf("open = %v, want 20000", exp.OpenAmount)
	}

	// PnL + slippage.
	resp, err = http.Get(srv.URL + "/v1/pnl")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("pnl: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/v1/slippage?pair=EURUSD")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("slippage: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
}

func TestListHedges(t *testing.T) {
	svc, _, st, _ := newTestService(t, nil)
	st.CreateHedge(&domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100, Tenor: domain.TenorSpot, Type: domain.TypeSpot, Status: domain.StatusPending, CreatedAt: time.Now()})
	st.CreateHedge(&domain.Hedge{ID: "h2", Currency: "JPY", Notional: 50, Tenor: domain.TenorSpot, Type: domain.TypeSpot, Status: domain.StatusExecuted, CreatedAt: time.Now()})
	mux := NewMux(svc)

	rec := doJSON(t, mux, http.MethodGet, "/v1/hedges", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Hedges []*domain.Hedge `json:"hedges"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Hedges) != 2 {
		t.Fatalf("hedges=%d want 2", len(out.Hedges))
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/hedges?currency=EUR", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Hedges) != 1 || out.Hedges[0].Currency != "EUR" {
		t.Fatalf("eur filter: %+v", out.Hedges)
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/hedges?status=executed", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Hedges) != 1 || out.Hedges[0].ID != "h2" {
		t.Fatalf("executed filter: %+v", out.Hedges)
	}
}

func TestListExposures(t *testing.T) {
	svc, _, _, tr := newTestService(t, nil)
	tr.AddExposure("EUR", 100000)
	tr.AddExposure("JPY", -30000)
	mux := NewMux(svc)

	rec := doJSON(t, mux, http.MethodGet, "/v1/exposures", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Exposures []*domain.Exposure `json:"exposures"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Exposures) != 2 {
		t.Fatalf("exposures=%d want 2", len(out.Exposures))
	}
}