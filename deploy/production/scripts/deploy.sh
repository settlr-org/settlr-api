#!/usr/bin/env bash
set -euo pipefail

[[ $(id -u) -eq 0 ]] || { echo 'run as root' >&2; exit 1; }
root_dir=/opt/settlr
env_file=/etc/settlr/production.env
token_file=/etc/settlr/dockerhub-read-token

"$root_dir/deploy/production/scripts/validate-production-env.sh" "$env_file"
[[ -f "$token_file" ]] || { echo "missing $token_file" >&2; exit 1; }
[[ $(stat -c '%a' "$token_file") == 600 ]] || { echo "$token_file must have mode 0600" >&2; exit 1; }
[[ -n ${DOCKERHUB_USERNAME:-} ]] || { echo 'set DOCKERHUB_USERNAME before running deploy' >&2; exit 1; }

if [[ "${SETTLR_SKIP_IMAGE_PULL:-false}" != true ]]; then
  docker login docker.io --username "$DOCKERHUB_USERNAME" --password-stdin < "$token_file"
  docker compose --project-directory "$root_dir" --env-file "$env_file" -f "$root_dir/docker-compose.production.yml" pull api
fi
docker compose --project-directory "$root_dir" --env-file "$env_file" -f "$root_dir/docker-compose.production.yml" up -d --remove-orphans
for attempt in $(seq 1 30); do
  if curl --fail --silent --show-error http://127.0.0.1:18080/readyz >/dev/null; then
    break
  fi
  if [[ "$attempt" == 30 ]]; then
    echo 'API did not become ready within 60 seconds' >&2
    exit 1
  fi
  sleep 2
done
docker compose --project-directory "$root_dir" --env-file "$env_file" -f "$root_dir/docker-compose.production.yml" ps
