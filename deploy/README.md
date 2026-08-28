# Deployment notes — 3 environments: local / staging / production

| Env | API | Web (Vercel) | DB volume | `APP_ENV` | Compose file | Email |
|-----|-----|--------------|-----------|-----------|--------------|-------|
| **local** | `http://localhost:18080` | `http://localhost:3000` | `pgdata-local` (`settlr_local`) | `development` | `docker-compose.yml` + `docker-compose.local.yml` | Mailpit `mailpit:1025` UI `http://localhost:8025` |
| **staging** | `https://settlrapi.theswissknife.com` (Cloudflare -> `127.0.0.1:18080` on this machine) | `https://settlr-staging.vercel.app` (Vercel project `settlr-staging`) + Tailscale `https://arch.tailbd5522.ts.net/settlr` | `pgdata` (`settlr`) | `staging` | `docker-compose.yml` + `docker-compose.staging.yml` (or base alone, `APP_ENV` defaults to `staging`) | Brevo (staging key) |
| **production** | `https://api.settlr.theswissknife.com` (dedicated VM) | `https://settlr.theswissknife.com` (Vercel project `settlr`) + `https://settlr-kappa.vercel.app` | `pgdata` on new VM | `production` | `docker-compose.production.yml` | Brevo (production key) |

The existing Cloudflare Worker remains the public edge for `settlrapi.theswissknife.com` and routes to this machine for staging. Nginx proxies to `127.0.0.1:18080`; `arch.tailbd5522.ts.net` is an additional accepted API origin/path for staging. Keep Cloudflare and Nginx configuration outside Git unless sanitized; inject secrets at runtime and maintain encrypted PostgreSQL/upload backups per environment. Never share `pgdata` or JWT secrets between environments.

## Local

```bash
cp .env.example .env  # then edit secrets if needed
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d
curl http://localhost:18080/health
open http://localhost:8025  # Mailpit inbox
docker compose -f docker-compose.yml -f docker-compose.local.yml logs -f api
```

Local uses `Mailpit` (`axllent/mailpit`). Registration emails land at `http://localhost:8025`; no Brevo key needed. `APP_ENV=development` leaks `verification_token`/`reset_token` in JSON for testing — staging/production never do.

## Staging (this machine)

```bash
# on host: /etc/settlr/staging.env or .env (never committed)
docker compose -f docker-compose.yml -f docker-compose.staging.yml up -d
# or: docker compose up -d  (base defaults APP_ENV=staging)
curl http://127.0.0.1:18080/health
curl https://arch.tailbd5522.ts.net/settlr/health
docker compose -f docker-compose.yml -f docker-compose.staging.yml logs -f api
```

Vercel project `settlr-staging` builds from `nabinkhanal00/settlr-web` `main` with `NEXT_PUBLIC_API_URL=https://settlrapi.theswissknife.com` and proxy `API_URL` matching. Uses Vercel-given `*.vercel.app` DNS.

## Production (dedicated server, future)

```bash
docker compose -f docker-compose.production.yml up -d
curl http://127.0.0.1:18080/health
curl https://api.settlr.theswissknife.com/health
```

Pin GHCR image by SHA or `v*` tag, run migrations on startup (handled by `cmd/settlr/main.go`), verify `/health` before cutting over.

# Delivery pipeline

Pull requests enforce `gofmt`, `go vet`, race-enabled tests, a production binary build, and a Docker Buildx build. Pushes to `main` and version tags publish immutable branch/tag/SHA images to GitHub Container Registry.

The host deployment remains intentionally separate from image publishing because server access and rollout policy are environment-specific. Each environment should pull a pinned SHA or version tag, run migrations through application startup, verify `/health`, and only then replace the prior container.
