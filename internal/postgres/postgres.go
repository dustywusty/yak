// Package postgres owns the reference server's connection pool, explicit
// migrations, and schema compatibility checks (design §12.3).
package postgres

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockKey is the pg_advisory_lock key used by `abby migrate`.
const migrationLockKey = 0x4162627930303031 // "Abby0001"

type migration struct {
	Version int
	Name    string
	SQL     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("bad migration name %q", e.Name())
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("bad migration name %q", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{Version: v, Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// RequiredVersion is the schema version this binary needs.
func RequiredVersion() int {
	ms, err := loadMigrations()
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].Version
}

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Migrate applies pending migrations under a session advisory lock so that
// concurrent invocations serialize. It returns the versions applied.
func Migrate(ctx context.Context, url string) ([]string, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrationLockKey)); err != nil {
		return nil, err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(migrationLockKey))

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version int PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, m := range ms {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, m.Version).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("%s: %w", m.Name, err)
			}
			if m.Version == 1 {
				if _, err := tx.Exec(ctx, `INSERT INTO server_state(history_epoch) VALUES ($1)`, NewEpoch()); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, name) VALUES ($1,$2)`, m.Version, m.Name)
			return err
		})
		if err != nil {
			return applied, err
		}
		applied = append(applied, m.Name)
	}
	return applied, nil
}

var ErrMigrationRequired = errors.New("database schema is behind this binary; run `abby migrate`")
var ErrSchemaTooNew = errors.New("database schema is newer than this binary supports")

// CheckSchema verifies the database is at exactly the version this binary
// requires. The server refuses to start otherwise (no racing migrations).
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var v int
	err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, ErrMigrationRequired
	}
	switch req := RequiredVersion(); {
	case v < req:
		return v, ErrMigrationRequired
	case v > req:
		return v, ErrSchemaTooNew
	}
	return v, nil
}

// NewEpoch returns a fresh history epoch identifier.
func NewEpoch() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "h" + hex.EncodeToString(b)
}

// RotateEpoch assigns a new history epoch. Operators run this after restoring
// a backup so that cursors from the abandoned future require resync (§13.2).
func RotateEpoch(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	e := NewEpoch()
	_, err := pool.Exec(ctx, `UPDATE server_state SET history_epoch=$1, epoch_rotated_at=now()`, e)
	return e, err
}
