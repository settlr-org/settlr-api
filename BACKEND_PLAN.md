# Settlr backend plan

## Baseline

The service is Go `net/http` with PostgreSQL migrations, Argon2id password hashing, JWT access tokens, rotating refresh sessions, cursor-based lists, integer smallest-unit money, local receipt storage, mail delivery, and an `/health` endpoint. The current test suite and `go vet` pass. The implementation registers materially more operations than the current 68-operation OpenAPI file, so the first backend milestone is contract reconciliation.

## API contract and additive interfaces

Make OpenAPI the reviewed source of truth and generate `@nabinkhanal00/settlr-api-client` for TypeScript consumers. Add schemas, enums, pagination/error examples, auth requirements, upload limits, and every route currently registered, including personal expenses/budget/stats/export, recurring expenses, rates, payment info, notification preferences, and friend ledgers.

Add web auth endpoints: `POST /api/v1/auth/web/login`, `POST /api/v1/auth/web/refresh`, and `POST /api/v1/auth/web/logout`. They return only the short-lived access token/user payload and rotate a host-only Secure HttpOnly SameSite cookie. Validate Origin and trusted proxy headers for cookie-authenticated state changes. Existing bearer login/refresh/logout remains for mobile compatibility.

Add idempotency-key handling for expense, personal expense, settlement, and recurring creation; device registration/removal for Expo push tokens; and authenticated media endpoints for user avatar, group avatar, payment QR, and receipts. Keep media on the local volume, enforce route-specific size/type limits, return opaque authenticated media IDs, generate thumbnails, and back up the volume encrypted off-machine.

## Hardening and operations

Keep the Cloudflare Worker and existing machine ingress unchanged. Configure exact production origins for `settlr.theswissknife.com` and `arch.tailbd5522.ts.net`, explicit local development origins, trusted-proxy client IP parsing, and no wildcard credentialed CORS. Replace the default `http.ListenAndServe` with explicit read-header/read/write/idle timeouts and header limits. Add route-aware multipart limits, outbound rate-provider timeouts, migration locking, graceful shutdown, readiness checks, structured redacted logs, and dependency vulnerability checks.

Sanitize Compose for production: do not publish PostgreSQL publicly, use non-default credentials from a secret manager/environment, keep Mailpit development-only, run the API container as non-root, and never commit `.env` or resolved Compose output. Rotate the credentials currently present in the local environment before any public exposure. Preserve the existing Nginx/Worker route; only document its proxy headers and required API CORS origins.

## Verification

CI runs `go test ./...`, `go vet ./...`, OpenAPI lint plus route-parity tests, integration tests for auth/session rotation, authorization, split arithmetic, pagination, uploads, idempotency, push preference behavior, and media access. Build the Docker image and validate Compose in an isolated project. Smoke-test `/health` and the documented domains without mutating the live database.
