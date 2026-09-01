package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// NewPool creates a new pgx pool with sensible defaults.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate runs pending database migrations using goose.
// It discovers the migrations directory (checking migrations/, backend/migrations/, /app/migrations),
// backfills goose_db_version from the legacy schema_migrations table if present (to avoid
// re-applying already-applied migrations on existing deployments), and then runs goose Up.
//
// The legacy schema_migrations table used TEXT PK versions like "0001_init_schema". Goose uses
// integer versions (1,2,3...) and a separate goose_db_version table with columns
// (id, version_id, is_applied, tstamp). On first run with goose, we copy existing legacy versions
// into goose_db_version so goose correctly sees them as applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migDir, migFS, err := discoverMigrationsFS()
	if err != nil {
		return err
	}
	slog.Info("migrations directory", "dir", migDir)

	// Backfill goose table from legacy schema_migrations before goose runs.
	if err := backfillGooseFromLegacy(ctx, pool); err != nil {
		// Backfill is best-effort; log but don't fail if it has warnings.
		// If legacy table doesn't exist, this is a fresh DB and goose will create from scratch.
		slog.Warn("backfill from schema_migrations failed", "error", err)
	}

	// Open a database/sql DB backed by pgx for goose.
	// goose v3 requires *sql.DB, while the app uses *pgxpool.Pool.
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() {
		_ = sqlDB.Close()
	}()
	// Verify connectivity for goose.
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sql db for migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	if len(results) > 0 {
		slog.Info("migrations applied", "count", len(results))
		for _, r := range results {
			slog.Info("applied migration", "version", r.Source.Version, "path", filepath.Base(r.Source.Path), "duration", r.Duration)
		}
		// Keep legacy schema_migrations in sync for observability/backward-compat.
		// Create the table if it doesn't exist and insert newly applied versions.
		if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err == nil {
			for _, r := range results {
				verStr := strings.TrimSuffix(filepath.Base(r.Source.Path), ".sql")
				if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, verStr); err != nil {
					slog.Warn("sync schema_migrations failed", "version", verStr, "error", err)
				}
			}
		}
	} else {
		slog.Info("no new migrations to apply")
	}
	return nil
}

// MigrateFS is like Migrate but uses the provided fs.FS directly (useful for embedded migrations or tests).
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	if err := backfillGooseFromLegacy(ctx, pool); err != nil {
		slog.Warn("backfill from schema_migrations failed", "error", err)
	}
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = sqlDB.Close() }()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sql db for migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	if len(results) > 0 {
		slog.Info("migrations applied via MigrateFS", "count", len(results))
		if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err == nil {
			for _, r := range results {
				verStr := strings.TrimSuffix(filepath.Base(r.Source.Path), ".sql")
				_, _ = pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, verStr)
			}
		}
	}
	return nil
}

// ValidateMigrations checks that migrations in the discovered directory are valid goose migrations.
func ValidateMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migDir, migFS, err := discoverMigrationsFS()
	if err != nil {
		return err
	}
	_ = migDir
	sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migFS)
	if err != nil {
		return fmt.Errorf("create goose provider for validate: %w", err)
	}
	// HasPending will trigger parsing and validation without applying.
	if _, err := provider.HasPending(ctx); err != nil {
		return fmt.Errorf("validate migrations: %w", err)
	}
	return nil
}

func discoverMigrationsFS() (string, fs.FS, error) {
	candidates := []string{"migrations", "backend/migrations", "/app/migrations", "../migrations", "../../migrations"}
	// Also try relative to cwd
	cwd, _ := os.Getwd()
	candidates = append(candidates, filepath.Join(cwd, "migrations"), filepath.Join(cwd, "backend/migrations"))
	// Deduplicate while preserving order
	seen := map[string]struct{}{}
	var uniq []string
	for _, c := range candidates {
		if _, ok := seen[c]; !ok {
			seen[c] = struct{}{}
			uniq = append(uniq, c)
		}
	}
	for _, d := range uniq {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		hasSQL := false
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") && hasNumericPrefix(e.Name()) {
				hasSQL = true
				break
			}
		}
		if hasSQL {
			return d, os.DirFS(d), nil
		}
	}
	return "", nil, fmt.Errorf("migrations directory not found (candidates: %v, cwd=%s)", uniq, cwd)
}

