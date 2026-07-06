package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Exposure represents a row in fx_exposures.
type Exposure struct {
	ID         int64
	Currency   string
	Amount     float64
	SourceFlow string
	TS         time.Time
}

// ExposureRepo is the typed repository for fx_exposures.
type ExposureRepo struct {
	pool *pgxpool.Pool
}

// NewExposureRepo returns an ExposureRepo backed by pool.
func NewExposureRepo(pool *pgxpool.Pool) *ExposureRepo {
	return &ExposureRepo{pool: pool}
}

// Create inserts a new exposure snapshot and returns the generated id.
func (r *ExposureRepo) Create(ctx context.Context, e *Exposure) error {
	return r.pool.QueryRow(ctx, `
INSERT INTO fx_exposures (currency, amount, source_flow, ts)
VALUES ($1, $2, $3, $4)
RETURNING id`,
		e.Currency, e.Amount, e.SourceFlow, e.TS,
	).Scan(&e.ID)
}

// Get returns the exposure with id.
func (r *ExposureRepo) Get(ctx context.Context, id int64) (Exposure, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, currency, amount, source_flow, ts
  FROM fx_exposures
 WHERE id = $1`, id)
	var e Exposure
	if err := row.Scan(&e.ID, &e.Currency, &e.Amount, &e.SourceFlow, &e.TS); err != nil {
		return Exposure{}, fmt.Errorf("exposure get: %w", err)
	}
	return e, nil
}

// ListByCurrency returns exposures for currency ordered by ts, up to limit rows.
func (r *ExposureRepo) ListByCurrency(ctx context.Context, currency string, limit int) ([]Exposure, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, currency, amount, source_flow, ts
  FROM fx_exposures
 WHERE currency = $1
 ORDER BY ts DESC
 LIMIT $2`, currency, limit)
	if err != nil {
		return nil, fmt.Errorf("exposure list: %w", err)
	}
	defer rows.Close()
	out := make([]Exposure, 0)
	for rows.Next() {
		var e Exposure
		if err := rows.Scan(&e.ID, &e.Currency, &e.Amount, &e.SourceFlow, &e.TS); err != nil {
			return nil, fmt.Errorf("exposure scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}