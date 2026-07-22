# Phase 2 Review Fix PR Plan

Hand-off for post-review hardening of Phase 2 Moneta (2a poison-skip, 2b `sync`, 2c `status`, 2d `accounts`/`tx`).
Do **not** start Phase 3 analytics or the deferred REST mirror in this stack.
Do **not** commit or push unless the maintainer explicitly asks.
No new dependencies.
Keep CGO-free.
Fake data only in tests and fixtures.

**Source:** local review of `a2cae31..HEAD` (all of Phase 2, merged), reconciled against a four-way multi-agent review and red/green reproduction on throwaway repo copies (2026-07-22).
Every wedge in PR1 was reproduced against the current tree before being written up; line numbers are current as of that reconcile.
**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md` (binding pending/posted, cents, and analytics-exclusion rules), `docs/decisions/`.

**Source ranking.**
1. This plan and the deep review behind it are primary: the PR1 wedges, DP1, the PR3 excluded-aggregate, and the PR5 TOON pins were all verified against the tree (red/green where behavior is observable).
2. The Grok full-Phase-2 review contributed the connection-health gap (PR7) and the ops items in [Reconciliation with the other reviews](#reconciliation-with-the-other-reviews); its overclaims were demoted after an SDK-grounded check.
3. The shallow `docs/phase2_code_review.md` (Gemini) is non-authoritative, is **not the control document**, and is header-stamped superseded by this plan. Its only surviving items were already covered here (MinInt64 `Money` in PR6, exit codes in PR2). Do not act on its "ready for production" framing or its unverified TOON speculation (see Reconciliation).

> **Read this first.**
> 2a's "skip a poison row instead of wedging the cursor" is correct for the poison classes it enumerates (unsupported currency, malformed amount/date, unsupported account/liability type).
> It is **incomplete**: `normalize` does not enforce every invariant `ingest` enforces, so three other single-row anomalies still reach `ingest`, hard-error, roll back the batch, and leave `sync_cursor` unadvanced - a permanent wedge that replays the same poison on every future sync.
> PR1 closes that gap structurally and is the reason this stack exists.
> The remaining PRs are smaller correctness, durability, and hardening items.
> Every PR below is fully specified; the two genuine product forks are resolved in [Decisions made in this plan](#decisions-made-in-this-plan) so no maintainer input is required to execute the stack.

## Goals

1. Make `ingest` wedge-proof: a single row that fails a validation invariant must be skipped and recorded, never roll back the batch and stall the cursor.
2. Give `link`/`sync` the same exit-code contract the read commands already honor (usage error = 2, runtime error = 1), so agents can branch on it.
3. Stop the `moneta tx` aggregate from counting transfers and excluded rows into spending totals, per the binding analytics-exclusion rule.
4. Make skipped records durable and auditable instead of an ephemeral stdout count, so a permanently dropped transaction leaves a trace.
5. Pin the TOON encoder's load-bearing safety property (no structure injection via `Number`) and close two small encoder-API gaps.
6. Clear the small confirmed correctness nits the review found, without changing behavior beyond the fix.
7. Make `moneta status` exit 3 meaningful by persisting `login_required` on reauth-class sync failures.

## Non-goals

- The deferred loopback REST mirror (still Phase 2+, but not in this stack).
- Phase 3 analytics, net-worth, or utilization math.
- RocketMoney CSV or manual provider implementations.
- `moneta tag` and the provider-vs-user field-ownership decision (carried forward from Phase 1 D2; belongs to the PR that adds `moneta tag`).
- Multi-currency, holdings, webhooks, whole-database encryption.

---

## Decisions made in this plan

Two PRs touch a genuine product fork.
Both are resolved here with reasoning so the implementer does not stall; the maintainer can override before execution.

### DP1. The wedge fix belongs in `ingest`, not `normalize`. (PR1)

The obvious-looking alternative is to patch `normalize` to pre-validate each invariant `ingest` checks (skip empty-name accounts, skip pending+pending-id rows, drop transactions whose account is absent).
**Reject that as the primary fix.**
`normalize` can never be guaranteed to anticipate every ingest invariant, so a per-case normalize patch will keep missing the next unanticipated anomaly - which is exactly how 2a shipped incomplete.
The durable fix is to make `ingest` itself treat a per-record **validation** failure as a skip rather than a batch-fatal error.

This is not a new pattern: `upsertLiability` already skips an unsupported-account-type liability instead of erroring (the `default` branch near `ingest.go:671-680`, surfaced through `IngestResult.Skipped`).
PR1 generalizes that same "record a `SkippedRecord`, continue the batch" behavior to the account and transaction paths.
The one invariant that must be preserved: a genuine **infrastructure** failure (a DB error from `ExecContext`, a `RowsAffected` mismatch, a cursor-changed conflict) still rolls back the whole batch.
Only per-record validation failures become skips.

### DP2. `tx` excludes `excluded` rows from the aggregate, not from the listing. (PR3)

`moneta-plan.md:73` and `:321` are binding: transfers and card payments are "excluded from spending analytics", and `excluded` is a derived cache of `category.kind` (a transfer category sets `excluded = 1`, asserted at `ingest_test.go:284`).
So the aggregate header (`total`/`inflow`/`outflow`) must filter on `transactions.excluded = 0`.
`excluded = 1` already subsumes transfers, so no separate `is_transfer` predicate is needed.

The **row listing** keeps showing every matching row.
`moneta tx` is a ledger view, and hiding transactions from the list would be surprising; only the analytics aggregate must obey the exclusion rule.
To keep the two consistent for a reader, PR3 adds an `excluded_count` field to the summary so an agent can see the aggregate omitted some rows (see PR3 for the exact shape).

---

## PR graph

```text
PR2 (exit codes)             ── independent, ship any time
PR3 (tx aggregate)           ── independent, ship any time
PR5 (TOON hardening)         ── independent, ship any time
PR7 (persist login_required) ── independent, ship any time (after PR1 is nicer, not required)