func hasNumericPrefix(name string) bool {
	before, _, ok := strings.Cut(name, "_")
	if !ok {
		return false
	}
	_, err := strconv.ParseInt(before, 10, 64)
	return err == nil
}

// backfillGooseFromLegacy copies applied versions from the legacy schema_migrations TEXT table
// into the new goose_db_version table. This ensures existing deployments (which have already applied
// migrations via the old manual runner) do not re-apply them with goose.
//
// It also handles the legacyMarkers case: older deployments may have the schema objects but not the
// bookkeeping rows. We reconstruct those rows by checking to_regclass.
func backfillGooseFromLegacy(ctx context.Context, pool *pgxpool.Pool) error {
	// Check if legacy table exists.
	var legacyExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations')`).Scan(&legacyExists); err != nil {
		return fmt.Errorf("check legacy table: %w", err)
	}
	if !legacyExists {
		return nil
	}

	// Ensure goose table exists. Provider will create it lazily, but we need it now for backfill.
	var gooseExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='goose_db_version')`).Scan(&gooseExists); err != nil {
		return fmt.Errorf("check goose table: %w", err)
	}
	if !gooseExists {
		if _, err := pool.Exec(ctx, `CREATE TABLE goose_db_version (id integer PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY, version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("create goose_db_version: %w", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
			return fmt.Errorf("insert zero version: %w", err)
		}
		slog.Info("created goose_db_version table")
	} else {
		// Ensure zero version exists.
		var zeroExists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=0)`).Scan(&zeroExists); err != nil {
			return fmt.Errorf("check zero version: %w", err)
		}
		if !zeroExists {
			if _, err := pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
				return fmt.Errorf("insert zero version: %w", err)
			}
		}
	}

	// Reconstruct legacy bookkeeping for older deployments that may have objects but no rows.
	legacyMarkers := []struct {
		version string
		object  string
	}{
		{"0001_init_schema", "users"},
		{"0002_add_missing", "expense_attachments"},
		{"0003_direct_recurring", "recurring_expenses"},
		{"0004_personal_expenses", "personal_expenses"},
		{"0006_prototype_parity", "personal_budgets"},
	}
	for _, marker := range legacyMarkers {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, marker.object).Scan(&exists); err != nil {
			return fmt.Errorf("check legacy marker %s: %w", marker.version, err)
		}
		if exists {
			if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, marker.version); err != nil {
				return fmt.Errorf("record legacy marker %s: %w", marker.version, err)
			}
		}
	}

	// Read legacy versions.
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("select legacy versions: %w", err)
	}
	defer rows.Close()
	var legacyVersions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scan legacy version: %w", err)
		}
		legacyVersions = append(legacyVersions, v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy versions: %w", err)
	}
	if len(legacyVersions) == 0 {
		return nil
	}

	// For each legacy version, parse integer prefix and ensure goose has it.
	for _, vStr := range legacyVersions {
		ver, err := parseLegacyVersion(vStr)
		if err != nil {
			slog.Warn("skip unparsable legacy version", "version", vStr, "error", err)
			continue
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied=true)`, ver).Scan(&exists); err != nil {
			return fmt.Errorf("check goose version %d: %w", ver, err)
		}
		if !exists {
			slog.Info("backfilling goose version from legacy", "legacy", vStr, "version", ver)
			if _, err := pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, ver); err != nil {
				return fmt.Errorf("backfill goose version %d: %w", ver, err)
			}
		}
	}
	return nil
}

func parseLegacyVersion(s string) (int64, error) {
	// Legacy versions are like "0001_init_schema" — numeric prefix before underscore.
	before, _, ok := strings.Cut(s, "_")
	if !ok {
		return 0, fmt.Errorf("no separator in %q", s)
	}
	// Trim leading zeros but keep at least one digit.
	n, err := strconv.ParseInt(before, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		return 0, fmt.Errorf("version must be >0: %s", s)
	}
	return n, nil
}

// EnsureMigrations is a helper for tests that ensures migrations are applied.
// It wraps Migrate and is kept for backward compatibility with testutil.
func EnsureMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return Migrate(ctx, pool)
}

// ListMigrationFiles returns sorted list of migration files in the discovered directory (for tooling).
func ListMigrationFiles() ([]string, error) {
	dir, _, err := discoverMigrationsFS()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// Legacy helpers kept for reference — not used by goose but exported for testutil back-compat.
var _ = sql.ErrNoRows
