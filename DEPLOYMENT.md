# Settlr Deployment Guide

> **Source of truth for how and when to ship.** Last updated: 2026-08-28
> Workspace: `/home/wizard/Dev/Projects/settlr` — 3 independent Git repos + 1 design system (4 total).
> Envs: `local` (your laptop) → `staging` (this host via Tailscale/CF) → `production` (dedicated VM, future).

---

## 1. Environment Matrix

| Env | Purpose | API | Web | Mobile API | DB | APP_ENV | Email | Compose / Deploy |
|-----|---------|-----|-----|------------|----|---------|-------|------------------|
| **local** | Daily dev, no prod data | `http://localhost:18080` | `http://localhost:3000` | `http://10.0.2.2:18080` (Android emu) / `http://localhost:18080` (iOS sim) | `pgdata-local` `settlr_local` | `development` | Mailpit `mailpit:1025` UI `http://localhost:8025` | `docker-compose.yml` + `docker-compose.local.yml` |
| **staging** | Pre-prod mirror, real Brevo, Tailscale, vercel preview | `https://settlrapi.theswissknife.com` → `127.0.0.1:18080` on this host | `https://settlr-staging.vercel.app` (Vercel `settlr-staging`, auto `*.vercel.app`) | `https://settlrapi.theswissknife.com` (`preview` channel) | `pgdata` `settlr` on this host | `staging` | Brevo staging key | `docker-compose.yml` + `docker-compose.staging.yml` |
| **production** | Live users, dedicated VM | `https://api.settlr.theswissknife.com` → `127.0.0.1:18080` on new VM | `https://settlr.theswissknife.com` (+ `settlr-kappa.vercel.app`) Vercel `settlr` | `https://api.settlr.theswissknife.com` (`production` channel) | `pgdata` `settlr` on new VM | `production` | Brevo prod key | `docker-compose.production.yml` |

- **Never share** `pgdata`, `uploads`, `JWT_SECRET`, `BREVO_API_KEY` between envs.
- `local` leaks `verification_token`/`reset_token` in JSON for testing; `staging`/`production` never do (`internal/auth/handlers.go:93`).
- `staging`/`production` enforce `JWT >=32` and `CORS != *` (`internal/config/config.go:49`).

---

## 2. Git Workflow & When to Deploy

### 2.1 Branching (trunk-based, per-repo)

```
main  ──●──●──●──●──●──►  protected, always green, auto-deploys
         \  \  \
feat/x ──●──●──┘  PR → main  (any repo independent)
fix/y  ──●─────┘
```

- **Each repo is independent.** Web/API/Mobile can be released at different cadences; no monorepo lockstep. Design (`settlr-design`) is manual publish only.
- **Create branch per repo where you change code:**

  ```bash
  git -C settlr-web checkout -b feat/group-stats
  git -C settlr-api checkout -b feat/group-stats
  # mobile only if needed
  ```

- **Commit small, format before push:**

  ```bash
  # web/mobile
  npm run format:check && npm run typecheck && npm test && npm run build
  git add -A && git commit -m "feat(web): add group stats csv export"
  ```

  ```bash
  # api
  test -z "$(gofmt -l .)" && go vet ./... && go test -race ./... && go build ./cmd/settlr
  git add -A && git commit -m "feat(api): add group stats endpoint"
  ```

- **PR → required check `verify` must be green.** Vercel/GitHub will block merge if `verify` fails.

### 2.2 When to Push / What Triggers What

| Action | Web (`settlr-web`) | API (`settlr-api`) | Mobile (`settlr-mobile`) |
|--------|--------------------|--------------------|--------------------------|
| `git push origin feat/x` (PR branch) | CI `verify` runs (audit, format, typecheck, test, build). **No deploy.** Fix failures before merge. | CI `verify` runs (gofmt, vet, vuln, race, build, docker). **No GHCR publish.** | CI `verify` runs (audit, format, typecheck, test, export:web). **No EAS Update.** |
| `git push origin main` (merge to main) | CI `verify` → **Vercel auto-deploys** both `settlr` (prod) **and** `settlr-staging` (staging) via Git integration (watches `main`). Wait for `● Ready` in `vercel ls`. No manual `vercel deploy`. | CI `verify` → `publish-container` → GHCR `ghcr.io/nabinkhanal00/settlr-api:main` + `sha-<hash>` + `v*` tags. **No host deploy.** You must SSH & pull. | CI `verify` → `update-production` (needs `EXPO_TOKEN`, env `production`) → `eas update --channel production --auto`. OTA to users. Binary requires manual `eas build`. |
| `git tag v1.2.0 && git push --tags` | — (web versioned via Vercel deployment ID, not tags) | Same as `main` plus tag `v1.2.0` + `sha` to GHCR. Use for prod pinning. | — |

