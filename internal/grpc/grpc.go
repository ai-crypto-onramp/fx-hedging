// Package grpc implements the internal gRPC service consumed by Pricing
// / Quote (GetLiveRate) and Treasury Orchestration (GetNetExposure,
// StreamExposure, SubmitHedgePlan). The server adapts the in-memory
// exposure tracker, the rate cache, the execution router, and the policy
// layer to the generated fxpb service interface.
package grpc

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/executor"
	fxpb "github.com/ai-crypto-onramp/fx-hedging/proto/fx/v1"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/ratecache"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"

	"github.com/google/uuid"
)

// Services bundles the dependencies used by the gRPC server.
type Services struct {
	Tracker  *exposure.Tracker
	Cache    *ratecache.Cache
	Policy   *policy.Policy
	Router   *executor.Router
	Store    store.Store
	Now      func() time.Time
}

// Server implements fxpb.FXServer.
type Server struct {
	fxpb.UnimplementedFXServer
	s *Services
}

// NewAdapter returns a gRPC server adapter over the given services.
func NewAdapter(s *Services) *Server {
	if s.Now == nil {
		s.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Server{s: s}
}

// GetLiveRate returns the cached live FX rate for a currency. If the rate
// is stale (older than the cache TTL) the call returns an error so the
// caller (Pricing / Quote) can fall back to an alternate source.
func (g *Server) GetLiveRate(ctx context.Context, req *fxpb.GetLiveRateRequest) (*fxpb.GetLiveRateResponse, error) {
	ccy := req.GetCurrency()
	if ccy == "" {
		return nil, errors.New("currency is required")
	}
	r, err := g.s.Cache.Get(ccy)
	if err != nil {
		return nil, err
	}
	// Cross-check the live rate against the revaluation rate used in P&L.
	// If they diverge by more than 100 bps, flag stale so callers fall back
	// rather than quote off a rate inconsistent with book revaluation.
	if !g.s.Cache.CrossCheck(ccy, 100) {
		return &fxpb.GetLiveRateResponse{
			Currency: ccy,
			Rate:     r.Rate,
			TsUnixNanos: r.At.UnixNano(),
			Stale: true,
		}, errors.New("live rate diverges from revaluation rate")
	}
	return &fxpb.GetLiveRateResponse{
		Currency:    ccy,
		Rate:        r.Rate,
		TsUnixNanos: r.At.UnixNano(),
		Stale:       false,
	}, nil
}

// GetNetExposure returns the current net exposure for a currency.
func (g *Server) GetNetExposure(ctx context.Context, req *fxpb.GetNetExposureRequest) (*fxpb.GetNetExposureResponse, error) {
	ccy := req.GetCurrency()
	if ccy == "" {
		return nil, errors.New("currency is required")
	}
	exp := g.s.Tracker.GetExposure(ccy)
	if exp == nil {
		return &fxpb.GetNetExposureResponse{Currency: ccy, TsUnixNanos: g.s.Now().UnixNano()}, nil
	}
	return &fxpb.GetNetExposureResponse{
		Currency:     exp.Currency,
		NetAmount:    exp.NetAmount,
		HedgeCoverage: exp.HedgeCoverage,
		OpenAmount:   exp.OpenAmount,
		TsUnixNanos:  exp.UpdatedAt.UnixNano(),
	}, nil
}

// StreamExposure streams exposure snapshots for a currency. The stream
// stays open until the client cancels or the context is done. A snapshot
// is pushed immediately (the current exposure) and then on each update
// via the tracker subscriber channel.
func (g *Server) StreamExposure(req *fxpb.StreamExposureRequest, stream fxpb.FX_StreamExposureServer) error {
	ccy := req.GetCurrency()
	if ccy == "" {
		return errors.New("currency is required")
	}
	ctx := stream.Context()
	ch := make(chan *domain.Exposure, 16)
	g.s.Tracker.Subscribe(ch)
	defer g.s.Tracker.Unsubscribe(ch)
	if exp := g.s.Tracker.GetExposure(ccy); exp != nil {
		if err := stream.Send(snapshotToProto(exp)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err()
		case exp, ok := <-ch:
			if !ok {
				return io.EOF
			}
			if exp.Currency != ccy {
				continue
			}
			if err := stream.Send(snapshotToProto(exp)); err != nil {
				return err
			}
		}
	}
}

func snapshotToProto(e *domain.Exposure) *fxpb.ExposureSnapshot {
	return &fxpb.ExposureSnapshot{
		Currency:     e.Currency,
		NetAmount:    e.NetAmount,
		HedgeCoverage: e.HedgeCoverage,
		OpenAmount:   e.OpenAmount,
		TsUnixNanos:  e.UpdatedAt.UnixNano(),
	}
}

// SubmitHedgePlan submits a batched hedge plan from Treasury Orchestration.
// Each leg is executed via the router; legs whose tenor is "forward" map
// to forward contracts in Stage 4 (the router submits them with the
// forward tenor + value date). Plans that would breach MAX_OPEN_EXPOSURE_USD
// are rejected with a reason and no legs are executed.
func (g *Server) SubmitHedgePlan(ctx context.Context, req *fxpb.SubmitHedgePlanRequest) (*fxpb.SubmitHedgePlanResponse, error) {
	planID := req.GetPlanId()
	if planID == "" {
		planIDv7, _ := uuid.NewV7()
		planID = "plan-" + planIDv7.String()
	}

	// Coordinate with the policy layer: reject plans that would breach the
	// open cap. Sum the proposed notional per currency and check the
	// resulting open exposure against the effective cap.
	proposed := map[string]float64{}
	for _, leg := range req.GetLegs() {
		proposed[leg.GetCurrency()] += leg.GetNotional()
	}
	for ccy, addNotional := range proposed {
		exp := g.s.Tracker.GetExposure(ccy)
		var open float64
		if exp != nil {
			open = exp.OpenAmount - addNotional
		} else {
			open = -addNotional
		}
		cap := g.s.Policy.EffectiveCap(ccy)
		if absFloat(open) > cap {
			return &fxpb.SubmitHedgePlanResponse{
				PlanId:       planID,
				Rejected:     true,
				RejectReason: "plan would breach MAX_OPEN_EXPOSURE_USD for " + ccy + " (open " + strconv.FormatFloat(open, 'f', 2, 64) + " > cap " + strconv.FormatFloat(cap, 'f', 2, 64) + ")",
			}, nil
		}
	}

	results := make([]*fxpb.HedgePlanLegResult, 0, len(req.GetLegs()))
	for _, leg := range req.GetLegs() {
		results = append(results, g.executeLeg(ctx, planID, leg))
	}
	return &fxpb.SubmitHedgePlanResponse{
		PlanId:  planID,
		Results: results,
	}, nil
}

func (g *Server) executeLeg(ctx context.Context, planID string, leg *fxpb.HedgePlanLeg) *fxpb.HedgePlanLegResult {
	ccy := leg.GetCurrency()
	if ccy == "" {
		return &fxpb.HedgePlanLegResult{Error: "currency is required"}
	}
	tenor := domain.Tenor(leg.GetTenor())
	if !domain.IsValidTenor(tenor) {
		// Default to spot when Treasury omits the tenor.
		tenor = domain.TenorSpot
	}
	htype := domain.TypeSpot
	if tenor == domain.TenorForward {
		htype = domain.TypeForward
	}
	notional := leg.GetNotional()
	if notional <= 0 {
		return &fxpb.HedgePlanLegResult{Currency: ccy, Error: "notional must be positive"}
	}

	// Map Treasury float tenor requests to forward contracts: a non-empty
	// value_date on a forward leg is preserved on the hedge record.
	var valueDate time.Time
	if leg.GetValueDate() != "" {
		if vd, err := time.Parse("2006-01-02", leg.GetValueDate()); err == nil {
			valueDate = vd
		}
	}

	hedgeID, _ := uuid.NewV7()
	reqIDv7, _ := uuid.NewV7()
	h := &domain.Hedge{
		ID:              hedgeID.String(),
		Currency:        ccy,
		Notional:        notional,
		Tenor:           tenor,
		Type:            htype,
		Status:          domain.StatusPending,
		ClientRequestID: planID + ":" + ccy + ":" + reqIDv7.String(),
		ValueDate:       valueDate,
		CreatedAt:       g.s.Now(),
		UpdatedAt:       g.s.Now(),
	}
	if h.ClientRequestID != "" {
		if existing := g.s.Store.GetHedgeByClientRequest(h.ClientRequestID); existing != nil {
			return legResultFromHedge(existing, "duplicate")
		}
	}

	dec := g.s.Policy.Decide(ccy, g.s.Tracker.GetExposure(ccy))
	h.PolicyRatio = g.s.Policy.EffectiveRatio(ccy)
	h.PolicyCapUSD = g.s.Policy.EffectiveCap(ccy)
	h.CapBreached = dec.BlockedByCap

	fills, err := g.s.Router.RouteAndExecute(ctx, h)
	if err != nil {
		h.Status = domain.StatusFailed
		g.s.Store.CreateHedge(h)
		return &fxpb.HedgePlanLegResult{Currency: ccy, HedgeId: h.ID, Status: string(domain.StatusFailed), Notional: notional, Error: err.Error()}
	}

	var slippageBPS float64
	for _, f := range fills {
		if h.QuotedRate != 0 {
			fslip := (f.Price - h.QuotedRate) / h.QuotedRate * 10_000.0
			slippageBPS = fslip
		}
		g.s.Store.AddSlippageSample(domain.SlippageSample{
			Pair:         ccy + "USD",
			QuotedRate:   h.QuotedRate,
			ExecutedRate: f.Price,
			SlippageBPS:  slippageBPS,
			Timestamp:    f.Timestamp,
		})
	}
	h.Fills = fills
	h.Status = domain.StatusExecuted
	h.SlippageBPS = slippageBPS
	h.UpdatedAt = g.s.Now()
	g.s.Store.CreateHedge(h)
	g.s.Tracker.AddCoverage(ccy, notional)
	return legResultFromHedge(h, "")
}

func legResultFromHedge(h *domain.Hedge, errStr string) *fxpb.HedgePlanLegResult {
	return &fxpb.HedgePlanLegResult{
		Currency:    h.Currency,
		HedgeId:     h.ID,
		Status:      string(h.Status),
		Notional:    h.Notional,
		QuotedRate:  h.QuotedRate,
		SlippageBps: h.SlippageBPS,
		Error:       errStr,
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}