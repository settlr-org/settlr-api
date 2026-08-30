#!/usr/bin/env bash
set -euo pipefail

env_file="${1:-/etc/settlr/production.env}"
[[ -f "$env_file" ]] || { echo "missing environment file: $env_file" >&2; exit 1; }

if [[ $(id -u) -eq 0 ]]; then
  mode=$(stat -c '%a' "$env_file")
  owner=$(stat -c '%U:%G' "$env_file")
  [[ "$mode" == 600 && "$owner" == root:root ]] || {
    echo "$env_file must be root:root with mode 0600 (got $owner $mode)" >&2; exit 1;
  }
fi

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a
for name in POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB DATABASE_URL JWT_SECRET JWT_REFRESH_SECRET APP_ENV APP_URL API_URL CORS_ORIGINS API_IMAGE MAIL_PROVIDER BREVO_API_KEY BACKUP_AGE_RECIPIENT; do
  [[ -n "${!name:-}" ]] || { echo "missing $name" >&2; exit 1; }
done
[[ "$APP_ENV" == production ]] || { echo 'APP_ENV must be production' >&2; exit 1; }
[[ "$APP_URL" == https://settlr.theswissknife.com ]] || { echo 'APP_URL must be canonical production URL' >&2; exit 1; }
[[ "$API_URL" == https://settlrapi.theswissknife.com ]] || { echo 'API_URL must be canonical production API URL' >&2; exit 1; }
[[ "$CORS_ORIGINS" == https://settlr.theswissknife.com ]] || { echo 'CORS_ORIGINS must contain only the canonical production web origin' >&2; exit 1; }
[[ "$TRUST_PROXY_HEADERS" == true ]] || { echo 'TRUST_PROXY_HEADERS must be true behind loopback Nginx' >&2; exit 1; }
[[ ${#JWT_SECRET} -ge 32 && ${#JWT_REFRESH_SECRET} -ge 32 ]] || { echo 'JWT secrets must each be at least 32 characters' >&2; exit 1; }
[[ "$API_IMAGE" =~ ^(ghcr\.io|docker\.io)/.+@sha256:[0-9a-f]{64}$ ]] || { echo 'API_IMAGE must be an immutable GHCR or Docker Hub sha256 digest' >&2; exit 1; }
[[ "$BACKUP_AGE_RECIPIENT" =~ ^age1 ]] || { echo 'BACKUP_AGE_RECIPIENT must be an age public recipient' >&2; exit 1; }
echo "production environment validation passed: $env_file"
