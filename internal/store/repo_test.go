package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// skipIfNoDB skips tests requiring a live Postgres. The acceptance criterion
// "go test ./internal/store/... passes against an ephemeral Postgres" is
// exercised in CI where DB_URL is set; locally these tests skip.
func skipIfNoDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		t.Skip("DB_URL not set; skipping live Postgres test")
	}
	return dsn
}

func TestMigrateAppliesAllMigrations(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	cfg := Config{DBURL: dsn}
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	for _, table := range []string{
		"fx_exposures", "hedges", "hedge_executions", "fx_pnl",
		"slippage_samples", "schema_migrations",
	} {
		var exists bool
		row := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table)
		if err := row.Scan(&exists); err != nil || !exists {
			t.Errorf("table %q missing after migrate (err=%v, exists=%v)", table, err, exists)
		}
	}

	// Idempotent: running again must not error.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}

func TestMigrateDownReversesSchema(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	cfg := Config{DBURL: dsn}
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	if err := MigrateDown(ctx, pool); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	for _, table := range []string{
		"fx_exposures", "hedges", "hedge_executions", "fx_pnl", "slippage_samples",
	} {
		var exists bool
		row := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table)
		if err := row.Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if exists {
			t.Errorf("table %q still present after migrate down", table)
		}
	}
	// Re-apply so subsequent tests in this run see a migrated schema.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("re-up migrate: %v", err)
	}
}

func TestExposureRepoCreateGetList(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	repo := NewExposureRepo(pool)
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	in := &Exposure{Currency: "EUR", Amount: 125000.50, SourceFlow: "payment", TS: ts}
	if err := repo.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("expected id populated")
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Currency != "EUR" || got.SourceFlow != "payment" {
		t.Fatalf("got = %+v", got)
	}

	list, err := repo.ListByCurrency(ctx, "EUR", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected at least one exposure row")
	}
}

