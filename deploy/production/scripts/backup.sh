#!/usr/bin/env bash
set -euo pipefail
umask 077

[[ $(id -u) -eq 0 ]] || { echo 'run as root' >&2; exit 1; }
root_dir=/opt/settlr
env_file=/etc/settlr/production.env
backup_dir=/var/backups/settlr
stamp=$(date -u +%Y%m%dT%H%M%SZ)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

"$root_dir/deploy/production/scripts/validate-production-env.sh" "$env_file"
set -a
# shellcheck disable=SC1090
source "$env_file"
set +a
mkdir -p "$backup_dir"

docker compose --project-directory "$root_dir" --env-file "$env_file" -f "$root_dir/docker-compose.production.yml" exec -T postgres \
  pg_dump --username "$POSTGRES_USER" --format=custom --dbname "$POSTGRES_DB" > "$work_dir/postgres.dump"
docker run --rm -v settlr-production_production-uploads:/uploads:ro -v "$work_dir":/backup alpine:3.20 \
  tar -C /uploads -czf /backup/uploads.tar.gz .
age -r "$BACKUP_AGE_RECIPIENT" -o "$backup_dir/postgres-$stamp.dump.age" "$work_dir/postgres.dump"
age -r "$BACKUP_AGE_RECIPIENT" -o "$backup_dir/uploads-$stamp.tar.gz.age" "$work_dir/uploads.tar.gz"
find "$backup_dir" -type f -name '*.age' -mtime +14 -delete