PR1 (ingest wedge-proofing)  ──► PR4 (persist skip audit trail)
PR6 (small cleanups)         ── independent, ship last (touches files others edit)
```

- **PR1 is the reason for this stack** and should land first among the correctness PRs.
- **PR4 lands after PR1** because it persists the skip records PR1 makes the batch produce.
- **PR2, PR3, PR5, PR7 are mutually independent** and touch disjoint files.
- **PR6 is low-priority cleanup**; land it last.

**Required for this stack:** PR1, PR2, PR3.
**Recommended:** PR4, PR7, PR5.
**Nice-to-have:** PR6, plus the tracked-but-deferred ops items in [Reconciliation](#reconciliation-with-the-other-reviews) (sandbox on the orchestrator path, `store.Open` not creating missing DBs for reads).

---

## PR1 - Make ingest wedge-proof (blocker)

**Severity:** confirmed. Three single-row anomalies permanently wedge an Item's sync cursor - the exact failure 2a existed to prevent.
**Anchors:** `internal/core/ingest.go:195-197` (empty name), `:264-269` (pending+pending-id), `:274` (unresolvable account); `internal/providers/plaid/provider.go:154-168` (cascade that does not cover absent accounts).

### Problem

Each of these reaches `ApplySync`, hard-errors, rolls back the whole batch, and leaves `sync_cursor` unchanged, so the next sync replays the same poison forever.

1. **Absent-account transaction (HIGH).**
   The 2a cascade (`provider.go:154-168`) builds `skippedAccountIDs` only from `RecordKindAccount` skips, so it drops transactions for *skipped* accounts but not for accounts that were never in `accounts/get` at all.
   Such a transaction reaches `w.account()` (`ingest.go:274`), which returns "account is not connected to this provider item", and the batch rolls back.
   This is already codified as intended behavior by `TestApplySyncRollsBackDataAndCursorOnFailure` (`ingest_test.go:399`): account `checking-1` plus a transaction referencing `missing-account` asserts `accounts=0, transactions=0, cursor=""`.
   Trigger is uncommon (a closed or de-selected account, or a Plaid consistency window) but the impact is a permanent wedge.

2. **Empty-name account (HIGH).**
   `normalizeAccounts` (`provider.go:331-337`) sets `name = account.Name`, falls back to `OfficialName`, and emits the account with `Name == ""` when both are empty.
   `upsertAccount` hard-errors `account name is required` (`ingest.go:195-197`).
   Reproduced red/green on a scratch copy: `ApplySync` returns `apply account 0: account name is required`, cursor stays `""`.
   Some institutions return sparse account metadata, so this is realistically reachable.

3. **Pending transaction carrying a pending_transaction_id (MEDIUM).**
   `normalize.go:75` copies `PendingTxnID` unconditionally regardless of status, so a pending row that also carries a `pending_transaction_id` yields `PendingTxnID != "" && Status == Pending`.
   `upsertTransaction` rejects that at `ingest.go:267-269`.
   Reproduced red/green: `apply added transaction 0: pending transaction id is only valid on a posted transaction`, cursor stays `""`.
   Near-impossible from real Plaid, but the same structural class and fixed by the same mechanism.

### Implementation

**File:** `internal/core/ingest.go`.

Introduce an explicit, distinguished skip signal so the batch loop can tell a per-record validation failure apart from an infrastructure failure.

```go
// skipRecord signals that one record failed a validation invariant and must
// be skipped and recorded, not treated as a batch-fatal error. Infrastructure
// errors (DB failures, RowsAffected mismatch, cursor conflicts) are returned
// as ordinary errors and still roll back the whole batch.
type skipRecord struct {
	rec canon.SkippedRecord
}

