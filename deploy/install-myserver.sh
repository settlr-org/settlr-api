#!/usr/bin/env bash
# One-time setup on myserver, run as root:
#   sudo bash install-myserver.sh <deploy-user> "<ci-public-key>"
# Installs the deploy scripts, a sudoers rule limited to the deploy script, and a CI key that can
# only trigger that script (forced command, no shell, no forwarding).
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run with sudo" >&2; exit 1; }
DEPLOY_USER="${1:?deploy user, e.g. nabin}"
PUBKEY="${2:?CI public key (ssh-ed25519 …)}"
HERE="$(cd "$(dirname "$0")" && pwd)"

install -o root -g root -m 0755 "$HERE/settlr-deploy" /usr/local/sbin/settlr-deploy
install -o root -g root -m 0755 "$HERE/settlr-deploy-ssh" /usr/local/sbin/settlr-deploy-ssh
touch /var/log/settlr-deploy.log && chmod 0640 /var/log/settlr-deploy.log

rule="$DEPLOY_USER ALL=(root) NOPASSWD: /usr/local/sbin/settlr-deploy"
tmp="$(mktemp)"; printf '%s\n' "$rule" > "$tmp"
visudo -cf "$tmp" >/dev/null
install -o root -g root -m 0440 "$tmp" /etc/sudoers.d/settlr-deploy && rm -f "$tmp"

home="$(getent passwd "$DEPLOY_USER" | cut -d: -f6)"
install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0700 "$home/.ssh"
keys="$home/.ssh/authorized_keys"
entry="restrict,command=\"/usr/local/sbin/settlr-deploy-ssh\" $PUBKEY"
touch "$keys"; chown "$DEPLOY_USER:$DEPLOY_USER" "$keys"; chmod 0600 "$keys"
grep -qxF "$entry" "$keys" || printf '%s\n' "$entry" >> "$keys"

echo "installed: /usr/local/sbin/settlr-deploy, /etc/sudoers.d/settlr-deploy, restricted CI key for $DEPLOY_USER"
