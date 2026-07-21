package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/clients"
	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/ratecache"
	"github.com/ai-crypto-onramp/fx-hedging/internal/settlement"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	"github.com/google/uuid"
)

// Service wires together the store, exposure tracker, FX provider, policy,
// audit sink, rate cache, settlement engine, and downstream clients,
// exposing handler methods.
type Service struct {
	Store    store.Store
	Tracker  *exposure.Tracker
	Provider provider.FXProvider
	Policy   *policy.Policy
	Audit    audit.Sink
	Cache    *ratecache.Cache
	Netter   *settlement.Engine
	AuditC   *clients.AuditClient
	ReconC   *clients.ReconClient
}

// NewService returns a Service ready to serve requests.
func NewService(st store.Store, tr *exposure.Tracker, p provider.FXProvider, pol *policy.Policy, a audit.Sink) *Service {
	return &Service{
		Store:    st,
		Tracker:  tr,
		Provider: p,
		Policy:   pol,
		Audit:    a,
		Cache:    ratecache.New(time.Second),
		Netter:   settlement.New(),
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

// addExposureReq is the POST /v1/exposure/{currency} request body.
//
// Breaking change: amount is a JSON string (decimal) instead of a JSON
// number, to preserve precision on money fields.
type addExposureReq struct {
	Amount  decimal.Decimal `json:"amount"`
	EventID string          `json:"event_id,omitempty"`
	Source  string          `json:"source,omitempty"`
}

// AddExposure handles POST /v1/exposure/{currency}.
//
// Idempotency: when EventID is supplied, a previously seen id is a no-op
// and the current exposure is returned unchanged (preventing double
// counting on replay). When adding the delta would breach the open
// exposure cap AND increase the open amount, the request is rejected with
// 409 Conflict and an alertable audit event is emitted.
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
	if req.Amount.IsZero() {
		writeError(w, http.StatusBadRequest, "amount must be non-zero")
		return
	}

	// Idempotency on event id.
	if req.EventID != "" && s.Tracker.Seen(req.EventID) {
		writeJSON(w, http.StatusOK, s.Tracker.GetExposure(currency))
		return
	}

	// Cap-breach guard: block new flow that would increase the open
	// exposure beyond the cap.
	if s.Policy != nil {
		current := s.Tracker.GetExposure(currency)
		if current != nil {
			openBefore := current.NetAmount.Sub(current.HedgeCoverage)
			openAfter := openBefore.Add(req.Amount)
			// Only block if this flow would *increase* the absolute open
			// exposure beyond the cap.
			capDec := decimal.NewFromFloat(s.Policy.EffectiveCap(currency))
			if absDecimal(openAfter).GreaterThan(capDec) && absDecimal(openAfter).GreaterThan(absDecimal(openBefore)) {
				s.emit(audit.Event{
					Type:     audit.EventCapBreach,
					Currency: currency,
					Detail:   "blocked exposure-increasing flow that would breach MAX_OPEN_EXPOSURE_USD",
					At:       time.Now().UTC(),
				})
				writeError(w, http.StatusConflict, "open exposure cap breached; flow blocked")
				return
			}
		}
	}

	if req.EventID != "" {
		s.Tracker.AddEvent(domain.ExposureEvent{
			EventID:  req.EventID,
			Currency: currency,
			Amount:   req.Amount,
			Source:   req.Source,
		})
	} else {
		s.Tracker.AddExposure(currency, req.Amount)
	}
	s.emit(audit.Event{
		Type:     audit.EventExposureAdded,
		Currency: currency,
		Detail:   "added exposure",
		At:       time.Now().UTC(),
	})
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

// createHedgeReq is the POST /v1/hedges request body.
//
// Breaking change: notional is a JSON string (decimal) instead of a JSON
// number, to preserve precision on money fields.
type createHedgeReq struct {
	Currency        string          `json:"currency"`
	Notional        decimal.Decimal `json:"notional"`
	Tenor           string          `json:"tenor"`
	Type            string          `json:"type"`
	ClientRequestID string          `json:"client_request_id,omitempty"`
}

// CreateHedge handles POST /v1/hedges.
//
// Idempotency: when ClientRequestID is supplied, a duplicate submission
// with the same id returns the original hedge unchanged (200 OK). Fill
// callbacks are idempotent on (venue, venue_trade_id) via Store.HasFill.
// Policy context (ratio used, cap state) is persisted on the hedge record.
// On each fill, slippage above SLIPPAGE_ALERT_BPS raises an alert event,
// the achieved rate feeds the live rate cache, and the P&L entry is
// persisted. Executed hedges are published to Reconciliation.
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
	if req.Notional.LessThanOrEqual(decimal.Zero) {
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

	// Idempotency on client request id: duplicate returns the original.
	if req.ClientRequestID != "" {
		if existing := s.Store.GetHedgeByClientRequest(req.ClientRequestID); existing != nil {
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}

	now := time.Now().UTC()
	hedgeID, _ := uuid.NewV7()
	h := &domain.Hedge{
		ID:              hedgeID.String(),
		Currency:        currency,
		Notional:        req.Notional,
		Tenor:           tenor,
		Type:            htype,
		Status:          domain.StatusPending,
		ClientRequestID: req.ClientRequestID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	// Persist policy context on the hedge record.
	if s.Policy != nil {
		h.PolicyRatio = s.Policy.EffectiveRatio(currency)
		h.PolicyCapUSD = decimal.NewFromFloat(s.Policy.EffectiveCap(currency)) // precision boundary: config float → decimal
		dec := s.Policy.Decide(currency, s.Tracker.GetExposure(currency))
		h.CapBreached = dec.BlockedByCap
		if h.CapBreached {
			s.emit(audit.Event{
				Type:     audit.EventCapBreach,
				Currency: currency,
				HedgeID:  h.ID,
				Detail:   "hedge created while open exposure exceeds cap",
				At:       time.Now().UTC(),
			})
		}
	}

	rate, err := s.Provider.Quote(currency, req.Notional.InexactFloat64(), string(tenor))
	if err != nil {
		h.Status = domain.StatusFailed
		h.UpdatedAt = time.Now().UTC()
		s.Store.CreateHedge(h)
		s.emitHedge(audit.EventHedgeFailed, h, "quote failed: "+err.Error())
		writeJSON(w, http.StatusBadGateway, h)
		return
	}
	h.QuotedRate = decimal.NewFromFloat(rate) // precision boundary: provider float → decimal
	// Feed the quoted rate into the live rate cache (decision-time rate).
	if s.Cache != nil {
		s.Cache.Update(currency, rate, "quote")
	}
	h.Status = domain.StatusExecuting
	h.UpdatedAt = time.Now().UTC()
	s.Store.CreateHedge(h)
	s.emitHedge(audit.EventHedgeCreated, h, "hedge created")

	fills, err := s.Provider.Execute(h)
	if err != nil {
		_, _ = s.Store.UpdateHedge(h.ID, func(stored *domain.Hedge) error {
			stored.Status = domain.StatusFailed
			stored.UpdatedAt = time.Now().UTC()
			return nil
		})
		h.Status = domain.StatusFailed
		s.emitHedge(audit.EventHedgeFailed, h, "execute failed: "+err.Error())
		writeJSON(w, http.StatusBadGateway, s.Store.GetHedge(h.ID))
		return
	}

	// Compute slippage, P&L, samples, cache + recon publish for each fill.
	slippageBPS, pnl, executedNotional := s.processFills(h, fills)

	_, _ = s.Store.UpdateHedge(h.ID, func(stored *domain.Hedge) error {
		stored.Status = domain.StatusExecuted
		stored.Fills = fills
		stored.SlippageBPS = slippageBPS
		stored.PnL = pnl
		stored.UpdatedAt = time.Now().UTC()
		return nil
	})

	// Persist P&L attribution entry.
	slippageCost := decimal.NewFromFloat(slippageBPS).Mul(req.Notional).Div(decimal.NewFromInt(10_000))
	s.Store.AddPnL(domain.PnL{
		Currency:   currency,
		Realized:   pnl,
		Components: domain.PnLComponent{SlippageCost: slippageCost, HedgePnL: pnl},
	})
	s.emit(audit.Event{
		Type:     audit.EventPnLEntry,
		Currency: currency,
		HedgeID:  h.ID,
		Detail:   "pnl entry recorded",
		At:       time.Now().UTC(),
	})

	// Increase hedge coverage by the executed notional.
	s.Tracker.AddCoverage(currency, executedNotional)

	out := s.Store.GetHedge(h.ID)
	s.emitHedge(audit.EventHedgeExecuted, out, "hedge executed")

	// Publish the execution record to Reconciliation.
	s.publishExecutionToRecon(out)
	writeJSON(w, http.StatusCreated, out)
}

// processFills computes per-fill slippage and P&L, records slippage
// samples (skipping idempotent duplicate fills), emits slippage alerts,
// feeds achieved rates back into the live rate cache, and returns the
// aggregate slippage (bps, float64), P&L (decimal), and executed notional
// (decimal).
func (s *Service) processFills(h *domain.Hedge, fills []domain.Fill) (slippageBPS float64, pnl, executedNotional decimal.Decimal) {
	for _, f := range fills {
		if s.Store.HasFill(f.Venue, f.VenueTradeID) {
			continue
		}
		if !h.QuotedRate.IsZero() {
			// precision boundary: decimal → float for bps ratio computation
			fslip := f.Price.Sub(h.QuotedRate).Div(h.QuotedRate).Mul(decimal.NewFromInt(10_000)).InexactFloat64()
			slippageBPS = fslip
			pnl = pnl.Add(h.QuotedRate.Sub(f.Price).Mul(f.Amount))
		}
		s.Store.AddSlippageSample(domain.SlippageSample{
			Pair:         h.Currency + "USD",
			QuotedRate:   h.QuotedRate,
			ExecutedRate: f.Price,
			SlippageBPS:  slippageBPS,
			Timestamp:    f.Timestamp,
		})
		executedNotional = executedNotional.Add(f.Amount)
		if s.Policy != nil && s.Policy.SlippageAlertBPS > 0 && absFloat(slippageBPS) > s.Policy.SlippageAlertBPS {
			s.emit(audit.Event{
				Type:     audit.EventSlippageAlert,
				Currency: h.Currency,
				HedgeID:  h.ID,
				Detail:   "slippage exceeded SLIPPAGE_ALERT_BPS",
				At:       time.Now().UTC(),
			})
		}
		if s.Cache != nil {
			s.Cache.Update(h.Currency, f.Price.InexactFloat64(), "fill") // precision boundary: decimal → cache float
		}
	}
	return slippageBPS, pnl, executedNotional
}

// publishExecutionToRecon publishes per-fill execution records to
// Reconciliation for T+1 matching.
func (s *Service) publishExecutionToRecon(out *domain.Hedge) {
	if s.ReconC == nil || out == nil || len(out.Fills) == 0 {
		return
	}
	for _, f := range out.Fills {
		_ = s.ReconC.PublishExecution(context.Background(), clients.ExecutionRecord{
			HedgeID:      out.ID,
			Currency:     out.Currency,
			Venue:        f.Venue,
			VenueTradeID: f.VenueTradeID,
			Notional:     f.Amount,
			FillPrice:    f.Price,
			QuotedPrice:  out.QuotedRate,
			SlippageBPS:  out.SlippageBPS,
			ExecutedAt:   f.Timestamp.UTC().Format(time.RFC3339),
		})
	}
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
	From       time.Time    `json:"from"`
	To         time.Time    `json:"to"`
	ByCurrency []domain.PnL `json:"by_currency"`
	Total      domain.PnL   `json:"total"`
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
		p.Realized = p.Realized.Add(h.PnL)
		p.Components.HedgePnL = p.Components.HedgePnL.Add(h.PnL)
		slippageCost := decimal.NewFromFloat(h.SlippageBPS).Mul(h.Notional).Div(decimal.NewFromInt(10_000))
		p.Components.SlippageCost = p.Components.SlippageCost.Add(slippageCost)
	}

	resp := pnlResponse{From: from, To: to}
	var total domain.PnL
	for _, p := range byCcy {
		p.Total = p.Realized.Add(p.Unrealized)
		resp.ByCurrency = append(resp.ByCurrency, *p)
		total.Realized = total.Realized.Add(p.Realized)
		total.Unrealized = total.Unrealized.Add(p.Unrealized)
		total.Components.HedgePnL = total.Components.HedgePnL.Add(p.Components.HedgePnL)
		total.Components.SlippageCost = total.Components.SlippageCost.Add(p.Components.SlippageCost)
	}
	total.Currency = "TOTAL"
	total.Total = total.Realized.Add(total.Unrealized)
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

// Slippage handles GET /v1/slippage?pair=&from=&to=. Aggregate slippage
// per currency is fed back into the policy layer to widen the hedge ratio
// for high-slippage currencies (policy tuning).
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
	byCcySum := map[string]float64{}
	byCcyCount := map[string]int{}
	for _, sm := range samples {
		sum += sm.SlippageBPS
		if sm.SlippageBPS > max {
			max = sm.SlippageBPS
		}
		ccy := strings.TrimSuffix(sm.Pair, "USD")
		byCcySum[ccy] += sm.SlippageBPS
		byCcyCount[ccy]++
	}
	if agg.Count > 0 {
		agg.Mean = sum / float64(agg.Count)
	}
	agg.Max = max
	// Feed aggregate slippage back into policy tuning.
	if s.Policy != nil {
		for ccy, totalSlip := range byCcySum {
			if ccy != "" && byCcyCount[ccy] > 0 {
				s.Policy.ApplySlippageTuning(ccy, totalSlip/float64(byCcyCount[ccy]))
			}
		}
	}
	writeJSON(w, http.StatusOK, slippageResponse{
		Pair:       pair,
		From:       from,
		To:         to,
		Samples:    samples,
		Aggregates: agg,
	})
}

// --- settlement netting ---

// Settlement handles GET /v1/settlement, returning netted per-currency
// settlement obligations across current flows and executed hedges, and
// publishing them to Reconciliation for T+1 matching.
func (s *Service) Settlement(w http.ResponseWriter, r *http.Request) {
	flowsPtrs := s.Tracker.AllExposures()
	flows := make([]domain.Exposure, 0, len(flowsPtrs))
	for _, e := range flowsPtrs {
		flows = append(flows, *e)
	}
	hedges := s.Store.AllHedges()
	obs := s.Netter.NetFromFlowsAndHedges(flows, hedges)
	// Emit + publish netted obligations to Reconciliation.
	for _, ob := range obs {
		s.emit(audit.Event{
			Type:     audit.EventSettlement,
			Currency: ob.Currency,
			Detail:   "netted settlement obligation",
			At:       ob.At,
		})
		if s.ReconC != nil {
			_ = s.ReconC.PublishObligation(context.Background(), ob)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"obligations": obs,
		"count":       len(obs),
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

// emit sends an audit event. It also forwards to the downstream
// audit-event-log client when configured, using the hedge id (or currency)
// as the entity key for per-entity ordering.
func (s *Service) emit(e audit.Event) {
	if s.Audit != nil {
		s.Audit.Emit(e)
	}
	if s.AuditC != nil {
		entity := e.HedgeID
		if entity == "" {
			entity = "ccy:" + e.Currency
		}
		eventID := string(e.Type) + ":" + entity + ":" + e.At.UTC().Format(time.RFC3339Nano)
		payload := clients.AuditPayload{
			EventType: string(e.Type),
			Source:    "fx-hedging",
			HedgeID:   e.HedgeID,
			Currency:  e.Currency,
			Detail:    e.Detail,
			At:        e.At.UTC().Format(time.RFC3339),
		}
		_ = s.AuditC.Emit(context.Background(), payload, eventID, entity)
	}
}

// emitHedge sends an audit event for a hedge state change.
func (s *Service) emitHedge(t audit.EventType, h *domain.Hedge, detail string) {
	s.emit(audit.Event{
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

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func absDecimal(x decimal.Decimal) decimal.Decimal {
	return x.Abs()
}

// NewMux builds the HTTP routing mux for the service.
func NewMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Healthz)
	mux.HandleFunc("GET /readyz", svc.Readyz)
	mux.HandleFunc("GET /v1/exposures", svc.ListExposures)
	mux.HandleFunc("GET /v1/exposure/{currency}", svc.GetExposure)
	mux.HandleFunc("POST /v1/exposure/{currency}", svc.AddExposure)
	mux.HandleFunc("GET /v1/hedges", svc.ListHedges)
	mux.HandleFunc("POST /v1/hedges", svc.CreateHedge)
	mux.HandleFunc("GET /v1/hedges/{id}", svc.GetHedge)
	mux.HandleFunc("GET /v1/pnl", svc.PnL)
	mux.HandleFunc("GET /v1/slippage", svc.Slippage)
	mux.HandleFunc("GET /v1/settlement", svc.Settlement)
	return mux
}

// ListHedges handles GET /v1/hedges?currency=&status=.
func (s *Service) ListHedges(w http.ResponseWriter, r *http.Request) {
	currency := strings.ToUpper(r.URL.Query().Get("currency"))
	status := strings.ToUpper(r.URL.Query().Get("status"))
	var all []*domain.Hedge
	if currency != "" {
		all = s.Store.HedgesByCurrency(currency)
	} else {
		all = s.Store.AllHedges()
	}
	if status != "" {
		filtered := all[:0]
		for _, h := range all {
			if string(h.Status) == status {
				filtered = append(filtered, h)
			}
		}
		all = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"hedges": all})
}

// ListExposures handles GET /v1/exposures.
func (s *Service) ListExposures(w http.ResponseWriter, r *http.Request) {
	exposures := s.Tracker.AllExposures()
	if exposures == nil {
		exposures = []*domain.Exposure{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exposures": exposures})
}
