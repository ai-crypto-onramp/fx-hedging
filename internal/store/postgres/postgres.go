package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store/migrations"
)

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	runner := migrations.NewRunner(
		func(c context.Context, q string, args ...any) error {
			_, err := pool.Exec(c, q, args...)
			return err
		},
		func(c context.Context, version string) (bool, error) {
			var exists bool
			err := pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists)
			return exists, err
		},
	)
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

func (d *DB) Pool() *pgxpool.Pool { return d.pool }

func (d *DB) CreateHedge(h *domain.Hedge) {
	ctx := context.Background()
	valueDate := h.ValueDate
	if valueDate.IsZero() {
		_, _ = d.pool.Exec(ctx, `INSERT INTO hedges
		(id, currency, notional, tenor, type, status, quoted_rate, slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			h.ID, h.Currency, h.Notional, string(h.Tenor), string(h.Type), string(h.Status),
			h.QuotedRate, h.SlippageBPS, h.PnL, h.ClientRequestID, h.PolicyRatio, h.PolicyCapUSD, h.CapBreached,
			h.CreatedAt, h.UpdatedAt)
	} else {
		_, _ = d.pool.Exec(ctx, `INSERT INTO hedges
		(id, currency, notional, tenor, type, status, quoted_rate, slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, value_date, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			h.ID, h.Currency, h.Notional, string(h.Tenor), string(h.Type), string(h.Status),
			h.QuotedRate, h.SlippageBPS, h.PnL, h.ClientRequestID, h.PolicyRatio, h.PolicyCapUSD, h.CapBreached,
			valueDate, h.CreatedAt, h.UpdatedAt)
	}
	d.syncFills(ctx, h)
}

func (d *DB) syncFills(ctx context.Context, h *domain.Hedge) {
	if len(h.Fills) == 0 {
		return
	}
	for _, f := range h.Fills {
		fillID, _ := uuid.NewV7()
		_, _ = d.pool.Exec(ctx, `INSERT INTO hedge_executions
		(id, hedge_id, venue, venue_trade_id, fill_price, quoted_price, slippage_bps, amount, ts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (venue, venue_trade_id) DO UPDATE SET
			fill_price=EXCLUDED.fill_price, quoted_price=EXCLUDED.quoted_price,
			slippage_bps=EXCLUDED.slippage_bps, amount=EXCLUDED.amount, ts=EXCLUDED.ts, updated_at=now()`,
			fillID, h.ID, f.Venue, f.VenueTradeID, f.Price, h.QuotedRate, h.SlippageBPS, f.Amount, f.Timestamp)
	}
}

func (d *DB) GetHedgeByClientRequest(reqID string) *domain.Hedge {
	if reqID == "" {
		return nil
	}
	ctx := context.Background()
	var h domain.Hedge
	var tenor, status, htype string
	var valueDate time.Time
	err := d.pool.QueryRow(ctx, `SELECT id, currency, notional, tenor, type, status, quoted_rate,
		slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, value_date, created_at, updated_at
		FROM hedges WHERE client_request_id=$1`, reqID).
		Scan(&h.ID, &h.Currency, &h.Notional, &tenor, &htype, &status, &h.QuotedRate,
			&h.SlippageBPS, &h.PnL, &h.ClientRequestID, &h.PolicyRatio, &h.PolicyCapUSD, &h.CapBreached,
			&valueDate, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil
	}
	h.Tenor = domain.Tenor(tenor)
	h.Type = domain.HedgeType(htype)
	h.Status = domain.HedgeStatus(status)
	if !valueDate.IsZero() {
		h.ValueDate = valueDate
	}
	if fills, err := d.loadFills(ctx, h.ID); err == nil {
		h.Fills = fills
	}
	return &h
}

func (d *DB) HasFill(venue, venueTradeID string) bool {
	if venue == "" || venueTradeID == "" {
		return false
	}
	ctx := context.Background()
	var exists bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hedge_executions WHERE venue=$1 AND venue_trade_id=$2)`, venue, venueTradeID).Scan(&exists)
	if err != nil {
		return false
	}
	return exists
}

func (d *DB) GetHedge(id string) *domain.Hedge {
	ctx := context.Background()
	var h domain.Hedge
	var tenor, status, htype string
	var valueDate time.Time
	err := d.pool.QueryRow(ctx, `SELECT id, currency, notional, tenor, type, status, quoted_rate,
		slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, value_date, created_at, updated_at
		FROM hedges WHERE id=$1`, id).
		Scan(&h.ID, &h.Currency, &h.Notional, &tenor, &htype, &status, &h.QuotedRate,
			&h.SlippageBPS, &h.PnL, &h.ClientRequestID, &h.PolicyRatio, &h.PolicyCapUSD, &h.CapBreached,
			&valueDate, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil
	}
	h.Tenor = domain.Tenor(tenor)
	h.Type = domain.HedgeType(htype)
	h.Status = domain.HedgeStatus(status)
	if !valueDate.IsZero() {
		h.ValueDate = valueDate
	}
	if fills, err := d.loadFills(ctx, h.ID); err == nil {
		h.Fills = fills
	}
	return &h
}

func (d *DB) loadFills(ctx context.Context, hedgeID string) ([]domain.Fill, error) {
	rows, err := d.pool.Query(ctx, `SELECT hedge_id, venue, venue_trade_id, fill_price, amount, ts
		FROM hedge_executions WHERE hedge_id=$1 ORDER BY ts ASC`, hedgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Fill
	for rows.Next() {
		var f domain.Fill
		if err := rows.Scan(&f.HedgeID, &f.Venue, &f.VenueTradeID, &f.Price, &f.Amount, &f.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (d *DB) UpdateHedge(id string, fn func(*domain.Hedge) error) (*domain.Hedge, error) {
	ctx := context.Background()
	h := d.GetHedge(id)
	if h == nil {
		return nil, store.ErrNotFound
	}
	if err := fn(h); err != nil {
		return nil, err
	}
	valueDate := h.ValueDate
	if valueDate.IsZero() {
		_, err := d.pool.Exec(ctx, `UPDATE hedges SET currency=$2, notional=$3, tenor=$4, type=$5,
			status=$6, quoted_rate=$7, slippage_bps=$8, pnl=$9, client_request_id=$10,
			policy_ratio=$11, policy_cap_usd=$12, cap_breached=$13, value_date=NULL, updated_at=$14
			WHERE id=$1`,
			id, h.Currency, h.Notional, string(h.Tenor), string(h.Type),
			string(h.Status), h.QuotedRate, h.SlippageBPS, h.PnL, h.ClientRequestID,
			h.PolicyRatio, h.PolicyCapUSD, h.CapBreached, h.UpdatedAt)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := d.pool.Exec(ctx, `UPDATE hedges SET currency=$2, notional=$3, tenor=$4, type=$5,
			status=$6, quoted_rate=$7, slippage_bps=$8, pnl=$9, client_request_id=$10,
			policy_ratio=$11, policy_cap_usd=$12, cap_breached=$13, value_date=$14, updated_at=$15
			WHERE id=$1`,
			id, h.Currency, h.Notional, string(h.Tenor), string(h.Type),
			string(h.Status), h.QuotedRate, h.SlippageBPS, h.PnL, h.ClientRequestID,
			h.PolicyRatio, h.PolicyCapUSD, h.CapBreached, valueDate, h.UpdatedAt)
		if err != nil {
			return nil, err
		}
	}
	d.syncFills(ctx, h)
	return d.GetHedge(id), nil
}

func (d *DB) HedgesByCurrency(currency string) []*domain.Hedge {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, currency, notional, tenor, type, status, quoted_rate,
		slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, value_date, created_at, updated_at
		FROM hedges WHERE currency=$1 ORDER BY created_at ASC`, currency)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return d.scanHedges(ctx, rows)
}

