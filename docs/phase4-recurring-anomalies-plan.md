# Phase 4 Recurring + Anomalies PR Plan

Executable hand-off for Phase 4 (build phase 4, "Recurring detection + anomaly detection", per `docs/moneta-plan.md` build phase 4).
**Status:** complete through PR9; the full graph below is implemented and both dashboard slots have definitive semantics.
Phase 3 is frozen; do not modify Phase 3 analytics behavior except the dashboard slots this phase fills, the merchant/card-date schema and Plaid boundary fixes, and the detector schema below.
No new dependencies.
Keep CGO-free.
Fake data only in tests and fixtures (see Privacy).
Do **not** commit or push unless the maintainer explicitly asks.

**Source:** grounded against `main` at `796effc` (Phase 3 complete + post-review PR #24 merged).
**Revision:** sixth polish (2026-07-31) after Codex/Opus review of the fifth polish; partial runs accept positive evidence but never absence-based destructive transitions, completeness uses current attempt outcomes, reset workflow and re-key scope are exact, and drift output is canonical TOON.
**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md` (binding: int64 cents, analytics exclusion via `excluded`, TOON/AXI output conventions, REST mirrors reads), `docs/phase3-analytics-plan.md` (structure model, D3-3 compute-on-read precedent).

> **Read this first.**
> Phase 4 filled the two dashboard placeholders (`upcoming_bills`, `anomalies`) that Phase 3 deliberately left `null` (R5/C1 in `AGENTS.md`).
> `upcoming_bills` now follows the detector gate, `anomalies` is always a real object, and `phase4_note` has been removed.
> This stack is **not** zero-DDL: migrations cover detector identity, lifecycle state, detector run status, optional enriched merchant storage, and full card next-payment dates.
> Detection quality on real Plaid data requires both a cleaner merchant field **and** a one-time history re-pull path; PR1 alone on an existing 14-month DB is not sufficient.
> Detection runs **once per `moneta sync`**, after all provider items finish, not inside each item sync.

## Goals

1. Detect recurring subscriptions, bills, and income streams from transaction history and persist them in `recurring_items` with stable identity and durable lifecycle state.
2. Ship the three Phase 4 reads from `moneta-plan.md`: `moneta recurring`, `moneta bills`, `moneta anomalies`, each with an authenticated REST mirror.
3. Fill the dashboard `upcoming_bills` and `anomalies` slots via a deliberate shape transition (null placeholders become real payloads; `phase4_note` narrows then disappears only when both slots can be honest).
4. Keep the agent-first invariants: compact TOON, grep-friendly fixed row fields, definitive empty states, exit codes 0/1/2 for new reads (exit 3 remains status/dashboard sync-health only).

## Non-goals

- A2 `expense_class` taxonomy column - separate stack; `fixed-variable` keeps its name-only heuristic.
- `moneta tag` and full D2 provider-vs-user field ownership - detection reads provider categorization as-is.
- Manual recurring management CLI (`moneta add recurring`, edit, deactivate) - `source='manual'` rows are honored if present, but no CLI writes them in this stack.
- Web UI, multi-currency, budgets, Plaid webhooks, investment holdings.
- ML / statistical anomaly models - v1 is a deterministic threshold heuristic.
- `--entity` flags - the shipped CLI is single-entity; Phase 4 commands follow existing flag conventions and do not add entity plumbing.
- Reopening Phase 3 decisions (D3-1 liability sign, D3-2 nullable money, D3-3 compute-on-read for analytics, B1 dashboard subcommand, name-only fixed heuristic).
- Yearly cadence detection in v1 (D4-8).
- Loan-payment schedules from transfer-excluded transactions in v1 (D4-9); card due dates still come from credit terms.
- Turning `moneta sync` into TOON/JSON or adding a REST sync endpoint - sync stays prose; only new prose summary lines are added.

---

## Starting state anchors (historical)

These anchors describe the pre-Phase-4 baseline used to design the now-complete stack.

- `recurring_items` exists (`internal/store/migrations/000001_initial_schema.up.sql`): `entity_id`, `name`, `kind` (`subscription|bill|income`), `cadence` (free-form TEXT), `expected_cents` (INTEGER NOT NULL), `next_expected_date` (nullable YYYY-MM-DD), `drift_pct` (REAL, default 0), `source` (`detected|manual`), `is_active`.
- Index today: `recurring_items(entity_id, kind, is_active)` only.
- `transactions.recurring_id` exists (`ON DELETE SET NULL`); no link provenance.
- Plaid merchant path today: `sdk_gateway.go` requests original description; `rawTransaction` has `Name` + `OriginalDescription` only; `normalize.go` prefers `OriginalDescription` then `Name` into `MerchantRaw`; `core.NormalizeMerchant` lowercases and collapses whitespace; `DedupHash` hashes that norm; Plaid primary match is `txn_provider_refs` (provider txn id), with `dedup_hash` as fallback when `ProviderTxnID == ""` (manual path).
- Card dues today: Plaid `NextPaymentDueDate` is reduced to `due_day` only (`normalize.go` `dateDay`); year/month discarded.
- Dashboard placeholders: `internal/report/dashboard.go` (`upcoming_bills`/`anomalies` null + `phase4_note`).
- Sync: `core.SyncProviderItem` -> `ApplySync` (one write txn, commit); CLI `runSync` / `syncItems` in `cmd/moneta/main.go` loops items and calls `SyncProviderItem` per item; prose only; exit 0/1/2; no `/v1/sync`.
- SQLite single connection: `store.Open` `SetMaxOpenConns(1)` - never start a second DB op while a write txn holds the only connection.
- Seed: `Loan Payments` is `kind='transfer'`; ingest sets `is_transfer` and `excluded` together for transfer kinds.
- `accounts.is_active` exists; `ReadCards` / `readLiabilities` currently do **not** filter on it (type only).
- Migrations are up/down pairs (`000001`..`000003`); next migration is `000004_*`.
- Phase 3 PR numbers in this doc are always written as **Phase 3 PRN** to avoid colliding with this plan's PR numbers.

---

## Decisions (settled defaults; confirm at PR1 kickoff, then freeze)

### D4-1. Recurring detection persists into `recurring_items`; anomalies stay compute-on-read.

`recurring_items` is a Phase 1 domain entity with lifecycle and an FK from transactions.
Anomaly detection is a pure function of history (D4-2) with no table.

### D4-2. Anomaly detection is compute-on-read, no anomaly table.

### D4-3. Minimal DDL (not zero DDL) — two migrations

Do not implement a single combined `000004` that mixes merchant/card columns with detector tables.
PR steps own the split below; D4-3 is only the inventory.

#### Migration `000004` (PR1) — merchant display + card due date

| Object | Change |
|---|---|
| `transactions.merchant_display` | `TEXT NOT NULL DEFAULT ''` — enriched display/group input |
| `credit_terms.next_payment_due_date` | `TEXT NULL` (YYYY-MM-DD) — full Plaid next-payment date |

`merchant_raw` stays institution original for dedup (D4-7).
`merchant_norm` stays `NormalizeMerchant(merchant_raw)`.
Keep `due_day` for `moneta cards`; bills prefer full date when present (D4-12).
Up + down pair with migration tests.

#### Migration `000005` (PR3) — detector identity, lifecycle, run state

| Object | Change |
|---|---|
| `recurring_items.detect_key` | `TEXT NOT NULL DEFAULT ''` |
| `recurring_items.amount_sign` | `INTEGER NOT NULL DEFAULT 0` CHECK IN (-1,0,1) |
| `recurring_items.miss_count` | `INTEGER NOT NULL DEFAULT 0` CHECK >= 0 |
| `recurring_items.last_matched_date` | `TEXT NULL` (YYYY-MM-DD) |
| `recurring_items.last_matched_cents` | `INTEGER NULL` — signed cents of last matched occurrence (drift input at read) |
| `recurring_items.schedule_anchor_day` | `INTEGER NULL` CHECK 1..31 — intended series day (D4-13) |
| partial UNIQUE | `(entity_id, detect_key, amount_sign)` WHERE `source='detected' AND detect_key <> '' AND amount_sign IN (-1,1)` |
| `detector_state` | singleton table, **STRICT**, seeded |

```sql
CREATE UNIQUE INDEX recurring_items_detected_identity_uidx
  ON recurring_items (entity_id, detect_key, amount_sign)
  WHERE source = 'detected' AND detect_key <> '' AND amount_sign IN (-1, 1);

CREATE TABLE detector_state (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  status TEXT NOT NULL DEFAULT 'never_run'
    CHECK (status IN ('never_run', 'ok', 'error', 'partial')),
  last_run_at TEXT,
  last_success_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  last_series_count INTEGER NOT NULL DEFAULT 0,
  last_skipped_overflow INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO detector_state (id, status) VALUES (1, 'never_run');
```

**Seed is required:** migration inserts `id=1` so readers never see `sql.ErrNoRows` before the first detect write.
Writers still upsert on `id=1`.
`ReadDetectorState` may also synthesize `never_run` if the row is missing (defense in depth), but the seeded row is the primary contract.
All new tables use `STRICT` (matches `000001` convention).

**Do not** store sync item ok/fail counts on `detector_state`.
Item health already lives in `provider_items.status` and is surfaced by `moneta status` / dashboard `sync` (`ListProviderItemStatuses`).
Those fields can disagree with a scoped `--item` run if snapshotted; derive item health at read time from `provider_items`, not from detector state.
Successful `ApplySync` writes `provider_items.status = 'ok'`; a reauth failure is set to `login_required` by the CLI.
A transient pull failure may leave the prior durable status unchanged, so durable status alone is **not** proof that the current sync attempt succeeded.

Down for `000005`: drop the partial unique index **before** dropping new columns/table.
Up + down tests on both migrations.

### D4-4. Detection never wedges sync; complete and partial persistence modes.

**Settled principle:** complete input permits positive and negative lifecycle evidence.
Partial input permits positive evidence, but absence is not evidence that a series ended.
A broken institution (`login_required` / transient sync error) must not blank recurring features or freeze their schedule projections.

**Exact CLI / library sequence for `moneta sync`:**

1. Determine the **full linked set** `L` at command start.
   The pull scope `P` is either `L` or the single `--item` selection.
2. For each item in `P`, run existing item sync only and retain the success/failure outcome for that exact attempt.
   Detection is **not** inside `SyncProviderItem`.
3. Define current-run completeness exactly:

   ```text
   complete_run = len(L) > 0 AND same_item_set(P, L) AND every attempt in P succeeded
   ```

   Durable `provider_items.status` is still read for health reporting and may be asserted after success, but it cannot replace current attempt outcomes because transient failures do not always change stored status.
   A scoped `--item` run is incomplete when more than one item is linked, even if every stored item status currently says `ok`.
4. If **zero** items in `P` succeeded:
   - Do not run detection or mutate series/back-links.
   - Set detector status `partial`, set `last_run_at`, clear `last_error`, and preserve `last_success_at`, series rows, `last_series_count`, and `last_skipped_overflow`.
   - Prose: `recurring: skipped (no item synced; see moneta status)`.
5. If **at least one** item succeeded, run detection once across the local snapshot.
   Candidate rows carry their owning `provider_item_id`; the persistence layer also receives the set of item IDs that succeeded in this run.
   Entity set = union of lookback candidate entities and entities with active detected rows.
6. Persist the detect result in one of two modes:
   - **Complete mode** (`complete_run = true`): full upsert, miss lifecycle, unseen-key deactivation, re-key, and back-link policy.
   - **Partial positive-evidence mode** (`complete_run = false`):
     - An emitted series is eligible for insert/refresh only when its subsequence contains at least one transaction owned by an item that succeeded in this run.
     - Insert newly emitted active series and refresh positive fields on eligible existing series: name, kind, cadence, expected cents, anchor, next-date forecast, and newer matched transaction evidence.
       Advance `last_matched_date` / `last_matched_cents` only from a member owned by an item that succeeded in this run.
     - A partial result may reactivate an inactive series only through the normal emitted subsequence requirement (`length >= 3`) with successful-item evidence.
     - Never change `is_active` from 1 to 0, never increase `miss_count` from unmatched steps, and never deactivate an unseen identity.
     - Back-links are additive only: link eligible emitted members when NULL or already this series; do not unlink, steal, or perform re-key cleanup.
     - Rows not eligible for positive refresh remain unchanged.
7. On detector transaction success:
   - Complete mode: status `ok`; set `last_success_at`, `last_run_at`, `last_series_count`, and `last_skipped_overflow`; clear `last_error`.
   - Partial mode: status `partial`; set `last_run_at`, `last_series_count`, and `last_skipped_overflow`; leave `last_success_at` unchanged; clear `last_error`.
8. On detector failure in either mode: roll back all series/back-link writes; in a short transaction set status `error`, `last_run_at`, and redacted `last_error`; leave `last_success_at` and prior series unchanged.
9. Overflow-skipped identities are never deactivated.
   Report the skip count on the successful detector state for either mode.

Never call detection while any ingest transaction is open because the pool has one connection.
Test injection seam: replaceable detect function on the post-loop path.

Sync CLI stays prose.
Print `recurring: N series`, `recurring: partial (positive evidence only)`, `recurring: skipped (no item synced)`, or `recurring: detection failed`, plus a non-zero overflow count.
No sync `--json`, no TOON sync document, no `/v1/sync`, no exit 3 for detection failure.
An empty-batch successful pull still counts as a successful current attempt.

### D4-5. Manual rows and back-links (including re-key).

- Detector match/update/deactivate only `source='detected'`.
- **Never** clear or overwrite `recurring_id` when the current target is a **manual** series.
- **Never** steal a link from another **emitted active** detected series in the same pass.
- **Complete-mode re-key / self-heal:** when old detect-key series `A` is deactivated and new series `B` is emitted after display-key migration, reassign only `B`'s emitted lookback member IDs that currently point to `A` or another inactive detected series.
  Do **not** blanket-unlink unrelated historical members of a genuinely ended series.
- Unlink of non-members for an emitted series stays lookback-scoped:
  `WHERE recurring_id = <this id> AND date >= lookback_start AND date <= lookback_end AND id NOT IN (member set)`.
- Pre-lookback historical links are always left intact.
- **Partial mode:** additive links only for emitted member transactions owned by items that succeeded in the current run; current link must be NULL or already this series.
  No unlink or re-key reassignment is allowed.

### D4-6. Detector freshness is mandatory; `partial` is not a kill-switch.

`detector_state.status` is the **latest** detector coordination status: `never_run` | `ok` | `partial` | `error`.

**Meanings:**

| status | Meaning |
|---|---|
| `never_run` | No successful lifecycle detect yet |
| `ok` | Last mutating detect finished after complete all-linked sync success |
| `partial` | Last sync was incomplete; rows are a conservative snapshot with last-complete data plus positive-evidence refreshes only |
| `error` | Last mutating detect **failed**; series rows may be last good, but detect output is untrustworthy |

**Read gate (settled — Opus kill-switch fix):**

| status | Detected rows on `moneta recurring` / `moneta bills` | dashboard `upcoming_bills` | Card dues |
|---|---|---|---|
| `never_run` | omit detected | `null` | included |
| `error` | omit detected | `null` | included |
| `partial` | **include** conservative detected snapshot | **include** (same projection as CLI) | included |
| `ok` | include detected | include | included |

Item outages already surface via `moneta status` exit 3 and dashboard `sync`; blanking recurring adds no signal.
Always put detector status in summaries so agents see `partial` while still getting conservative bills.

**Partial schedule projection at read time:** for every active detected row shown under `partial`, derive an effective next date without mutating storage.
Starting from stored `next_expected_date`, cadence, and `schedule_anchor_day`, advance calendar steps until the earliest `S` satisfying `asOf <= S + grace`.
Use that effective date for `moneta recurring`, bills filtering, `due_status`, and dashboard projection.
Do not change stored miss count, active state, or matched evidence from this read-time projection.
If no detected rows exist yet, an empty partial result means "none known from incomplete input," not a definitive complete-data empty; the detector status carries that distinction.

**Unified detector object** (same four fields everywhere — CLI bills, recurring, status, dashboard):

```text
detector: {
  status,              // never_run | ok | partial | error
  last_run_at,         // null if never
  last_success_at,     // last complete-mode ok; null if never
  last_skipped_overflow
}
```

`moneta status` / `GET /v1/status` also include redacted `last_error` when status is `error`.
Item health remains the existing provider-item list on status/dashboard sync — **not** duplicated into this object.

**Mandatory surfaces in PR5:**

| Surface | Requirement |
|---|---|
| `moneta status` | detector object + last_error when error |
| `GET /v1/status` | same |
| Dashboard `recurring_detect` | detector object (four fields) |

PR5 updates `AGENTS.md` + README for the new `recurring_detect` key (review checks prose; Go tests assert the report document, not AGENTS.md text).

`phase4_note` narrows then is removed in PR9.
After PR9: agents use `recurring_detect` + bills/anomaly slots.

| status | dashboard `upcoming_bills` |
|---|---|
| `never_run` / `error` | `null` |
| `partial` / `ok` + nothing due | empty table + `count: 0` |
| `partial` / `ok` + rows | payload |

### D4-7. Merchant identity: preserve raw, add display, re-pull history.

**Problem:** exact grouping on OriginalDescription fragments production series; incremental sync never rewrites old rows.

**Settled design:**

1. **`merchant_raw` stays provenance for the institution original** when Plaid sends `original_description`; if original is empty, fall back to `name` (same as today for that branch). Prefer **not** stuffing enriched `merchant_name` into `merchant_raw`.
2. **New `merchant_display`** (nullable-as-empty string): set at ingest from preference `merchant_name` > `name` > `original_description` (first non-empty).
3. **`merchant_norm`** remains `NormalizeMerchant(merchant_raw)` so existing dedup hashing and spend merchant grouping keep their input definition for the raw field. Document explicitly: changing display does not change Plaid primary matching (`txn_provider_refs`); `dedup_hash` is only the fallback when `ProviderTxnID == ""`.
4. **Detection grouping input** uses `merchant_display` when non-empty, else `merchant_norm` / raw norm, then applies the **frozen** `detect_key` algorithm (D4-7a).
5. **History transition (required, not optional):** PR2 ships safe full history replay via `moneta sync --reset-cursor` (D4-14).
   Link tokens already request 730 days of history (`sdk_gateway.go` `SetDaysRequested(730)`), so a re-pull covers the 14-month lookback with margin.
   README preferred upgrade path: run **one unscoped** `moneta sync --reset-cursor` so every linked item is attempted in the same complete run.
   If recovery requires scoped `--item` reset runs, finish with a plain unscoped `moneta sync`; scoped runs remain `partial` when multiple items are linked.
   Without re-pull, pre-PR1 rows keep empty `merchant_display` and fall back to descriptor `merchant_norm`, which can temporarily create **duplicate detect_keys** for the same merchant until re-pull completes (residual risk).

### D4-7a. Frozen `detect_key` algorithm (not an open knob).

Algorithm id **v1**. Changing it later is a rekey migration, not threshold tuning.

```
input = merchant_display if merchant_display != "" else merchant_norm
s = strings.ToLower(strings.Join(strings.Fields(input), " "))
// Strip trailing tokens until neither pattern matches; re-Join Fields after each strip.
// Go regex (compile once) — frozen v1:
//   tailHashDigits = regexp.MustCompile(`#\d+$`)
//   tailLongDigits = regexp.MustCompile(`(?:^|\s)\d{4,}$`)
// For tailHashDigits: remove the match (and any immediately preceding space left behind), then Fields-join.
// For tailLongDigits: remove the match, then Fields-join.
// Both "merchant #123" and "merchant#123" strip to "merchant".
detect_key = final s
empty => not a candidate
```

Golden tests (required in PR4) — must match the regexes above:

| input | detect_key |
|---|---|
| `Netflix` | `netflix` |
| `netflix #123` | `netflix` |
| `netflix#123` | `netflix` |
| `store 1234` | `store` |
| `store#99` | `store` |
| `acme 12` | `acme 12` (digit run length < 4 kept) |

**Display `name` on recurring rows:** latest non-empty `merchant_display` if set, else `merchant_raw`, as provided (no title-case).

### D4-8. Yearly cadence is out of v1 detection.

Canonical detected cadences: `weekly|biweekly|monthly|quarterly` only.

**Lookback (exact):** inclusive date range:

- `end = asOf` (YYYY-MM-DD)
- `start = first day of the calendar month of (asOf minus 14 months)`

Example: `asOf = 2026-07-30` → `start = 2025-05-01`, `end = 2026-07-30`.

### D4-9. Detection input matches spend exclusion; loans are an accepted gap.

Candidates: `status='posted'`, `excluded=0`, `is_transfer=0`, non-empty `detect_key`.
`Loan Payments` never enters; kind rule must not mention it.
`moneta bills` merges credit-card dues only (plus detected subscription/bill rows).
Loans stay in `moneta debts` as balances.

### D4-10. Signed money on recurring series; bills column uses obligation magnitude.

- Stored `expected_cents` is signed like transactions: outflows negative (four -$15.00 → `-1500`), inflows positive.
- `amount_sign` is `-1` or `+1`.
- Persist **`last_matched_cents`** (nullable int64) = signed amount of the last matched subsequence member on each successful emit/upsert.
  Authoritative drift input at read time; do not join `transactions` for drift.

**Frozen drift formula (percent of magnitude, not fraction; works for negative outflows):**

```
if last_matched_cents is NULL or expected_cents == 0: drift_pct = null, drift = false
latest_mag   = abs(big.Int(last_matched_cents))
expected_mag = abs(big.Int(expected_cents))
delta_mag    = latest_mag - expected_mag
// Hundredths of one percent, truncate toward zero. big.Int prevents delta*10000 overflow.
signed_delta_pct_x100 = (delta_mag * 10000) / expected_mag
// Render signed_delta_pct_x100 / 100 as a canonical toon.Number.
// Precision is 2 decimal places, but canonical TOON removes trailing fractional zeros.
// Example: expected -10000, last -12000 -> computed +20.00 -> emitted 20
// Example: expected -10000, last -8000  -> computed -20.00 -> emitted -20
drift = true iff abs(signed_delta_pct_x100) > 1000   // abs percent > 10.00
```

Never use `cli.Ratio` with a negative denominator for this field.
Use `math/big` or an equivalent checked helper for the frozen multiply/divide path; do not perform `delta * 10000` in int64.
Never emit float64.
Schema column `drift_pct REAL` remains for Phase 1 compatibility; **writers leave it at 0**; not compared in idempotency tests.

- **`moneta bills` amounts** are non-negative obligation magnitudes (checked_abs for recurring; card min payments positive).
- **`moneta recurring`** keeps signed `cli.Money(expected_cents)`.

### D4-11. Cadence is not part of upsert identity.

Identity: `(entity_id, detect_key, amount_sign)` via partial unique index.
Cadence changes update the same row.

### D4-12. Card due dates prefer full `next_payment_due_date`; provenance is honest.

Plaid already supplies a full next-payment date; v1 only stored the day.

- Canon: `Liability.NextPaymentDueDate canon.Date` (empty when unknown), same ISO type as `Transaction.Date`.
- Ingest writes full ISO date into `credit_terms.next_payment_due_date` when parseable, and sets `due_day` from that date when present.
- **Liability merge (single pair rule):** inject a normalization calendar date `asOf` (provider `now` date, same clock as other normalize paths).
  Among non-empty full dates for one account, pick **one** winner:
  1. Prefer the **earliest date `>= asOf`** (future-or-equal).
  2. If none are future-or-equal, prefer the **latest date `< asOf`** (most recent past).
  3. Set `DueDay` from that winning date's day-of-month.
  4. Only when **no** full date exists on any record: keep the existing smallest nonzero `DueDay` rule and leave full date empty.
  Never pick due_day from record A and full date from record B.
  Example: asOf `2026-07-30`, candidates `2025-01-15` and `2026-08-20` → win `2026-08-20`.

- **Card bills date selection** (asOf known at read; **card_grace_days = 3**, same as monthly recurring):
  1. If full date `D` is present and `asOf <= D + card_grace_days`: show date `D`, `date_source = provider_reported`.
     - `due_status = upcoming` if `D > asOf`
     - `due_status = due` if `D == asOf`
     - `due_status = in_grace` if `D < asOf` (still within grace — **do not project away** a just-past card due)
  2. If full date `D` is present and `asOf > D + card_grace_days`: project forward by calendar months (anchor day from `D`, EOM-safe) to the next open step `S` with `asOf <= S + card_grace_days`; `date_source = projected_from_past_provider_date`; assign upcoming/due/in_grace relative to `S`.
  3. Else if only `due_day`: project from asOf with the same grace rules; `date_source = day_of_month_estimate`.

- Recurring detector rows: `date_source = detected_schedule`.
- Unified `date_source` ∈ `provider_reported` | `projected_from_past_provider_date` | `day_of_month_estimate` | `detected_schedule`.
- Unified `due_status` ∈ `upcoming` | `due` | `in_grace` (same three values for cards and recurring).
- Card rows: `kind = bill`, `source = card_due`.
- Include only `accounts.type = 'credit_card' AND accounts.is_active = 1`.
  Do **not** claim parity with `ReadCards`.

### D4-13. Schedule grace, due-today, durable misses, stable anchor.

Constants (v1 freeze):

- `miss_grace_days = 3` for monthly/quarterly; `1` for weekly/biweekly.
- Scheduled date `S` becomes a **miss** only when `asOf > S + grace` and no matching payment for that step.
- **`next_expected_date` stays on the open step through grace:** the earliest scheduled `S` such that `asOf <= S + grace` (not yet fully missed).
  So if `S = 2026-07-01`, grace 3, and `asOf = 2026-07-02`, `next_expected_date` remains `2026-07-01` (in grace), not August.
  Only after `asOf > S + grace` without a match does the walk advance and count a miss.
- Matching a step: posted txn, same detect_key + amount within tolerance + same sign, date in `[S - grace, S + grace]`, not already consumed. Prefer subsequence members during full recompute.

**Bills visibility for in-grace past dates:**

- Include active recurring outflows when `next_expected_date <= end` **and** `asOf <= next_expected_date + grace` (obligation still open).
- Row field `due_status`:
  - `upcoming` if `next_expected_date > asOf`
  - `due` if `next_expected_date == asOf`
  - `in_grace` if `next_expected_date < asOf` and `asOf <= next_expected_date + grace`

**Month/quarter step arithmetic (EOM-safe, non-jittering anchor):**

- Derive `schedule_anchor_day` from the **whole subsequence**, not from the last clamped post alone:
  1. Collect day-of-month of each subsequence member.
  2. `mode_day` = modal day; ties → larger day.
  3. If `max(days) >= 28` and at least one member falls on the last calendar day of its month, set anchor to `max(days)` (preserves Jan 31 through Feb 28 clamp).
  4. Else set anchor to `mode_day`.
- Each step: target year-month = add 1 or 3 months to the cursor's year-month; day = `min(schedule_anchor_day, days_in_target_month)`.
- Always re-apply anchor day; never chain from a previously clamped day as the new anchor.

**Full recompute path (emitted series):** rebuild from lookback subsequence; recompute miss_count, last_matched_date, last_matched_cents, next_expected_date, is_active, schedule_anchor_day from asOf + grace rules.

**Partial persistence override (D4-4):** the pure detector still returns the full recompute result, but persistence treats missing transactions as unknown.
For an eligible emitted identity, it may refresh positive fields and advance the forecast, but it must preserve an existing active row as active and must not increase durable `miss_count` from unmatched steps.
If the emitted result would be inactive, do not insert an inactive new row and do not deactivate an existing row.
Rows not emitted or not backed by a successful item remain unchanged; partial read-time projection in D4-6 keeps their stored schedule useful.

**Unseen path, complete mode only:** on successful complete-mode detect, any previously active detected row for an entity in the entity set (D4-4) whose identity was **not emitted and not overflow-skipped** is set `is_active = 0` in one shot.
Overflow-skipped identities are recorded in the detect pass and **must not** be deactivated (a pathological amount must not erase last-good series).

**Reactivation:** only via a newly emitted subsequence of length >= 3.
Partial mode additionally requires successful-item evidence per D4-4.
No single-orphan reactivation.

### D4-14. Safe full history replay (`--reset-cursor`).

**Do not** clear `provider_items.sync_cursor` in the DB before the provider pull.
That discards the last known-good checkpoint if the pull fails, pagination aborts, or another process races.

**Settled orchestration** (requires separating pull cursor from ingest CAS expectation):

1. Load provider items (in-memory `item.SyncCursor` = current DB cursor).
2. For each selected item with `--reset-cursor`:
   - `pullCursor = ""` (empty string → Plaid returns full update history per Transactions API).
   - `expectedStoredCursor = item.SyncCursor` (still the DB value; **not** cleared first).
3. Call `provider.Sync(ctx, pullCursor)` with the empty pull cursor.
4. Call `ApplySync` with `ExpectedCursor = expectedStoredCursor` and the replay batch.
   Existing CAS (`ingest.go` re-read + `UPDATE ... WHERE sync_cursor = ?`) advances the DB cursor to `batch.NextCursor` **only on successful commit**.
5. If pull or ingest fails, DB cursor is unchanged; retry is safe.

Implement by extending the sync path so pull cursor and expected stored cursor are distinct parameters (today they are conflated as one `item.SyncCursor` fed to both sides).
No separate "clear cursor" store API is required for the happy path.
Add tests: failed pull leaves cursor; successful replay advances; concurrent CAS still returns `ErrCursorChanged`; after successful reset sync, previously ingested rows gain non-empty `merchant_display` when Plaid returns enriched names; **multi-page** replay accumulates into one ApplySync; provider **restart/error loop** retries from the original stored cursor without losing the checkpoint (full 730-day replay may re-pull entirely on mutation error — document cost).
For the upgrade workflow, prefer an unscoped reset covering all linked items.
Scoped resets are safe but partial when multiple items are linked and must be followed by one unscoped sync.

---

## Detection approach (recurring)

Heuristic v1, deterministic, no ML.
All amounts signed int64 cents.
Overflow: every `abs`, tolerance product, amount distance, category abs sum, and monthly-equivalent multiply must use checked int64 helpers (same spirit as `addTrendCents` / MinInt64 guards).
On overflow, **skip** that series (or anomaly category) and continue (never panic); increment a visible `skipped_overflow` counter so empty + skipped is distinguishable from genuine empty.
Tests cover MinInt64 and non-zero skipped_overflow.

### Inputs

- Posted, `excluded = 0`, `is_transfer = 0`.
- Lookback `[start, end]` per D4-8 example.
- Per `entity_id` isolation.
- Grouping field: `merchant_display` if non-empty else `merchant_norm`.

### Algorithm (normative)

1. Load candidates (date, amount_cents, merchant fields, category, id).
2. Compute `detect_key` via D4-7a; drop empty.
3. Partition by `(detect_key, amount_sign)`; drop zero amounts.
4. **Amount cluster (one-shot median):**
   - Sort by amount asc, date asc, id asc.
   - Lower-middle median seed (even n: index `n/2 - 1`).
   - Tolerance = `max(checked_abs(expected) * 5 / 100, 100)` cents.
   - Keep within tolerance; recompute median; if count < 3 or `checked_abs(expected) < 500`, no series.
5. **Cadence best-fit subsequence (coverage + recency):**
   - Sort kept by date asc, id asc.
   - Windows (days): weekly 6-8, biweekly 13-15, monthly 27-33, quarterly 85-95.
   - A candidate is any subsequence length >= 3 with every consecutive gap in the same window.
   - **Qualify** a candidate if either:
     - it includes the latest kept occurrence, or
     - `len(subsequence) * 100 / len(kept) >= 50` (integer math).
     (Without the "includes latest" escape, three current monthlies lose to eight old weeklies on coverage alone.)
   - **Rank** qualified candidates: (1) includes latest kept occurrence (true first), (2) latest last date, (3) longer length, (4) latest first date, (5) cadence name order `weekly < biweekly < monthly < quarterly`.
   - Required test: eight old weeklies + three current monthlies ending on the latest charge → monthly wins.
6. **Kind:** positive → income; negative + dominant category exact `Rent and Utilities` → bill; else subscription.
   Dominant: highest count among subsequence; ties by higher checked abs amount sum, then name asc; all NULL → subscription for outflows.
7. **Schedule from subsequence + grace (D4-13):** compute `last_matched_date`, `last_matched_cents`, `miss_count`, `next_expected_date`, `is_active`, `schedule_anchor_day`.
8. **Drift flag** from `last_matched_cents` vs expected (D4-10); do not require writing REAL `drift_pct`.
9. **Emit** series including ordered **member transaction ids** (subsequence only). Overflow on a partition: skip emit for that identity, count `skipped_overflow`, keep identity off the "unseen" deactivation list.

### Persistence

- Upsert detected identity; refresh name, kind, cadence, expected_cents, next_expected_date, is_active, miss_count, last_matched_date, **last_matched_cents**, schedule_anchor_day, detect_key, amount_sign, updated_at.
- Leave column `drift_pct` at **0** always (not compared in idempotency tests; read path uses last_matched_cents).
- After upserts on a successful **complete-mode** run: deactivate active detected rows for entities in the entity set whose identity was **not emitted and not overflow-skipped**.
- Partial-mode persistence follows D4-4/D4-13 and never performs unseen or miss-based deactivation.
- Never touch manual rows.

### Back-linking

Order in a **complete-mode** detect transaction after series upserts/deactivations are computed:

1. For each emitted series, reassign only its emitted lookback member IDs that currently point at a detected series deactivated in this pass or already inactive.
2. Link remaining emitted members when `recurring_id` is NULL or already this series.
3. Unlink lookback non-members still pointing at this emitted series ID.
4. Never touch manual targets, never steal from another emitted active series, and never blanket-clear links merely because their detected series became inactive.
5. Never clear pre-lookback historical links.

In **partial mode**, perform step 2 only for member transactions owned by an item that succeeded in the current run.
Skip re-key reassignment and every unlink operation.

---

## Bills read

`moneta bills [--days 30]` (default 30, validated 1-366).

- Store API: `ReadBills(ctx, db, asOf, days)` — one definition for CLI, REST, and dashboard projection.
- Horizon end: `end = asOf + days` calendar days.
  Example: asOf `2026-07-01`, days 30 → through `2026-07-31`.
- Summary always includes the **full four-field detector object** from D4-6 (not a shorter subset).
- **Recurring rows** when detector status is `ok` or `partial` (D4-6); omitted when `never_run` or `error`:
  - Active outflows (`kind IN ('subscription','bill')`, `is_active=1`) with
    `effective_next_expected_date <= end` AND `asOf <= effective_next_expected_date + grace`.
  - Under `ok`, effective date is the stored date.
    Under `partial`, use the read-time projected date from D4-6 so a persistent item outage does not make every recurring bill disappear after one grace window.
- **Card rows** (always considered when active card): after D4-12 date selection yields date `D`,
  include iff `D <= end` AND `asOf <= D + card_grace_days`.
  (Explicit lower bound so in-grace past dues are not dropped by a naive `d >= asOf` filter.)
- Dashboard `upcoming_bills`: null when `never_run`/`error`; when `ok`/`partial`, same rows as CLI capped to 5 + count.
- Unified fixed row fields: `date`, `name`, `amount` (nullable non-negative Money), `source` (`recurring|card_due`), `kind`, `date_source`, `due_status`.
- Ordering: date asc, name asc, source asc, kind asc.

Card amounts = `min_payment_cents` when present else null — residual understate risk.

## Recurring list read (`moneta recurring`)

When detector status is `never_run` or `error`: return **no** `source='detected'` rows; still return `source='manual'` rows if any; summary includes detector object.

When status is `ok` or `partial`:

- Rows: **all** detected rows (active and inactive) plus manuals, fixed fields including `active` bool.
- Under `partial`, active detected rows emit the effective projected date from D4-6; inactive and manual rows keep their stored date.
- Default sort: `active` desc, then emitted `next_expected_date` asc NULLS last, then name asc.
- Kind counts in summary: count **active detected** rows by kind only (manuals excluded from kind counts).
- Monthly-equivalent: **active** subscription+bill with canonical cadences only (signed expected math as PR6).
- Manuals never enter monthly-equivalent.
- `--kind` filters apply to the listed rows (detected + manual).

---

## Anomaly approach

Compute-on-read, category-level (Phase 3 PR4 MoM: category-id identity, Uncategorized included).

1. Scope: posted, `excluded=0`, `is_transfer=0`, outflows; spend magnitudes positive.
2. `--period YYYY-MM` defaults to **previous complete calendar month** relative to asOf.
   Reject future periods (period month-start after asOf's month → usage/error).
   Dashboard anomalies use that same default and **must include `period` in the slot payload**.
3. **Period spend vs baseline (settled, no contradiction):**
   - **Default / complete past period:** period spend = full calendar month totals; baseline = mean of the **three complete calendar months** immediately before that period (full-month totals each).
   - **Current month requested explicitly:** period spend = MTD days `1..D` where `D = asOf.day` clamped to month length; baseline = mean of MTD slices days `1..D` from each of the three complete calendar months immediately before the current month (same `D`, clamped per month). Never mix full-month baselines with MTD period spend.
   Eligible only when >=2 of 3 baseline months/slices nonzero **and baseline > 0**.
4. New accounts can flag (category-level only) - residual after linking a new card.
5. Signal: `spend > 2 * baseline` AND `spend - baseline >= 5000`, overflow-checked multiply.
   On overflow for a category: **skip that category** (deterministic); increment summary `skipped_overflow`.
6. Ordering: `(spend - baseline)` desc, `spend` desc, category name asc, **category id asc**.
7. `deviation_ratio`: `cli.Ratio(spend-baseline, baseline, 4)` fraction (1.5 = +150% over baseline).
8. Summary always includes `skipped_overflow` (0 when none) so empty + skipped ≠ genuine empty.
9. Dashboard slot: `{period, count, top[3], skipped_overflow}` with top fields category, spend, baseline, deviation_ratio.

---

## Schema migration sketch

| Object | Change | PR |
|---|---|---|
| `transactions.merchant_display` | TEXT NOT NULL DEFAULT '' | PR1 |
| `credit_terms.next_payment_due_date` | TEXT NULL YYYY-MM-DD | PR1 |
| Plaid normalize merchant + full due date | code | PR1 |
| `moneta sync --reset-cursor` / full re-pull docs | CLI + README | PR2 |
| `recurring_items` identity + lifecycle cols incl. `last_matched_cents` + partial UNIQUE | migration | PR3 |
| `detector_state` STRICT + seed + partial status | migration | PR3 |
| detect engine pure | store | PR4 |
| persist + post-loop sync detect | core + CLI | PR5 |
| reads + dashboard slots | later PRs | PR6+ |

**Settled split:** PR1 lands migration `000004` (`merchant_display` + `next_payment_due_date`).
PR3 lands migration `000005` (recurring identity/lifecycle columns, partial unique index, `detector_state`).
Each migration is an up/down pair with tests.
Never land the partial unique index without its columns.

---

## PR graph

```text
PR1 Plaid merchant_display + full card due date (DDL + normalize)
  └─► PR2 sync --reset-cursor + README re-pull instructions
        └─► PR3 recurring identity/lifecycle DDL + detector_state
              └─► PR4 pure recurring detection engine
                    └─► PR5 persist + post-loop detect + status surface (no back-link yet)
                          └─► PR6 back-link + moneta recurring + /v1/recurring
                                └─► PR7 moneta bills + /v1/bills + dashboard upcoming_bills
PR8 anomaly engine + moneta anomalies + /v1/anomalies ─► PR9 dashboard anomalies + final notes
```

All PR1-PR9 nodes are complete.

- PR8-PR9 independent of PR1-PR7 for pure anomalies, but may start after PR1 if desired.
- Each new read includes REST in the same PR.
- Tests first; stop and report after each PR.
- `go build ./... && go vet ./... && go test ./...` + CGO-free + race per CI.
- README on every user-facing command/behavior PR.
- `AGENTS.md` updated in **PR5** (`recurring_detect` dashboard key), **PR7** (bills slot), and **PR9** (anomalies slot / phase4_note).

### PR1 - Merchant display + full card due date

**Anchors:** `internal/providers/plaid/{gateway,sdk_gateway,normalize}.go`, `internal/canon/types.go`, `internal/core/ingest.go`, migration pair.

#### Steps

1. Migration `000004`: `transactions.merchant_display`, `credit_terms.next_payment_due_date` (+ down + tests).
2. **Settled canon fields (not implementer choice):**
   - `canon.Transaction.MerchantDisplay string`
   - `canon.Transaction.MerchantRaw string` unchanged meaning (original / name fallback)
   - `canon.Liability.NextPaymentDueDate canon.Date` empty when unknown (same type/ISO rules as `Transaction.Date`); keep `DueDay int`
3. Gateway: fetch `merchant_name`.
   Normalize: `MerchantRaw` = original_description if non-empty else name; `MerchantDisplay` = first non-empty of merchant_name, name, original_description.
4. Ingest insert/update writes both merchant columns; provider-id match rewrites both.
5. Liability normalize writes full next-payment date + due_day derived from that date; merge path selects **one asOf-aware pair** (D4-12).
6. Tests: preference order; empty fallbacks; merge prefers future date over older past; date+day from same winner; `DedupHash` still keys off `MerchantRaw`.

#### Acceptance

- New syncs populate `merchant_display` without putting enriched names into `merchant_raw` when original exists.
- Full card due date stored when Plaid sends it; merge keeps due_day and full date coherent.

#### Out of scope

- Detection, cursor replay (PR2).

---

### PR2 - Safe history re-pull (`--reset-cursor`)

**Anchors:** `cmd/moneta/main.go` `runSync`/`syncItems`, `core.SyncProviderItem` / `ApplySync` cursor CAS (`ingest.go` ExpectedCursor + finish UPDATE), Plaid `Sync(ctx, cursor)`.

#### Steps

1. Implement D4-14: separate `pullCursor` from `expectedStoredCursor`; `--reset-cursor` sets pull to `""` and leaves DB cursor as CAS expectation until commit.
2. Do **not** DELETE/clear `sync_cursor` before pull (that races and trips `ErrCursorChanged` if memory still holds the old value, and loses the checkpoint on failure).
3. README: preferred Phase 4 upgrade is one unscoped `moneta sync --reset-cursor` covering all linked items; 730-day link history covers lookback.
   If scoped reset runs are used, require a final unscoped `moneta sync` and explain that scoped runs remain partial when multiple items are linked.
4. Tests: failed pull preserves cursor; success advances to replay NextCursor; concurrent change still CAS-fails; after success, previously ingested rows show non-empty `merchant_display` when the provider returns enriched names; multi-page accumulator replay commits once; restart-from-original-cursor on provider mutation error.

#### Acceptance

- Documented safe replay; purpose test: historical rows gain `merchant_display` after reset+sync; no detection yet.

---

### PR3 - Detector schema

**Anchors:** migrations, store migration tests.

#### Steps

1. Migration `000005`: recurring identity/lifecycle columns including **`last_matched_cents`**, partial unique index, `detector_state` **STRICT** with seeded `id=1, status='never_run'` and `partial` allowed in CHECK (+ down drops index first).
2. `ReadDetectorState` returns seeded never_run before any detect write; writers upsert id=1.

#### Tests

```
TestMigration000005DetectorSchema: up applies; down clean; partial unique rejects duplicate detected identity;
  last_matched_cents column exists; detector_state is STRICT; status never_run seeded.
TestDetectorStateUpsert: ok/partial/error transitions; ReadDetectorState before any upsert still never_run.
```

#### Out of scope

- Detect algorithm, sync wiring.

---

### PR4 - Pure recurring detection engine

**Anchors:** store read patterns; D4-7a/D4-13 algorithm text.

#### Steps

1. `detectRecurringSeries(rows, asOf)` pure; thin DB loader for candidates (no writes).
   Candidate provenance includes `provider_item_id` through the transaction's account so partial persistence can require successful-item evidence.
2. Implement frozen detect_key, cluster, coverage-gated best-fit, grace schedule, signed expected, overflow guards.

#### Tests first

```
TestDetectRecurringFindsMonthlySubscription: four -1500 monthly -> expected -1500, monthly.
TestDetectRecurringIncomeAndRentBillKind.
TestDetectRecurringRejectsIrregularSparseTinyAndLowCoverage.
TestDetectRecurringSkipsPendingExcludedTransferEmptyKey.
TestDetectRecurringCadenceWindows: weekly/biweekly/monthly/quarterly; yearly does not classify.
TestDetectRecurringBestFitSkipsExtraAndPrefersRecentOverLongerOldRun.
TestDetectRecurringGraceKeepsNextOnOpenStep: asOf == due -> next==asOf; asOf = due+1 within grace -> next still due date (not advanced).
TestDetectRecurringInGraceVisibleToBillsHorizonRules.
TestDetectRecurringTwoMissesAfterGraceDeactivates.
TestDetectRecurringEOMAnchorPreserves31ThroughFebruary.
TestDetectRecurringAnchorUsesModeNotLastJitterDay.
TestDetectRecurringEntityIsolation: every entity_id in lookback.
TestDetectRecurringOverflowIncrementsSkippedCounter.
TestDetectKeyV1FrozenFixtures: golden strings including hash-with/without space.
```

#### Acceptance

- Hand-computed fixtures; zero writes.

---

### PR5 - Persist + post-loop detection + status surface

**Anchors:** `syncItems` item loop end, `SyncResult`, `detector_state`, status/dashboard sync section.

#### Steps

1. Compute `complete_run` from exact current outcomes: full linked set attempted and every attempt succeeded.
2. When at least one item succeeds, detect once and persist in complete or partial positive-evidence mode per D4-4; no back-link yet (PR6).
3. Complete mode performs full lifecycle including unseen deactivation; partial mode may insert/refresh eligible emitted rows but forbids every absence-based active-to-inactive transition.
4. If every attempted item fails, skip detection, preserve rows, and set `partial`.
5. Failure isolation; injection seam; statuses never_run/ok/partial/error.
6. Prose lines for ok / partial-positive / partial-skip / detect-failed / overflow count.
7. **Mandatory** status surfaces (D4-6 four-field object): `moneta status`, `GET /v1/status`, dashboard `recurring_detect`.
8. Update `AGENTS.md` + README for `recurring_detect` (prose review); Go test asserts report document shape.
9. Empty-batch success counts as a successful attempt; complete versus partial still follows the exact scope rule.

#### Tests first

```
TestRecurringUpsertIdempotentAndCadenceUpdatesSameRow: last_matched_cents refreshed; drift_pct column stays 0.
TestRecurringNeverTouchesManual.
TestRecurringUnseenKeyDeactivatesOnSuccessfulDetect.
TestRecurringOverflowSkipDoesNotDeactivateExistingSeries.
TestRecurringEntityWithOnlyActiveRowsStillDeactivatesUnseen.
TestSyncDetectRunsOnceAfterAllItems: multi-item fixture -> single detect invocation.
TestSyncTransientFailureCannotUseOldDurableOK: P=L, item B attempt fails while stored status stays ok -> complete_run false.
TestSyncPartialPositiveEvidenceUpsertsWithoutNegativeTransitions: successful-item member refreshes row; no active-to-inactive or miss increase.
TestSyncPartialLeavesFailedItemOnlySeriesUnchanged.
TestSyncScopedItemSuccessDoesNotPromoteGlobalOk: --item A ok with multiple linked -> partial positive mode.
TestSyncAllItemsFailSetsPartialNotStaleOk: prior ok becomes partial; detector not called; series preserved.
TestSyncSurvivesDetectionFailure: ingest kept; status=error; no series writes; last_success_at unchanged.
TestDetectRunsOnEmptyBatchWhenComplete; empty scoped success is still partial when multiple items linked.
TestStatusAndDashboardSurfaceDetectorState: never_run before first run; four-field object exact.
TestAPIStatusIncludesDetectorState.
TestReportDashboardIncludesRecurringDetectKey (report package — not AGENTS.md text).
```

#### Out of scope

- Back-link, recurring CLI.

---

### PR6 - Back-link + `moneta recurring` + `/v1/recurring`

#### Steps

1. Back-link policy D4-5: complete mode reassigns only conflicting emitted member IDs; partial mode is additive only for successful-item members.
2. `ReadRecurring` per recurring list contract above + CLI + REST + README.
3. Fixed row fields: `name`, `kind`, `cadence`, `expected` (signed Money), `next_expected_date`, `drift_pct` (percent 2dp per D4-10), `drift` (bool), `active`, `source`.
4. Sort: active desc, next_expected_date asc NULLS last, name asc.
5. Summary: four-field detector object; kind counts = active detected only; monthly-eq as below when status allows detected listing.
6. Monthly-equivalent (active subscription+bill, canonical cadences, signed expected, trunc toward zero, overflow-checked):
   - weekly: `expected * 52 / 12`
   - biweekly: `expected * 26 / 12`
   - monthly: `expected`
   - quarterly: `expected / 3`
   - else: unconverted

#### Tests

```
TestBackLinkRekeyFromDeactivatedDetectedToNewSeries.
TestBackLinkPreservesManualOtherActivePreLookbackAndUnrelatedEndedSeriesLinks.
TestBackLinkPartialModeOnlyAddsSuccessfulItemMembersAndNeverUnlinks.
TestReadRecurringListsActiveAndInactive; kind counts active-only; manuals always when present.
TestReadRecurringOmitsDetectedWhenNeverRunOrError; includes when partial or ok.
TestReadRecurringPartialProjectsEffectiveNextDateWithoutMutatingStoredLifecycle.
TestReadRecurringDriftPercentExample: -10000 expected, -12000 last -> canonical drift_pct 20, drift true.
TestReadRecurringDriftPercentUsesBigIntegerBoundaryMath.
TestRunRecurringTOONJSON; TestAPIRecurringMirrorsCLI.
```

---

### PR7 - `moneta bills` + dashboard `upcoming_bills`

#### Steps

1. `ReadBills` per bills contract (magnitudes, date_source, due_status, grace visibility, detector gate for never_run/error only).
2. CLI + REST + README; CLI and dashboard agree (same store read); four-field detector summary.
3. Dashboard: null when never_run/error; when ok/partial, top 5 + count; `recurring_detect` already present from PR5.
4. Update `AGENTS.md` R5/C1 for bills.

#### Tests

```
TestReadBillsMergesRecurringAndCards; month-end; asOf injected.
TestReadBillsIncludesDueOnAsOfAndInGrace; magnitude non-negative; card kind bill; date_source values.
TestReadBillsCardInGraceInsideHorizon; card outside end excluded; card past grace projected then horizon-filtered.
TestReadBillsActiveCardsOnly.
TestReadBillsOmitsDetectedWhenNeverRunOrErrorKeepsCards; includes detected when partial.
TestReadBillsPartialProjectsRecurringBeyondStoredGraceWithoutMutatingRow.
TestReadBillsDetectorSummaryFourFields.
TestDashboardBillsNullWhenNeverRunOrError; shows rows when partial or ok.
TestDashboardBillsEmptyWhenOkNothingDue.
TestDashboardFillsUpcomingBillsWhenOk.
TestAPIBillsMirrorsCLI.
```

---

### PR8 - Anomalies engine + CLI + REST

#### Steps

1. `ReadAnomalies` with previous-month default, MTD path, baseline > 0, overflow skip with summary counter, ordering with category id.
2. CLI + REST + README.

#### Tests

```
TestReadAnomaliesFlagsOnlyTrueSpikes; baseline history; baseline>0; default previous month; MTD;
  future period rejected; new account old category can flag; ordering; ratio fraction;
  overflow sets skipped_overflow and can yield empty rows with non-zero skipped.
TestRunAnomaliesTOONJSON; TestAPIAnomaliesMirrorsCLI.
```

---

### PR9 - Dashboard anomalies + final cleanup

#### Steps

1. Slot `{period, count, top[3], skipped_overflow}` for default previous month (must match anomaly item 9).
2. Remove `phase4_note` only when both slots follow D4-6 honesty (`recurring_detect` already present from PR5).
3. `AGENTS.md` + README final.

#### Tests

```
TestDashboardAnomaliesIncludePeriodAndSkippedOverflow; empty count 0; phase4_note absent when appropriate;
  bills gating unchanged on detect error/partial.
```

---

## Privacy

- Fixtures: invented merchants only (`Streambox Example`, `Gym Example`, `Cloudhost Example`, `Payroll Example`).
- Detection errors: redacted counts/text only.
- `docs/decisions/0006` applies.

## Copy-paste agent prompt (PR1 only)

> Read `AGENTS.md`, `docs/product-spec.md`, and the binding rules in `docs/moneta-plan.md` first (int64 cents, analytics exclusion, TOON output).
> Execute **PR1 only** from `docs/phase4-recurring-anomalies-plan.md` - merchant_display + full card next-payment date foundation.
> Confirm D4-1..D4-14 with the maintainer at kickoff; if any is rejected, stop and report.
> Migration `000004` only (not 000005): `transactions.merchant_display`, `credit_terms.next_payment_due_date` (up+down+tests).
> Canon: add `Transaction.MerchantDisplay` and `Liability.NextPaymentDueDate canon.Date` (empty when unknown).
> Plaid: fetch merchant_name; MerchantRaw = original_description (fallback name); MerchantDisplay = merchant_name > name > original_description.
> Write full next_payment_due_date when parseable; DueDay derived from that date; merge liabilities with asOf-aware earliest-future-or-latest-past pair (D4-12).
> Do not change NormalizeMerchant beyond consuming MerchantRaw as today; do not start detection, --reset-cursor, or detector schema.
> Write tests first; watch them fail; then implement.
> No new dependencies. CGO-free. Fake data only.
> Do not commit, push, or open a PR unless I explicitly ask.
> Stop and report with `go test ./...` output when PR1 is complete.

---

## Open questions

Most prior R4 items are **closed** in D4-*. Remaining soft items:

- **R4-grace values:** monthly/quarterly grace 3 days and weekly/biweekly 1 day are defaults; confirm at PR4 if the maintainer wants different numbers (data-only retune).
- **R4-coverage:** 50% subsequence coverage floor - confirm at PR4.
- **R4-loan gap:** still accepted (D4-9); reopen only if maintainer wants excluded Loan Payments in detection later.

Thresholds (amount ±5%/100¢, min $5, 2x+$50 anomaly) remain v1 defaults retunable without schema changes.
`detect_key` algorithm is **not** retunable without migration (D4-7a).

---

## Residual risks

- Without PR2 `--reset-cursor` re-pull, pre-PR1 rows keep empty `merchant_display` and fall back to descriptor norms; detection under-finds and can briefly emit **duplicate series** (display key vs raw key) until a full re-pull; unseen-key deactivation self-heals after full re-pull.
- Noisy institutions with empty merchant_name still fragment; FN preferred over FP, reinforced by $5 floor and 50% coverage.
- Two true price tiers at one merchant: one-shot median keeps one tier.
- Descriptor renames still fragment; no user merge without moneta tag/D2.
- Loans and transfer-category obligations absent from bills; use `moneta debts`.
- Card amounts are min payments; cash needs can understate.
- `day_of_month_estimate` and `projected_from_past_provider_date` card dates can still be wrong month until a liabilities sync supplies a fresh full date.
- Category-level anomalies can fire after linking a new account into an old category.
- On detect `error` / `never_run`, agent-facing reads omit detected recurring rows (dashboard nulls `upcoming_bills`); on `partial`, the conservative snapshot remains visible while item health is visible on `moneta status`.
- Partial persistence can add or refresh a series from successful-item evidence, but it deliberately preserves possibly ended series and skips destructive re-key cleanup until a complete run.
- Read-time projection under `partial` keeps active schedules useful but can continue forecasting an obligation that actually ended on an unavailable item; detector status makes that uncertainty explicit.
- A full 730-day `--reset-cursor` replay is one large in-memory accumulate + one ApplySync transaction; provider mutation restarts re-pull the full history (cost residual, checkpoint-safe).
- Scoped reset recovery requires a final unscoped sync before detector status can return to `ok` when multiple items are linked.
- MTD anomaly baselines with `asOf.day = 31` weight short months (e.g. February) as full-month slices after clamp; can flip a flag near month end.

---

## Review changelog

### Fixed in first rewrite (kept)

Yearly out; Loan Payments gap; signed expected; cadence out of identity; post-commit detect intent; manual protection; cli.Ratio fraction; anchors corrected.

### Fixed in second rewrite (kept)

merchant_display + re-pull path; detect once after item loop; durable lifecycle cols; grace/due-today intent; no single-txn reactivation; detector status model; back-link members only; frozen detect_key; bills magnitudes; full card dates; lookback example; PR split; migration pairs.

### Fixed in third rewrite (kept)

Migration split; D4-14 safe replay; EOM anchor; grace visibility; best-fit recency; date_source enum; CLI/dashboard honesty; mandatory status surfaces; seeded detector_state; skipped_overflow; multi-entity intent; PR2 acceptance.

### Fixed in fourth polish (kept)

last_matched_cents; detect_key regex; detector_state STRICT + partial; entity set union; overflow preserve; lookback unlink; card grace; MTD baselines; PR9 skipped_overflow; canon.Date; PR2 multi-page tests.

### Fixed in fifth polish (2026-07-31; complete-input freeze superseded below)

1. **`partial` is not a kill-switch:** reads show detected rows under `partial`; the sixth polish refined them into a conservative snapshot with read-time projection.
2. **Complete-input freeze introduced, then narrowed in the sixth polish:** scoped `--item` cannot promote global `ok`, but partial runs now accept positive evidence.
3. **No sync item counts on detector_state;** item health stays on `provider_items` / existing status surfaces.
4. **Unified four-field detector object** everywhere (dropped duplicate counters from D4-6).
5. **Re-key back-links introduced;** the sixth polish narrowed reassignment to conflicting emitted member IDs so unrelated ended-series history remains linked.
6. **Liability merge asOf-aware:** earliest future-or-equal, else latest past.
7. **`moneta recurring` inclusion** explicit (active+inactive when allowed; kind counts active-only).
8. **Drift percent formula** frozen on magnitudes; the sixth polish made its arithmetic overflow-safe and its output canonical TOON.
9. **Card horizon predicate** explicit: `D <= end AND asOf <= D + grace`.
10. **Report package test** for `recurring_detect` key (not AGENTS.md prose).
11. Residual: MTD day-31 short-month weighting.

### Fixed in this sixth polish (2026-07-31)

1. **Exact completeness:** `complete_run` requires the full linked set and success for every current attempt; stale durable `ok` cannot hide a transient failure.
2. **Partial positive-evidence mode:** emitted rows backed by a successful item may insert/refresh, while unseen deactivation, miss-based active-to-inactive transitions, destructive unlink, and re-key cleanup remain complete-only.
3. **Partial schedule projection:** recurring and bills roll active stored schedules forward at read time without mutating miss/lifecycle state, so persistent outages do not reduce bills to cards-only after grace.
4. **Reset upgrade path:** prefer one unscoped reset; scoped recovery ends with an unscoped sync.
5. **Back-link re-key scope:** reassign only conflicting emitted member IDs; preserve unrelated history for ended detected series.
6. **Canonical drift output:** big-integer percent calculation avoids overflow; two-decimal precision emits canonical TOON without trailing zeros (`20`, not `20.00`).
7. **Recurring sort contract:** removed the malformed alternative and kept one settled ordering.
