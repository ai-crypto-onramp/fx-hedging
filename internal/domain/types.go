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
	VenueTradeID string    `json:"venue_trade_id"`
	Price        float64   `json:"price"`
	Amount       float64   `json:"amount"`
	Timestamp    time.Time `json:"timestamp"`
}

// Hedge is a hedge record with fills, slippage, and P&L.
type Hedge struct {
	ID           string     `json:"id"`
	Currency     string     `json:"currency"`
	Notional     float64    `json:"notional"`
	Tenor        Tenor      `json:"tenor"`
	Type         HedgeType  `json:"type"`
	Status       HedgeStatus `json:"status"`
	QuotedRate   float64    `json:"quoted_rate"`
	Fills        []Fill     `json:"fills"`
	SlippageBPS  float64    `json:"slippage_bps"`
	PnL          float64    `json:"pnl"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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
	Pair          string    `json:"pair"`
	QuotedRate    float64   `json:"quoted_rate"`
	ExecutedRate  float64   `json:"executed_rate"`
	SlippageBPS   float64   `json:"slippage_bps"`
	Timestamp     time.Time `json:"timestamp"`
}