func (e skipRecord) Error() string { return "skip: " + e.rec.Detail }
```

Convert the three validation failures from `fmt.Errorf(...)` to a returned `skipRecord{...}`:

- `upsertAccount` empty-name check (`ingest.go:195-197`) →
  `return skipRecord{canon.SkippedRecord{Kind: canon.RecordKindAccount, ID: account.ProviderAccountID, Reason: canon.SkipMalformedRecord, Detail: "missing account name"}}`
- `upsertTransaction` pending+pending-id check (`ingest.go:267-269`) →
  `skipRecord{... Kind: RecordKindTransaction, ID: transaction.ProviderTxnID, Reason: SkipMalformedRecord, Detail: "pending row carries pending id"}`
- `upsertTransaction` unresolvable account: when `w.account()` (`ingest.go:274`) returns the "not connected" error, convert **that specific case** to
  `skipRecord{... Kind: RecordKindTransaction, ID: transaction.ProviderTxnID, Reason: SkipAccountSkipped, Detail: transaction.AccountRef}`.
  Do not swallow other errors from `w.account()`; only the account-not-found case becomes a skip.

In the batch apply loop (where `upsertAccount` / `upsertTransaction` / `upsertLiability` are called), wrap each call:

```go
var skip skipRecord
if err := w.upsertTransaction(ctx, txn); errors.As(err, &skip) {
	result.Skipped = append(result.Skipped, skip.rec)
	continue
} else if err != nil {
	return nil, err // infrastructure error: roll back the batch
}
```

Apply the same wrap to the account and liability loops.
The account cascade falls out for free: an account skipped for an empty name is never inserted, so its transactions later hit the "account not connected" path and are skipped by the same transaction rule - no separate ingest-side cascade map is needed.

**Do not** change `provider.go`'s normalize-side skips or its existing cascade; they remain correct for the classes they cover.
This PR moves the *last line of defense* into `ingest`, which is the layer that knows the full invariant set and the persisted state.

> **Preserve atomicity for infrastructure errors.**
> The value of `ApplySync`'s single transaction is that a disk/DB failure never commits a half-applied batch.
> Keep that: only `skipRecord` short-circuits to a skip; every other error still returns and rolls back.

### Tests first (`internal/core/ingest_test.go`)

Two currently-passing tests **codify the old wedge behavior and must be reframed** - call this out in the PR body so it does not read as an unexplained assertion change:

- `TestApplySyncRollsBackDataAndCursorOnFailure` (`ingest_test.go:399`) currently asserts an unknown-account transaction rolls the whole batch back (`accounts=0, transactions=0, cursor=""`).
  Under the fix, the valid account `checking-1` **is** written, the unknown-account transaction becomes a skip, and the cursor **advances**.
  Rewrite it to assert `accounts=1, transactions=0, result.Skipped` contains one `RecordKindTransaction` skip, and `cursor="cursor-1"`.

Add these regressions (red on current code, green after):

```
TestApplySyncSkipsEmptyNameAccountInsteadOfWedging
  Batch: Accounts=[{ProviderAccountID:"acct-noname", Name:"", Type:checking, USD}], NextCursor:"cursor-1"
  Assert: no error; accounts=0; result.Skipped has one RecordKindAccount skip; cursor=="cursor-1"