**Rule:**
- **Push feature branch** any time → get CI feedback in ~1 min. **Do not merge** until `verify` green.
- **Merge to main** = **ship to staging + prod (web)** and **publish API image**. Only merge when staging-tested and ready for users. For risky changes, merge to `main` during low-traffic, verify staging `/health` + Vercel preview first, then promote.
- **Mobile OTA** ships on every `main` push; for breaking native changes, bump `version` and do `eas build` instead.

### 2.3 Deploy Gates (when to promote vs rollback)

- **Local gate:** All 3 `verify` commands pass locally. Browser QA via `agent-browser` (light+dark, desktop 1280 + mobile 390) — see `FEATURE_CHECKLIST.md`. No console errors, no horizontal scroll, icons visible.
- **Staging gate:** After `main` merge, within 2 min check:
  - `curl https://settlrapi.theswissknife.com/health` → `{"status":"ok"}`
  - `curl https://settlr-staging.vercel.app/api-proxy/health` → `{"status":"ok"}`
  - Open staging web, login, smoke: `Overview → Groups → Friends → Activity`, create group/expense, dark toggle.
  - `docker compose -f docker-compose.yml -f docker-compose.staging.yml logs --tail 50 api` no errors.
  - If any fails: **rollback web** via Vercel Dashboard → Deployments → previous `● Ready` → Promote; **rollback API** via `docker pull ghcr.io/nabinkhanal00/settlr-api:sha-<prev> && docker compose up -d`.
- **Production gate:** Same as staging but against `https://api.settlr.theswissknife.com` + `https://settlr.theswissknife.com`. Only promote after staging has baked ≥30 min with no errors. Announce in `#deploys` if you have Slack.

---

## 3. Local Development

### 3.1 One-time Setup

```bash
# 0. Clone (already done in /home/wizard/Dev/Projects/settlr)
ls settlr-api settlr-web settlr-mobile settlr-design

# 1. Android env (for mobile, even if you only test web — needed for Maestro)
echo 'export ANDROID_HOME=$HOME/Android/Sdk' >> ~/.bashrc
echo 'export ANDROID_SDK_ROOT=$ANDROID_HOME' >> ~/.bashrc
echo 'export PATH=$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH' >> ~/.bashrc
source ~/.bashrc
adb --version          # 37.0.1
emulator -list-avds    # settlr (Pixel 7, Android 14) — pre-created

# 2. Install deps per repo
cd settlr-web && npm ci
cd ../settlr-api && go mod download
cd ../settlr-mobile && npm ci
cd ../settlr-design && npm ci
```

### 3.2 Running Local (full stack)

**Terminal 1 — API + Postgres + Mailpit:**

```bash
cd /home/wizard/Dev/Projects/settlr/settlr-api
cp .env.example .env   # already correct for local (Mailpit)
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d
docker compose -f docker-compose.yml -f docker-compose.local.yml ps
curl http://localhost:18080/health
# → {"status":"ok"}
open http://localhost:8025   # Mailpit — registration tokens appear here
docker compose -f docker-compose.yml -f docker-compose.local.yml logs -f api
# to stop:
# docker compose -f docker-compose.yml -f docker-compose.local.yml down
# to wipe DB (danger: local only):
# docker compose -f docker-compose.yml -f docker-compose.local.yml down -v
```

**Terminal 2 — Web:**

