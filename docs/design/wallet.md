# Settlr Wallet — Ivy Wallet parity on top of Splitwise-style splitting

Status: approved direction (2026-10-13). Target: feature parity with Ivy Wallet's final
release (repo archived 2026-04), on Android **and** web, plus the existing splitting features.

## Decisions

| Topic | Decision |
|---|---|
| Groups ↔ wallet | **Unified ledger.** Group expenses and settlements show up in the wallet. Paying a group expense or a settlement can name the account the money came from / went to. Your *share* is your expense; what you fronted for others is a receivable, not spending. |
| Loans | Ivy-style loans (lend/borrow + records) for anyone, **kept distinct** from friend balances. |
| Platforms | Android + web for everything. Android-only extras: widgets, biometric lock, daily reminder. |
| CSV import | Ivy's 10 formats + Splitwise export + a generic column-mapping importer. |
| Source of truth | Server (Postgres). Android keeps working offline through the existing outbox; all new writes take `Idempotency-Key`. |
| Back-compat | `/api/v1/personal/*` keeps working (Play still ships 1.1.0) as a thin layer over transactions. |

## Money model

- Amounts are `BIGINT` minor units, always positive; direction comes from `type`.
- Every account has one currency. A transaction's currency is its account's currency
  (transactions without an account — planned items, group shares — carry their own).
- Totals in the user's **base currency** (`users.default_currency`) use
  `rates(user overrides → daily fetched rates)`.

## Schema (migration 0013+)

```
accounts(id, user_id, name, currency, color, icon, include_in_balance bool default true,
         sort_order int, archived_at, created_at, updated_at, deleted_at)

transactions(id, user_id, type INCOME|EXPENSE|TRANSFER,
             account_id NULL,              -- null only for planned items without account
             to_account_id NULL,           -- TRANSFER only
             amount BIGINT >0, currency CHAR(3),
             to_amount BIGINT NULL,        -- TRANSFER into another currency
             category_id NULL, title, description,
             occurred_at timestamptz NULL, -- null = planned/unpaid
             due_at timestamptz NULL,      -- set for planned items
             planned_payment_id NULL, loan_id NULL, loan_record_id NULL,
             source MANUAL|ADJUSTMENT|LOAN|IMPORT|PERSONAL_LEGACY,
             import_ref text NULL,         -- dedupe on re-import
             created_at, updated_at, deleted_at)
transaction_tags(transaction_id, tag_id)
tags(id, user_id, name, color, icon, sort_order)

planned_payments(id, user_id, type, account_id, to_account_id, amount, to_amount, currency,
                 category_id, title, description, start_at, one_time bool,
                 interval_n int, interval_unit DAY|WEEK|MONTH|YEAR, active, ...)
   -- the server keeps exactly one pending transaction (occurred_at NULL, due_at set) per
   -- active plan; "pay" sets occurred_at (and generates the next one), "skip" deletes it
   -- and generates the next.

budgets(id, user_id, name, amount, currency, category_ids uuid[], account_ids uuid[], sort_order)
loans(id, user_id, name, type LEND|BORROW, amount, currency, color, icon, note,
      account_id NULL, occurred_at, sort_order, created_at, ...)
loan_records(id, loan_id, type INCREASE|DECREASE, amount, note, occurred_at,
             account_id NULL, transaction_id NULL, ...)
exchange_rate_overrides(user_id, base, quote, rate numeric, PRIMARY KEY(user_id, base, quote))

user_settings(user_id PK, month_start_day 1..28 default 1, hide_balance, hide_income,
              treat_transfers_as_flow bool, theme, reminder_enabled, reminder_time,
              onboarded_at)

-- unified ledger links (added to existing tables)
expenses.payer_account_id  NULL   -- the payer's account the money left from
settlements.from_account_id NULL  -- payer side
settlement_accounts(settlement_id, user_id, account_id) -- receiver picks later
```