TestApplySyncSkipsPendingWithPendingIDInsteadOfWedging
  Batch: Accounts=[valid checking-1], Added=[{ProviderTxnID:"t1", PendingTxnID:"p", AccountRef:"checking-1",
         2026-07-01, -100, pending, USD}], NextCursor:"cursor-1"
  Assert: no error; transactions=0; result.Skipped has one RecordKindTransaction skip; cursor=="cursor-1"
```

Add one test that **guards the preserved atomicity** so a future refactor cannot turn infrastructure errors into silent skips:

```
TestApplySyncStillRollsBackOnInfrastructureError
  Force a non-validation failure inside the batch transaction (e.g. a closed DB handle, or an
  injected Exec error) and assert ApplySync returns an error, writes nothing, and leaves cursor "".
```

Keep green: all existing plaid poison tests (`TestProviderSyncPoisonRecordDoesNotWedgeItem`, `TestProviderSyncSkipsUnsupportedCurrencyTransactions`), `TestApplySyncSkipsLiabilityForUnsupportedAccountType`, and the pending/posted dedup suite.

### Acceptance

- The three anomalies skip-and-advance instead of wedging; each has a red-before/green-after test.
- A genuine infrastructure error still rolls back the whole batch (its own test).
- `SyncResult.Skipped` surfaces the new ingest skips (already wired via `sync.go:92-95`).
- `go test ./...` passes.

---

## PR2 - Correct exit codes for `link`/`sync` usage errors

**Severity:** medium. Agent-facing: breaks exit-code-based control flow.
**Anchor:** `cmd/moneta/main.go:44-65` (the `link` and `sync` cases in `run()`).

### Problem

`run()` maps every non-help, non-`Canceled` error from `runLink`/`runSync` to exit **1**.
But unknown flags and stray positional args are usage errors, which the convention (and `accounts`/`status`/`tx`) treat as exit **2**.
Verified on a built binary:

- `moneta sync --bogus` → 1; `moneta accounts --bogus` → 2
- `moneta sync extra` → 1; `moneta status extra` → 2
- `moneta link --bogus` / `moneta link extra` → 1

Separately, `sync`/`link` print the parse error twice: the `flag` package writes the error and usage to stderr on a parse failure, then `run()` prints `error: <same message>` again (verified).

### Implementation

Mirror what the read commands already do: have `runLink`/`runSync` signal usage errors distinctly and return **2** without re-printing.
The read commands return `2` right after `flags.Parse` fails (and after the positional-arg and required-flag checks) and print their own single message.

Concretely, in `runLink`/`runSync`:

- On `flags.Parse(args)` returning a non-`ErrHelp` error, return a sentinel the caller maps to 2 (or restructure `runLink`/`runSync` to return an `int` exit code like `runStatus`/`runTx` do, which is the cleaner alignment - the three read commands already return `int`).
- Treat the positional-arg rejection (`link does not accept positional arguments`, `sync does not accept positional arguments`) and the missing-`--db` message as usage → 2, matching the read commands.
- Do not double-print: when `flag` already wrote the message, return the code without a second `error:` line.

The lowest-risk change that removes the whole inconsistency is to convert `runLink`/`runSync` to the same `func(...) int` shape as `runStatus`/`runAccounts`/`runTx` and delete the wrapper `switch` arms' error-to-1 mapping for those two.
Keep `context.Canceled` mapping to a clean exit (no `error:` spam on Ctrl+C).

### Tests first (`cmd/moneta/main_test.go`)

Add table cases asserting exit codes for all five commands so the contract is pinned:

```
sync --bogus     -> 2      link --bogus   -> 2
sync extra       -> 2      link extra     -> 2
accounts --bogus -> 2 (regression guard; already 2)
```

Assert `moneta sync --bogus` writes the parse error **once** to stderr (no duplicated `error:` line).

### Acceptance

- All five commands return 2 for unknown flags and stray positional args, 1 for runtime errors, 0 on success.
- No duplicated parse-error output.
- `go test ./...` passes.

---

## PR3 - Exclude transfers/excluded rows from the `tx` aggregate

**Severity:** medium. The headline `inflow`/`outflow`/`total` an agent reads first disagrees with the binding analytics-exclusion rule.
**Anchor:** `internal/store/transactions.go:91-104` (`SummarizeTransactions`).

### Problem

`SummarizeTransactions` has no `excluded` predicate, so `total`/`inflow`/`outflow` fold in internal transfers (double-counted on both sides) and rows the system marked excluded.
`moneta-plan.md:73` / `:321` require transfers and card payments to be excluded from spending analytics, and `excluded = 1` is the derived cache of exactly those (`ingest_test.go:284`).
See [DP2](#decisions-made-in-this-plan): the aggregate must filter; the row listing stays complete.

### Implementation

**File:** `internal/store/transactions.go`.

Add `AND transactions.excluded = 0` to the **summary** query only (`SummarizeTransactions`, `ingest.go`-style clause at `:91-104`).
Do **not** add it to `transactionFilterWhere` (that constant is shared with `ListTransactions`, whose listing must stay complete).
Duplicate the filter body into the summary query with the extra predicate, or thread an `includeExcluded bool` into a small internal helper - keep it parameterized, no string interpolation of user input.

Add an `excluded_count` to `TransactionSummary` and to the `summary` object in `buildTxDoc` (`cmd/moneta/tx.go:153-159`) so a reader can see the aggregate omitted rows:

```
summary:
  count: <all matching rows>
  excluded: <count of excluded rows in the match>
  total: <sum over non-excluded>
  inflow: <...>
  outflow: <...>
