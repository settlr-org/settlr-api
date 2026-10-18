# Wallet conformance suite

Shared by `settlr-api` (Postgres) and `settlr-android` (on-device SQLite engine, see
`docs/design/local-first.md`). It pins the wallet API's observable behaviour so the two engines cannot drift.

* `scenarios/<name>.json` — a script of requests (`method`, `path`, optional `body`, optional `id` to save the
  response, `quiet` to compare only the status). `{{id.path.to.value}}` references a saved response.
* `golden/<name>.json` — what this API answered, normalised: generated ids become `<id:N>` (numbered by first
  appearance), `created_at`/`updated_at`/`archived_at`/cursors become `<ts>`, adjustment times become `<ts>`.

Go: `go test ./internal/wallet -run TestConformance` verifies; add `-update-conformance` to rewrite the golden files
after an intended behaviour change. Android copies this folder into its unit-test resources
(`scripts/sync-conformance.sh` in settlr-android) and replays every scenario against the local engine.

Rate table used by both: `USD→NPR 133, USD→EUR 0.9, EUR→USD 1/0.9` (see `testRates` in `helpers_test.go`).
