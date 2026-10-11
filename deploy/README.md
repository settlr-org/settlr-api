# Production deploy (myserver)

Production API: `https://settlrapi.theswissknife.com` → nginx → `127.0.0.1:18080`. That's compose
project `settlr` in `/opt/settlr`, with env from `/etc/settlr/production.env`.

**How it works:** on every push to `main`, CI publishes `docker.io/<user>/settlr-api@sha256:…`.
`deploy-production` then SSHes in and runs `settlr-deploy-image`, which:

- pulls the digest;
- retags it `settlr-api:latest`;
- recreates only `api`;
- waits for `/readyz`, and rolls back to the previous image if it never gets ready.

## One-time setup

```bash
ssh-keygen -t ed25519 -N '' -C settlr-ci-deploy -f settlr-ci-deploy   # on your machine
scp -P <port> deploy/settlr-deploy-image deploy/settlr-deploy-ssh deploy/install-myserver.sh settlr-ci-deploy.pub myserver:/tmp/
ssh -t myserver 'cd /tmp && sudo bash install-myserver.sh "$USER" "$(cat settlr-ci-deploy.pub)" docker.io/<dockerhub-user>/settlr-api'
gh secret set DEPLOY_HOST    -R settlr-org/settlr-api   # myserver address
gh secret set DEPLOY_PORT    -R settlr-org/settlr-api   # its SSH port
gh secret set DEPLOY_USER    -R settlr-org/settlr-api   # the user passed to the installer
gh secret set DEPLOY_SSH_KEY -R settlr-org/settlr-api < settlr-ci-deploy && shred -u settlr-ci-deploy
```

**What the CI key can do:** it is `restrict`ed to a forced command (`settlr-deploy-ssh`). That
command only runs `sudo -n /usr/local/sbin/settlr-deploy-image '<digest>'`, and the script only
accepts digests from the repository pinned in `/etc/settlr/deploy-image-repo`.

## Manual deploy and rollback

```bash
ssh myserver 'sudo -n /usr/local/sbin/settlr-deploy-image docker.io/<user>/settlr-api@sha256:<digest>'
```

To roll back, run the same command with the previous digest (see `/var/log/settlr-deploy.log` or
the CI run that produced it).