```

Compute `excluded` as `COUNT(*)` over the match minus the non-excluded count, in the same single query (one extra `SUM(CASE WHEN excluded=1 THEN 1 ELSE 0 END)`), so it stays one round trip.

### Tests first (`internal/store/transactions_test.go`, `cmd/moneta/tx_test.go`)

```
TestSummarizeTransactionsExcludesTransferAndExcludedRows
  Seed one account with: a +500000 transfer-in (excluded=1), a -500000 transfer-out (excluded=1),
  and a -2000 posted purchase (excluded=0).
  Assert: count == 3, excluded == 2, total == -2000, inflow == 0, outflow == -2000.
  (On current code total/inflow/outflow wrongly reflect the transfers.)
```

Update the `tx` golden/TOON assertion to include the `excluded` field.

### Acceptance

- The aggregate reflects only `excluded = 0` rows; the listing still shows all matching rows.
- `excluded_count` is present and correct.
- `go test ./...` passes.

---

## PR4 - Persist the skip audit trail

**Severity:** medium. Skipped records advance the cursor past them (correct for skip-not-wedge) but are recorded nowhere durable, so a permanently dropped transaction cannot be audited or reconciled.
**Anchor:** `internal/core/ingest.go:775-792` (`import_runs` INSERT has no skipped column); `cmd/moneta/main.go:249-256` (ephemeral stdout count only).

### Problem

For persistent poison (unsupported currency/type) the drop is fine - the record could never be stored anyway.
For a **transient** malformed transaction the cursor advances past it and it is gone with no trace: `import_runs` shows unchanged counts, and the user sees `"1 record skipped"` once.
Land this after PR1, which increases how many records take the skip path.

### Implementation

Smallest durable option: add a `skipped` integer column to `import_runs` (migration) and write `len(batch.Skipped) + len(ingestResult.Skipped)` into it in the same INSERT.
This preserves the atomic per-migration commit convention and needs no new table.

If per-record forensics are wanted (recommended but larger), add a `skipped_records` table keyed by `import_run_id` storing `(kind, provider_id, reason, detail)` - **opaque fields only**, matching `canon.SkippedRecord`, never amounts/merchant/account names.
Insert rows inside the same batch transaction so the audit trail commits atomically with the cursor advance.
Keep this PR to the `import_runs.skipped` count if the table feels like scope creep; note the deferral in the PR body.

Surface it: `moneta status` can show a per-item skipped-since-last-sync signal (optional, only if the table lands).

### Tests first

```
TestApplySyncRecordsSkippedCountInImportRun
  Apply a batch with one skipped record; assert the import_runs row has skipped == 1.