```bash
cd /home/wizard/Dev/Projects/settlr/settlr-web
cp .env.development.example .env.local   # API_URL=http://localhost:18080
npm run dev
# → http://localhost:3000
# The Next.js proxy at /api-proxy forwards to localhost:18080
curl http://localhost:3000/api-proxy/health  # via proxy
```

**Terminal 3 — Mobile (choose one):**

```bash
cd /home/wizard/Dev/Projects/settlr/settlr-mobile
cp .env.development.example .env.local
# .env.local: EXPO_PUBLIC_API_URL=http://10.0.2.2:18080 (Android emu) or http://localhost:18080 (iOS sim)
npm run typecheck && npm test && npm run export:web  # sanity
npm start
# → Expo CLI: scan QR with Expo Go, or press 'w' for web at http://localhost:8081
# For Android emulator with dev-client:
# npm run start -- --android  # or `npx expo run:android`
```

**Verification commands (run before every commit):**

```bash
# web (in settlr-web)
npm run format:check && npm run typecheck && npm test && npm run build

# api (in settlr-api)
test -z "$(gofmt -l .)" && go vet ./... && go run golang.org/x/vuln/cmd/govulncheck@latest ./... && go test -race ./... && go build ./cmd/settlr && docker build -t settlr-backend:local .

# mobile (in settlr-mobile)
npm run format:check && npm run typecheck && npm test && npm run export:web
```

Use `agent-browser` for UI: test `/` (landing), `/login`, `/register`, `/overview` (needs auth — `docker exec settlr-postgres-local psql -U settlr -d settlr_local -c "update users set email_verified_at=now()"`), `/groups`, `/friends` at 1280 and 390, light+dark.

---

## 4. Staging Environment

### 4.1 Architecture

```
GitHub push main
  ├─→ Vercel settlr-staging (settlr-staging.vercel.app) — builds with NEXT_PUBLIC_API_URL=https://settlrapi.theswissknife.com
  └─→ GHCR ghcr.io/nabinkhanal00/settlr-api:main|sha-* (no auto-deploy)

Internet ──Cloudflare Worker──→ nginx :80/:8443 ──→ 127.0.0.1:18080 (api, staging)
Tailscale Funnel https://arch.tailbd5522.ts.net:443 ──→ 127.0.0.1:8443 ──→ same nginx
Expo preview channel ──→ https://settlrapi.theswissknife.com
```

- Host: this machine (`arch.tailbd5522.ts.net` via `tailscale funnel status`)
- Env file: **never committed**, at `./.env` or `/etc/settlr/staging.env` (`600` perms). Template `.env.staging.example`.
- Vercel project `settlr-staging` `prj_hyQYyzxJav7m9j3Dz7PSxPlbYDpw`, linked to `nabinkhanal00/settlr-web` `main`, env `NEXT_PUBLIC_API_URL` + `API_URL` = `https://settlrapi.theswissknife.com` for `production`+`preview`.

### 4.2 Deploying to Staging

**Web (auto):**

```bash
# After `git push origin main` in settlr-web:
vercel ls --scope nabinkhanal00  # watch settlr-staging → ● Ready (14s)
curl https://settlr-staging.vercel.app/api-proxy/health
# If manual needed (dirty tree):
cd settlr-web && vercel deploy --project settlr-staging --prod --yes
```

**API (manual, intentional separation):**

```bash
# 1. Ensure GHCR image published (check Actions: API CI and container release → publish-container green)
# 2. SSH to host (this machine for staging, else ssh staging-host)
cd /home/wizard/Dev/Projects/settlr/settlr-api
# Pull pinned SHA (from Actions run, e.g. sha-23d2861)
docker pull ghcr.io/nabinkhanal00/settlr-api:main
# or pin:
# docker pull ghcr.io/nabinkhanal00/settlr-api:sha-23d2861
# Tag for compose if compose uses local build:
docker tag ghcr.io/nabinkhanal00/settlr-api:main settlr-backend:staging

# 3. Up with staging compose (uses staging env, staging pgdata)
docker compose -f docker-compose.yml -f docker-compose.staging.yml up -d
# or if host .env already has staging secrets, just:
# docker compose -f docker-compose.yml -f docker-compose.staging.yml --env-file .env.staging up -d

# 4. Verify
curl http://127.0.0.1:18080/health
curl https://arch.tailbd5522.ts.net/health
curl https://settlrapi.theswissknife.com/health
curl https://settlr-staging.vercel.app/api-proxy/health
docker compose -f docker-compose.yml -f docker-compose.staging.yml logs --tail 50 api
# Check migrations applied: logs show "applying migration 0008..."
```

