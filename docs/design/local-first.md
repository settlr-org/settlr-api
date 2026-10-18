# Local-first wallet with optional end-to-end-encrypted sync (Android)

Status: design, agreed direction 2026-10-18. Supersedes the server-side guest accounts shipped in API #40
(`POST /auth/guest`, `/auth/upgrade*`, `users.is_guest`), which become unused once this lands.

## Goal (user's words)
"No login and offline (use device storage). Login/network should only be required for group expenses setup.
The system should have a sync feature: if enabled, it is synced to the cloud, and personal expense tracking
should have end-to-end encryption."

## Principles
1. **The wallet is on the device.** Accounts, transactions, categories, tags, budgets, loans, planned payments,
   rates, settings, reports, search and exports all work with no account and no network.
2. **An account is only for people features:** groups, friends, invitations, shared expenses, settle-up.
3. **Sync is optional and end-to-end encrypted.** The server stores opaque ciphertext for personal data and can
   neither read nor compute on it. Group data stays server-side and readable (the server must compute balances).
4. **No second implementation drifting away.** The on-device engine is verified against the real API with a shared
   conformance suite (same scripted requests, same expected JSON).

## Architecture (Android)
The app talks to its backend through Retrofit/OkHttp; every write already goes through an HTTP-shaped outbox. So the
engine is a **local server behind the OkHttp chain**:

```
Compose UI → ViewModels → SettlrApi (Retrofit) → OkHttp
                                                   ├─ LocalWalletInterceptor  (wallet routes → LocalWalletServer → SQLite)
                                                   ├─ OfflineCacheInterceptor (group routes, cached while signed in)
                                                   └─ network (group routes, auth, sync)
```

* `LocalWalletServer` answers exactly the wallet routes of `SettlrApi` (accounts, transactions, ledger, overview,
  reports, budgets, loans, planned, tags, categories, rates, settings, search, export) with JSON identical to the
  server's, from a SQLite database (Room/SupportSQLite) mirroring the server schema (hundredths storage,
  COALESCE(occurred_at, due_at) range semantics, transfer pairs, adjustments, FX with rate overrides).
* The UI, ViewModels, offline cache and outbox are unchanged. Wallet writes succeed locally at once; the outbox
  stays for group writes.
* App state splits into **wallet (always available)** and **account (optional)**. `AuthState.SignedOut` no longer
  hides the app; it only gates group screens ("Sign in to use groups").
* **Unified ledger:** while signed in, group-derived entries (your share of group expenses, settlements) are fetched
  from `GET /ledger?source=group` into a read-only local table and unioned into ledger/overview/reports.
* Ids are client-generated UUIDv4 for everything local (server-assigned ids are no longer needed for wallet data).

## Conformance suite
`conformance/` holds scripted scenarios (JSON: request list + normalised expected responses, ids/timestamps masked).
The Go integration tests replay them against Postgres and write/verify the golden files; the Android JVM tests
replay the same files against `LocalWalletServer`. A scenario fails on any difference, so semantic changes must land
in both engines.

## Sync (optional, end-to-end encrypted)
* **Keys.** A random 256-bit *data key* is created on the device when sync is enabled. It is wrapped with a key
  derived from the user's *recovery passphrase* (Argon2id) and the wrapped blob + salt + KDF parameters are stored on
  the server (`PUT /sync/key`). Lose the passphrase and the cloud copy is unrecoverable by design; the device copy
  is unaffected. A second device downloads the wrapped key, the user types the passphrase, done. Passphrase change
  re-wraps the same data key.
* **Records.** Each local row (account, transaction, tag, …) is one record. The client encrypts
  `{kind, payload}` with AES-256-GCM (random nonce, record id + revision bound as AAD) and uploads
  `{id, rev, deleted, nonce, ciphertext}`. The server cannot see the kind or any field.
* **Protocol.** `GET /sync/changes?since=<cursor>` (server cursor = monotonically increasing sequence) and
  `POST /sync/records` (batch upsert; the server assigns the sequence). Conflicts: last writer wins per record by
  (client `updated_at`, device id); deletions are tombstones. Because derived values (balances, reports) are
  computed on the device, merging records is enough.
* **Server tables.** `sync_keys(user_id, salt, kdf, wrapped_key, version)` and
  `sync_records(user_id, id, seq, deleted, nonce, ciphertext, updated_at)` plus a per-user sequence. Hard caps on
  record size and count; rows removed by account deletion.
* **Not in scope for E2EE:** group data (readable by the server, needed for balances) and the user profile.

## What happens to existing server-side wallets
* Web keeps using the server wallet (plaintext) until a web local-first rewrite; it is **not** end-to-end encrypted.
  This is a known gap, to be decided separately.
* Android signed-in users with a server wallet get "Import from my account": the v2 JSON backup
  (`GET /me/export.json`) is loaded into the local database; afterwards the app offers to delete the server copy
  and to enable encrypted sync.

## Delivery stages
1. Conformance harness + `LocalWalletServer` for accounts/transactions/categories/tags/settings/ledger/overview;
   no-login Welcome flow; group gate. (Wallet usable offline with no account.)
2. Remaining engine: budgets, loans, planned payments, rates, reports, search, exports, backup/restore + CSV import.
3. Unified ledger from group entries; "Import from my account".
4. Encrypted sync (server tables + endpoints, client crypto, UI: enable, passphrase, add device, status).
5. Hardening: migrations of the local schema, backup/restore of the local DB, large-data performance.
