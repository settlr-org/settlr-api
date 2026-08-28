# Settlr Testing Strategy — Don't Ship Bugs

> Goal: every feature in `settlr-web/FEATURE_CHECKLIST.md` (51 API routes) is verified **before** production. Stack: Vitest (unit) + RNTL (component) + Maestro (E2E on Android emulator) + EAS (OTA) + Manual browser smoke.

## 1. Test Pyramid

```
Manual browser smoke (agent-browser, light/dark, 1280/390) — 5 min before merge
        ▲
Maestro E2E on Android emulator (Pixel 7, API 34) — CI android-e2e job, ~15 min
        ▲
RNTL Component + Vitest (jsdom) — CI verify, <1 min
        ▲
Go race + vitest unit — CI verify, <1 min
```

- **Unit:** `money` splits, `api` timeout/refresh, `config` strict env.
- **Component (RNTL):** Login form, groups list, add expense modal, friends search, invite — mocked `msw` + `expo-secure-store` mock (`test/setup.ts`).
- **E2E (Maestro):** Full user journeys on **real Android** (not just web). Web smoke mirrored on mobile.
- **OTA/Preview:** EAS `preview` channel → internal testing, `production` → users.

## 2. What to Test — Full Feature Matrix

Derived from `FEATURE_CHECKLIST.md` and `openapi.json` (51 routes). **Must be green before prod promote.**

| Area | Web | Mobile | E2E Flow (`maestro/flows/*.yaml`) |
|------|-----|--------|-----------------------------------|
| Auth | register → 201 + verification_required, login 403 EMAIL_NOT_VERIFIED, resend non-enumerating, verify via token, login 200, keep-me-signed, deep link, logout, forgot/reset/verify edge | same via `authenticate` + `SecureStore` | `auth.yaml` |
| Navigation | sidebar 5+3 desktop, warm cache 60s, mobile 6 (Home/Groups/Friends/Add/Activity/Account), drawer, theme before paint, Ant icons only | 5 tabs + FAB, safe-area, 76+insets, size 22, borderTopWidth | manual + Maestro `deep-link.yaml` |
| Overview | net/owed/owing, groups, quick actions, friends preview, activity preview | same | `groups.yaml` |
| Groups | CRUD, types HOME/TRIP/COUPLE/EVENT/OTHER, ledger, tabs Expenses/Balances/Members/Settlements, search, equal/exact/percentage/shares, delete, repayments, settlement, member add/role/remove/leave, archive/delete, recurring, stats/csv/json, expense edit, comments, attachments, settlements edit | create + equal-split (others TODO) | `expense-equal.yaml` |
| Friends | search, invite email, pending, request accept/reject, direct ledger, profile, block | **NEW** `friends.tsx` same | `friends.yaml` |
| Personal | monthly spend/budget, expense CRUD, categories, CSV | — (future) | — |
| Activity/Search/Notifs | timeline, search people/groups/expenses, inbox read/unread | activity feed only | `groups.yaml` activity part |
| Settings | name/currency, notif prefs, password, sessions, payment QR, resend, export, delete | currency, notifs, security | manual |

**Every new Splitwise feature:** add backend `migration/test` + `openapi` + web + mobile + Maestro flow **as one slice** (see `HANDOFF.md:169`).

## 3. Running Locally (pre-push gate)

```bash
# All repos — must pass before git push
cd settlr-web && npm run format:check && npm run typecheck && npm test && npm run build
cd ../settlr-api && test -z "$(gofmt -l .)" && go vet ./... && go test -race ./... && go build ./cmd/settlr
cd ../settlr-mobile && npm run format:check && npm run typecheck && npm test && npm run export:web

# Android E2E (full)
cd settlr-api && docker compose -f docker-compose.yml -f docker-compose.local.yml up -d && curl localhost:18080/health
cd ../settlr-mobile
./scripts/emulator.sh &   # boots Pixel 7 in ~25s, leaves running
./scripts/e2e.sh          # builds APK if missing, installs, runs maestro/flows/*.yaml
# Check: http://localhost:8025 for Mailpit tokens (local leaks token in JSON)
# Manual browser: npm run dev --workspace=web, open 1280 & 390, light/dark
```

## 4. CI Gates

- **PR (feat/x):** only `verify` runs. **Do not merge** if red. No deploy, no OTA.
- **Push main:** `verify` → `android-e2e` (needs verify pass) → if green, auto `vercel` deploys staging+prod web, GHCR publish, EAS preview. **Staging bake 30 min** before prod manual pull.
- **Branch protection:** Require `verify` + `android-e2e` (for main) as required checks in GitHub Settings → Branches → `main` → Require status checks.

## 5. Maestro Flows — Where

- `settlr-mobile/maestro/flows/auth.yaml`, `groups.yaml`, `expense-equal.yaml`, `friends.yaml`, `deep-link.yaml`
- Run subset: `maestro test maestro/flows/auth.yaml`
- Report: `maestro/report.xml` + screenshots `~/.maestro/tests/...` (uploaded as artifact in CI)
- Local override: `EXPO_PUBLIC_API_URL=http://10.0.2.2:18080 maestro test ...` (emulator) vs `https://settlrapi.theswissknife.com` (staging)

## 6. Debugging

- `adb logcat | grep -i settlr` — native crashes
- `adb shell input keyevent 82` — dismiss lock if emulator stuck
- `maestro --verbose` — yaml step dumps
- `expo-secure-store` web: we use `localStorage` fallback (`src/api.ts:12`), if you see `setValueWithKeyAsync is not a function`, rebuild.

## 7. When to Add New Tests

- New endpoint → add `api.test.ts` + `handlers_integration_test.go` + RNTL component test + Maestro flow
- New screen → add `*.stories.tsx` in `settlr-design` + RNTL + Maestro
- Bug fix → add regression test that would have caught it (unit or Maestro) before fixing code

## 8. Release Checklist (copy to PR description)

```
- [ ] `verify` green locally (web/api/mobile)
- [ ] `android-e2e` green locally (or CI)
- [ ] agent-browser smoke: landing, login, overview→groups warm cache (loadingSeen 0), friends invite, dark toggle, 390 no overlap
- [ ] staging health: curl 4 endpoints 200, manual smoke on settlr-staging
- [ ] maestro staging smoke (optional but recommended)
- [ ] prod pin tag chosen, backups fresh, rollback plan known
```

Never ship if any box is red.