**Mobile (OTA preview):**

```bash
cd settlr-mobile
# .env.staging.example → EXPO_PUBLIC_API_URL=https://settlrapi.theswissknife.com
npx eas update --channel preview --auto  # or wait for push-main auto to production, test via preview channel
# For native binary:
# eas build --profile preview --platform android  # → internal testing on Play
```

### 4.3 Staging Checklist Before Promoting to Prod

- [ ] `docker compose config` vet shows `APP_ENV=staging`, `CORS` includes both vercel domains + tailscale, `MAIL_PROVIDER=brevo`
- [ ] `curl` all 4 health endpoints (127, tailscale, cf api, vercel proxy) → `200`
- [ ] Login on staging web, create group, add equal-split expense, check balances recompute, send friend invite, accept on second account
- [ ] Mobile preview: install dev APK on emulator, same flows via Maestro (see §6)
- [ ] No errors in `docker logs`, no `panic`, migrations `applied`
- [ ] Vercel staging deployment `● Ready` and `vercel ls` shows latest commit SHA

---

## 5. Production Environment

### 5.1 Architecture (dedicated VM, not this host)

```
Vercel settlr (prod) https://settlr.theswissknife.com (+ settlr-kappa.vercel.app)
  └─→ https://api.settlr.theswissknife.com ──CF/DNS──→ new VM nginx :443→127.0.0.1:18080

VM: Ubuntu 22.04+, Docker, Tailscale (optional), UFW 80/443, A/AAAA api.settlr.theswissknife.com → VM IP, CF proxied or direct, certbot/ACME
```

- Vercel project `settlr` `prj_KmrFhcGc0PBqNJeiATIuMiDT6w9c`, `NEXT_PUBLIC_API_URL=API_URL=https://api.settlr.theswissknife.com`
- VM env at `/etc/settlr/production.env` (`600`), template `.env.production.example`. Tight `CORS_ORIGINS=https://settlr.theswissknife.com` only.
- Compose `docker-compose.production.yml` (`build: .` → change to `image: ghcr.io/...:${TAG}` for prod pinning).

### 5.2 First-time Production Bring-up

```bash
# On new VM (as root)
apt update && apt install -y docker.io docker-compose-plugin tailscale nginx certbot
tailscale up
mkdir -p /etc/settlr /opt/settlr && cd /opt/settlr
git clone git@github.com:nabinkhanal00/settlr-api.git .  # or scp compose + .env
cp .env.production.example /etc/settlr/production.env
# EDIT: POSTGRES_PASSWORD, JWT_SECRET (openssl rand -hex 32), JWT_REFRESH_SECRET, BREVO_API_KEY
chmod 600 /etc/settlr/production.env
# DNS: add A/AAAA api.settlr.theswissknife.com → VM IP in Cloudflare
# Nginx: /etc/nginx/sites-available/settlr → proxy_pass http://127.0.0.1:18080; certbot --nginx -d api.settlr.theswissknife.com
# Pull & run pinned GHCR tag (e.g. v1.0.0 or sha-xxxx)
TAG=sha-23d2861 docker compose -f docker-compose.production.yml --env-file /etc/settlr/production.env up -d
curl http://127.0.0.1:18080/health
curl https://api.settlr.theswissknife.com/health
```

### 5.3 Deploying to Production (after staging bake ≥30 min)

**Web:**

- Already auto-deployed on `push main` to `settlr` (prod). Verify:

  ```bash
  vercel ls --scope nabinkhanal00 | grep settlr | head
  curl https://settlr.theswissknife.com/api-proxy/health
  curl https://settlr-kappa.vercel.app/api-proxy/health
  ```

- Rollback: Vercel Dashboard → `settlr` → Deployments → previous `● Ready` → **Promote to Production** (instant, no code push).

