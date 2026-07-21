// Package migrations embeds SQL migration files and exposes a tiny
// migration runner. The runner records applied versions in a
// schema_migrations table so migrations are idempotent: re-applying an
// already-applied version is a no-op.
package migrations

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed *.sql
var fs embed.FS

// Migration is a single versioned migration pair (up + down).
type Migration struct {
	Version string
	Up      string
	Down    string
}

// All returns all embedded migrations sorted by version.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return nil, err
	}
	byVersion := map[string]*Migration{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "_up.sql") {
			version := strings.TrimSuffix(name, "_up.sql")
			b, err := fs.ReadFile(name)
			if err != nil {
				return nil, err
			}
			m := byVersion[version]
			if m == nil {
				m = &Migration{Version: version}
				byVersion[version] = m
			}
			m.Up = string(b)
		} else if strings.HasSuffix(name, "_down.sql") {
			version := strings.TrimSuffix(name, "_down.sql")
			b, err := fs.ReadFile(name)
			if err != nil {
				return nil, err
			}
			m := byVersion[version]
			if m == nil {
				m = &Migration{Version: version}
				byVersion[version] = m
			}
			m.Down = string(b)
		}
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	out := make([]Migration, 0, len(versions))
	for _, v := range versions {
		m := byVersion[v]
		if m.Up == "" || m.Down == "" {
			return nil, fmt.Errorf("migrations: version %s missing up or down", v)
		}
		out = append(out, *m)
	}
	return out, nil
}

// ExecFunc executes a SQL statement with no rows returned.
type ExecFunc func(ctx context.Context, query string, args ...any) error

// QueryFunc returns whether a version is already applied.
type QueryFunc func(ctx context.Context, version string) (bool, error)

// Runner runs migrations against a database that supports Exec + Query.
type Runner struct {
	exec  ExecFunc
	query QueryFunc
}

// NewRunner returns a migration runner. If query is nil the runner relies on
// the embedded SQL being idempotent (CREATE TABLE IF NOT EXISTS) and always
// records versions as applied.
func NewRunner(exec ExecFunc, query QueryFunc) *Runner {
	return &Runner{exec: exec, query: query}
}

// Up applies all pending migrations in order. Idempotent.
func (r *Runner) Up(ctx context.Context) error {
	if err := r.exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrations: create schema_migrations: %w", err)
	}
	migs, err := All()
	if err != nil {
		return err
	}
	for _, m := range migs {
		if r.query != nil {
			applied, err := r.query(ctx, m.Version)
			if err != nil {
				return err
			}
			if applied {
				continue
			}
		}
		if err := r.exec(ctx, m.Up); err != nil {
			return fmt.Errorf("migrations: apply %s: %w", m.Version, err)
		}
		if err := r.exec(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`, m.Version, time.Now().UTC()); err != nil {
			return fmt.Errorf("migrations: record %s: %w", m.Version, err)
		}
	}
	return nil
}

// Down reverts all migrations in reverse order. Idempotent.
func (r *Runner) Down(ctx context.Context) error {
	migs, err := All()
	if err != nil {
		return err
	}
	for i := len(migs) - 1; i >= 0; i-- {
		m := migs[i]
		if r.query != nil {
			applied, err := r.query(ctx, m.Version)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
		}
		if err := r.exec(ctx, m.Down); err != nil {
			return fmt.Errorf("migrations: revert %s: %w", m.Version, err)
		}
		if err := r.exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, m.Version); err != nil {
			return err
		}
	}
	return nil
}