func TestHedgeRepoCreateIdempotentAndGet(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	// Clean prior rows for this request id to make the test deterministic.
	if _, err := pool.Exec(ctx, `DELETE FROM hedges WHERE client_request_id = $1`, "test-stage1-req-1"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	repo := NewHedgeRepo(pool)
	in := &Hedge{
		Currency: "EUR", Notional: 250000, Tenor: "spot", Type: HedgeTypeSpot,
		Status: HedgeStatusSubmitted, ClientRequestID: "test-stage1-req-1",
	}
	if err := repo.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	firstID := in.ID

	// Duplicate submission with same client_request_id must be idempotent.
	dup := &Hedge{
		Currency: "EUR", Notional: 250000, Tenor: "spot", Type: HedgeTypeSpot,
		Status: HedgeStatusSubmitted, ClientRequestID: "test-stage1-req-1",
	}
	if err := repo.Create(ctx, dup); err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if dup.ID != firstID {
		t.Errorf("idempotent create: dup.ID = %d, want %d", dup.ID, firstID)
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != HedgeStatusSubmitted {
		t.Errorf("status = %q, want submitted", got.Status)
	}

	if err := repo.UpdateStatus(ctx, in.ID, HedgeStatusFilled); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated, _ := repo.Get(ctx, in.ID)
	if updated.Status != HedgeStatusFilled {
		t.Errorf("after update status = %q, want filled", updated.Status)
	}

	byReq, err := repo.GetByClientRequestID(ctx, "test-stage1-req-1")
	if err != nil {
		t.Fatalf("get by request id: %v", err)
	}
	if byReq.ID != in.ID {
		t.Errorf("by request id = %d, want %d", byReq.ID, in.ID)
	}
}

func TestHedgeExecutionRepoCreateIdempotentAndList(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	// Set up a parent hedge.
	hrepo := NewHedgeRepo(pool)
	if _, err := pool.Exec(ctx, `DELETE FROM hedges WHERE client_request_id = $1`, "test-stage1-exec-req"); err != nil {
		t.Fatalf("cleanup hedges: %v", err)
	}
	h := &Hedge{
		Currency: "USD", Notional: 100000, Tenor: "spot", Type: HedgeTypeSpot,
		Status: HedgeStatusSubmitted, ClientRequestID: "test-stage1-exec-req",
	}
	if err := hrepo.Create(ctx, h); err != nil {
		t.Fatalf("hedge create: %v", err)
	}

	repo := NewHedgeExecutionRepo(pool)
	qr := 1.0850
	in := &HedgeExecution{
		HedgeID: h.ID, Venue: "bank-fx", VenueTradeID: "vt-1",
		FillPrice: 1.0855, FillAmount: 100000, QuotedPrice: &qr, Status: ExecutionStatusFilled,
	}
	if err := repo.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("expected id populated")
	}

	dup := *in
	dup.ID = 0
	if err := repo.Create(ctx, &dup); err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if dup.ID != in.ID {
		t.Errorf("idempotent create: dup.ID = %d, want %d", dup.ID, in.ID)
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VenueTradeID != "vt-1" {
		t.Errorf("venue_trade_id = %q", got.VenueTradeID)
	}

	list, err := repo.ListByHedge(ctx, h.ID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(list) = %d, want 1", len(list))
	}

	count, err := repo.CountByHedge(ctx, h.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestPnLRepoCreateGetList(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	repo := NewPnLRepo(pool)
	in := &PnL{Currency: "EUR", Component: PnLRevaluation, Amount: -1234.56}
	if err := repo.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.ID == 0 || in.TS.IsZero() {
		t.Fatal("expected id and ts populated")
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Component != PnLRevaluation {
		t.Errorf("component = %q", got.Component)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	list, err := repo.ListByCurrency(ctx, "EUR", from, to, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected at least one pnl row")
	}
}

func TestSlippageRepoCreateGetList(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	// Parent hedge + execution for FK.
	hrepo := NewHedgeRepo(pool)
	if _, err := pool.Exec(ctx, `DELETE FROM hedges WHERE client_request_id = $1`, "test-stage1-slip-req"); err != nil {
		t.Fatalf("cleanup hedges: %v", err)
	}
	h := &Hedge{
		Currency: "EUR", Notional: 100000, Tenor: "spot", Type: HedgeTypeSpot,
		Status: HedgeStatusSubmitted, ClientRequestID: "test-stage1-slip-req",
	}
	if err := hrepo.Create(ctx, h); err != nil {
		t.Fatalf("hedge create: %v", err)
	}
	erepo := NewHedgeExecutionRepo(pool)
	e := &HedgeExecution{
		HedgeID: h.ID, Venue: "fx-venue", VenueTradeID: "vt-slip",
		FillPrice: 1.0855, FillAmount: 100000, Status: ExecutionStatusFilled,
	}
	if err := erepo.Create(ctx, e); err != nil {
		t.Fatalf("execution create: %v", err)
	}

	repo := NewSlippageRepo(pool)
	in := &SlippageSample{
		HedgeID: h.ID, ExecutionID: e.ID, CurrencyPair: "EURUSD",
		QuotedRate: 1.0850, FillRate: 1.0855, SlippageBPS: 0.5,
	}
	if err := repo.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.ID == 0 || in.TS.IsZero() {
		t.Fatal("expected id and ts populated")
	}

	got, err := repo.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CurrencyPair != "EURUSD" {
		t.Errorf("pair = %q", got.CurrencyPair)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	list, err := repo.ListByPair(ctx, "EURUSD", from, to, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected at least one slippage row")
	}
}

func TestHealthChecker(t *testing.T) {
	dsn := skipIfNoDB(t)
	ctx := context.Background()
	pool, err := Open(ctx, Config{DBURL: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	h := NewHealthChecker(pool)
	if err := h.Check(ctx); err != nil {
		t.Errorf("check: %v", err)
	}
}