**API:**

```bash
# 1. Pick pinned tag from staging that baked (e.g. sha-23d2861 or v1.2.0)
TAG=sha-23d2861
ssh prod-vm
cd /opt/settlr
docker pull ghcr.io/nabinkhanal00/settlr-api:$TAG
TAG=$TAG docker compose -f docker-compose.production.yml --env-file /etc/settlr/production.env up -d
# or if compose uses `image: ghcr.io/...:${TAG}`:
# TAG=$TAG docker compose -f docker-compose.production.yml up -d

# 2. Verify
curl http://127.0.0.1:18080/health
curl https://api.settlr.theswissknife.com/health
docker logs --tail 50 settlr-backend-production  # or settlr-backend-1
# Check migration logs, no 5xx

# 3. Prune old images
docker system prune -f
```

**Mobile:**

```bash
cd settlr-mobile
# Bump version in app.json/app.config.ts if breaking native
npm run typecheck && npm test && npm run export:web  # gate
# OTA (non-breaking JS):
npx eas update --channel production --auto
# Binary (breaking or store):
eas build --profile production --platform android --auto-submit
eas build --profile production --platform ios --auto-submit
# Verify via Expo dashboard channel promotion + Play Internal Testing
```

### 5.4 Production Checklist & Rollback

- [ ] GHCR tag pinned (not `main` floating) and `docker inspect` shows `$TAG`
- [ ] Health 200 on both local and public, Vercel proxy 200
- [ ] Smoke login + group + expense on `https://settlr.theswissknife.com`
- [ ] `docker volume ls` shows `pgdata` (not `pgdata-local`)
- [ ] Backups fresh: `pg_dump` + `uploads` tar + encrypted offsite (see §7)
- **Rollback web:** Vercel promote previous.
- **Rollback API:** `TAG=sha-<prev> docker compose ... up -d`
- **Rollback mobile OTA:** `eas channel:rollback --channel production` or republish.

---

## 6. Android Test System — Full Environment to Catch Bugs Pre-Prod

### 6.1 Host Already Ready

- `~/Android/Sdk` (`platform-tools/adb 37.0.1`, `emulator 37.1.11`, `build-tools 34/35`, `platforms android-34/35`, `system-images android-34/google_apis/x86_64:14`, `ndk 26`)
- AVD `settlr` (Pixel 7, Android 14, 4c/2560MB, `hw.gpu.enabled=no` → use `-gpu swiftshader_indirect`, KVM `/dev/kvm` world-rw, boots `emulator-5554` → `device` in ~25s)
- `java 21.0.2` (mise), `node 26.7.0`
- Missing shell env: add to `~/.bashrc`:

  ```bash
  export ANDROID_HOME=$HOME/Android/Sdk
  export ANDROID_SDK_ROOT=$ANDROID_HOME
  export PATH=$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH
  ```

### 6.2 What We Just Built

- **Fixed Expo deps:** `app.config.ts` canonical (`extra.apiUrl`), `eas.json` env per profile (`development 10.0.2.2:18080`, `preview settlrapi`, `production api.settlr...`), `src/api.ts` web `localStorage` fallback for `SecureStore`, `app/(tabs)/friends.tsx` + fixed `_layout.tsx` (5 tabs + FAB, `size 22`, `insets`).

### 6.3 Install Test Stack (one-time)

```bash
cd /home/wizard/Dev/Projects/settlr/settlr-mobile
# Fix Expo doctor (2/21 fails without these)
npx expo install expo-font expo-constants expo-linking --fix
# Unit/component
npm i -D @testing-library/react-native @testing-library/jest-native jest-expo msw
# Maestro E2E (Expo-recommended, YAML, no Gradle patching)
curl -Ls https://get.maestro.mobile.dev | bash
# or: mise use -g maestro@latest
maestro --version  # 1.41+
# Optional dev-client for on-device (needed when you add SQLCipher)
npm i -D expo-dev-client
npx expo prebuild --clean --platform android  # generates android/ (gitignore)
```

### 6.4 Scripts (make them executable, `chmod +x`)

