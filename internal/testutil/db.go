package testutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testPool *pgxpool.Pool
var mu sync.Mutex
var fileMu *os.File
var fileMuOnce sync.Once

func TestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool != nil {
		return testPool
	}
	ctx := context.Background()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = deriveTestDBURL(os.Getenv("DATABASE_URL"))
	}
	// Ensure the dedicated test database exists (never touch the dev/main DB).
	if err := ensureTestDatabase(ctx, dbURL); err != nil {
		t.Fatalf("failed to ensure test database: %v", err)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect test DB: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("postgres not available: %v", err)
	}
	// Ensure schema exists (for CI fresh DB)
	ensureMigrated(t, pool)
	testPool = pool
	return pool
}

// deriveTestDBURL points tests at a "settlr_test" database on the same server
// as DATABASE_URL (or the default local URL), so test truncation can never
// wipe a development database.
func deriveTestDBURL(base string) string {
	if base == "" {
		base = "postgres://settlr:settlr@localhost:5432/settlr?sslmode=disable"
	}
	i := strings.LastIndex(base, "/")
	j := strings.Index(base[i+1:], "?")
	if j < 0 {
		return base[:i+1] + "settlr_test"
	}
	return base[:i+1] + "settlr_test" + base[i+1+j:]
}

// ensureTestDatabase creates the test database if it does not exist,
// connecting to the server's default "postgres" database first.
func ensureTestDatabase(ctx context.Context, dbURL string) error {
	query := ""
	if i := strings.Index(dbURL, "?"); i >= 0 {
		query = dbURL[i:]
		dbURL = dbURL[:i]
	}
	i := strings.LastIndex(dbURL, "/")
	dbName := dbURL[i+1:]
	adminURL := dbURL[:i+1] + "postgres" + query
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, dbName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName)); err != nil {
			return err
		}
	}
	return nil
}

func ensureMigrated(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// Package tests run in parallel and share settlr_test. Serialize schema setup
	// so concurrent ALTER/CREATE statements cannot deadlock PostgreSQL.
	fileLock(t)
	defer fileUnlock(t)
	ctx := context.Background()
	// Find migrations directory
	candidates := []string{"migrations", "../migrations", "../../migrations", "backend/migrations"}
	// Also try relative to cwd
	cwd, _ := os.Getwd()
	candidates = append(candidates, filepath.Join(cwd, "migrations"), filepath.Join(cwd, "backend/migrations"))
	var migDir string
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err == nil && info.IsDir() {
			migDir = c
			break
		}
	}
	if migDir == "" {
		t.Fatalf("migrations directory not found (cwd=%s)", cwd)
	}
	entries, err := os.ReadDir(migDir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
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
			t.Fatalf("check legacy migration %s: %v", marker.version, err)
		}
		if exists {
			if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, marker.version); err != nil {
				t.Fatalf("record legacy migration %s: %v", marker.version, err)
			}
		}
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)
	for _, name := range ups {
		version := strings.TrimSuffix(name, ".up.sql")
		var applied bool
		if err := pool.QueryRow(ctx, `SELECT true FROM schema_migrations WHERE version=$1`, version).Scan(&applied); err != nil && err != pgx.ErrNoRows {
			t.Fatalf("check migration %s: %v", version, err)
		}
		if applied {
			continue
		}
		data, err := fs.ReadFile(os.DirFS(migDir), name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin migration %s: %v", version, err)
		}
		_, err = tx.Exec(ctx, string(data))
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("migration %s failed: %v", name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record migration %s: %v", version, err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatalf("commit migration %s: %v", version, err)
		}
	}
	// Ensure pgcrypto extension (for gen_random_uuid) if not already via migration
	_, _ = pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS "pgcrypto"`)
}

func TruncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	// Truncate in order respecting FKs
	_, err := pool.Exec(ctx, `
		TRUNCATE activity_events, notifications, settlements, expense_splits, expenses, categories, friendships, group_members, groups, email_verification_tokens, password_reset_tokens, sessions, users, group_invites, friend_invites, expense_comments, expense_attachments RESTART IDENTITY CASCADE;
	`)
	if err != nil {
		// Fallback: try truncating known tables individually
		tables := []string{"activity_events", "notifications", "settlements", "expense_splits", "expenses", "categories", "friendships", "group_members", "groups", "sessions", "users"}
		for _, tbl := range tables {
			_, _ = pool.Exec(ctx, fmt.Sprintf("TRUNCATE %s CASCADE", tbl))
		}
	}
}

func fileLock(t *testing.T) {
	t.Helper()
	fileMuOnce.Do(func() {
		var err error
		fileMu, err = os.OpenFile("/tmp/settlr_test.lock", os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
	})
	if err := syscall.Flock(int(fileMu.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("failed to lock: %v", err)
	}
}

func fileUnlock(t *testing.T) {
	t.Helper()
	if err := syscall.Flock(int(fileMu.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("failed to unlock: %v", err)
	}
}

func CleanDB(t *testing.T) *pgxpool.Pool {
	pool := TestDB(t)
	mu.Lock()
	fileLock(t)
	t.Cleanup(func() {
		fileUnlock(t)
		mu.Unlock()
	})
	TruncateAll(t, pool)
	return pool
}
