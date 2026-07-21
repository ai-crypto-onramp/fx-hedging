package migrations

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestAllReturnsInit(t *testing.T) {
	ms, err := All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("len = %d, want 1", len(ms))
	}
	if ms[0].Version != "0001_init" {
		t.Fatalf("version = %q", ms[0].Version)
	}
	if ms[0].Up == "" || ms[0].Down == "" {
		t.Fatal("up/down empty")
	}
}

func TestInitCreatesAllTables(t *testing.T) {
	ms, _ := All()
	up := ms[0].Up
	want := []string{"fx_exposures", "hedges", "hedge_executions", "fx_pnl", "slippage_samples"}
	for _, w := range want {
		if !strings.Contains(up, w) {
			t.Errorf("up missing table %q", w)
		}
	}
	wantIdx := []string{
		"fx_exposures_currency_ts_idx",
		"hedges_status_idx",
		"hedge_executions_hedge_venue_trade_idx",
	}
	for _, w := range wantIdx {
		if !strings.Contains(up, w) {
			t.Errorf("up missing index %q", w)
		}
	}
}

func TestRunnerUpIdempotent(t *testing.T) {
	applied := map[string]bool{}
	var executed []string
	exec := func(ctx context.Context, q string, args ...any) error {
		executed = append(executed, q)
		if strings.Contains(q, "INSERT INTO schema_migrations") {
			applied["0001_init"] = true
		}
		return nil
	}
	query := func(ctx context.Context, version string) (bool, error) {
		return applied[version], nil
	}
	r := NewRunner(exec, query)
	if err := r.Up(context.Background()); err != nil {
		t.Fatalf("up: %v", err)
	}
	first := len(executed)
	if err := r.Up(context.Background()); err != nil {
		t.Fatalf("up2: %v", err)
	}
	// Second run only re-executes the CREATE TABLE IF NOT EXISTS schema_migrations.
	if len(executed) != first+1 {
		t.Fatalf("second Up should only re-run schema_migrations create: executed grew from %d to %d", first, len(executed))
	}
}

func TestRunnerDownReverts(t *testing.T) {
	execCalls := 0
	exec := func(ctx context.Context, q string, args ...any) error {
		execCalls++
		return nil
	}
	applied := map[string]bool{"0001_init": true}
	query := func(ctx context.Context, version string) (bool, error) {
		return applied[version], nil
	}
	r := NewRunner(exec, query)
	if err := r.Down(context.Background()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if execCalls == 0 {
		t.Fatal("Down should execute the down migration")
	}
}

func TestRunnerPropagatesExecError(t *testing.T) {
	want := errors.New("boom")
	exec := func(ctx context.Context, q string, args ...any) error {
		if strings.Contains(q, "CREATE TABLE") && strings.Contains(q, "schema_migrations") {
			return nil
		}
		return want
	}
	r := NewRunner(exec, nil)
	if err := r.Up(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestMigrationFilenamesSorted(t *testing.T) {
	entries, err := fs.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i+1 < len(names); i++ {
		if names[i] == names[i+1] {
			t.Fatalf("duplicate migration file %q", names[i])
		}
	}
}

func TestRunnerUpQueryError(t *testing.T) {
	exec := func(ctx context.Context, q string, args ...any) error {
		return nil
	}
	qerr := errors.New("query boom")
	query := func(ctx context.Context, version string) (bool, error) {
		return false, qerr
	}
	r := NewRunner(exec, query)
	if err := r.Up(context.Background()); !errors.Is(err, qerr) {
		t.Fatalf("err = %v, want %v", err, qerr)
	}
}

func TestRunnerDownQueryError(t *testing.T) {
	exec := func(ctx context.Context, q string, args ...any) error {
		return nil
	}
	qerr := errors.New("query boom")
	query := func(ctx context.Context, version string) (bool, error) {
		return false, qerr
	}
	r := NewRunner(exec, query)
	if err := r.Down(context.Background()); !errors.Is(err, qerr) {
		t.Fatalf("err = %v, want %v", err, qerr)
	}
}

func TestRunnerDownSkipsUnapplied(t *testing.T) {
	execCalls := 0
	exec := func(ctx context.Context, q string, args ...any) error {
		execCalls++
		return nil
	}
	query := func(ctx context.Context, version string) (bool, error) {
		return false, nil // nothing applied
	}
	r := NewRunner(exec, query)
	if err := r.Down(context.Background()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if execCalls != 0 {
		t.Fatalf("Down should skip unapplied migrations; exec calls = %d", execCalls)
	}
}

func TestRunnerDownExecError(t *testing.T) {
	want := errors.New("down boom")
	exec := func(ctx context.Context, q string, args ...any) error {
		return want
	}
	query := func(ctx context.Context, version string) (bool, error) {
		return true, nil
	}
	r := NewRunner(exec, query)
	if err := r.Down(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestRunnerUpRecordsVersionExecError(t *testing.T) {
	// First few calls succeed (CREATE TABLE + apply up); the INSERT fails.
	want := errors.New("insert boom")
	calls := 0
	exec := func(ctx context.Context, q string, args ...any) error {
		calls++
		if strings.Contains(q, "INSERT INTO schema_migrations") {
			return want
		}
		return nil
	}
	r := NewRunner(exec, nil)
	if err := r.Up(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestRunnerDownDeleteExecError(t *testing.T) {
	want := errors.New("delete boom")
	exec := func(ctx context.Context, q string, args ...any) error {
		if strings.Contains(q, "DELETE FROM schema_migrations") {
			return want
		}
		return nil
	}
	query := func(ctx context.Context, version string) (bool, error) {
		return true, nil
	}
	r := NewRunner(exec, query)
	if err := r.Down(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
