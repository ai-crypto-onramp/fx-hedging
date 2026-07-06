package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SlippageSample represents a row in slippage_samples.
type SlippageSample struct {
	ID           int64
	HedgeID      int64
	ExecutionID  int64
	CurrencyPair string
	QuotedRate   float64
	FillRate     float64
	SlippageBPS  float64
	TS           time.Time
}

// SlippageRepo is the typed repository for slippage_samples.
type SlippageRepo struct {
	pool *pgxpool.Pool
}

// NewSlippageRepo returns a SlippageRepo backed by pool.
func NewSlippageRepo(pool *pgxpool.Pool) *SlippageRepo {
	return &SlippageRepo{pool: pool}
}

// Create inserts a new slippage sample and returns the generated id and timestamp.
func (r *SlippageRepo) Create(ctx context.Context, s *SlippageSample) error {
	return r.pool.QueryRow(ctx, `
INSERT INTO slippage_samples (hedge_id, execution_id, currency_pair,
                              quoted_rate, fill_rate, slippage_bps)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, ts`,
		s.HedgeID, s.ExecutionID, s.CurrencyPair,
		s.QuotedRate, s.FillRate, s.SlippageBPS,
	).Scan(&s.ID, &s.TS)
}

// Get returns the slippage sample with id.
func (r *SlippageRepo) Get(ctx context.Context, id int64) (SlippageSample, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, hedge_id, execution_id, currency_pair, quoted_rate,
       fill_rate, slippage_bps, ts
  FROM slippage_samples
 WHERE id = $1`, id)
	var s SlippageSample
	if err := row.Scan(&s.ID, &s.HedgeID, &s.ExecutionID, &s.CurrencyPair,
		&s.QuotedRate, &s.FillRate, &s.SlippageBPS, &s.TS); err != nil {
		return SlippageSample{}, fmt.Errorf("slippage get: %w", err)
	}
	return s, nil
}

// ListByPair returns slippage samples for currencyPair within [from, to] ordered by ts, up to limit rows.
func (r *SlippageRepo) ListByPair(ctx context.Context, currencyPair string, from, to time.Time, limit int) ([]SlippageSample, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, hedge_id, execution_id, currency_pair, quoted_rate,
       fill_rate, slippage_bps, ts
  FROM slippage_samples
 WHERE currency_pair = $1 AND ts >= $2 AND ts < $3
 ORDER BY ts DESC
 LIMIT $4`, currencyPair, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("slippage list: %w", err)
	}
	defer rows.Close()
	out := make([]SlippageSample, 0)
	for rows.Next() {
		var s SlippageSample
		if err := rows.Scan(&s.ID, &s.HedgeID, &s.ExecutionID, &s.CurrencyPair,
			&s.QuotedRate, &s.FillRate, &s.SlippageBPS, &s.TS); err != nil {
			return nil, fmt.Errorf("slippage scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}