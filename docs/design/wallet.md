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
- Reports: `GET /reports?…filters…` (totals + by-category for pie + entries), `GET /reports/export.csv`.
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
  Stale category/account ids stay in the arrays (they simply match nothing).
- **Loans.** Bookings are transactions with `source = LOAN` (+ `loan_id`, `loan_record_id`):
  they move balances, appear in the ledger as `read_only`, are excluded from income/expense
  everywhere (overview, summaries, budgets, `/personal/*`), and PATCH/DELETE/duplicate/pay/
  skip on them → 409 `LOAN_MANAGED`. Cross-currency bookings convert via rates or take
  `account_amount`. Deleting an account unlinks loans/records and deletes their bookings.

Non-goals: Ivy Cloud / GitHub backup (removed upstream), wishlist, in-app changelog/poll.
