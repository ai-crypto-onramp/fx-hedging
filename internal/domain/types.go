package domain

import "time"

// HedgeStatus is the lifecycle state of a hedge.
type HedgeStatus string

const (
	StatusPending    HedgeStatus = "pending"
	StatusExecuting  HedgeStatus = "executing"
	StatusExecuted   HedgeStatus = "executed"
	StatusFailed     HedgeStatus = "failed"
)

// Tenor is the settlement timing of a hedge.
type Tenor string

const (
	TenorSpot     Tenor = "spot"
	TenorForward  Tenor = "forward"
)

// HedgeType is the kind of hedge instrument.
type HedgeType string

const (
	TypeSpot    HedgeType = "spot"
	TypeForward HedgeType = "forward"
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
type Exposure struct {
	Currency      string    `json:"currency"`
	NetAmount     float64   `json:"net_amount"`
	HedgeCoverage float64   `json:"hedge_coverage"`
	OpenAmount    float64   `json:"open_amount"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Fill is a single execution fill for a hedge.
type Fill struct {
	HedgeID      string    `json:"hedge_id"`
	Venue        string    `json:"venue,omitempty"`
	VenueTradeID string    `json:"venue_trade_id"`
	Price        float64   `json:"price"`
	Amount       float64   `json:"amount"`
	Timestamp    time.Time `json:"timestamp"`
}

// Hedge is a hedge record with fills, slippage, and P&L.
type Hedge struct {
	ID              string      `json:"id"`
	Currency        string      `json:"currency"`
	Notional        float64     `json:"notional"`
	Tenor           Tenor       `json:"tenor"`
	Type            HedgeType   `json:"type"`
	Status          HedgeStatus `json:"status"`
	QuotedRate      float64     `json:"quoted_rate"`
	Fills           []Fill      `json:"fills"`
	SlippageBPS     float64     `json:"slippage_bps"`
	PnL             float64     `json:"pnl"`
	ClientRequestID string      `json:"client_request_id,omitempty"`
	PolicyRatio     float64     `json:"policy_ratio,omitempty"`
	PolicyCapUSD    float64     `json:"policy_cap_usd,omitempty"`
	CapBreached     bool        `json:"cap_breached,omitempty"`
	ValueDate       time.Time   `json:"value_date,omitempty"`
	Venue           string      `json:"venue,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// PnLComponent is a named P&L attribution component.
type PnLComponent struct {
	HedgePnL     float64 `json:"hedge_pnl"`
	SpotPnL      float64 `json:"spot_pnl"`
	SlippageCost float64 `json:"slippage_cost"`
}

// PnL is the P&L view for a currency.
type PnL struct {
	Currency   string       `json:"currency"`
	Realized   float64      `json:"realized"`
	Unrealized float64      `json:"unrealized"`
	Total      float64      `json:"total"`
	Components PnLComponent `json:"components"`
}

// SlippageSample is a single quoted-vs-executed rate sample.
type SlippageSample struct {
	Pair         string    `json:"pair"`
	QuotedRate   float64   `json:"quoted_rate"`
	ExecutedRate float64   `json:"executed_rate"`
	SlippageBPS  float64   `json:"slippage_bps"`
	Timestamp    time.Time `json:"timestamp"`
}

// HedgeDecision is the output of the policy layer for a currency: how much
// to hedge, the suggested tenor, and whether the decision is blocked by the
// open-exposure cap.
type HedgeDecision struct {
	Currency     string  `json:"currency"`
	Notional     float64 `json:"notional"`
	TenorHint    Tenor   `json:"tenor_hint"`
	BlockedByCap bool    `json:"blocked_by_cap"`
	Reason       string  `json:"reason,omitempty"`
}

// ExposureEvent is an inbound flow event that contributes to per-currency
// net exposure. EventID is used for idempotency on replay.
type ExposureEvent struct {
	EventID  string    `json:"event_id"`
	Currency string    `json:"currency"`
	Amount   float64   `json:"amount"`
	Source   string    `json:"source,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

// SettlementObligation is a netted per-currency cash obligation to move
// on settlement, produced by the netting engine.
type SettlementObligation struct {
	Currency string    `json:"currency"`
	Amount   float64   `json:"amount"` // signed: positive = receive, negative = pay
	Legs     int       `json:"legs"`
	At       time.Time `json:"at"`
}