```

### Acceptance

- A skipped record leaves a durable trace tied to its import run.
- No sensitive payload is persisted in the skip trail.
- `go test ./...` passes.

---

## PR5 - Harden the TOON encoder

**Severity:** low/medium. The encoder is injection-safe today; this pins the property and closes two small API gaps.
**Anchors:** `internal/toon/toon.go:48-49` (`Number` regex), `:125-135` (table header, no dup-column check), `:240` (control-rune set).

### Problem

- **M5 - the load-bearing safety property is untested.**
  `Number` is emitted verbatim, guarded solely by the anchored regex.
  It correctly rejects `Number("5\ninjected: true")` today because Go's `$` is end-of-text, but nothing pins it: a future change to `(?m)` or adding `.` to the pattern would reopen structure injection with a fully green suite.
- **L5 - duplicate table columns are silently accepted** (`Table{Fields:["a","a"]}` → `t[1]{a,a}:`), inconsistent with the object-level duplicate-key guard at `toon.go:84-88`.
  Not reachable today (field lists are static literals) but a real encoder-API gap.
- **L6 - interior U+2028/U+2029/U+0085/U+007F are emitted raw.**
  Spec-legal (only U+0000-U+001F must be quoted), but these are line separators to JS engines and some terminals, and untrusted merchant names flow through here.

### Implementation

**File:** `internal/toon/toon.go`, tests in `internal/toon/toon_test.go`.

- Add a duplicate-column check to `encodeTable` mirroring `encodeObject` (return `fmt.Errorf("duplicate table column %q", name)`).
- Optionally extend `needsQuotes`/`hasControlRune` to also force-quote U+0085, U+2028, U+2029, and U+007F.
  This is a defensible tightening for the agent-facing threat model; keep it if it does not disturb the goldens, otherwise document the decision.
- No behavior change to `Number` itself - it is correct; the fix is a test.

### Tests first

```
TestNumberCannotInjectStructure
  Assert Marshal(Object{{Key:"n", Value:Number("5\ninjected: true")}}) returns an error, and
  that a plain Number("5") round-trips. Pin the anchored-regex guarantee.

TestTableRejectsDuplicateColumns
  Assert encoding Table{Fields:["a","a"], Rows:[][]any{{1,2}}} returns an error.

(If L6 is taken) TestLineSeparatorsAreQuoted
  Assert a value containing U+2028 is quoted/escaped, not emitted raw.
