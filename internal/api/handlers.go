package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	"github.com/google/uuid"
)

// Service wires together the store, exposure tracker, FX provider, policy,
// and audit sink, exposing handler methods.
type Service struct {
	Store    *store.Store
	Tracker  *exposure.Tracker
	Provider provider.FXProvider
	Policy   *policy.Policy
	Audit    audit.Sink
}

// NewService returns a Service ready to serve requests.
func NewService(st *store.Store, tr *exposure.Tracker, p provider.FXProvider, pol *policy.Policy, a audit.Sink) *Service {
	return &Service{
		Store:    st,
		Tracker:  tr,
		Provider: p,
		Policy:   pol,
		Audit:    a,
	}
}

// --- request / response helpers ---

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// --- exposure ---

type addExposureReq struct {
	Amount float64 `json:"amount"`
}

// AddExposure handles POST /v1/exposure/{currency}.
func (s *Service) AddExposure(w http.ResponseWriter, r *http.Request) {
	currency := strings.ToUpper(r.PathValue("currency"))
	if currency == "" {
		writeError(w, http.StatusBadRequest, "currency is required")
		return
	}
	var req addExposureReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Amount == 0 {
		writeError(w, http.StatusBadRequest, "amount must be non-zero")
		return
	}
	s.Tracker.AddExposure(currency, req.Amount)
	if s.Audit != nil {
		s.Audit.Emit(audit.Event{
			Type:     audit.EventExposureAdded,
			Currency: currency,
			Detail:   "added exposure",
			At:       time.Now().UTC(),
		})
	}
	exp := s.Tracker.GetExposure(currency)
	writeJSON(w, http.StatusOK, exp)
}

// GetExposure handles GET /v1/exposure/{currency}.
func (s *Service) GetExposure(w http.ResponseWriter, r *http.Request) {
	currency := strings.ToUpper(r.PathValue("currency"))
	exp := s.Tracker.GetExposure(currency)
	if exp == nil {
		writeJSON(w, http.StatusOK, &domain.Exposure{
			Currency:  currency,
			UpdatedAt: time.Now().UTC(),
		})
		return
	}
	writeJSON(w, http.StatusOK, exp)
}

// --- hedges ---

type createHedgeReq struct {
	Currency string  `json:"currency"`
	Notional float64 `json:"notional"`
	Tenor    string  `json:"tenor"`
	Type     string  `json:"type"`
}

