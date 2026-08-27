# Deployment notes

The existing Cloudflare Worker remains the public edge for `settlrapi.theswissknife.com` and routes to this machine. Nginx proxies to `127.0.0.1:18080`; `arch.tailbd5522.ts.net` is an additional accepted API origin/path. Keep Cloudflare and Nginx configuration outside Git unless sanitized; inject secrets at runtime and maintain encrypted PostgreSQL/upload backups.
# Delivery pipeline

Pull requests enforce `gofmt`, `go vet`, race-enabled tests, a production binary build, and a Docker Buildx build. Pushes to `main` and version tags publish immutable branch/tag/SHA images to GitHub Container Registry.

The host deployment remains intentionally separate from image publishing because server access and rollout policy are environment-specific. Production should pull a pinned SHA or version tag, run migrations through the application startup, verify `/health`, and only then replace the prior container.
