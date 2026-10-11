# Production deploy (myserver)

Production API: `https://settlrapi.theswissknife.com` → nginx → `127.0.0.1:18080`. That's compose
project `settlr` in `/opt/settlr`, with env from `/etc/settlr/production.env`.

**How it works:** on every push to `main` with a green `verify`, `deploy-production` SSHes in and
runs `settlr-deploy <commit-sha>`. The script:

- refuses any commit that isn't on `origin/main`;
- checks out the commit in `/opt/settlr` (the public repo, so no registry credentials are needed);
- runs `docker compose build api` and recreates only `api`;
- waits for `/readyz`, and rolls back to the previous commit and image if it never gets ready.

## One-time setup

```bash
ssh-keygen -t ed25519 -N '' -C settlr-ci-deploy -f settlr-ci-deploy   # on your machine
scp -P <port> deploy/settlr-deploy deploy/settlr-deploy-ssh deploy/install-myserver.sh settlr-ci-deploy.pub myserver:/tmp/
ssh -t myserver 'cd /tmp && sudo bash install-myserver.sh "$USER" "$(cat settlr-ci-deploy.pub)"'
gh secret set DEPLOY_HOST    -R settlr-org/settlr-api   # myserver address
gh secret set DEPLOY_PORT    -R settlr-org/settlr-api   # its SSH port
gh secret set DEPLOY_USER    -R settlr-org/settlr-api   # the user passed to the installer
gh secret set DEPLOY_SSH_KEY -R settlr-org/settlr-api < settlr-ci-deploy && shred -u settlr-ci-deploy
```

**What the CI key can do:** it is `restrict`ed to a forced command (`settlr-deploy-ssh`). That
command only runs `sudo -n /usr/local/sbin/settlr-deploy <40-hex sha>`, and the script only deploys
commits that are already on `origin/main`.

## Manual deploy and rollback

```bash
ssh myserver 'sudo -n /usr/local/sbin/settlr-deploy <commit-sha-on-main>'
```

To roll back, run the same command with an earlier commit on `main`. Each deploy is logged with
its previous SHA in `/var/log/settlr-deploy.log`.
