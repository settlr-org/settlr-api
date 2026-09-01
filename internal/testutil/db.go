package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/settlr-org/settlr-api/internal/database"
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
	// Use the centralized goose-based migrator (handles legacy schema_migrations backfill).
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
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