**`settlr-mobile/scripts/emulator.sh`:**
```bash
#!/usr/bin/env bash
set -euo pipefail
export ANDROID_HOME=$HOME/Android/Sdk
export PATH=$ANDROID_HOME/emulator:$ANDROID_HOME/platform-tools:$PATH
avd="${1:-settlr}"
echo "→ Booting $avd ..."
emulator -avd "$avd" -no-window -no-audio -gpu swiftshader_indirect -no-snapshot-save &
# wait for boot
adb wait-for-device
adb shell 'while [[ -z $(getprop sys.boot_completed) ]]; do sleep 1; done;'
echo "✓ emulator ready: $(adb devices)"
adb shell input keyevent 82  # dismiss lock
```

**`settlr-mobile/scripts/e2e.sh`:**
```bash
#!/usr/bin/env bash
set -euo pipefail
# Assumes API at 10.0.2.2:18080 (local via 18080→8080) and emulator booted
export ANDROID_HOME=$HOME/Android/Sdk
export PATH=$ANDROID_HOME/platform-tools:$PATH
# Ensure API is up
curl -sf http://10.0.2.2:18080/health || curl -sf http://localhost:18080/health || { echo "API not up"; exit 1; }
# Build dev APK if missing (cached)
if [[ ! -f ./dist/development.apk ]]; then
  npx eas build --profile development --platform android --local --output ./dist/development.apk --no-wait || npx expo run:android --variant debug
fi
# Install on emulator
adb install -r ./dist/development.apk || adb install -r ./android/app/build/outputs/apk/debug/app-debug.apk
# Run Maestro flows against staging or local (override EXPO_PUBLIC_API_URL if needed)
maestro test maestro/flows --format junit --output ./maestro/report.xml
```

**`settlr-mobile/maestro/flows/auth.yaml` (example):**
```yaml
appId: com.settlr.app
---
- launchApp:
    clearState: true
- tapOn: "Create account"
- inputText: "Maestro Tester"
- tapOn: "Email"
- inputText: "maestro+${MAESTRO_RUN_ID}@test.local"
- tapOn: "Password"
- inputText: "Test123!Test123!"
- tapOn: "Create"
- assertVisible: "Verify your email"
- tapOn: "Back to login"
# For verified run, pre-verify via API: docker exec psql -c "update users set email_verified_at=now() where email like 'maestro%'"
- inputText: "maestro+${MAESTRO_RUN_ID}@test.local"
- inputText: "Test123!Test123!"
- tapOn: "Sign in"
- assertVisible: "Overview"
- assertVisible: "Home"
```

Create sibling flows: `groups.yaml` (create/list), `expense-equal.yaml`, `friends.yaml`, `invite.yaml`, `deep-link.yaml` (`maestro test` can run `adb shell am start -a android.intent.action.VIEW -d "settlr://groups/xxx"`), `offline.yaml`.

### 6.5 CI — GitHub Actions (add to `settlr-mobile/.github/workflows/ci-cd.yml`)

```yaml
android-e2e:
  needs: verify
  if: github.event_name == 'push'  # or 'pull_request' with funnel
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-node@v4
      with: { node-version: 22, cache: npm }
    - uses: actions/setup-java@v4
      with: { distribution: temurin, java-version: 21 }
    - uses: android-actions/setup-android@v3
    - run: npm ci
    - run: npm run typecheck && npm test
    # Cache AVD + Maestro
    - uses: reactivecircus/android-emulator-runner@v2
      with:
        api-level: 34
        target: google_apis
        arch: x86_64
        avd-name: settlr
        script: |
          npm run export:web &
          ./scripts/e2e.sh
    - uses: actions/upload-artifact@v4
      if: always()
      with: { name: maestro-report, path: maestro/report.xml }
```

Keep `verify` fast (jsdom+RNTL+vitest), `android-e2e` slower but catches real device issues (SecureStore, deep links, rotation, large text, permission). Cache `~/.android` and `maestro`.

### 6.6 Daily Test Loop (to not ship bugs)

