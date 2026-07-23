# Phase 3 Analytics PR Plan

Executable hand-off for Phase 3 (build phase 3, "Analytics views", per `docs/moneta-plan.md:298-302`).
Do **not** implement until the maintainer explicitly starts a PR.
Do **not** start Phase 4 (recurring / anomaly detection) work in this stack.
No new dependencies.
Keep CGO-free.
Fake data only in tests and fixtures.
Do **not** commit or push unless the maintainer explicitly asks.

**Source:** grounded against `main` at `bb71492` (Phase 2 complete + polish PR #11 merged); line numbers current as of 2026-07-23.
**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md` (binding: int64 cents, analytics exclusion via `excluded`, TOON/AXI output conventions, REST mirrors reads).

> **Read this first.**
> The three cross-cutting decisions D3-1/D3-2/D3-3 are **settled** (see below) - do not reopen them.
> PR1 and PR2 are the correctness **foundation** and must land before any feature PR.
> One finding from the Phase 2 review (M1, liability `abs()`) is resolved *by* PR1 - do not touch that code outside PR1.

## Goals

1. Land the analytics-correctness foundation: an honest liability balance sign convention (D3-1) and nullable optional money (D3-2), so every downstream figure is trustworthy.
2. Ship the Phase 3 read surface: `networth --history`, `moneta trends` (five metrics), `moneta cards`, and the bare `moneta` dashboard, each mirrored over the existing authenticated loopback REST server.
3. Keep analytics compute-on-read (D3-3); introduce no precompute tables in this stack.

## Non-goals

- Recurring detection, anomaly detection, `moneta recurring`, drift flags, upcoming-bill prediction - **build phase 4**. The dashboard surfaces those slots as empty placeholders (see PR10).
- `moneta tag` and the provider-vs-user field-ownership decision (Phase 1 D2 carry-forward, `moneta-plan.md:194-195`) - separate stack; trends reflect provider categories until it lands.
- Manual / rmcsv providers, `moneta add`, token-less provider-item storage.
- Multi-currency, holdings, webhooks, web UI, whole-database encryption.
- A materialized net-worth snapshot table (see D3-3 - deferred, not built here).

---

## Decisions settled (do not reopen)

### D3-1. Liability `current_cents` is normalized to positive-when-owed at ingest; readers stop taking `abs()`.

Before PR1, Plaid balances were stored as-is (`internal/providers/plaid/provider.go:381`), and the analytics readers took `abs()` of a liability balance (`internal/store/networth.go:150-151`, `internal/store/debts.go:101-102`).
That collapses a genuine credit balance (institution owes the user, e.g. an overpaid card) into positive debt - the Phase 2 review's M1.

**Decided (Option A):** liability `current_cents` is normalized **at the ingest boundary** to a single convention - **positive = owed, negative = in credit** - and the reader `abs()` calls are removed.
Assets are unaffected (their sign is already meaningful).
The interim "abs = debt magnitude" behavior documented in `docs/phase2-polish-pr-plan.md` ends when PR1 lands.

> **PR1 verification result.** Plaid's [`/liabilities/get` documentation](https://plaid.com/docs/api/products/liabilities/) defines credit current balance as positive when owed and negative when the lender owes the user, while loan current balance is remaining principal. The published Sandbox response confirms positive current balances for both types: `410` for its credit card and `65262` / `56302.06` for its student loan / mortgage. Plaid's raw convention therefore already matches D3-1 uniformly.

### D3-2. Optional money is nullable, never sentinel-zero.

Before PR2, `optionalMoneyToCents(nil)` returned `(0, nil)` and `canon.Balance`/`canon.Liability` carried bare `int64`, so ingest wrote `0` where Plaid omitted a value - conflating "not reported" with "a reported zero."
The SQL columns are **already nullable** (`balance_snapshots.available_cents`, `balance_snapshots.limit_cents`, `credit_terms.limit_cents` are `INTEGER` without `NOT NULL`), and the debts reader **already** scans them as `sql.NullInt64` (`internal/store/debts.go:111`) - so the read layer is NULL-ready and the write layer is the gap.

**Decided:** optional money becomes nullable end-to-end (`*int64` in `canon`, real SQL `NULL` on write).
PR2 applies this consistently to `available_cents` and `limit_cents` on `balance_snapshots`, plus liability limit, minimum-payment, and last-statement money; a present `0.00` remains a non-nil pointer to zero.
The binding money rule at `moneta-plan.md:268` is amended to add: "optional money is nullable (`*int64` / SQL `NULL`), never sentinel-zero."

### D3-3. Analytics are compute-on-read; no precompute tables in this stack.

At personal-finance scale the Phase 2 reads already compute on read.
Net-worth **history** is also correct and stable compute-on-read: past-day `balance_snapshots` rows are immutable because sync only upserts *today's* date (`UNIQUE (account_id, date)`; the write always uses `p.now()`), so a historical series cannot be retroactively corrupted.

**Decided:** compute-on-read for every Phase 3 view, including `networth --history`.
A materialized daily net-worth snapshot table is the one sanctioned future exception but is **not built here**; revisit only if a measured query is slow or a requirement to freeze net worth against retroactive edits appears.
The `moneta-plan.md:251` header ("Analytics (precomputed at sync ...)") and the `:254` line ("Net worth snapshots are computed at sync time") are amended to say analytics are computed on read except the per-account balance snapshot that sync already writes.

---

## Schema / migration sketch

**What changes:**

| Area | Change | PR |
|---|---|---|
| `internal/canon/types.go` | `Balance.AvailableCents`, `Balance.LimitCents`, and liability limit / minimum-payment / last-statement money → `*int64` | PR2 |
| `internal/providers/plaid/normalize.go` | `optionalMoneyToCents` returns `(*int64, error)` (nil when the Plaid field is nil); liability-balance normalization to positive-when-owed | PR1 (sign), PR2 (nullable) |
| `internal/core/ingest.go` | balance + liability upserts bind `NULL` for nil optional money (`:651-666`, liability terms ~`:700-715`) | PR2 |
| `internal/store/networth.go`, `internal/store/debts.go` | remove the `amount < 0 { amount = -amount }` blocks (`networth.go:150-151`, `debts.go:101-102`); keep the `MinInt64` overflow guards | PR1 |
| `internal/store/migrations/000003_*` | **conditional** data backfill normalizing existing liability `balance_snapshots.current_cents` to the D3-1 convention | PR1 |
| `docs/moneta-plan.md` | amend `:268` (nullable clause) and `:251`/`:254` (compute-on-read wording) | PR1/PR2 |

**What does NOT change:**

- `balance_snapshots.current_cents` stays `INTEGER NOT NULL` (a current balance is always present; only available/limit are optional).
- No DDL for nullability - the optional columns are already nullable; D3-2 is a Go-layer + write-path change only.
- No new tables. No precompute/materialized views. No change to exclusion / period / dedup / poison-skip logic.
- Migration numbering continues from `000002`; PR1's backfill is `000003` (data-only `UPDATE`, reversible `.down.sql` that restores the prior sign iff the up-migration flipped anything).

**Backfill scope (PR1).** Verification confirmed that Plaid already reports positive-when-owed uniformly, so migration `000003` is a **documented no-op** with a preservation test.
Existing negative liability snapshots are genuine credits and are preserved; the migration is *not* a blind sign flip.

---

## PR graph

```text
PR1 (sign convention)  ─┐
PR2 (nullable money)   ─┴─► foundation complete ─┬─► PR3  networth --history
                                                 ├─► PR4  trends: mom
                                                 │      └─► PR5 merchants ─ PR6 utilization* ─ PR7 savings ─ PR8 fixed-variable
                                                 ├─► PR9  cards
                                                 └─► PR10 bare moneta dashboard (composes the above)

* PR6 (utilization trend) additionally depends on PR2 (honest nullable limits).
```

- **PR1 + PR2 are the foundation** and must both merge before any feature PR. I split the maintainer's suggested single "foundation" PR into two so each has an isolated red/green; they may be developed on one branch if preferred, but are specified separately.
- **Feature PRs (PR3-PR10) are independent of each other** except PR6→PR2 and PR10→(all). Ship in any order after the foundation.
- **Each feature PR includes its REST mirror in the same PR** (a `/v1/...` handler in `internal/api/`), matching the Phase 2 pattern where the api package mirrors each read. `networth --history` extends the existing `/v1/networth` with a query param rather than adding a route.
- **Every PR:** write the regression test first, watch it fail, then implement. Stop and report after each. `go build ./... && go vet ./... && go test ./...` must pass.

---

## PR1 - Liability sign convention (foundation, D3-1)

**Anchors:** `internal/providers/plaid/provider.go:381`, `internal/store/networth.go:146-155`, `internal/store/debts.go:96-107`.

### Steps
1. **Verify** Plaid's raw balance sign per liability type (`credit_card`, `loan`) against Plaid `/liabilities/get` docs + Sandbox; record the finding in the PR description. This gates step 4.
2. **Normalize at ingest:** in the Plaid provider, map each liability account's `current` to positive-when-owed / negative-when-in-credit using the verified convention, so `canon.Balance.CurrentCents` for a liability already carries the canonical sign. Assets pass through unchanged.
3. **Remove reader `abs()`:** delete the `if amount < 0 { amount = -amount }` blocks in `networth.go:150-151` and `debts.go:101-102`. Keep the `MinInt64` guards. A negative liability balance now correctly *reduces* `LiabilitiesCents` / shows as a credit.
4. **Backfill migration `000003`:** normalize existing liability `balance_snapshots.current_cents` to the convention. No-op (with preservation test) if step 1 shows uniform positive-when-owed.

### Tests first
```
TestNetworthCreditBalanceCountsAsAsset (store): a credit card with current_cents = -5000 (a $50
  credit) reduces liabilities / raises net worth by $50 — asserts the opposite of the pre-PR1
  abs() behavior. Red before step 3.
TestDebtsCreditBalanceReportedNegative (store): the same card appears with a negative owed balance,
  not +50.00.
TestNetworthLoanStillCountsAsDebt / TestDebtsLoanStillPositive: a normal owed balance is unchanged.
TestMigration000003PreservesGenuineCredits (or IsNoOp): existing negative rows are not blindly
  flipped; asserts the verified-convention transform.
```

### Acceptance
- A liability credit balance raises net worth and shows negative in `debts`; a normal owed balance is unchanged.
- The interim "abs = debt magnitude" note in `docs/phase2-polish-pr-plan.md` is removed / marked resolved.
- `moneta-plan.md:252-253` compute-on-read wording amended (co-located with PR2's `:268` edit if PRs are combined).

### Out of scope
- Nullable optional money (PR2). Multi-currency (`canon.Balance` currency carry-forward stays deferred). Liability-kind enrichment (`moneta-plan.md:190-191`).

---

## PR2 - Nullable optional money (foundation, D3-2)

**Anchors:** `internal/canon/types.go:53-71`, `internal/providers/plaid/normalize.go:300-305`, `internal/core/ingest.go:651-666`, `internal/store/debts.go:111`.

### Steps
1. `canon.Balance.AvailableCents`/`LimitCents` and all optional liability money fields (`LimitCents`, `MinPaymentCents`, `LastStatementCents`) → `*int64`.
2. `optionalMoneyToCents` returns `(*int64, error)` - `nil` when the Plaid field is nil, else a pointer to the cents value.
3. Ingest binds `NULL` (Go `nil`) for those columns instead of `0`; a non-nil pointer to zero binds SQL `0`.
4. Readers: `networth`/`debts` treat a NULL limit/available as absent (the debts reader's `sql.NullInt64` path at `:111` already does this - verify no reader assumes non-null). `cli.Ratio` already returns nil for a non-positive/absent denominator, so utilization stays blank for a missing limit - now for the right reason.
5. Amend `moneta-plan.md:268` with the nullable clause.

### Tests first
```
TestBalanceUpsertWritesNullForMissingLimit (store/core): a batch whose Plaid limit is absent stores
  SQL NULL, not 0 — query `SELECT limit_cents IS NULL`. Red before step 3.
TestUtilizationBlankForNullLimitNotZero: an account with a NULL limit shows no utilization; an
  account with a real 0 limit is distinguishable at the store layer.
```

### Acceptance
- Absent optional money round-trips as NULL, present values unchanged; utilization honest.
- No historical backfill (sentinel-0 is unrecoverable - see R2); go-forward only.

### Out of scope
- The sign convention (PR1), historical sentinel-zero backfill, and any additional liability-kind enrichment.

---

## PR3 - `networth --history`

**Status:** implemented as a compute-on-read daily series with an inclusive local-calendar window and authenticated REST mirror.

**Anchors:** existing as-of query `internal/store/networth.go:87-106` (CTE `ranked_balances` / `ROW_NUMBER() PARTITION BY account_id ... WHERE date <= ?`), `cmd/moneta/networth.go`, `internal/api/handlers.go` (`handleNetworth`).

Extend the existing as-of computation into a **series**: for each day in the requested window, sum each account's latest snapshot on-or-before that day (carry-forward across sync gaps). Compute-on-read (D3-3), no new table.

### Tests first
```
TestNetworthHistoryCarriesForwardAcrossGaps (store): snapshots on day 1 and day 5 only; a 5-day
  history returns a value for every day, days 2–4 equal to day 1.
TestNetworthHistoryRespectsSign (store): depends on PR1 — a credit balance mid-window raises the
  series, not lowers it.
TestNetworthHistoryWindowBounds (cmd): --history 90d inclusive/exclusive ends match documented
  period semantics.
```
### Acceptance
- `moneta networth --history 90d` (TOON + `--json`) and `GET /v1/networth?history=90d` return exactly 90 points ending on today's local date, inclusive; empty-state and `hint:` line present.
### Out of scope
- Any precompute table. Trends (PR4+).

---

## PR4 - `moneta trends --metric mom`, plus PR5-PR8 (one metric per PR)

**PR4 status:** `mom` is implemented with calendar-month comparison, category-ID grouping, absolute-delta ordering, CLI TOON/JSON, and authenticated REST.
PR5 merchants is next; the other metric names remain rejected until their own PRs land.

New `moneta trends` command + `/v1/trends`, following AXI conventions (summary, per-row, truncation, hint, `--json`). Reuses `spend`/`cashflow` exclusion + period helpers. Each `--metric` is its **own PR** on this template:

| PR | `--metric` | Reads / notes | Extra dep |
|---|---|---|---|
| PR4 | `mom` | month-over-month category spend deltas over posted non-excluded outflows | - |
| PR5 | `merchants` | top merchants by spend in a period | - |
| PR6 | `utilization` | credit utilization trend over `balance_snapshots` limit vs balance per day | **PR2** (NULL limit ⇒ excluded from the trend, not treated as 0) |
| PR7 | `savings` | savings rate = net / inflow over a period (reuse `cli.Ratio`) | - |
| PR8 | `fixed-variable` | fixed vs variable expense split (definition to confirm - see R3) | - |

### Tests first (per metric)
```
Store test seeding a known fixture → asserts the exact metric numbers (incl. exclusion of
transfers/excluded rows and, for utilization, correct handling of a NULL limit).
CLI test: TOON shape + --json + empty state + unknown --metric rejected with exit 2.
REST test: /v1/trends?metric=<m> mirrors the CLI numbers.
```
### Acceptance (per PR)
- The metric matches the fixture by hand-computed value; exclusion invariants hold; REST mirrors CLI.
### Out of scope
- Other metrics (their own PRs). Anomaly flags (phase 4).

---

## PR9 - `moneta cards`

Credit-card-focused projection of the `debts` data (utilization / APR / due dates), filtered to `credit_card`. Small; largely a focused view over the PR1/PR2-corrected debts store. Includes `/v1/cards`.

### Tests first
```
TestCardsShowsUtilizationAndDueDates (store/cmd): a seeded card renders utilization from a real
  limit, blank from a NULL limit (PR2), correct APR basis points, due day.
```
### Acceptance
- `moneta cards` + `/v1/cards`; utilization honest (depends on PR1 + PR2).
### Out of scope
- Loans (stay in `debts`). Payment scheduling.

---

## PR10 - Bare `moneta` dashboard (last)

Composes the above into the content-first dashboard (`moneta-plan.md:214`): net worth, cash, utilization, sync health.
"Upcoming bills" and "anomaly count" are **Phase 4** inputs - render them as explicit empty placeholders with a one-line "available in a later phase" note, not fabricated values.
Root REST route (`/v1/` dashboard payload) mirrors it.

### Tests first
```
TestDashboardComposesSections: asserts each section reads from its underlying store fn; bills/anomaly
  slots render as documented empty placeholders, never fabricated.
```
### Acceptance
- `moneta` (no args currently prints usage/exit 2 - decide: dashboard becomes the no-arg default, or `moneta dashboard`; see R3). Sync-health reflects `provider_items.status` incl. `login_required` (exit-3 semantics from Phase 2 PR7).
### Out of scope
- Recurring/anomaly computation (phase 4). Any write.

---

## Copy-paste agent prompt (PR1 only)

> Read `AGENTS.md` and the binding rules in `docs/moneta-plan.md` first (int64 cents, analytics exclusion, TOON output).
> Execute **PR1 only** from `docs/phase3-analytics-plan.md` - the liability sign convention (D3-1). Do not start PR2 or any feature PR.
> **First, verify** Plaid's raw balance sign for each liability type (`credit_card`, `loan`) against the Plaid `/liabilities/get` docs and Sandbox, and write the finding into your report - the backfill migration depends on it. If the raw convention is not already positive-when-owed, stop and report before writing the backfill.
> Then: normalize liability `current_cents` to positive-when-owed at the Plaid ingest boundary; remove the reader `abs()` blocks in `internal/store/networth.go:150-151` and `internal/store/debts.go:101-102` (keep the `MinInt64` guards); add migration `000003` (a documented no-op with a preservation test if the raw convention is already positive-when-owed, otherwise a scoped sign backfill for liability `balance_snapshots` rows).
> Write each regression test first and watch it fail on current code before implementing.
> Do not touch nullable-money work (that is PR2). Do not change exclusion, period, dedup, or poison-skip logic.
> No new dependencies. Keep CGO-free. Fake data only in tests.
> Do not commit, push, or open a PR unless I explicitly ask. Stop and report when PR1 is complete with `go test ./...` output.

---

## Residual risks / open questions

- **R1 (resolved by PR1).** Plaid documentation and its published Sandbox `/liabilities/get` response confirm positive-when-owed current balances for credit-card and loan accounts. Migration `000003` is therefore a documented no-op that preserves existing negative credits.
- **R2 (accepted limitation).** D3-2 cannot reclassify historical sentinel-0 optional-money rows to NULL (a stored `0` is ambiguous between "real zero" and "was missing"). Utilization for accounts whose limit predates PR2 stays sentinel-0 until a fresh sync overwrites that account's *current-day* row with NULL; historical days remain sentinel-0. Go-forward only, by design.
- **R3 (maintainer input welcome, not blocking).** Two definitional choices: (a) the "fixed vs variable" split (PR8) needs a concrete rule - propose category-kind-based or recurring-linked (recurring is phase 4, so category-kind for now); (b) whether the dashboard is the no-arg `moneta` default or an explicit `moneta dashboard` subcommand. Defaults proposed in-line; confirm at PR4/PR10 time.
- **R4 (scope boundary).** Without `moneta tag` / D2 (out of scope), all trends reflect provider categorization; a user cannot yet re-bucket a miscategorized merchant, so category trends inherit Plaid's category quality.
- **R5 (dashboard/phase-4 coupling).** PR10's dashboard has two Phase-4-shaped holes (upcoming bills, anomaly count). Decide whether to ship the dashboard in Phase 3 with documented placeholders or defer it to Phase 4 when those inputs exist. Recommendation: ship with placeholders so the surface is stable, fill in phase 4.
