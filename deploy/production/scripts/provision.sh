#!/usr/bin/env bash
# First-time Hetzner VPS setup. Run as root on an otherwise fresh Ubuntu VPS.
set -euo pipefail

[[ $(id -u) -eq 0 ]] || { echo 'run as root' >&2; exit 1; }
repo_url="${1:-https://github.com/nabinkhanal00/settlr-api.git}"
root_dir=/opt/settlr
hostname=settlrapi.theswissknife.com

apt-get update
apt-get install -y ca-certificates curl git nginx certbot python3-certbot-nginx ufw age
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi
docker compose version

ufw default deny incoming
ufw default allow outgoing
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable

install -d -m 0700 /etc/settlr
install -d -m 0755 /var/backups/settlr /var/www/certbot
if [[ ! -d "$root_dir/.git" ]]; then
  git clone "$repo_url" "$root_dir"
else
  git -C "$root_dir" fetch --tags origin
  git -C "$root_dir" pull --ff-only
fi
chown -R root:root "$root_dir"
find "$root_dir/deploy/production/scripts" -type f -name '*.sh' -exec chmod 0700 {} +

if [[ ! -f /etc/settlr/production.env ]]; then
  install -m 0600 -o root -g root "$root_dir/deploy/production/production.env.example" /etc/settlr/production.env
fi
if [[ ! -f /etc/settlr/dockerhub-read-token ]]; then
  install -m 0600 -o root -g root /dev/null /etc/settlr/dockerhub-read-token
fi
install -m 0700 -o root -g root "$root_dir/deploy/production/scripts/deploy-image.sh" /usr/local/sbin/settlr-deploy-image

# Bootstrap only HTTP so that the ACME HTTP-01 request can succeed. The full
# TLS configuration is installed by issue-certificate.sh after DNS cutover.
cat > "/etc/nginx/sites-available/$hostname" <<EOF
server {
    listen 80;
    listen [::]:80;
    server_name $hostname;
    location ^~ /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://\$host\$request_uri; }
}
EOF
ln -sfn "/etc/nginx/sites-available/$hostname" "/etc/nginx/sites-enabled/$hostname"
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl reload nginx

install -m 0644 -o root -g root "$root_dir/deploy/production/systemd/settlr-backup.service" /etc/systemd/system/settlr-backup.service
install -m 0644 -o root -g root "$root_dir/deploy/production/systemd/settlr-backup.timer" /etc/systemd/system/settlr-backup.timer
install -m 0644 -o root -g root "$root_dir/deploy/production/systemd/settlr-monitor.service" /etc/systemd/system/settlr-monitor.service
install -m 0644 -o root -g root "$root_dir/deploy/production/systemd/settlr-monitor.timer" /etc/systemd/system/settlr-monitor.timer
systemctl daemon-reload
systemctl enable --now settlr-backup.timer
systemctl enable --now settlr-monitor.timer

cat <<'EOF'
Provisioning complete. Before deployment:
  1. Fill /etc/settlr/production.env with NEW production-only values and chmod 0600 it.
  2. Put a Docker Hub read token in /etc/settlr/dockerhub-read-token (root:root 0600).
  3. Point the DNS A/AAAA record for settlrapi.theswissknife.com at this VPS, then run
     /opt/settlr/deploy/production/scripts/issue-certificate.sh.
  4. Set DOCKERHUB_USERNAME and run deploy/production/scripts/deploy.sh.
EOF
