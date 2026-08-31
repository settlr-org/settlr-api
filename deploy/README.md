# Settlr deployment environments

| Environment | API hostname | Host | Data/secrets |
|---|---|---|---|
| Local | `http://localhost:18080` | developer machine | local-only Compose volumes and Mailpit |
| Staging | `https://settlrstagingapi.theswissknife.com` | current machine | existing staging volumes and staging-only credentials |
| Production | `https://settlrapi.theswissknife.com` | isolated Hetzner VPS | `settlr-production` volumes and new production-only credentials |

`settlrstagingapi.theswissknife.com` is staging-only. Never repoint it, reuse its data, or copy its JWT, Postgres, uploads, or Brevo values to production.

## Production VPS

The versioned production-host bundle is under [`production/`](./production). It installs Docker, Compose, Nginx, Certbot, UFW, a loopback-only API stack, and daily encrypted backups. Secrets are never committed: the only production environment file is `/etc/settlr/production.env` owned by `root:root` with mode `0600`.

1. On the fresh VPS, clone this repository to `/opt/settlr`, then run `deploy/production/scripts/provision.sh` as root.
2. Populate `/etc/settlr/production.env` from `deploy/production/production.env.example`, generating new Postgres/JWT/Brevo values and supplying an off-VPS age recipient. Validate it with `deploy/production/scripts/validate-production-env.sh`.
3. Store a Docker Hub read token in `/etc/settlr/dockerhub-read-token` (`root:root`, `0600`). Set `DOCKERHUB_USERNAME` only for the deploy command.
4. Set the Cloudflare A/AAAA records for `settlrapi.theswissknife.com` to the VPS. Before cutover, test the HTTP Nginx vhost using `curl --resolve settlrapi.theswissknife.com:80:VPS_IP http://settlrapi.theswissknife.com/health`; it should redirect to HTTPS. With DNS live, run `LETSENCRYPT_EMAIL=ops@example.com deploy/production/scripts/issue-certificate.sh`.
5. Set `API_IMAGE` to a full Docker Hub `@sha256:` digest that passed the staging gate, then run `DOCKERHUB_USERNAME=... deploy/production/scripts/deploy.sh` as root. Verify `curl http://127.0.0.1:18080/readyz`, container health, and `curl --resolve settlrapi.theswissknife.com:443:VPS_IP https://settlrapi.theswissknife.com/readyz` before relying on public DNS.

Nginx is the sole public ingress and proxies only to `127.0.0.1:18080`; Compose never publishes Postgres. Production CORS permits only `https://settlr.theswissknife.com`, and trusted proxy headers are enabled only because that loopback boundary exists.

## Cutover and rollback

After DNS and certificate verification, confirm external `/health` and `/readyz`, a CORS preflight from `https://settlr.theswissknife.com`, registration/verification delivery, login/refresh/logout, and an authenticated expense workflow. Confirm staging remains healthy at `https://settlrstagingapi.theswissknife.com/health` without changing that machine.

Record the deployed digest and the old DNS/origin before cutover. Production intentionally begins empty: no migration is required. Roll back by restoring the prior DNS/origin and, if necessary, putting the prior pinned digest in `/etc/settlr/production.env` and re-running `deploy.sh`.

## Backup and restore drill

`settlr-backup.timer` runs daily at 03:15 UTC. It writes separate age-encrypted Postgres dumps and uploads archives to `/var/backups/settlr`, deleting files older than 14 days. The age private key must be kept outside the VPS.

For a restore drill on an isolated host, decrypt a matching pair with `age -d -i /secure/off-vps-age-key.txt`, start an empty production Compose stack, restore the database with `pg_restore --clean --if-exists --username settlr --dbname settlr`, and extract the uploads archive into the `settlr-production_production-uploads` volume. Validate `/readyz` and a sample attachment before considering the drill successful.

These are VPS-local backups. They protect against an application/data mistake but **do not survive loss of the VPS**; copy encrypted backup artifacts to separate storage for disaster recovery.

## Monitoring

`settlr-monitor.timer` runs every five minutes and fails its corresponding service if API readiness, either container's health, root filesystem capacity (85%), or the last encrypted Postgres backup (26 hours) is unhealthy. Inspect it with `systemctl status settlr-monitor.service`. Connect this status to an external alert receiver/uptime service; no alert destination is stored in this repository.
