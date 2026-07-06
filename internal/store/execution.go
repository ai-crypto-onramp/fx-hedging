package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExecutionStatus enumerates hedge execution lifecycle states.
type ExecutionStatus string

const (
	ExecutionStatusFilled   ExecutionStatus = "filled"
	ExecutionStatusPartial  ExecutionStatus = "partial"
	ExecutionStatusRejected ExecutionStatus = "rejected"
)

// HedgeExecution represents a row in hedge_executions.
type HedgeExecution struct {
	ID           int64
	HedgeID      int64
	Venue        string
	VenueTradeID string
	FillPrice    float64
	FillAmount   float64
	QuotedPrice  *float64
	SlippageBPS  *float64
	Status        ExecutionStatus
	ExecutedAt   time.Time
}

// HedgeExecutionRepo is the typed repository for hedge_executions.
type HedgeExecutionRepo struct {
	pool *pgxpool.Pool
}

// NewHedgeExecutionRepo returns a HedgeExecutionRepo backed by pool.
func NewHedgeExecutionRepo(pool *pgxpool.Pool) *HedgeExecutionRepo {
	return &HedgeExecutionRepo{pool: pool}
}

// Create inserts a new hedge execution. Idempotent on (hedge_id, venue_trade_id).
func (r *HedgeExecutionRepo) Create(ctx context.Context, e *HedgeExecution) error {
	return r.pool.QueryRow(ctx, `
INSERT INTO hedge_executions (hedge_id, venue, venue_trade_id, fill_price,
                               fill_amount, quoted_price, slippage_bps, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (hedge_id, venue_trade_id) DO NOTHING
RETURNING id, executed_at`,
		e.HedgeID, e.Venue, e.VenueTradeID, e.FillPrice, e.FillAmount,
		e.QuotedPrice, e.SlippageBPS, e.Status,
	).Scan(&e.ID, &e.ExecutedAt)
}

// Get returns the hedge execution with id.
func (r *HedgeExecutionRepo) Get(ctx context.Context, id int64) (HedgeExecution, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, hedge_id, venue, venue_trade_id, fill_price, fill_amount,
       quoted_price, slippage_bps, status, executed_at
  FROM hedge_executions
 WHERE id = $1`, id)
	var e HedgeExecution
	if err := scanExecution(row, &e); err != nil {
		return HedgeExecution{}, fmt.Errorf("execution get: %w", err)
	}
	return e, nil
}

// ListByHedge returns executions for hedge_id ordered by executed_at, up to limit rows.
func (r *HedgeExecutionRepo) ListByHedge(ctx context.Context, hedgeID int64, limit int) ([]HedgeExecution, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, hedge_id, venue, venue_trade_id, fill_price, fill_amount,
       quoted_price, slippage_bps, status, executed_at
  FROM hedge_executions
 WHERE hedge_id = $1
 ORDER BY executed_at DESC
 LIMIT $2`, hedgeID, limit)
	if err != nil {
		return nil, fmt.Errorf("execution list: %w", err)
	}
	defer rows.Close()
	out := make([]HedgeExecution, 0)
	for rows.Next() {
		var e HedgeExecution
		if err := scanExecution(rows, &e); err != nil {
			return nil, fmt.Errorf("execution scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountByHedge returns the number of executions recorded for hedgeID.
func (r *HedgeExecutionRepo) CountByHedge(ctx context.Context, hedgeID int64) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM hedge_executions WHERE hedge_id = $1`, hedgeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("execution count: %w", err)
	}
	return n, nil
}

type execScanner interface {
	Scan(dest ...any) error
}

func scanExecution(r execScanner, e *HedgeExecution) error {
	return r.Scan(
		&e.ID, &e.HedgeID, &e.Venue, &e.VenueTradeID, &e.FillPrice,
		&e.FillAmount, &e.QuotedPrice, &e.SlippageBPS, &e.Status, &e.ExecutedAt,
	)
}