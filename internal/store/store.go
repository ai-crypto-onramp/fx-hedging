package store

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the store configuration read from the environment.
type Config struct {
	DBURL            string
	Port             string
	GRPCPort         string
	HedgeRatioTarget float64
	MaxOpenExposure  float64
	MinConns         int32
	MaxConns         int32
	ConnMaxLifetime  time.Duration
}

// LoadConfig reads store configuration from environment variables using the
// defaults documented in README.md.
func LoadConfig() Config {
	cfg := Config{
		DBURL:            os.Getenv("DB_URL"),
		Port:             envStr("PORT", "8080"),
		GRPCPort:         envStr("GRPC_PORT", "9090"),
		HedgeRatioTarget: envFloat("HEDGE_RATIO_TARGET", 0.90),
		MaxOpenExposure:  envFloat("MAX_OPEN_EXPOSURE_USD", 500000),
		MinConns:         int32(envInt("DB_MIN_CONNS", 2)),
		MaxConns:         int32(envInt("DB_MAX_CONNS", 25)),
		ConnMaxLifetime:  envDuration("DB_CONN_MAX_LIFETIME_SECONDS", 300*time.Second),
	}
	return cfg
}

// Open opens a pooled Postgres connection and applies all migrations.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.DBURL == "" {
		return nil, fmt.Errorf("DB_URL is required")
	}
	pcfg, err := pgxpool.ParseConfig(cfg.DBURL)
	if err != nil {
		return nil, fmt.Errorf("parse DB_URL: %w", err)
	}
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MaxConnLifetime = cfg.ConnMaxLifetime

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return pool, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

// HealthChecker runs a liveness probe against the DB pool for the /healthz handler.
type HealthChecker struct {
	pool *pgxpool.Pool
}

// NewHealthChecker returns a HealthChecker for the given pool.
func NewHealthChecker(pool *pgxpool.Pool) *HealthChecker {
	return &HealthChecker{pool: pool}
}

// Check returns nil if the DB pool is reachable, otherwise an error describing
// the failure.
func (h *HealthChecker) Check(ctx context.Context) error {
	if h.pool == nil {
		return fmt.Errorf("db pool not initialized")
	}
	if err := h.pool.Ping(ctx); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	return nil
}