```

### Acceptance

- `Number` structure-injection is pinned by a test that fails if the regex is loosened.
- Duplicate table columns error like duplicate object keys.
- `go test ./...` passes.

---

## PR6 - Small correctness cleanups (nice-to-have)

**Severity:** low. Land last; touches files other PRs edit.

1. **`cli.Money(math.MinInt64)` overflow** (`internal/cli/output.go:32-37`).
   `magnitude = -cents` overflows at `MinInt64`, producing a non-canonical string (`--92233720368547758.-8`); `ValidNumber` rejects it so `Render` errors rather than corrupts, but the boundary should not depend on that.
   Guard it: `if cents == math.MinInt64 { ... }` handle explicitly, or compute magnitude with `int64` care.
   Note the asymmetry - `transactionAmountToCents` already guards `MinInt64` at ingest, so the two boundaries should agree.
   Test: `Money(math.MinInt64)` returns a canonical `ValidNumber`.

2. **Liability skip over-count** (`internal/providers/plaid/normalize.go:96-170`).
   The same `account_id` malformed in `credit[]` and valid in `student[]` is both skip-counted and applied, so `SyncResult.Skipped` over-reports.
   De-dup: do not emit a skip for an account that also merges a valid liability.
   Count-only, low likelihood.

3. **Orphaned terms on liability type change** (`internal/core/ingest.go:671-680`).
   The credit and loan branches delete each other's obsolete terms rows; the default/skip branch deletes neither, so an account that changes from `credit_card`/`loan` to a non-terms type keeps a stale `credit_terms`/`loan_terms` row.
   Delete both obsolete terms rows in the skip branch too.

4. **Store-side defense-in-depth** (`internal/store/transactions.go:139,46-47`).
   `ListTransactions` treats `limit <= 0` as unbounded and applies date bounds without a format/order check - safe only because the CLI caller validates first.
   Optional: validate at the store boundary too, or document the caller contract in the doc comment.

5. **Soft-fail `PRODUCT_NOT_READY` on `/liabilities/get`** (`internal/providers/plaid/errors.go:56-67`).
   `liabilitiesUnavailable` already soft-fails five optional-product codes (`NO_LIABILITY_ACCOUNTS`, `PRODUCTS_NOT_SUPPORTED`, `PRODUCT_NOT_ENABLED`, `ACCESS_NOT_GRANTED`, `ADDITIONAL_CONSENT_REQUIRED`), but not `PRODUCT_NOT_READY`, which Plaid returns while a product's initial pull is still running shortly after link.
   Checked against the tree: this is **not a wedge** - the sync run fails with exit 1, the cursor is untouched, and a later retry succeeds once the product is ready - which is why it is a nice-to-have, not required.
   Fix: add `PRODUCT_NOT_READY` to the `liabilitiesUnavailable` switch so liabilities arrive on a later sync like the other optional-product cases; no retry loop (only add bounded retry if production is ever shown to need it).
   Test: gateway returns `PRODUCT_NOT_READY` from liabilities → `Sync` succeeds with empty liabilities (mirror the existing `liabilitiesUnavailable` test shape).

6. **README staleness** (`README.md:14`).
   "The hardening stack addresses the confirmed review findings before Phase 2 begins" predates the Phase 2 merge; update the sentence to reflect that Phase 2 is merged and this stack hardens it.
   `AGENTS.md` and `docs/product-spec.md` are otherwise current; no other doc drift found.

### Acceptance

- Each item has a focused test where behavior is observable (item 1, 3, 5).
- `go test ./...` passes.

---

## PR7 - Persist `login_required` so status exit 3 is real

**Severity:** medium (recommended tier, alongside PR4). Today `moneta status` exit 3 and its reconnect hint are effectively dead code.
**Anchors:** `internal/store/status.go:14-16` (the gap, self-documented), `cmd/moneta/status.go:82-83` (exit 3 read path), `internal/core/ingest.go:757-759` (success already resets `ok`), `internal/store/provider_items.go:60` (re-link resets `ok`), `internal/providers/plaid/errors.go:12` + `provider.go:83,110-111` (reauth classification precedent).

### Problem

Nothing in the product ever writes `provider_items.status = 'login_required'`.
The store layer says so itself (`status.go:14-16`: "'login_required' today can only come from an out-of-band write").
So when an Item needs reauth, `moneta sync` fails with exit 1 while `moneta status` still reports `ok` and exits 0 - the one signal built to drive "go re-link" never fires.
The read path is fully wired and waiting: `status.go:82-83` returns 3 when any item has `login_required`, `prioritizeAttention` keeps those rows visible under truncation, and the hint text names the fix.

Vocabulary, to prevent drift: the **stored status value** is `login_required`; **exit 3** means reconnection needed.
There is no TOON key named `reconnection_needed`; do not introduce one.

### Implementation

Three small pieces, none inside the ApplySync transaction:

1. **Classification helper** (`internal/providers/plaid/errors.go`).
   Export `IsLoginRequired(err error) bool`: true when the unwrapped `*APIError` code is `ITEM_LOGIN_REQUIRED` - the same code `Connections()` already maps to `login_required` (`provider.go:83,110-111`).
   If other codes are later deemed reauth-class, they join this helper so CLI and `Connections()` can never disagree.

2. **Store write** (`internal/store/provider_items.go`).
   Add `SetProviderItemStatus(ctx, db, provider, itemID, status string) error` - a single parameterized UPDATE in its own short transaction.
   Update the stale comment at `status.go:14-16`.

3. **Wire-up** (`cmd/moneta/main.go`, `syncItems` error branch).
   When `core.SyncProviderItem` fails and `plaid.IsLoginRequired(err)`, call `SetProviderItemStatus(..., "login_required")` before `continue`, and say so on stderr (institution + item id + "re-run moneta link", no token, no raw Plaid body).
   This runs after the failed sync returns, so it is naturally outside the rolled-back batch transaction.
   The reset path already exists and must be kept: a successful `ApplySync` writes `status = 'ok'` (`ingest.go:757-759`) and a re-link save does too (`provider_items.go:60`).

Error text and logs must not include tokens or raw Plaid payloads; `sanitizeSDKError` already strips them - keep it that way.

### Tests first

```
TestSyncPersistsLoginRequiredOnReauthError  (cmd/moneta/main_test.go)
  Fake provider whose Sync returns &plaid.APIError{Code: "ITEM_LOGIN_REQUIRED"} (via the builder).
  Run syncItems; assert provider_items.status == "login_required" and the run reports the item failed.

TestStatusExitsThreeAfterReauthPersist  (cmd/moneta/status_test.go)
  Seed an item, set status login_required via SetProviderItemStatus; assert moneta status exits 3.

TestSuccessfulSyncResetsStatusToOK
  Item with status login_required; a successful sync flips it back to ok (pins ingest.go:757-759).

