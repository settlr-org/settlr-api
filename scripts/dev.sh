#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE=(docker compose -f "$ROOT/docker-compose.local.yml")

# Replace the former containerized local API with Air on the host. Staging uses
# a different Compose project and is not affected.
docker stop settlr-api-local >/dev/null 2>&1 || true
"${COMPOSE[@]}" up -d --remove-orphans postgres mailpit

for _ in {1..30}; do
  if "${COMPOSE[@]}" exec -T postgres pg_isready -U settlr -d settlr_local >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
"${COMPOSE[@]}" exec -T postgres pg_isready -U settlr -d settlr_local >/dev/null

export DATABASE_URL="postgres://settlr:local-dev-password@127.0.0.1:5433/settlr_local?sslmode=disable"
export JWT_SECRET="local-dev-jwt-secret-0123456789abcdef-change-me-32chars"
export JWT_REFRESH_SECRET="local-dev-refresh-secret-0123456789abcdef-change-me-32chars"
export PORT="18081"
export APP_ENV="development"
export APP_URL="http://localhost:3000"
export API_URL="http://localhost:18081"
export CORS_ORIGINS="http://localhost:3000,http://localhost:3001,http://localhost:19006,http://127.0.0.1:3000,http://127.0.0.1:19006"
export MAIL_PROVIDER="smtp"
export SMTP_HOST="127.0.0.1"
export SMTP_PORT="1026"
export SMTP_USER=""
export SMTP_PASS=""
export BREVO_API_KEY=""
export MAIL_FROM_EMAIL="noreply@localhost"
export MAIL_FROM_NAME="Settlr"

cd "$ROOT"
if command -v air >/dev/null 2>&1; then
  exec air -c .air.toml
fi
exec go run github.com/air-verse/air@v1.63.0 -c .air.toml
