package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PnLComponent enumerates P&L attribution components.
type PnLComponent string

const (
	PnLRevaluation PnLComponent = "revaluation"
	PnLSlippage    PnLComponent = "slippage"
	PnLRealized    PnLComponent = "realized"
)

// PnL represents a row in fx_pnl.
type PnL struct {
	ID       int64
	Currency string
	HedgeID  *int64
	Component PnLComponent
	Amount   float64
	Rate     *float64
	TS       time.Time
}

// PnLRepo is the typed repository for fx_pnl.
type PnLRepo struct {
	pool *pgxpool.Pool
}

// NewPnLRepo returns a PnLRepo backed by pool.
func NewPnLRepo(pool *pgxpool.Pool) *PnLRepo {
	return &PnLRepo{pool: pool}
}

// Create inserts a new P&L entry and returns the generated id and timestamp.
func (r *PnLRepo) Create(ctx context.Context, p *PnL) error {
	return r.pool.QueryRow(ctx, `
INSERT INTO fx_pnl (currency, hedge_id, component, amount, rate)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, ts`,
		p.Currency, p.HedgeID, p.Component, p.Amount, p.Rate,
	).Scan(&p.ID, &p.TS)
}

// Get returns the P&L entry with id.
func (r *PnLRepo) Get(ctx context.Context, id int64) (PnL, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, currency, hedge_id, component, amount, rate, ts
  FROM fx_pnl
 WHERE id = $1`, id)
	var p PnL
	if err := row.Scan(&p.ID, &p.Currency, &p.HedgeID, &p.Component, &p.Amount, &p.Rate, &p.TS); err != nil {
		return PnL{}, fmt.Errorf("pnl get: %w", err)
	}
	return p, nil
}

// ListByCurrency returns P&L entries for currency within [from, to] ordered by ts, up to limit rows.
func (r *PnLRepo) ListByCurrency(ctx context.Context, currency string, from, to time.Time, limit int) ([]PnL, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, currency, hedge_id, component, amount, rate, ts
  FROM fx_pnl
 WHERE currency = $1 AND ts >= $2 AND ts < $3
 ORDER BY ts DESC
 LIMIT $4`, currency, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("pnl list: %w", err)
	}
	defer rows.Close()
	out := make([]PnL, 0)
	for rows.Next() {
		var p PnL
		if err := rows.Scan(&p.ID, &p.Currency, &p.HedgeID, &p.Component, &p.Amount, &p.Rate, &p.TS); err != nil {
			return nil, fmt.Errorf("pnl scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}