Legacy `personal_expenses` rows migrate into `transactions` (type EXPENSE,
source PERSONAL_LEGACY) on a per-user default account **"Cash"** (created in the user's
default currency). The table is kept read-only for one release, then dropped.

## Unified ledger (read model)

A SQL view `ledger_entries` feeds balances, reports and search:

| Origin | Account effect | Counts as expense/income |
|---|---|---|
| Wallet transaction | ±amount on account (transfer: −amount / +to_amount) | its type, by category |
| Group expense, I paid, account chosen | −full amount on that account | **my share** as expense (expense's category) |
| Group expense, someone else paid | none | **my share** as expense |
| Settlement I paid, account chosen | −amount | none (it settles a debt) |
| Settlement I received, account chosen | +amount | none |

Shares come from `expense_splits`; edits/deletes of group expenses flow through
automatically because nothing is copied. Group entries are read-only in the wallet
(edit them in the group).

## API (all under /api/v1, all writes idempotent)

- Accounts: `GET/POST /accounts`, `GET/PATCH/DELETE /accounts/{id}`, `POST /accounts/reorder`,
  `POST /accounts/{id}/adjust` (posts an ADJUSTMENT to reach a target balance),
  `GET /accounts/{id}/summary?from&to`.
- Transactions: `GET /transactions?from&to&type&account&category&tag&q&min&max&excl_q&excl_tag&planned&cursor`,
  `POST /transactions`, `GET/PATCH/DELETE /transactions/{id}`, `POST /transactions/{id}/duplicate`,
  `POST /transactions/{id}/pay`, `POST /transactions/{id}/skip`, `POST /transactions/skip-overdue`.
- Ledger: `GET /ledger?…same filters…` (wallet + group entries, paginated),
  `GET /overview?from&to` (balance, income, expense, upcoming, overdue, projected balance).
- Categories: existing + `PATCH/DELETE /categories/{id}`, `POST /categories/reorder`,
  `GET /categories/{id}/summary?from&to`.
- Tags: `GET/POST /tags`, `PATCH/DELETE /tags/{id}`.
- Planned: `GET/POST /planned`, `GET/PATCH/DELETE /planned/{id}`.
- Budgets: `GET/POST /budgets?from&to` (with spent), `PATCH/DELETE /budgets/{id}`, `POST /budgets/reorder`.
- Loans: `GET/POST /loans`, `GET/PATCH/DELETE /loans/{id}`, `GET/POST /loans/{id}/records`,
  `PATCH/DELETE /loan-records/{id}`, `POST /loans/reorder`.
- Reports: `GET /reports?…filters…` (totals, category breakdown for the pie, per-account totals), `GET /reports/trend`, `GET /reports/export.csv` (see Phase 3 notes).
- Rates: existing `GET /rates` + `GET/PUT/DELETE /me/rates/{base}/{quote}`.
- Settings: `GET/PATCH /me/settings`.
- Backup: `GET /me/backup` (zip of JSON), `POST /me/restore` (replace or merge).
- Import: `POST /import/preview` (multipart CSV + format) → parsed rows, mapping, duplicates;
  `POST /import/commit`.
- Group links: `PATCH /expenses/{id}` accepts `payer_account_id`; settlements accept
  `from_account_id`; `PUT /settlements/{id}/account` for the receiver.

## Phases (each is its own PRs, merged API → web → Android)

1. **Foundation** — accounts, transactions (income/expense/transfer, multi-currency),
   tags, settings, rate overrides, personal→transactions migration + compat layer,
   ledger view incl. group links. Web + Android: Accounts tab, transaction editor with
   calculator keypad, account/category detail, home with period selector.
2. **Planning** — planned payments (upcoming/overdue, pay/skip), budgets, loans.
3. **Insight** — reports with full filter set, pie charts with drill-down, balance
   projection, transaction search, CSV export of filtered results.
4. **Data** — backup/restore, CSV import (all formats), onboarding flow.
5. **Android platform** — biometric lock, widgets (add-transaction, balance), daily
   reminder, AMOLED theme, hide balance/income, large-number formatting.

### Phase 2 implementation notes (migration 0015)

- **Planned payments.** One pending transaction per active plan (unique partial index).
  `resolved_until` = due date of the latest paid/skipped occurrence; the next occurrence
  is the first one after it, always computed from `start_at` on the UTC calendar
  (month/year add clamps to month end: Jan 31 → Feb 28 → Mar 31; Feb 29 yearly → Feb 28,
  Feb 29 again in leap years). Plan PATCH updates the pending item in place (same id).
  DELETE of a pending plan item via `/transactions/{id}` acts like skip; PATCH setting its
  `occurred_at` acts like pay; a paid plan occurrence can't be made unpaid (422).
  `skip-overdue` moves recurring plans to their first occurrence ≥ now. Deleting an
  account deletes its plans. Plans without account are allowed (pay needs `account_id`).
- **Overview.** `overdue` = unpaid items due before now (any date); `upcoming` = due now or
  later inside [from, to). `projected_balance` = balance + signed sum of all unpaid items
  due before `to` on included accounts (account-less items count; transfers per side).
- **Budgets.** EXPENSE ledger entries (my group shares included) in range, matching
  categories (empty = all). `account_ids` empty → overview rule (non-balance accounts
  excluded, every group share counts); set → only entries booked to those accounts (a group
  share only when I paid it from one of them). Adjustments, loans, planned items never count.
  Stale category/account ids stay in the arrays (they simply match nothing); PATCH only
  validates newly added ids, so such budgets stay editable (send the list without them to prune).
- **Loans.** Bookings are transactions with `source = LOAN` (+ `loan_id`, `loan_record_id`):
  they move balances, appear in the ledger as `read_only`, are excluded from income/expense
  everywhere (overview, summaries, budgets, `/personal/*`), and PATCH/DELETE/duplicate/pay/
  skip on them → 409 `LOAN_MANAGED`. Cross-currency bookings convert via rates or take
  `account_amount`. Deleting an account unlinks loans/records and deletes their bookings.

### Search, export, backup and filters (migrations 0016, 0017)

- **Global search** (`GET /search?q=`). Existing `users`/`groups`/`expenses` are unchanged;
  seven wallet groups are added, always present (empty array when nothing matches or `q` is
  empty): `transactions`, `accounts`, `loans`, `planned`, `budgets`, `categories`, `tags`.
  All are scoped to the caller (categories: own + system), at most 10 each, matched
  case-insensitively as a substring with `%`, `_` and `\` taken literally (the pattern is
  escaped in Go and used with `ILIKE ... ESCAPE '\'`); `q` is trimmed (min 1 character, as
  before) and cut at 100 characters for the wallet groups. `transactions` match title,
  description or tag name, include planned items (their pending plan occurrences too), and
  exclude `ADJUSTMENT` and `LOAN` sources (loans are listed in `loans`). Order: transactions
  newest first, the rest by sort order / name. Migration 0016 adds trigram GIN indexes on
  `transactions.title/description` (pg_trgm was already enabled by 0005); the transaction
  query UNIONs title / description / tag matches so each branch can use them. The other
  groups are small per user and use the existing per-user indexes.
