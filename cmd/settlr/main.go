package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/activity"
	"github.com/nabinkhanal00/settlr-api/internal/attachments"
	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/balances"
	"github.com/nabinkhanal00/settlr-api/internal/categories"
	"github.com/nabinkhanal00/settlr-api/internal/comments"
	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/expenses"
	"github.com/nabinkhanal00/settlr-api/internal/export"
	"github.com/nabinkhanal00/settlr-api/internal/friends"
	"github.com/nabinkhanal00/settlr-api/internal/groups"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
	"github.com/nabinkhanal00/settlr-api/internal/mailer"
	"github.com/nabinkhanal00/settlr-api/internal/notifications"
	"github.com/nabinkhanal00/settlr-api/internal/personal"
	"github.com/nabinkhanal00/settlr-api/internal/rates"
	"github.com/nabinkhanal00/settlr-api/internal/recurring"
	"github.com/nabinkhanal00/settlr-api/internal/search"
	"github.com/nabinkhanal00/settlr-api/internal/settlements"
	"github.com/nabinkhanal00/settlr-api/internal/stats"
	"github.com/nabinkhanal00/settlr-api/internal/users"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	setupLogger(cfg.Env)

	ctx := context.Background()
	pool, err := newPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := runMigrations(ctx, pool); err != nil {
		slog.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	if err := seedSystemCategories(ctx, pool); err != nil {
		slog.Warn("seed categories failed", "error", err)
	}

	if len(os.Args) > 1 && os.Args[1] == "seed" {
		if err := runSeed(ctx, pool); err != nil {
			slog.Error("seed failed", "error", err)
			os.Exit(1)
		}
		slog.Info("seed complete")
		return
	}

	authSvc := &auth.Service{Pool: pool, Cfg: cfg}
	mailSender := mailer.FromConfig(cfg.Mail)
	authHandler := &auth.Handler{Svc: authSvc, Mailer: mailSender}
	rateLimiter := httpx.NewRateLimiter(20, 60*1_000_000_000)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})
	// Docs are not public — require auth (future: admin-only). Only authenticated users can view.
	mux.Handle("GET /openapi.json", auth.Middleware(authSvc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openapiJSON)
	})))
	mux.Handle("GET /docs", auth.Middleware(authSvc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(docsHTML))
	})))

	// Auth routes with rate limiting
	authHandler.RegisterRoutes(mux)
	// Wrap auth endpoints with rate limiter via a middleware that checks prefix
	// Instead we register a catch-all that rate-limits /api/v1/auth/*
	// Simpler: we keep auth routes as registered but add a global rate limiter that only applies to /auth
	// For now global limiter checks path prefix in middleware below.

	usersHandler := &users.Handler{Pool: pool, AuthSvc: authSvc}
	groupsHandler := &groups.Handler{Pool: pool, Mailer: mailSender}
	expensesHandler := &expenses.Handler{Pool: pool}
	balancesHandler := &balances.Handler{Pool: pool}
	settlementsHandler := &settlements.Handler{Pool: pool}
	friendsHandler := &friends.Handler{Pool: pool}
	notificationsHandler := &notifications.Handler{Pool: pool}
	categoriesHandler := &categories.Handler{Pool: pool}
	statsHandler := &stats.Handler{Pool: pool}
	commentsHandler := &comments.Handler{Pool: pool}
	attachmentsHandler := &attachments.Handler{Pool: pool}
	activityHandler := &activity.Handler{Pool: pool}
	searchHandler := &search.Handler{Pool: pool}
	recurringHandler := &recurring.Handler{Pool: pool}
	exportHandler := &export.Handler{Pool: pool}
	personalHandler := &personal.Handler{Pool: pool}
	ratesHandler := &rates.Handler{}

	authMw := auth.Middleware(authSvc)
	usersHandler.RegisterRoutes(mux, authMw)
	groupsHandler.RegisterRoutes(mux, authMw)
	expensesHandler.RegisterRoutes(mux, authMw)
	balancesHandler.RegisterRoutes(mux, authMw)
	settlementsHandler.RegisterRoutes(mux, authMw)
	friendsHandler.RegisterRoutes(mux, authMw)
	notificationsHandler.RegisterRoutes(mux, authMw)
	categoriesHandler.RegisterRoutes(mux, authMw)
	statsHandler.RegisterRoutes(mux, authMw)
	commentsHandler.RegisterRoutes(mux, authMw)
	attachmentsHandler.RegisterRoutes(mux, authMw)
	activityHandler.RegisterRoutes(mux, authMw)
	searchHandler.RegisterRoutes(mux, authMw)
	recurringHandler.RegisterRoutes(mux, authMw)
	exportHandler.RegisterRoutes(mux, authMw)
	personalHandler.RegisterRoutes(mux, authMw)
	ratesHandler.RegisterRoutes(mux, authMw)

	// Materialize due recurring expenses on startup and every 15 minutes
	recurringHandler.RunDue(ctx)
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recurringHandler.RunDue(ctx)
			}
		}
	}()

	// Strip /settlr prefix when served via Tailscale path https://arch.../settlr -> http://127.0.0.1:18080
	muxWithPrefix := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/settlr") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/settlr")
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
		}
		mux.ServeHTTP(w, r)
	})
	// Global middleware stack
	var handler http.Handler = muxWithPrefix
	// Rate limit only auth endpoints
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// also check original with /settlr prefix
		if strings.HasPrefix(r.URL.Path, "/settlr") {
			path = strings.TrimPrefix(r.URL.Path, "/settlr")
		}
		if strings.HasPrefix(path, "/api/v1/auth/") {
			if !rateLimiter.Allow(r.RemoteAddr + ":" + path) {
				httpx.WriteJSON(w, 429, map[string]any{"error": map[string]string{"code": "RATE_LIMITED", "message": "too many requests"}})
				return
			}
		}
		muxWithPrefix.ServeHTTP(w, r)
	})
	handler = httpx.SecurityHeaders(handler)
	handler = httpx.CORS(cfg.CORSOrigins)(handler)
	handler = httpx.RequestSizeLimit(handler)
	handler = httpx.RequestID(handler)
	handler = httpx.Logger(handler)
	handler = httpx.Recover(handler)

	addr := ":" + cfg.Port
	slog.Info("starting server", "addr", addr, "env", cfg.Env)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func newPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return pool, nil
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dirs := []string{"migrations", "backend/migrations", "/app/migrations"}
	var migFS fs.FS
	var migDir string
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return fmt.Errorf("read migrations directory %s: %w", d, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
				migFS = os.DirFS(d)
				migDir = d
				break
			}
		}
		if migFS != nil {
			break
		}
	}
	if migFS == nil {
		cwd, _ := os.Getwd()
		slog.Info("migrations directory not found", "cwd", cwd)
		return fmt.Errorf("migrations directory not found")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migFS, ".")
	if err != nil {
		return err
	}
	// Older deployments may have the schema but not migration bookkeeping.
	// Reconstruct the records from durable objects before applying new files.
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
			return fmt.Errorf("check legacy migration %s: %w", marker.version, err)
		}
		if exists {
			if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, marker.version); err != nil {
				return fmt.Errorf("record legacy migration %s: %w", marker.version, err)
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
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT true FROM schema_migrations WHERE version=$1`, version).Scan(&exists); err != nil && err != pgx.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if exists {
			continue
		}
		data, err := fs.ReadFile(migFS, name)
		if err != nil {
			return err
		}
		slog.Info("applying migration", "version", version, "dir", migDir)
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", version, err)
		}
		_, err = tx.Exec(ctx, string(data))
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s failed: %w", version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
	}
	return nil
}

func seedSystemCategories(ctx context.Context, pool *pgxpool.Pool) error {
	cats := []struct{ name, icon, color, grouping string }{
		{"Food", "utensils", "#F59E0B", "Food and Drink"},
		{"Drinks", "cup", "#06B6D4", "Food and Drink"},
		{"Groceries", "shopping-cart", "#10B981", "Food and Drink"},
		{"Transport", "car", "#6366F1", "Transportation"},
		{"Travel", "plane", "#8B5CF6", "Transportation"},
		{"Entertainment", "film", "#EC4899", "Entertainment"},
		{"Shopping", "bag", "#F43F5E", "Life"},
		{"Rent", "home", "#14B8A6", "Home"},
		{"Utilities", "zap", "#EAB308", "Utilities"},
		{"Health", "heart", "#EF4444", "Life"},
		{"Education", "book", "#3B82F6", "Life"},
		{"Other", "tag", "#6B7280", "Uncategorized"},
		{"Games", "gamepad", "#8B5CF6", "Entertainment"},
		{"Movies", "film", "#EC4899", "Entertainment"},
		{"Music", "music", "#F59E0B", "Entertainment"},
		{"Sports", "activity", "#10B981", "Entertainment"},
		{"Dining Out", "utensils", "#F59E0B", "Food and Drink"},
		{"Liquor", "wine", "#06B6D4", "Food and Drink"},
		{"Electronics", "smartphone", "#6366F1", "Home"},
		{"Furniture", "sofa", "#8B5CF6", "Home"},
		{"Mortgage", "home", "#14B8A6", "Home"},
		{"Pets", "heart", "#EF4444", "Home"},
		{"Childcare", "baby", "#F59E0B", "Life"},
		{"Clothing", "shirt", "#3B82F6", "Life"},
		{"Gifts", "gift", "#EC4899", "Life"},
		{"Bicycle", "bike", "#6366F1", "Transportation"},
		{"Bus/Train", "bus", "#8B5CF6", "Transportation"},
		{"Gas/Fuel", "fuel", "#F43F5E", "Transportation"},
		{"Hotel", "building", "#14B8A6", "Transportation"},
		{"Parking", "square-parking", "#EAB308", "Transportation"},
		{"Plane", "plane", "#8B5CF6", "Transportation"},
		{"Taxi", "car", "#6366F1", "Transportation"},
		{"Cleaning", "sparkles", "#14B8A6", "Utilities"},
		{"Electricity", "zap", "#EAB308", "Utilities"},
		{"Water", "droplets", "#06B6D4", "Utilities"},
	}
	for _, c := range cats {
		_, _ = pool.Exec(ctx,
			`INSERT INTO categories (name, icon, color, grouping, is_system) VALUES ($1,$2,$3,$4,true) ON CONFLICT (name) DO UPDATE SET grouping=$4, icon=$2, color=$3`, c.name, c.icon, c.color, c.grouping)
	}
	return nil
}

func setupLogger(env string) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if env == "development" {
		opts.Level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(h))
}

//go:embed openapi.json
var openapiJSON []byte
var _ embed.FS

var docsHTML = `<!doctype html><html><head><title>Settlr API Docs</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"/>
</head><body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url:"/openapi.json",dom_id:"#swagger-ui"})</script>
</body></html>`