// CreateHedge handles POST /v1/hedges.
func (s *Service) CreateHedge(w http.ResponseWriter, r *http.Request) {
	var req createHedgeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	currency := strings.ToUpper(req.Currency)
	if currency == "" {
		writeError(w, http.StatusBadRequest, "currency is required")
		return
	}
	if req.Notional <= 0 {
		writeError(w, http.StatusBadRequest, "notional must be positive")
		return
	}
	tenor := domain.Tenor(req.Tenor)
	if !domain.IsValidTenor(tenor) {
		writeError(w, http.StatusBadRequest, "invalid tenor (spot or forward)")
		return
	}
	htype := domain.HedgeType(req.Type)
	if !domain.IsValidHedgeType(htype) {
		writeError(w, http.StatusBadRequest, "invalid type (spot or forward)")
		return
	}

	now := time.Now().UTC()
	h := &domain.Hedge{
		ID:        uuid.NewString(),
		Currency:  currency,
		Notional:  req.Notional,
		Tenor:     tenor,
		Type:      htype,
		Status:    domain.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	rate, err := s.Provider.Quote(currency, req.Notional, string(tenor))
	if err != nil {
		h.Status = domain.StatusFailed
		h.UpdatedAt = time.Now().UTC()
		s.Store.CreateHedge(h)
		s.emit(audit.EventHedgeFailed, h, "quote failed: "+err.Error())
		writeJSON(w, http.StatusBadGateway, h)
		return
	}
	h.QuotedRate = rate
	h.Status = domain.StatusExecuting
	h.UpdatedAt = time.Now().UTC()
	s.Store.CreateHedge(h)
	s.emit(audit.EventHedgeCreated, h, "hedge created")

	fills, err := s.Provider.Execute(h)
	if err != nil {
		_, _ = s.Store.UpdateHedge(h.ID, func(stored *domain.Hedge) error {
			stored.Status = domain.StatusFailed
			stored.UpdatedAt = time.Now().UTC()
			return nil
		})
		h.Status = domain.StatusFailed
		s.emit(audit.EventHedgeFailed, h, "execute failed: "+err.Error())
		writeJSON(w, http.StatusBadGateway, s.Store.GetHedge(h.ID))
		return
	}

	// Compute slippage and P&L; record fill + sample.
	var slippageBPS, pnl float64
	for _, f := range fills {
		if h.QuotedRate != 0 {
			fslip := (f.Price - h.QuotedRate) / h.QuotedRate * 10_000.0
			slippageBPS = fslip
			pnl += (h.QuotedRate - f.Price) * f.Amount
		}
		s.Store.AddSlippageSample(domain.SlippageSample{
			Pair:         currency + "USD",
			QuotedRate:   h.QuotedRate,
			ExecutedRate: f.Price,
			SlippageBPS:  slippageBPS,
			Timestamp:    f.Timestamp,
		})
	}

	_, _ = s.Store.UpdateHedge(h.ID, func(stored *domain.Hedge) error {
		stored.Status = domain.StatusExecuted
		stored.Fills = fills
		stored.SlippageBPS = slippageBPS
		stored.PnL = pnl
		stored.UpdatedAt = time.Now().UTC()
		return nil
	})

	// Increase hedge coverage by the executed notional.
	s.Tracker.AddCoverage(currency, req.Notional)

	out := s.Store.GetHedge(h.ID)
	s.emit(audit.EventHedgeExecuted, out, "hedge executed")
	writeJSON(w, http.StatusCreated, out)
}

// GetHedge handles GET /v1/hedges/{id}.
func (s *Service) GetHedge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h := s.Store.GetHedge(id)
	if h == nil {
		writeError(w, http.StatusNotFound, "hedge not found")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// --- P&L ---

// pnlResponse is the GET /v1/pnl response shape.
type pnlResponse struct {
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	ByCurrency  []domain.PnL  `json:"by_currency"`
	Total       domain.PnL    `json:"total"`
}

// PnL handles GET /v1/pnl?from=&to=.
func (s *Service) PnL(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	all := s.Store.AllHedges()
	byCcy := make(map[string]*domain.PnL)
	for _, h := range all {
		if !inRange(h.CreatedAt, from, to) {
			continue
		}
		p, ok := byCcy[h.Currency]
		if !ok {
			p = &domain.PnL{Currency: h.Currency}
			byCcy[h.Currency] = p
		}
		p.Realized += h.PnL
		p.Components.HedgePnL += h.PnL
		p.Components.SlippageCost += h.SlippageBPS * h.Notional / 10_000.0
	}

	resp := pnlResponse{From: from, To: to}
	var total domain.PnL
	for _, p := range byCcy {
		p.Total = p.Realized + p.Unrealized
		resp.ByCurrency = append(resp.ByCurrency, *p)
		total.Realized += p.Realized
		total.Unrealized += p.Unrealized
		total.Components.HedgePnL += p.Components.HedgePnL
		total.Components.SlippageCost += p.Components.SlippageCost
	}
	total.Currency = "TOTAL"
	total.Total = total.Realized + total.Unrealized
	resp.Total = total
	writeJSON(w, http.StatusOK, resp)
}

// --- slippage ---

// slippageAggregates holds aggregate stats for a pair.
type slippageAggregates struct {
	Pair  string  `json:"pair"`
	Count int     `json:"count"`
	Mean  float64 `json:"mean_bps"`
	Max   float64 `json:"max_bps"`
}

// slippageResponse is the GET /v1/slippage response shape.
type slippageResponse struct {
	Pair       string                  `json:"pair"`
	From       time.Time               `json:"from"`
	To         time.Time               `json:"to"`
	Samples    []domain.SlippageSample `json:"samples"`
	Aggregates slippageAggregates      `json:"aggregates"`
}

// Slippage handles GET /v1/slippage?pair=&from=&to=.
func (s *Service) Slippage(w http.ResponseWriter, r *http.Request) {
	pair := strings.ToUpper(r.URL.Query().Get("pair"))
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	samples := s.Store.SlippageSamples(pair, from, to)
	agg := slippageAggregates{Pair: pair, Count: len(samples)}
	var sum, max float64
	for _, sm := range samples {
		sum += sm.SlippageBPS
		if sm.SlippageBPS > max {
			max = sm.SlippageBPS
		}
	}
	if agg.Count > 0 {
		agg.Mean = sum / float64(agg.Count)
	}
	agg.Max = max
	writeJSON(w, http.StatusOK, slippageResponse{
		Pair:       pair,
		From:       from,
		To:         to,
		Samples:    samples,
		Aggregates: agg,
	})
}

// --- health ---

// Healthz handles GET /healthz.
func Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz handles GET /readyz.
func (s *Service) Readyz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- helpers ---

// emit sends an audit event for a hedge state change.
func (s *Service) emit(t audit.EventType, h *domain.Hedge, detail string) {
	if s.Audit == nil {
		return
	}
	s.Audit.Emit(audit.Event{
		Type:     t,
		HedgeID:  h.ID,
		Currency: h.Currency,
		Detail:   detail,
		At:       time.Now().UTC(),
	})
}

// parseRange parses the from/to query params as RFC3339 times. Either may be
// omitted to leave that bound unbounded.
func parseRange(r *http.Request) (time.Time, time.Time, error) {
	var from, to time.Time
	var err error
	if v := r.URL.Query().Get("from"); v != "" {
		from, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid 'from' (use RFC3339)")
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		to, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid 'to' (use RFC3339)")
		}
	}
	return from, to, nil
}

// inRange reports whether t is within [from, to] (zero bounds are unbounded).
func inRange(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// NewMux builds the HTTP routing mux for the service.
func NewMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Healthz)
	mux.HandleFunc("GET /readyz", svc.Readyz)
	mux.HandleFunc("GET /v1/exposure/{currency}", svc.GetExposure)
	mux.HandleFunc("POST /v1/exposure/{currency}", svc.AddExposure)
	mux.HandleFunc("POST /v1/hedges", svc.CreateHedge)
	mux.HandleFunc("GET /v1/hedges/{id}", svc.GetHedge)
	mux.HandleFunc("GET /v1/pnl", svc.PnL)
	mux.HandleFunc("GET /v1/slippage", svc.Slippage)
	return mux
}