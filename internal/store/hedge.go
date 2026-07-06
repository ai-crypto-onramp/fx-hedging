package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HedgeStatus enumerates hedge lifecycle states.
type HedgeStatus string

const (
	HedgeStatusSubmitted HedgeStatus = "submitted"
	HedgeStatusPartial   HedgeStatus = "partial"
	HedgeStatusFilled    HedgeStatus = "filled"
	HedgeStatusRejected  HedgeStatus = "rejected"
	HedgeStatusCancelled HedgeStatus = "cancelled"
)

// HedgeType enumerates hedge instrument types.
type HedgeType string

const (
	HedgeTypeSpot    HedgeType = "spot"
	HedgeTypeForward HedgeType = "forward"
)

// Hedge represents a row in hedges.
type Hedge struct {
	ID              int64
	Currency        string
	Notional        float64
	Tenor           string
	Type            HedgeType
	Status          HedgeStatus
	QuotedRate      *float64
	PolicyRatio     *float64
	PolicyCapUSD    *float64
	ClientRequestID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// HedgeRepo is the typed repository for hedges.
type HedgeRepo struct {
	pool *pgxpool.Pool
}

// NewHedgeRepo returns a HedgeRepo backed by pool.
func NewHedgeRepo(pool *pgxpool.Pool) *HedgeRepo {
	return &HedgeRepo{pool: pool}
}

// Create inserts a new hedge and returns the generated id. If a hedge with the
// same client_request_id already exists, the existing row is returned (idempotent).
func (r *HedgeRepo) Create(ctx context.Context, h *Hedge) error {
	return r.pool.QueryRow(ctx, `
INSERT INTO hedges (currency, notional, tenor, type, status, quoted_rate,
                    policy_ratio, policy_cap_usd, client_request_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (client_request_id) DO NOTHING
RETURNING id, status, created_at, updated_at`,
		h.Currency, h.Notional, h.Tenor, h.Type, h.Status, h.QuotedRate,
		h.PolicyRatio, h.PolicyCapUSD, h.ClientRequestID,
	).Scan(&h.ID, &h.Status, &h.CreatedAt, &h.UpdatedAt)
}

// Get returns the hedge with id.
func (r *HedgeRepo) Get(ctx context.Context, id int64) (Hedge, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, currency, notional, tenor, type, status, quoted_rate,
       policy_ratio, policy_cap_usd, client_request_id, created_at, updated_at
  FROM hedges
 WHERE id = $1`, id)
	var h Hedge
	if err := scanHedge(row, &h); err != nil {
		return Hedge{}, fmt.Errorf("hedge get: %w", err)
	}
	return h, nil
}

// GetByClientRequestID returns the hedge with the given client_request_id.
func (r *HedgeRepo) GetByClientRequestID(ctx context.Context, clientRequestID string) (Hedge, error) {
	row := r.pool.QueryRow(ctx, `
SELECT id, currency, notional, tenor, type, status, quoted_rate,
       policy_ratio, policy_cap_usd, client_request_id, created_at, updated_at
  FROM hedges
 WHERE client_request_id = $1`, clientRequestID)
	var h Hedge
	if err := scanHedge(row, &h); err != nil {
		return Hedge{}, fmt.Errorf("hedge get by request id: %w", err)
	}
	return h, nil
}

// UpdateStatus sets the status of the hedge with id and refreshes updated_at.
func (r *HedgeRepo) UpdateStatus(ctx context.Context, id int64, status HedgeStatus) error {
	ct, err := r.pool.Exec(ctx, `
UPDATE hedges
   SET status = $2, updated_at = now()
 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("hedge update status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListByStatus returns hedges with status ordered by created_at, up to limit rows.
func (r *HedgeRepo) ListByStatus(ctx context.Context, status HedgeStatus, limit int) ([]Hedge, error) {
	rows, err := r.pool.Query(ctx, `
SELECT id, currency, notional, tenor, type, status, quoted_rate,
       policy_ratio, policy_cap_usd, client_request_id, created_at, updated_at
  FROM hedges
 WHERE status = $1
 ORDER BY created_at DESC
 LIMIT $2`, status, limit)
	if err != nil {
		return nil, fmt.Errorf("hedge list: %w", err)
	}
	defer rows.Close()
	out := make([]Hedge, 0)
	for rows.Next() {
		var h Hedge
		if err := scanHedge(rows, &h); err != nil {
			return nil, fmt.Errorf("hedge scan: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHedge(r rowScanner, h *Hedge) error {
	return r.Scan(
		&h.ID, &h.Currency, &h.Notional, &h.Tenor, &h.Type, &h.Status,
		&h.QuotedRate, &h.PolicyRatio, &h.PolicyCapUSD, &h.ClientRequestID,
		&h.CreatedAt, &h.UpdatedAt,
	)
}