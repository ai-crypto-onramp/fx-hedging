package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// HedgeStatus is the lifecycle state of a hedge.
type HedgeStatus string

const (
	StatusPending   HedgeStatus = "PENDING"
	StatusExecuting HedgeStatus = "EXECUTING"
	StatusExecuted  HedgeStatus = "EXECUTED"
	StatusFailed    HedgeStatus = "FAILED"
)

// Tenor is the settlement timing of a hedge.
type Tenor string

const (
	TenorSpot    Tenor = "SPOT"
	TenorForward Tenor = "FORWARD"
)

// HedgeType is the kind of hedge instrument.
type HedgeType string

const (
	TypeSpot    HedgeType = "SPOT"
	TypeForward HedgeType = "FORWARD"
)

// IsValidTenor reports whether t is a recognized tenor.
func IsValidTenor(t Tenor) bool {
	switch t {
	case TenorSpot, TenorForward:
		return true
	}
	return false
}

// IsValidHedgeType reports whether t is a recognized hedge type.
func IsValidHedgeType(t HedgeType) bool {
	switch t {
	case TypeSpot, TypeForward:
		return true
	}
	return false
}

// Exposure is the per-currency net exposure view.
//
// Money fields (NetAmount, HedgeCoverage, OpenAmount) are decimal.Decimal
// for precision; slippage/policy ratios remain float64 elsewhere.
type Exposure struct {
	Currency      string          `json:"currency"`
	NetAmount     decimal.Decimal `json:"net_amount"`
	HedgeCoverage decimal.Decimal `json:"hedge_coverage"`
	OpenAmount    decimal.Decimal `json:"open_amount"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Fill is a single execution fill for a hedge.
type Fill struct {
	HedgeID      string          `json:"hedge_id"`
	Venue        string          `json:"venue,omitempty"`
	VenueTradeID string          `json:"venue_trade_id"`
	Price        decimal.Decimal `json:"price"`
	Amount       decimal.Decimal `json:"amount"`
	Timestamp    time.Time       `json:"timestamp"`
}

// Hedge is a hedge record with fills, slippage, and P&L.
//
// Money fields (Notional, QuotedRate, PnL, PolicyCapUSD) are decimal.Decimal.
// SlippageBPS and PolicyRatio are dimensionless ratios in bps/0..1 and stay
// float64.
type Hedge struct {
	ID              string          `json:"id"`
	Currency        string          `json:"currency"`
	Notional        decimal.Decimal `json:"notional"`
	Tenor           Tenor           `json:"tenor"`
	Type            HedgeType       `json:"type"`
	Status          HedgeStatus     `json:"status"`
	QuotedRate      decimal.Decimal `json:"quoted_rate"`
	Fills           []Fill          `json:"fills"`
	SlippageBPS     float64         `json:"slippage_bps"`
	PnL             decimal.Decimal `json:"pnl"`
	ClientRequestID string          `json:"client_request_id,omitempty"`
	PolicyRatio     float64         `json:"policy_ratio,omitempty"`
	PolicyCapUSD    decimal.Decimal `json:"policy_cap_usd,omitempty"`
	CapBreached     bool            `json:"cap_breached,omitempty"`
	ValueDate       time.Time       `json:"value_date,omitempty"`
	Venue           string          `json:"venue,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// PnLComponent is a named P&L attribution component.
type PnLComponent struct {
	HedgePnL     decimal.Decimal `json:"hedge_pnl"`
	SpotPnL      decimal.Decimal `json:"spot_pnl"`
	SlippageCost decimal.Decimal `json:"slippage_cost"`
}

// PnL is the P&L view for a currency.
type PnL struct {
	Currency   string          `json:"currency"`
	Realized   decimal.Decimal `json:"realized"`
	Unrealized decimal.Decimal `json:"unrealized"`
	Total      decimal.Decimal `json:"total"`
	Components PnLComponent    `json:"components"`
}

// SlippageSample is a single quoted-vs-executed rate sample.
//
// QuotedRate/ExecutedRate are rate-as-price (decimal); SlippageBPS is a
// dimensionless bps figure (float64).
type SlippageSample struct {
	Pair         string          `json:"pair"`
	QuotedRate   decimal.Decimal `json:"quoted_rate"`
	ExecutedRate decimal.Decimal `json:"executed_rate"`
	SlippageBPS  float64         `json:"slippage_bps"`
	Timestamp    time.Time       `json:"timestamp"`
}

// HedgeDecision is the output of the policy layer for a currency: how much
// to hedge, the suggested tenor, and whether the decision is blocked by the
// open-exposure cap.
type HedgeDecision struct {
	Currency     string          `json:"currency"`
	Notional     decimal.Decimal `json:"notional"`
	TenorHint    Tenor           `json:"tenor_hint"`
	BlockedByCap bool            `json:"blocked_by_cap"`
	Reason       string          `json:"reason,omitempty"`
}

// ExposureEvent is an inbound flow event that contributes to per-currency
// net exposure. EventID is used for idempotency on replay.
type ExposureEvent struct {
	EventID  string          `json:"event_id"`
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"`
	Source   string          `json:"source,omitempty"`
	At       time.Time       `json:"at,omitempty"`
}

// SettlementObligation is a netted per-currency cash obligation to move
// on settlement, produced by the netting engine.
type SettlementObligation struct {
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"` // signed: positive = receive, negative = pay
	Legs     int             `json:"legs"`
	At       time.Time       `json:"at"`
}