- **Ledger filters.** `/transactions` and `/ledger` accept `source` and `exclude_source`
  (comma lists, case-insensitive; unknown value → 422) and `hide_adjustments=true|false`
  (alias for `exclude_source=ADJUSTMENT`; anything else → 422). Sources: `MANUAL`,
  `ADJUSTMENT`, `LOAN`, `IMPORT`, `PERSONAL_LEGACY` (`/ledger` also `GROUP`, the value of
  group expense shares and settlements). They are plain WHERE predicates, so the
  `(sort time, id)` cursor stays correct. Clients hide "Initial balance"/"Balance
  adjustment" rows in history by default with `hide_adjustments=true`.
- **Backup format, `GET /me/export.json` (version 2).** A single JSON document:
  `version` (2), `exported_at`, `user` (profile without password hash/tokens/sessions),
  `settings`, `rate_overrides`, `accounts`, `categories` (owned only; system categories are
  referenced by id), `category_order`, `tags`, `transactions`, `planned_payments`, `budgets`,
  `loans` (each with `records`), `groups` (per group: `expenses` and `settlements` from the
  caller's view), and `data`, the version 1 flat group-expense list kept for existing readers.
  Rules a restore can rely on: only live (not soft-deleted) rows; every id is the stored id
  and every cross-reference (`account_id`, `to_account_id`, `category_id`, `tag_ids`,
  `planned_payment_id`, `loan_id`, `loan_record_id`, budget `category_ids`/`account_ids`)
  points at an id in this document, or at a system category; transactions are the raw
  rows including planned items (`occurred_at` null), `ADJUSTMENT`/`LOAN` bookings,
  `source` and `import_ref`; amounts are stored minor units (hundredths for every currency).
  `personal_expenses` have no section: migration 0013 moved them into transactions
  (`source = PERSONAL_LEGACY`). Transactions stream in id-ordered pages of 1000; if a page
  fails after the response started the connection is aborted (`http.ErrAbortHandler`, re-panicked by `httpx.Recover`; never a cleanly truncated file). Export handlers extend the write deadline to 5 minutes; migration 0017 adds `(user_id, id)` for the paging.
  New sections must be additive; breaking changes bump `version`.
- **CSV.** `/me/export.csv` is unchanged (group expenses). New, all authenticated and
  owner-scoped: `/me/export/transactions.csv` (all wallet transactions incl. planned and
  adjustment/loan bookings, newest first, streamed in pages; group-share ledger entries are
  not included), `/me/export/accounts.csv`, `/me/export/budgets.csv`, `/me/export/loans.csv`.
  Cells are written with `encoding/csv` (RFC 4180 quoting); text cells starting with
  `=`, `+`, `-`, `@`, tab or CR get a leading `'` (formula injection), numeric cells never.
  Amounts appear as `amount_minor` (stored integer) and `amount` (decimal, 2 places).
- **Mail diagnostics.** Brevo answers 201 and rejects later when the sender is not
  validated. The API now logs every successful send (`provider`, `message_id`, `subject`;
  recipients only masked, e.g. `m***@gmail.com`) and at startup, in a background goroutine
  with a short timeout, calls Brevo `GET /v3/senders` and WARNs when `MAIL_FROM_EMAIL` is not
  registered/active (an unlisted sender on a domain-authenticated Brevo domain, checked via `GET /v3/senders/domains`, is fine); it also WARNs for log-only mode (empty/unknown `MAIL_PROVIDER` without
  credentials), a missing Brevo key and placeholder `@example.com` senders. `/readyz` is
  deliberately unchanged (it is unauthenticated; mail state is not worth exposing there).

### Phase 3 implementation notes: reports (migrations 0018, 0019)

Everything below shares the `/ledger` filter set, parsed by one function (`parseFilters`) and
written once as a SQL block that `ListLedgerEntries`, `ListTransactions` and the four report
queries (`wallet_reports.sql`, which says to keep the blocks in sync) all carry, so a report
and the entries list behind it can never disagree (a consistency test pages `/ledger` for ~30
filter combinations and compares count/income/expense with `/reports`).

- **Filters** (identical on `/ledger`, `/transactions`, `/reports`, `/reports/trend`,
  `/reports/export.csv`): `from`, `to`, `type` (INCOME,EXPENSE,TRANSFER; ledger also
  SETTLEMENT; unknown -> 422), `account` (either side of a transfer), `category` (ids; the value
  `none` = uncategorized, alone or mixed with ids), `tag`, `excl_tag`, `q`, `excl_q`, `min`, `max`
  (the entry's own amount in its own currency; min > max -> 422), `source`, `exclude_source`,
  `hide_adjustments`, `group`, `kind`, `planned`, and (ledger and reports)
  `include_group_shares=true|false`. Id lists hold at most 100 ids.
  **Keywords.** `q` is split on whitespace; every term must occur (case-insensitive) in the
  title, the description or a tag name; `excl_q` drops an entry when any term occurs. Terms are
  LIKE-escaped (`%`, `_`, `\` are literal). Only the first 10 terms are used and each is cut to 100
  characters: silently, never a 422, so the web search box can send its raw value to every endpoint.
  SQL: `title ILIKE ALL(p) OR description ILIKE ALL(p)`, then for one term a short-circuiting tag-name
  EXISTS, for several terms the concatenation `title || ' ' || description || ' ' || tag names`
  (terms split across fields; a term has no whitespace so it can never straddle the separators).
  `excl_q` is `title ILIKE ANY OR description ILIKE ANY OR tag EXISTS`. Group entries have no tags.
  This replaced the earlier single unescaped substring on title/description.
- **Date basis.** Range filters (`from`/`to`) compare `COALESCE(occurred_at, due_at)` everywhere
  (`/ledger`, `/transactions`, reports, trend, CSV; overview/summaries use `occurred_at`, the same
  value for paid entries), so a group share dated D counts at 00:00 UTC of D whatever time it was
  created and every view agrees for windows that do not start at UTC midnight (e.g. +05:45).
  `sort_at` is used only for ordering and the cursor.
- **What counts.** Income and expense are the paid INCOME/EXPENSE wallet transactions and my
  share of group expenses (a group expense I only fronted has share 0: it is not an entry and
  not counted). Never counted: `ADJUSTMENT` and `LOAN` sources (whatever `source` says),
  settlements, planned items unless `planned=true|all` is passed (the opt-in is the one
  deliberate departure from "planned never counts": it lets a client show a forecast), and
  transactions on accounts with `include_in_balance = false` (same rule as `/overview`, so the
  report for a period equals the overview; an explicit `account` filter lifts the rule, so
  the user can report on such an account). **Transfers** are reported as `transfers_out`
  (source leg, source currency) and `transfers_in` (target leg, `to_amount` in the target
  currency), each converted separately; with an `account` filter only legs on the selected
  accounts count. With the user setting `treat_transfers_as_flow` they are also added to
  `income`/`expense`/`net`, `by_account` and the trend (and to `count`), never to `by_category`
  (transfers have no category).
- **Currency.** SQL groups by (currency, category | account | bucket); Go converts each group
  with the overview converter (user overrides, daily rates, cross rates) and sums. Pairs
  without a rate are left out and listed in `missing_rates`. Nothing is converted per row except
  the CSV `base_amount`, which is the row's own amount.
- **`GET /reports`**: `currency, from, to, income, expense, net, transfers_in, transfers_out,
  count, entries_count, breakdown, by_category[], by_account[], treat_transfers_as_flow, hide_adjustments,
  missing_rates`. `count` = entries that contribute to income/expense/net (no loans, adjustments,
  settlements or zero-share rows; transfers only as flow); `entries_count` = exactly the rows `/ledger`
  and the CSV list for the same filters. `breakdown=expense|income` (default expense) picks the side of `by_category`
  (`category_id|null, name, icon, color, amount, count, percent`), sorted by amount desc then
  name, uncategorized as `category_id: null, name "Uncategorized"` (categories the caller cannot
  see count as uncategorized). **Percent rule:** one decimal; tenths of a percent are allotted
  by the largest-remainder method (floor each share of 1000, hand the leftover tenths to the
  largest fractional parts, earlier = larger amount wins ties), so the list always sums to exactly
  100.0. `by_account` rows (`account_id|null, name, currency, income, expense, net, base_income,
  base_expense, base_net`): amounts in the account's own currency, `base_*` in the base currency;
  `account_id: null, name "No account"` collects group shares someone else paid (base currency).
- **`GET /reports/trend`**: `from` and `to` required, `bucket=day|week|month` (default month).
  Response `currency, from, to, bucket, buckets[{start,end,income,expense,net}], missing_rates`,
  zero-filled from the bucket containing `from` to the last bucket starting before `to`. Buckets
  are UTC (nothing here depends on DST): days at 00:00; weeks are ISO weeks starting **Monday**;
  months start on the user's `month_start_day` (default 1 = calendar months, so the trend lines
  up with the home period). Buckets are whole (`end` exclusive) while only entries inside
  [from, to) are counted. More than 400 buckets -> 422 ("use a larger bucket or a shorter
  range"). Ranges over 10 years and `to <= from` -> 422 on all report endpoints.
- **`GET /reports/export.csv`**: the ledger entries behind the numbers, newest first, streamed
  in keyset pages (1000 rows, doubling up to 20 000: every page rescans the ledger view, so big
  exports use few pages; 100k rows export in ~2.5 s). It is the `/ledger` result set, so
  ADJUSTMENT/LOAN rows appear unless filtered out. Columns `date, kind, type, title, description,
  account, to_account, category, tags, amount_minor, amount, currency,
  to_amount_minor, to_amount, to_currency (transfers: what arrived), base_amount, base_currency,
  group, source` (`kind` = TRANSACTION | GROUP_EXPENSE | SETTLEMENT; amount = the entry's own
  amount, my share for group expenses). Cell quoting, formula protection (`csvSafe`), the 5-minute
  write deadline, flushing and abort-on-failure (`http.ErrAbortHandler`) are the helpers of
  `internal/export` (exported for reuse). Filename `settlr-report-<from>-<to>.csv`.
- **Former group members (migration 0019).** Group expense shares and settlements stay in the ledger
  of a user who left or was removed (it is their own financial history and their account balances
  depend on it: money they paid from an account keeps its effect), but the free text other people
  wrote is redacted once `group_members` no longer has the user: `description` of group entries
  (expense notes, settlement notes) becomes `''` in `/ledger`, the entries inside category/account
  summaries and `/reports/export.csv`. Title (the expense description), amount, category and group name
  stay. There is no removal timestamp, so expenses created or edited after the user left are not told
  apart. Search (`/search` expenses), the group export and the v2 backup's `groups` section already
  require current membership. Reports contain no free text.
- **Performance** (100k transactions + 6k group shares for one user, Postgres 18): all-time
  report ~0.6 s end to end (three ~130-220 ms aggregates, sequential scans: the user owns the
  whole table), a month ~60 ms after migration 0018, which adds
  `transactions (user_id, COALESCE(occurred_at, due_at)) WHERE deleted_at IS NULL`: the ledger
  view's sort time, so date ranges become index range scans (a month: 213 ms -> 9-20 ms per
  aggregate). Keyword filters are written term by term (`title ILIKE t OR description ILIKE t OR id = ANY(ids of
  tags whose name matches t)`, AND-ed over up to 10 terms) so each arm can use the 0016 trigram indexes and
  the tag index: on 500k transactions of one user, `/ledger` page with `q`: rare term 5.6 s -> 49 ms, tag
  name 6.5 s -> 120 ms, two terms 5.4 s -> 85 ms, no match 6.0 s -> 0.6 ms, very common term 2.6 s -> 0.8 s
  (before: one `ILIKE ALL` over the concatenated title/description/tag names, a scan with a per-row tag
  aggregate). An index on the same expression in
  descending order did not help `/ledger` paging (the planner keeps Sort over Append for the
  UNION view), so none was added.

Non-goals: Ivy Cloud / GitHub backup (removed upstream), wishlist, in-app changelog/poll.
