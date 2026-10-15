package categories_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func migrationSection(t *testing.T, which string) string {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/0020_distinct_system_category_styles.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(raw), "-- +goose Down", 2)
	if which == "up" {
		return strings.TrimPrefix(parts[0], "-- +goose Up")
	}
	return parts[1]
}

func TestSystemCategoryStylesAreDistinct(t *testing.T) {
	pool := testutil.CleanDB(t)
	ctx := context.Background()
	// The 0005 seed: related pairs share icon and colour. A user category reuses a system name.
	for _, c := range [][3]string{
		{"Plane", "plane", "#8B5CF6"}, {"Travel", "plane", "#8B5CF6"}, {"Taxi", "car", "#6366F1"}, {"Transport", "car", "#6366F1"},
		{"Rent", "home", "#14B8A6"}, {"Mortgage", "home", "#14B8A6"}, {"Movies", "film", "#EC4899"}, {"Entertainment", "film", "#EC4899"},
		{"Food", "utensils", "#F59E0B"}, {"Dining Out", "utensils", "#F59E0B"}, {"Utilities", "zap", "#EAB308"}, {"Electricity", "zap", "#EAB308"},
		{"Pets", "heart", "#EF4444"}, {"Health", "heart", "#EF4444"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO categories (name, icon, color, is_system) VALUES ($1, $2, $3, true)`, c[0], c[1], c[2]); err != nil {
			t.Fatal(err)
		}
	}
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (name, email, password_hash) VALUES ('U', 'cat-style@test.local', 'x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO categories (name, icon, color, owner_id) VALUES ('Plane', 'plane', '#8B5CF6', $1)`, uid); err != nil {
		t.Fatal(err)
	}
	pairs := func() map[string]string {
		rows, err := pool.Query(ctx, `SELECT name, icon || '|' || color FROM categories WHERE is_system`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var n, p string
			_ = rows.Scan(&n, &p)
			out[n] = p
		}
		return out
	}
	up := migrationSection(t, "up")
	for i := 0; i < 2; i++ { // idempotent
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatal(err)
		}
	}
	got := pairs()
	seen := map[string]string{}
	for n, p := range got {
		if other, dup := seen[p]; dup {
			t.Fatalf("%s and %s share %s", n, other, p)
		}
		seen[p] = n
	}
	for _, pair := range [][2]string{{"Plane", "Travel"}, {"Taxi", "Transport"}, {"Rent", "Mortgage"}, {"Movies", "Entertainment"}, {"Food", "Dining Out"}, {"Utilities", "Electricity"}, {"Pets", "Health"}} {
		if got[pair[0]] == got[pair[1]] {
			t.Fatalf("%v still identical: %s", pair, got[pair[0]])
		}
	}
	// Colours come from the theme's chart palette; user-created categories are untouched.
	chart := "#0E7C7B #FF8A65 #0F3D3C #E0B04A #6C8EBF #9FC3BF #C2678F #B8CFCB"
	for n, p := range got {
		if !strings.Contains(chart, strings.Split(p, "|")[1]) {
			t.Fatalf("%s uses a colour outside chart1..8: %s", n, p)
		}
	}
	var icon, color string
	if err := pool.QueryRow(ctx, `SELECT icon, color FROM categories WHERE owner_id = $1 AND name = 'Plane'`, uid).Scan(&icon, &color); err != nil || icon != "plane" || color != "#8B5CF6" {
		t.Fatalf("user category changed: %s %s %v", icon, color, err)
	}
	// Down restores the previous look.
	if _, err := pool.Exec(ctx, migrationSection(t, "down")); err != nil {
		t.Fatal(err)
	}
	if p := pairs()["Plane"]; p != "plane|#8B5CF6" {
		t.Fatalf("down: %s", p)
	}
}