1. **Pre-commit local:** `npm run typecheck && npm test && npm run export:web` (web+mobile) + `go vet` (api) — **must be green** or CI will block.
2. **Emulator smoke before push:** `scripts/emulator.sh &` + `scripts/e2e.sh` → all 5 Maestro flows pass + no `adb logcat` crashes.
3. **PR:** CI `verify` + `android-e2e` must be `● Success` before merge.
4. **Staging bake:** after `main` push, manual smoke on `settlr-staging` + `settlrapi` health, then Maestro against staging (`EXPO_PUBLIC_API_URL=https://settlrapi.theswissknife.com maestro test --env STAGING=1`).
5. **Prod promote:** only after staging bake ≥30 min, no Sentry/Crashlytics alerts, QA signs off.

---

## 7. Backups, Monitoring, Secrets — Don't Skip

- **Backups (cron on each DB host):**
  ```bash
  # /etc/cron.d/settlr-backup
  0 2 * * * root pg_dump -Fc -U settlr -d settlr | gpg --encrypt --recipient ops@settlr.app | rclone rcat gdrive:backups/settlr-$(date +\%F).dump.gpg
  0 2 * * * root tar czf - /var/lib/docker/volumes/settlr_uploads/_data | gpg -e | rclone rcat gdrive:backups/uploads-$(date +\%F).tar.gz.gpg
  # Test restore monthly on pgdata-restore volume
  ```
- **Monitoring:** UptimeRobot on `GET /health` (local, tailscale, cf, vercel proxy), Vercel Analytics + Sentry for JS, `docker logs --tail` alerts, `govulncheck` in CI already.
- **Secrets:** Remove plaintext `BREVO_API_KEY` from `settlr-api/.env` (currently `xkeysib-...`); use `/etc/settlr/*.env 600` + `git-crypt`/`sops` or Vercel/EAS env UI. Add `detect-secrets` pre-commit hook. Never log tokens.

---

## 8. Troubleshooting

- `docker compose config` error `set DATABASE_URL` → you forgot `-f docker-compose.local.yml` or `.env.local` — base defaults to staging, local needs override.
- `CORS error` on web → `APP_ENV=staging|production` is strict, check `CORS_ORIGINS` includes `https://settlr-staging.vercel.app` or `https://settlr.theswissknife.com` exactly.
- `maestro test` cannot find `com.settlr.app` → `adb install -r` missing or emulator not booted (`adb devices` should show `emulator-5554 device`).
- `10.0.2.2:18080` timeout from emulator → host API not listening on `127.0.0.1:18080` (check `docker ps`, `curl localhost:18080/health` on host).
- Vercel build fails `NEXT_PUBLIC_API_URL` mismatch → Dashboard `settlr` vs `settlr-staging` have different `API_URL`/`NEXT_PUBLIC_API_URL`; re-`vercel pull` and check `vercel env ls`.
- `expo-secure-store` `setValueWithKeyAsync is not a function` on web → we patched `src/api.ts` with `localStorage` fallback; if you see it, rebuild `npm run export:web`.

---

## 9. TL;DR Commands

```bash
# LOCAL (full reset)
cd settlr-api && docker compose -f docker-compose.yml -f docker-compose.local.yml down -v && docker compose -f docker-compose.yml -f docker-compose.local.yml up -d && curl localhost:18080/health && open http://localhost:8025
cd ../settlr-web && cp .env.development.example .env.local && npm i && npm run dev
cd ../settlr-mobile && cp .env.development.example .env.local && npm i && npm start -- --web

# STAGING deploy (after main push)
vercel ls --scope nabinkhanal00
docker pull ghcr.io/nabinkhanal00/settlr-api:main && docker compose -f docker-compose.yml -f docker-compose.staging.yml up -d && curl http://127.0.0.1:18080/health

# PROD deploy (after staging bake, pin tag)
TAG=sha-23d2861 ssh prod-vm 'cd /opt/settlr && docker pull ghcr.io/nabinkhanal00/settlr-api:$TAG && TAG=$TAG docker compose -f docker-compose.production.yml up -d && curl https://api.settlr.theswissknife.com/health'
```

Keep this guide in repo root and link from `settlr-api/deploy/README.md` → `../../DEPLOYMENT.md`.

