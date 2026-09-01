.PHONY: dev deps-down migrate-up migrate-down migrate-create sqlc-generate sqlc-vet goose-validate vet test

dev:
	./scripts/dev.sh

deps-down:
	docker compose -f docker-compose.local.yml down

# Database migrations via goose (uses migrations/*.sql with -- +goose Up/Down)
# Requires DATABASE_URL env var or .env file. Example: make migrate-up DATABASE_URL=postgres://...
migrate-up:
	go run github.com/pressly/goose/v3/cmd/goose@latest postgres "$${DATABASE_URL}" -dir migrations up

migrate-down:
	go run github.com/pressly/goose/v3/cmd/goose@latest postgres "$${DATABASE_URL}" -dir migrations down

migrate-create:
	@# Usage: make migrate-create NAME=add_foo
	@test -n "$(NAME)" || (echo "NAME required, e.g. make migrate-create NAME=add_foo" && exit 1)
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations create $(NAME) sql

# Alternative helpers that use the database package (pgx pool) instead of goose CLI
migrate-up-go:
	go run ./cmd/settlr --migrate-only 2>&1 | head -20

# sqlc code generation and vetting
sqlc-generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate

sqlc-vet:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest vet

goose-validate:
	go run github.com/pressly/goose/v3/cmd/goose@latest postgres "$${DATABASE_URL:-postgres://settlr:local-dev-password@127.0.0.1:5433/settlr_local?sslmode=disable}" -dir migrations validate

vet:
	go vet ./...

test:
	go test -race ./internal/...

test-short:
	go test -short ./internal/...

build:
	go build -o /tmp/settlr ./cmd/settlr

lint: vet sqlc-vet
