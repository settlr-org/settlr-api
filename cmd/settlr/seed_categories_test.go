package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func migrationBlocks(t *testing.T) (up, down string) {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/0020_distinct_system_category_styles.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(raw), "-- +goose Down", 2)
	return strings.TrimPrefix(parts[0], "-- +goose Up"), parts[1]
}

func visuals(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT name, icon || '|' || color FROM categories WHERE is_system`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		_ = rows.Scan(&n, &v)
		out[n] = v
	}
	return out
}

func sameMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The boot-time seed must not undo migration 0020: it carries the same table, inserts visuals only
// for new categories and never overwrites icon/colour of existing ones.
func TestSeedMatchesMigrationAndNeverRestoresOldStyles(t *testing.T) {
	pool := testutil.CleanDB(t)
	ctx := context.Background()
	up, down := migrationBlocks(t)
	re := regexp.MustCompile(`\('([^']+)', '([^']+)', '(#[0-9A-F]+)'\)`)
	want, old := map[string]string{}, map[string]string{}
	for _, m := range re.FindAllStringSubmatch(up, -1) {
		want[m[1]] = m[2] + "|" + m[3]
	}
	for _, m := range re.FindAllStringSubmatch(down, -1) {
		old[m[1]] = m[2] + "|" + m[3]
	}

	// Production state before the deploy: the old duplicates are in the table.
	for name, v := range old {
		p := strings.SplitN(v, "|", 2)
		if _, err := pool.Exec(ctx, `INSERT INTO categories (name, icon, color, grouping, is_system) VALUES ($1,$2,$3,'Life',true)`, name, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, up); err != nil { // the migration on boot
		t.Fatal(err)
	}
	if err := seedSystemCategories(ctx, pool); err != nil { // then the seed
		t.Fatal(err)
	}
	after := visuals(t, pool)
	if !sameMaps(after, want) {
		t.Fatalf("seed changed the migrated styles:\n got %v\nwant %v", after, want)
	}
	seen := map[string]string{}
	for n, v := range after {
		if o, dup := seen[v]; dup {
			t.Fatalf("%s and %s share %s", n, o, v)
		}
		seen[v] = n
	}
	// A second boot changes nothing.
	if err := seedSystemCategories(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !sameMaps(visuals(t, pool), want) {
		t.Fatal("second seed changed styles")
	}
	// After the Down the seed must not re-apply the new values either.
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if err := seedSystemCategories(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !sameMaps(visuals(t, pool), old) {
		t.Fatal("seed re-applied new styles after the migration was rolled back")
	}

	// A fresh database (empty table): the seed alone yields exactly the migration's table.
	if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE is_system`); err != nil {
		t.Fatal(err)
	}
	if err := seedSystemCategories(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !sameMaps(visuals(t, pool), want) {
		t.Fatalf("seed table differs from migration 0020: %v", visuals(t, pool))
	}
}
