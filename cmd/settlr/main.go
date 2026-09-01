package main

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/settlr-org/settlr-api/internal/database"
	db "github.com/settlr-org/settlr-api/internal/db"

	"github.com/settlr-org/settlr-api/internal/activity"
	"github.com/settlr-org/settlr-api/internal/attachments"
	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/balances"
	"github.com/settlr-org/settlr-api/internal/categories"
	"github.com/settlr-org/settlr-api/internal/comments"
	"github.com/settlr-org/settlr-api/internal/config"
	"github.com/settlr-org/settlr-api/internal/expenses"
	"github.com/settlr-org/settlr-api/internal/export"
	"github.com/settlr-org/settlr-api/internal/friends"
	"github.com/settlr-org/settlr-api/internal/groups"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/mailer"
	"github.com/settlr-org/settlr-api/internal/notifications"
	"github.com/settlr-org/settlr-api/internal/personal"
	"github.com/settlr-org/settlr-api/internal/rates"
	"github.com/settlr-org/settlr-api/internal/recurring"
	"github.com/settlr-org/settlr-api/internal/search"
	"github.com/settlr-org/settlr-api/internal/settlements"
	"github.com/settlr-org/settlr-api/internal/stats"
	"github.com/settlr-org/settlr-api/internal/users"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	setupLogger(cfg.Env)

	ctx := context.Background()
	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
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

	// sqlc Queries wraps the pool for type-safe queries (see internal/db/queries/groups.sql).
	// Handlers that have been migrated to sqlc use Queries; legacy Pool usage remains for unmigrated handlers.
	queries := db.New(pool)

	authSvc := &auth.Service{Pool: pool, Queries: queries, Cfg: cfg}
	mailSender := mailer.FromConfig(cfg.Mail)
	authHandler := &auth.Handler{Svc: authSvc, Mailer: mailSender, Queries: queries}
	rateLimiter := httpx.NewRateLimiter(20, time.Minute)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ready"})
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

	usersHandler := &users.Handler{Pool: pool, Queries: queries, AuthSvc: authSvc}
	groupsHandler := &groups.Handler{Pool: pool, Queries: queries, Mailer: mailSender}
	expensesHandler := &expenses.Handler{Pool: pool, Queries: queries}
	balancesHandler := &balances.Handler{Pool: pool, Queries: queries}
	settlementsHandler := &settlements.Handler{Pool: pool, Queries: queries}
	friendsHandler := &friends.Handler{Pool: pool, Queries: queries, Mailer: mailSender}
	notificationsHandler := &notifications.Handler{Pool: pool, Queries: queries}
	categoriesHandler := &categories.Handler{Pool: pool, Queries: queries}
	statsHandler := &stats.Handler{Pool: pool, Queries: queries}
	commentsHandler := &comments.Handler{Pool: pool, Queries: queries}
	attachmentsHandler := &attachments.Handler{Pool: pool, Queries: queries}
	activityHandler := &activity.Handler{Pool: pool, Queries: queries}
	searchHandler := &search.Handler{Pool: pool, Queries: queries}
	recurringHandler := &recurring.Handler{Pool: pool, Queries: queries}
	exportHandler := &export.Handler{Pool: pool, Queries: queries}
	personalHandler := &personal.Handler{Pool: pool, Queries: queries}
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
			if !rateLimiter.Allow(httpx.ClientIP(r, cfg.TrustProxyHeaders) + ":" + path) {
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
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return database.Migrate(ctx, pool)
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