func (d *DB) AllHedges() []*domain.Hedge {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, currency, notional, tenor, type, status, quoted_rate,
		slippage_bps, pnl, client_request_id, policy_ratio, policy_cap_usd, cap_breached, value_date, created_at, updated_at
		FROM hedges ORDER BY created_at ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return d.scanHedges(ctx, rows)
}

func (d *DB) scanHedges(ctx context.Context, rows pgx.Rows) []*domain.Hedge {
	out := make([]*domain.Hedge, 0)
	for rows.Next() {
		var h domain.Hedge
		var tenor, status, htype string
		var valueDate time.Time
		if err := rows.Scan(&h.ID, &h.Currency, &h.Notional, &tenor, &htype, &status, &h.QuotedRate,
			&h.SlippageBPS, &h.PnL, &h.ClientRequestID, &h.PolicyRatio, &h.PolicyCapUSD, &h.CapBreached,
			&valueDate, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return out
		}
		h.Tenor = domain.Tenor(tenor)
		h.Type = domain.HedgeType(htype)
		h.Status = domain.HedgeStatus(status)
		if !valueDate.IsZero() {
			h.ValueDate = valueDate
		}
		if fills, err := d.loadFills(ctx, h.ID); err == nil {
			h.Fills = fills
		}
		out = append(out, &h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (d *DB) AddSlippageSample(sample domain.SlippageSample) {
	ctx := context.Background()
	sampleID, _ := uuid.NewV7()
	_, _ = d.pool.Exec(ctx, `INSERT INTO slippage_samples
	(id, pair, quoted_rate, executed_rate, slippage_bps, ts)
	VALUES ($1,$2,$3,$4,$5,$6)`,
		sampleID, sample.Pair, sample.QuotedRate, sample.ExecutedRate, sample.SlippageBPS, sample.Timestamp)
}

func (d *DB) AddPnL(p domain.PnL) {
	ctx := context.Background()
	pnlID, _ := uuid.NewV7()
	_, _ = d.pool.Exec(ctx, `INSERT INTO fx_pnl
	(id, currency, component, realized, unrealized, rate, ts)
	VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		pnlID, p.Currency, "HEDGE_PNL", p.Realized, p.Unrealized, p.Components.SlippageCost, time.Now().UTC())
}

func (d *DB) AllPnL() []domain.PnL {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT currency, realized, unrealized, rate FROM fx_pnl ORDER BY id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.PnL
	for rows.Next() {
		var p domain.PnL
		var slippageCost float64
		if err := rows.Scan(&p.Currency, &p.Realized, &p.Unrealized, &slippageCost); err != nil {
			return out
		}
		p.Total = p.Realized + p.Unrealized
		p.Components.HedgePnL = p.Realized
		p.Components.SlippageCost = slippageCost
		out = append(out, p)
	}
	return out
}

func (d *DB) AppendExposureSnapshot(e *domain.Exposure) {
	ctx := context.Background()
	expID, _ := uuid.NewV7()
	_, _ = d.pool.Exec(ctx, `INSERT INTO fx_exposures
	(id, currency, net_amount, hedge_coverage, open_amount, ts)
	VALUES ($1,$2,$3,$4,$5,$6)`,
		expID, e.Currency, e.NetAmount, e.HedgeCoverage, e.OpenAmount, e.UpdatedAt)
}

func (d *DB) ExposureSnapshots(currency string) []domain.Exposure {
	ctx := context.Background()
	q := `SELECT currency, net_amount, hedge_coverage, open_amount, ts FROM fx_exposures`
	args := []any{}
	if currency != "" {
		q += " WHERE currency=$1"
		args = append(args, currency)
	}
	q += " ORDER BY ts ASC"
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.Exposure
	for rows.Next() {
		var e domain.Exposure
		if err := rows.Scan(&e.Currency, &e.NetAmount, &e.HedgeCoverage, &e.OpenAmount, &e.UpdatedAt); err != nil {
			return out
		}
		out = append(out, e)
	}
	return out
}

func (d *DB) SlippageSamples(pair string, from, to time.Time) []domain.SlippageSample {
	ctx := context.Background()
	q := `SELECT pair, quoted_rate, executed_rate, slippage_bps, ts FROM slippage_samples`
	args := []any{}
	conds := []string{}
	if pair != "" {
		conds = append(conds, "pair=$"+strconv.Itoa(len(args)+1))
		args = append(args, pair)
	}
	if !from.IsZero() {
		conds = append(conds, "ts>=$"+strconv.Itoa(len(args)+1))
		args = append(args, from)
	}
	if !to.IsZero() {
		conds = append(conds, "ts<=$"+strconv.Itoa(len(args)+1))
		args = append(args, to)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY ts ASC"
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.SlippageSample
	for rows.Next() {
		var s domain.SlippageSample
		if err := rows.Scan(&s.Pair, &s.QuotedRate, &s.ExecutedRate, &s.SlippageBPS, &s.Timestamp); err != nil {
			return out
		}
		out = append(out, s)
	}
	return out
}

var _ store.Store = (*DB)(nil)

var _ = errors.New