# Deployment notes

The existing Cloudflare Worker remains the public edge for `settlrapi.theswissknife.com` and routes to this machine. Nginx proxies to `127.0.0.1:18080`; `arch.tailbd5522.ts.net` is an additional accepted API origin/path. Keep Cloudflare and Nginx configuration outside Git unless sanitized; inject secrets at runtime and maintain encrypted PostgreSQL/upload backups.
