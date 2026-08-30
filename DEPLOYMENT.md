# Settlr API deployment

Production runs on an isolated Hetzner VPS at `https://settlrapi.theswissknife.com`. The authoritative host runbook, scripts, Nginx configuration, encrypted-backup procedure, restore drill, cutover checklist, and rollback procedure are in [`deploy/README.md`](./deploy/README.md).

Environment boundaries are deliberate:

| Environment | API endpoint | Host |
|---|---|---|
| Local | `http://localhost:18080` | development machine |
| Staging | `https://settlrstagingapi.theswissknife.com` | current staging machine |
| Production | `https://settlrapi.theswissknife.com` | Hetzner VPS |

Do not move, alter, or reuse anything from the staging host during production deployment. In particular, production has its own Compose project/volumes, database, JWT secrets, Postgres password, uploads, and Brevo credential. Its API accepts browser requests only from `https://settlr.theswissknife.com`.

Images are promoted manually only after the staging CI gate has passed and are referenced by complete GHCR image digest, never a mutable tag. Production starts empty; DNS rollback plus redeploying the earlier approved digest is sufficient if cutover fails.
