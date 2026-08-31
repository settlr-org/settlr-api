#!/usr/bin/env bash
# Local production guardrail. A non-zero exit marks the systemd service failed
# so an external monitor can alert on it without exposing deployment secrets.
set -euo pipefail

root_dir=/opt/settlr
env_file=/etc/settlr/production.env
compose=(docker compose --project-directory "$root_dir" --env-file "$env_file" -f "$root_dir/docker-compose.production.yml")

curl --fail --silent --show-error http://127.0.0.1:18080/readyz >/dev/null
for container in settlr-postgres-production settlr-backend-production; do
  status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container")
  [[ "$status" == healthy ]] || { echo "$container is $status" >&2; exit 1; }
done

used_percent=$(df -P / | awk 'NR == 2 {gsub("%", "", $5); print $5}')
[[ "$used_percent" -lt 85 ]] || { echo "root filesystem is ${used_percent}% full" >&2; exit 1; }

find /var/backups/settlr -type f -name 'postgres-*.dump.age' -newermt '26 hours ago' -print -quit | grep -q . || {
  echo 'no Postgres backup from the last 26 hours' >&2
  exit 1
}