Non-reauth failure guard: a sync failing with a different APIError code leaves status unchanged.
```

### Acceptance

- Reauth-class sync failure durably sets `login_required`; `moneta status` then exits 3 with the reconnect hint.
- Successful sync or re-link resets to `ok`.
- No tokens or raw provider bodies in output or storage.
- Optional follow-up (only after this lands): one line in `AGENTS.md` noting status exit 3 is live now that reauth is persisted.
- `go test ./...` passes.

---

## Reconciliation with the other reviews

Dispositions for Grok items not already absorbed above, so there is no silent disagreement between reviews.
Grok's review under-ranked the absent-account and pending+pending-id wedges and missed the `tx` excluded-aggregate; PR1/PR3 remain central and DP1 stands over its normalize-only empty-name fix.

| Item (Grok) | Disposition |
|------|--------|
| `PRODUCT_NOT_READY` soft-fail | Investigated against the tree: `liabilitiesUnavailable` (`errors.go:56-67`) omits it, but the failure is transient (exit 1, cursor untouched, retry succeeds), **not a wedge path**. Folded as PR6 item 5: one-line soft-fail + test, no retry loop. |
| Sandbox integration still bypasses `SyncProviderItem` | Tracked, deferred. Nice-to-have: point the sandbox test at the orchestrator path so it exercises decrypt→build→ApplySync. Not required for this stack. |
| `store.Open` creates a missing DB on typo'd read paths | Tracked, deferred. Read commands should eventually fail on a missing DB instead of creating an empty one; not a sync wedge; do not block PR1-PR3. |
| Extract doc builders / service layer for REST | Tracked, deferred. Prerequisite for the REST mirror stack, not this one. |
| Structured skip dump on `moneta sync` (`--verbose-skips`) | Optional add-on to PR4 only if the `skipped_records` table lands; not a new PR. |

| Item (Gemini, non-authoritative) | Disposition |
|------|--------|
| MinInt64 `Money` guard | Already PR6 item 1. Gemini over-called severity: real money never approaches MinInt64 and the failure is a loud render error, not corruption; the guard is for symmetry with the ingest-side check. |
| Exit-code inconsistency | Already PR2 (independently verified here on a built binary). |
| TOON "`.` in keys" / "quote NaN" changes | Dropped. Not verified against toon-format spec v3.3; the deep review checked quoting against the spec and found the current rules correct. Reopen only with a failing spec-grounded test. |
| "Ready for production" framing | Rejected. PR1's wedges are real, reproduced red/green. |

---

## Explicitly deferred (do not do in this stack)

| Item | Reason |
|------|--------|
| Loopback REST mirror | Deferred Phase 2+ surface; not a review finding. Its own stack. |
| `moneta tag` + provider-vs-user field ownership | Carried forward from Phase 1 D2. Belongs to the PR that adds `moneta tag`; needs a schema decision (`category_user_set`/`entity_user_set`). |
| `*int64` optional money refactor | Settled in Phase 1 D1. Contradicts binding `moneta-plan.md:231`; revisit at Phase 3 analytics. |
| Positional `Table` API misalignment (TOON) | Inherent to a positional API; callers build rows next to the field list. Static in shipped code. Not worth an API redesign now. |
| Rejecting `int32`/`uint64`/`float*` in TOON | Hard-fails safe; float rejection actively enforces the no-float-money invariant. Leave as-is. |
| Splitting `ingest.go` | Defer until a PR needs to; PR1 edits it in place. |

---

## Copy-paste agent prompt

> Read `AGENTS.md` and the binding rules in `docs/moneta-plan.md` first (cents, pending/posted, analytics exclusion).
> Execute `docs/phase2-review-fix-pr-plan.md`. This plan is the gate; do not treat any other review document (including any copy of `phase2_code_review.md`) as the gate.
> Required: PR1, PR2, PR3. Recommended: PR4, PR7 (persist login_required), PR5. Nice-to-have: PR6.
> PR1 is the priority - it closes the residual cursor wedges 2a left open; the design is fixed in DP1 (skip in `ingest`, keep infrastructure errors fatal).
> For every PR, write the regression test first, watch it fail on current code, then make it pass.
> Do not commit, push, or open a PR unless I explicitly ask.
> No new dependencies. Keep CGO-free. Fake data only in tests.
> Stop and report after each PR so I can verify before you continue.
