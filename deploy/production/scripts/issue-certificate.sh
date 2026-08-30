#!/usr/bin/env bash
set -euo pipefail

[[ $(id -u) -eq 0 ]] || { echo 'run as root' >&2; exit 1; }
root_dir=/opt/settlr
hostname=settlrapi.theswissknife.com

# DNS must already resolve to this VPS. --webroot avoids relying on Certbot's
# generated Nginx edits and leaves the versioned site configuration authoritative.
certbot certonly --webroot -w /var/www/certbot -d "$hostname" --non-interactive --agree-tos --email "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL}"
install -m 0644 -o root -g root "$root_dir/deploy/production/nginx/$hostname.conf" "/etc/nginx/sites-available/$hostname"
nginx -t
systemctl reload nginx
systemctl enable --now certbot.timer
