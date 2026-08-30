#!/usr/bin/env bash
# Root-owned entry point for CI. It accepts only one immutable Docker Hub
# digest, serializes deployments, and delegates all other checks to deploy.sh.
set -euo pipefail

[[ $(id -u) -eq 0 ]] || { echo 'run as root' >&2; exit 1; }
[[ $# -eq 1 ]] || { echo 'usage: deploy-image.sh docker.io/nabinkhanal688/settlr-api@sha256:<digest>' >&2; exit 1; }
image=$1
[[ "$image" =~ ^docker\.io/nabinkhanal688/settlr-api@sha256:[0-9a-f]{64}$ ]] || {
  echo 'refusing non-canonical Docker Hub image digest' >&2
  exit 1
}

exec 9>/var/lock/settlr-production-deploy.lock
flock -n 9 || { echo 'another production deployment is already running' >&2; exit 1; }
sed -i "s|^API_IMAGE=.*|API_IMAGE=$image|" /etc/settlr/production.env
exec env DOCKERHUB_USERNAME=nabinkhanal688 /opt/settlr/deploy/production/scripts/deploy.sh
