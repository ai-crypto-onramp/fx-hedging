package store

import (
	"os"
	"testing"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected 1 migration pair, got %d", len(migrations))
	}
	m := migrations[0]
	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
	if m.Up == "" {
		t.Errorf("missing Up script")
	}
	if m.Down == "" {
		t.Errorf("missing Down script")
	}
}

func TestLoadMigrationsContainsExpectedTables(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	wantTables := []string{
		"fx_exposures",
		"hedges",
		"hedge_executions",
		"fx_pnl",
		"slippage_samples",
	}
	for _, table := range wantTables {
		found := false
		for _, m := range migrations {
			if contains(m.Up, table) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no migration creates table %q", table)
		}
	}
}

func TestLoadMigrationsContainsExpectedIndexes(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	up := migrations[0].Up
	wantIndexes := []string{
		"idx_fx_exposures_currency_ts",
		"idx_hedges_status",
		"idx_hedge_executions_hedge_venue_trade",
	}
	for _, idx := range wantIndexes {
		if !contains(up, idx) {
			t.Errorf("migration up missing index %q", idx)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	os.Unsetenv("DB_URL")
	os.Unsetenv("PORT")
	os.Unsetenv("GRPC_PORT")
	os.Unsetenv("HEDGE_RATIO_TARGET")
	os.Unsetenv("MAX_OPEN_EXPOSURE_USD")

	cfg := LoadConfig()
	if cfg.DBURL != "" {
		t.Errorf("DBURL = %q, want empty", cfg.DBURL)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.GRPCPort != "9090" {
		t.Errorf("GRPCPort = %q, want 9090", cfg.GRPCPort)
	}
	if cfg.HedgeRatioTarget != 0.90 {
		t.Errorf("HedgeRatioTarget = %v, want 0.90", cfg.HedgeRatioTarget)
	}
	if cfg.MaxOpenExposure != 500000 {
		t.Errorf("MaxOpenExposure = %v, want 500000", cfg.MaxOpenExposure)
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("DB_URL", "postgres://fx:pwd@db:5432/fx?sslmode=require")
	t.Setenv("PORT", "9080")
	t.Setenv("GRPC_PORT", "9091")
	t.Setenv("HEDGE_RATIO_TARGET", "0.75")
	t.Setenv("MAX_OPEN_EXPOSURE_USD", "250000")

	cfg := LoadConfig()
	if cfg.DBURL != "postgres://fx:pwd@db:5432/fx?sslmode=require" {
		t.Errorf("DBURL = %q", cfg.DBURL)
	}
	if cfg.Port != "9080" {
		t.Errorf("Port = %q, want 9080", cfg.Port)
	}
	if cfg.GRPCPort != "9091" {
		t.Errorf("GRPCPort = %q, want 9091", cfg.GRPCPort)
	}
	if cfg.HedgeRatioTarget != 0.75 {
		t.Errorf("HedgeRatioTarget = %v, want 0.75", cfg.HedgeRatioTarget)
	}
	if cfg.MaxOpenExposure != 250000 {
		t.Errorf("MaxOpenExposure = %v, want 250000", cfg.MaxOpenExposure)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}