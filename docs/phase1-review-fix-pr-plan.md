# Phase 1 Review Fix PR Plan

Hand-off for post-review hardening of Phase 1 Moneta.
Do **not** start Phase 2 CLI/REST work in this stack.
Do **not** commit or push unless the maintainer explicitly asks.
No new dependencies.
Keep CGO-free.
Fake data only in tests and fixtures.

**Source:** local code review of the Phase 1 working tree, reconciled against a verified multi-agent review (2026-07-17).
Every claim below was checked against the working tree; line numbers are current as of that reconcile.
**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md` (binding pending/posted and cents rules), `docs/decisions/`.

> **Read this first.**
> Three PRs from the original draft of this plan were dropped or narrowed because the code contradicts their premise.
> The maintainer has already ruled on both open questions; see [Decisions already made](#decisions-already-made-do-not-relitigate) and [Explicitly deferred](#explicitly-deferred-do-not-do-in-this-stack).
> Do not reinstate dropped work from memory of the earlier draft, and do not reopen a settled decision.
> Every PR below is fully specified: no maintainer input is required to execute this stack.

## Goals

1. Stop ledger corruption from fuzzy matching on native-ID (Plaid) transactions.
2. Stop the Link flow from minting unrecoverable orphan Plaid Items on browser disconnect.
3. Stop UTC date stamping from silently discarding balance snapshots Plaid cannot backfill.
4. Stop fabricating a zero current balance when Plaid reports a partial balance.
5. Align `AGENTS.md` and `docs/product-spec.md` with what Phase 1 actually implements.
6. Make a fresh database actually syncable: add the missing entity bootstrap, then a first-class load, decrypt, Sync, ApplySync path at the library level (CLI can wait for Phase 2).
7. Clear the confirmed dead code and duplication the review found, without changing behavior.

## Non-goals

- Full AXI CLI (`moneta sync` command) or REST API (Phase 2).
- RocketMoney CSV or manual provider implementations.
- Entity-rule engine (schema ships in Phase 1 by design; application is Phase 2+).
- Splitting `ingest.go` into multiple files (defer until Phase 2 touches it).
- Webhooks, multi-currency, holdings, whole-database encryption.
- Provider-vs-user field ownership on UPDATE (see Decision points; this is Phase 2 work).

---

## Decisions already made (do not relitigate)

Both open questions from the earlier draft are settled.
They are recorded here with their reasoning so a future agent does not reopen them.
Neither blocks any PR in this stack.

### D1. Optional money stays `int64`. Decided: defer the `*int64` refactor.

`docs/moneta-plan.md:231` is binding and reads:

> All monetary amounts are `int64` cents in SQLite and in every internal Go struct; no `float64` money anywhere in the core.

Changing `canon.Balance.AvailableCents` / `canon.Liability.LimitCents` to `*int64` would distinguish "unknown" from "zero", but it contradicts the letter of that rule and would need a doc amendment first.

**Decision: do not do it now.**
Nothing in the tree reads these columns.
`grep` for readers of `limit_cents` / `available_cents` / `apr` returns only the writes in `ingest.go`: no utilization math, no net-worth code, no division to blow up.
Phase 3 analytics is the natural forcing function and will show which columns actually need the distinction, so the refactor is cheaper and better-informed then.

`available_cents` and `limit_cents` keep writing `0` where Plaid omits them.
**PR4 fixes the one case that is harmful today** (a fabricated `current_cents = 0`) without touching `canon`.
Revisit at Phase 3; amend `moneta-plan.md:231` at that time.

### D2. `excluded` is sync-owned. Decided: no change to UPDATE ownership now.

The earlier draft assumed `excluded` is a user-owned override that re-sync must not clobber.
**The plan doc says the opposite, and the code matches the doc:**

- `moneta-plan.md:190` specs the Phase 2 command as `moneta tag <txn-id> [--entity] [--cat] [--note]`, with **no `--exclude` flag**.
- `moneta-plan.md:73`: transfer rows are "auto-excluded from spending analytics".
- `moneta-plan.md:284`: "Transfers and card payments excluded from analytics via `category.kind`."
- `ingest_test.go:284` asserts `isTransfer == 1 && excluded == 1` for a transfer category, codifying `excluded` as a derived cache of `category.kind`.

Dropping `excluded` from the UPDATE list would freeze a stale value, not protect a user edit.

The genuine collision is narrower: `updateTransaction` rewrites `category_id` (`ingest.go:458`) and `entity_id` (`ingest.go:452`), which is exactly what `moneta tag --cat` / `--entity` will write.
**Decision: leave current behavior alone.** Two reasons:

1. No user override can exist yet.
   `cmd/moneta/main.go` ships only `link` and `help`, and no code outside `ingest.go` writes `transactions`.
2. Freezing `category_id` on UPDATE has a real cost.
   Plaid commonly refines a category between pending and posted, so freezing on first insert would keep the pending-time guess forever.
   Freezing `category_id` while still deriving `is_transfer` / `excluded` from the fresh mapping would let those three drift out of sync.

**Carry-forward for Phase 2:** the PR that introduces `moneta tag` must decide whether a provider recategorization or a user edit wins, and per-column override flags (`category_user_set`, `entity_user_set`) are the likely answer.
That is a schema migration and belongs in that PR.
PR5 of this stack records the carry-forward in `moneta-plan.md` so it is not lost.

---

## PR graph

```text
PR5 (docs drift, code-free) ──► ship first, unblocks the next agent session

PR1 (fuzzy gate)          ──┐
PR2 (Link orphan)         ──┤
PR3 (balance date)        ──┼──► PR6 (entity bootstrap + orchestration) ──► PR7 (cleanup)
PR4 (partial balance)     ──┘
```

- **PR1 through PR5 are mutually independent** and can ship in any order or in parallel.
  They touch disjoint files.
- **PR6 lands after them** so the orchestrator walks an already-correct ingest path.
  It also closes the entity bootstrap gap, without which no fresh database can sync at all.
- **PR7 lands last.** It is pure cleanup and touches files the earlier PRs edit.
- **PR5 is code-free** and can ship immediately; it is the one that stops the next agent session from acting on a false status.

---

## PR1 - Gate fuzzy pending matching (blocker)

**Severity:** confirmed ledger corruption on the primary Plaid path. Money silently disappears.
**Anchor:** `internal/core/ingest.go:351`.

### Problem

In `findTransaction` the lookup order is:

1. Lookup by `ProviderTxnID`.
2. Lookup by `PendingTxnID`.
3. Exact `dedup_hash`, but **only** when `ProviderTxnID == ""` (line 335).
4. The plus/minus 3-day fuzzy pending match, gated **only** on `status == posted` (line 351).

Step 4 never checks `ProviderTxnID`, so a posted Plaid transaction that missed the ID lookups reaches the fuzzy query at line 354 and claims an unrelated pending row.
`docs/moneta-plan.md:244` is binding and says fuzzy matching is "a fallback, never the main mechanism", scoped to ID-less providers.

Reproduced failure:

- Pending charge `plaid-pending-A` is outstanding (2026-07-10, `-1200`, "Coffee Shop").
- A genuinely different second coffee posts directly as `plaid-posted-C` (2026-07-11, same amount and merchant, own `transaction_id`, no `pending_transaction_id`).
  It claims A's row via fuzzy match.
- Charge A later posts as `plaid-posted-A` carrying `PendingTxnID = "plaid-pending-A"` and ID-matches the same row.
- Result: `count(transactions) = 1`, `sum(amount_cents) = -1200`, and two distinct Plaid `transaction_id`s point at one row.
  Expected `2` and `-2400`.
  $12 of real spending is gone from every total.

### Implementation

**File:** `internal/core/ingest.go`, `findTransaction`, line 351.

Change the early return so a native-ID transaction never reaches the fuzzy query:

```go
if transaction.Status != canon.TxnStatusPosted || transaction.ProviderTxnID != "" {
	return 0, false, nil
}
```

> **Do not** use `ProviderTxnID == "" && PendingTxnID == ""` as the condition.
> The second clause is dead code: `upsertTransaction` already rejects `PendingTxnID != "" && ProviderTxnID == ""` at `ingest.go:253-255`, so `ProviderTxnID == ""` already implies `PendingTxnID == ""`.
> Adding it invites a future reader to think the two can vary independently.

When `ProviderTxnID != ""` and the ID lookup misses (orphan or stale ref), **insert a new row**.
Prefer a duplicate over a wrong merge: a duplicate is visible and fixable, a merge silently destroys money.

### Tests first (`internal/core/ingest_test.go`)

Add the regression **before** the fix and watch it fail (red, then green).

New test, name it distinctly from the existing `TestApplySyncKeepsDistinctNativeTransactionsWithIdenticalDetails` (line 155), which covers posted-vs-posted and already passes:

```
TestApplySyncDoesNotFuzzyMatchPostedTransactionWithNativeID

Batch 1: Added = [{ProviderTxnID: "plaid-pending-A", 2026-07-10, -1200, "Coffee Shop", pending}]
Batch 2: Added = [{ProviderTxnID: "plaid-posted-C", 2026-07-11, -1200, "Coffee Shop", posted, PendingTxnID: ""}]
Batch 3: Added = [{ProviderTxnID: "plaid-posted-A", 2026-07-12, -1200, "Coffee Shop", posted, PendingTxnID: "plaid-pending-A"}]

Assert after batch 3:
  count(transactions)   == 2
  sum(amount_cents)     == -2400
```

On current code this fails with `1` and `-1200`.

Keep green (all already pass, and the fix was verified not to disturb them):

- `TestApplySyncReplacesPendingWithPostedByNativeID` (line 14)
- `TestApplySyncUsesFuzzyFallbackForIDLessPendingTransition` (line 103) - the ID-less path has `ProviderTxnID == ""` and is unaffected
- `TestApplySyncKeepsDistinctNativeTransactionsWithIdenticalDetails` (line 155)

### Acceptance

- The fuzzy `SELECT` is unreachable whenever `ProviderTxnID != ""`.
- The new regression fails before the fix and passes after.
- `go test ./...` passes.

---

## PR2 - Detach Link completion from the request context (blocker)

**Severity:** confirmed. Mints a permanent, unrecoverable orphan Plaid Item.
**Anchor:** `internal/providers/plaid/link_server.go:255`.

### Problem

The completion handler passes the live request context straight into the token exchange:

```go
item, err := s.backend.CompleteLink(
	request.Context(),   // link_server.go:255
	input.PublicToken,
	input.Institution,
)
```

That ctx flows into `link.go:86` (`exchangePublicToken`) and on into `link.go:103` (`store.SaveProviderItem`).
If the browser disconnects between those two calls, the exchange has already spent the single-use public token and minted a permanent Item at Plaid, but the save fails with `context.Canceled` and the ciphertext is discarded.

Reproduced with a raw-socket client disconnecting mid-handler:
`exchangeCalls=1 (public token spent)`, `err=save provider item: context canceled`, `provider_items rows=0`.

The orphan is **unrecoverable**: `/item/remove` needs the access token that was never persisted, and re-linking mints a new Item rather than reclaiming it.
`ensureJSONEnd` at line 248 reads the body to EOF, which is exactly what arms net/http's disconnect detection, so the cancellation path is live before `CompleteLink` runs.
The window is roughly one second, while the page shows "Saving the connection locally...".

### Implementation

**File:** `internal/providers/plaid/link_server.go`, around line 255.

Run the exchange and persist on a context detached from request liveness, with its own timeout:

```go
completionCtx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 30*time.Second)
defer cancel()

item, err := s.backend.CompleteLink(completionCtx, input.PublicToken, input.Institution)
```

`context.WithoutCancel` requires Go 1.21+.
`go.mod` pins `go 1.26.0` / `toolchain go1.26.5`, so it is available.
No new dependency; both `context` and `time` are stdlib.

**Scope honestly.**
This cures the tab-close and navigate-away variants only.
It does **not** cure Ctrl+C: `link_server.go:171-178` calls `server.Close()`, which kills in-flight connections, and `main.go` exits the process regardless.
Do not claim otherwise in the commit message.
A full fix for the Ctrl+C variant means graceful shutdown that drains in-flight completions, which is out of scope here.

### Tests (`internal/providers/plaid/link_server_test.go`)

1. Client disconnects after the exchange succeeds but before the save completes.
   Assert `provider_items` has exactly one row, that is, the save survived the disconnect.
   Follow the existing raw-socket or `httptest` patterns in that file.
2. Existing Link tests stay green.

### Acceptance

- A disconnect during the save window leaves a persisted `provider_items` row, not an orphan.
- No token material is logged on any path.

---

## PR3 - Stamp balance snapshot dates in the account's calendar, not UTC

**Severity:** confirmed. Permanently discards balance history Plaid cannot backfill.
**Anchor:** `internal/providers/plaid/provider.go:254`.

### Problem

```go
balanceDate := canon.Date(p.now().UTC().Format("2006-01-02"))   // provider.go:254
```

This is the only `.UTC()` call in the repo, and it puts `balance_snapshots.date` on a different calendar from `transactions.date`, which arrives from Plaid in institution-local time.

A user in `America/Los_Angeles` syncing at 17:30 PDT on Jul 16 stamps `"2026-07-17"`.
Syncing again at 09:00 PDT on Jul 17 stamps the same key, and `upsertBalance`'s `ON CONFLICT (account_id, date) DO UPDATE` (`ingest.go:576`) overwrites it.
No `2026-07-16` row ever exists.

This is not an edge case.
UTC midnight lands at 14:00 to 20:00 local across every US zone, and `normalize.go:208` hard-rejects non-USD, so every user sits in a zone whose sync-day boundary bisects the evening.
Plaid exposes **no historical-balance API**, so every snapshot lost now is unrecoverable even after the code is fixed.
That is why this ships before Phase 3 rather than with it.

`provider_test.go:142` does not catch this: its injected `2026-07-17T23:00:00+08:00` yields the same date with or without `.UTC()`, so the assertion passes either way.

### Implementation

**File:** `internal/providers/plaid/provider.go:254`.

Stamp the date in the host's local wall clock rather than UTC.
`p.now` is already injectable (`provider.go:24`, defaulted to `time.Now` at `provider.go:67`), so the change is to drop `.UTC()`:

```go
balanceDate := canon.Date(p.now().Format("2006-01-02"))
```

**Decided: use the host's local zone. Do not add a timezone config field.**
Moneta is self-hosted, single-owner, and local-first per `docs/product-spec.md`, so the host's zone is the owner's zone.
An explicit `--timezone` flag would be more correct only if the host and the institution ever disagree, and nothing else in the tree needs that knob today.
Adding it now would violate the "no unrequested flexibility" rule in `~/.claude/CLAUDE.md`.
Revisit only if a real host/institution mismatch shows up.

Tests must inject `p.now` with an explicit zone rather than depending on the machine's `TZ`, so the suite is deterministic on any host.

### Tests (`internal/providers/plaid/provider_test.go`)

Replace or supplement the ineffective assertion at line 142 with a case that actually discriminates:

1. Inject `now` as 17:30 in a zone west of UTC (for example `America/Los_Angeles` on 2026-07-16).
   Assert the stamped date is `2026-07-16`, not `2026-07-17`.
   This fails on current code.
2. Evening sync then next-morning sync produce **two** `balance_snapshots` rows, not one.

### Acceptance

- An evening sync is stamped with the local calendar day.
- Two syncs straddling UTC midnight but inside one local day do not collide.

---

## PR4 - Do not fabricate a zero current balance

**Severity:** plausible, not yet proven against a live payload. Writes permanent wrong history.
**Anchor:** `internal/providers/plaid/provider.go:282`.

### Problem

```go
if account.Current == nil && account.Available == nil && account.Limit == nil {
	continue   // provider.go:282
}
```

The guard skips only the **all-nil** case.
With `Current: nil` and `Available: &1500.0`, it passes, and `optionalMoneyToCents(nil)` returns `0` (`normalize.go:215-217`), writing an asserted `current_cents = 0`.
A credit card then reads as fully paid off.

`balance_snapshots.current_cents` is `NOT NULL` (schema line 187), so **skipping the snapshot is the fix, not writing NULL.**

**Honest uncertainty:** the trigger is contract-allowed but unproven for this call path.
The Plaid SDK documents "current may be null. When this happens, available is guaranteed not to be null", which is exactly the shape that defeats this guard, but scopes that note to `/accounts/balance/get`, while `sdk_gateway.go:107` calls `AccountsGet`.
No test covers a partial-nil balance.
Fix it anyway: the change is one condition, and `balance_snapshots` is an append-only series that Plaid cannot backfill, so a fabricated row written today is permanent.

### Implementation

**File:** `internal/providers/plaid/provider.go:282`.

```go
if account.Current == nil {
	continue
}
```

A snapshot without a current balance carries no usable information: `current_cents` is the required column and the one net worth reads.

**Do not** change `canon.Balance` to `*int64` in this PR.
See [D1](#decisions-already-made-do-not-relitigate).
`available_cents` and `limit_cents` keep writing `0` for now; that is a separate, non-urgent decision with no reader today.

### Tests (`internal/providers/plaid/provider_test.go`)

1. Account with `Current: nil`, `Available: 1500.00` produces **no** balance row.
   This fails on current code, which emits `current_cents = 0`.
2. Account with all three nil still produces no balance row (existing behavior, keep green).
3. Account with a present current still produces its row.

### Acceptance

- No snapshot is written without a current balance.
- No fabricated `current_cents = 0` reaches the DB.

---

## PR5 - Docs drift (code-free, ship first)

**Severity:** confirmed. Actively misleads the next agent session.
**Anchor:** `AGENTS.md:11-12`.

### Problem

`AGENTS.md:12` states flatly:

> Design is approved; implementation has not started.

and `AGENTS.md:11` still names the Current Goal as Phase 1.
Both are false.
`README.md:11` says Phase 1 is complete, `docs/moneta-plan.md:3` says "Status: approved design, Phase 1 complete", and 38 Go files exist on disk.

`git diff HEAD -- AGENTS.md` proves the file **was** edited in this change set (em-dash fixes at lines 37-40, web-UI rewrite at line 53), so the status lines were missed rather than deliberately held.

This matters more than a typical doc nit: `README.md:36` ("start with `AGENTS.md`") and `AGENTS.md:17` make this file the mandatory entry path.
The next agent reads "implementation has not started", and re-scaffolds working packages or reports Phase 1 as the next task.
`AGENTS.md:12` is the **only** remaining "not started" claim in the repo, so nothing else hedges it.

### Changes

**`AGENTS.md`**

- Line 12: replace "Design is approved; implementation has not started." with the real status: Phase 1 implemented, under post-review hardening, not yet committed.
- Line 11: Current Goal becomes Phase 2 (AXI CLI + REST), or "Phase 1 hardening" while this stack is open.

**`docs/product-spec.md:18`**

- Stale in the same direction under "## Current Priority". Same correction.

**`docs/moneta-plan.md`**

- Package tree: mark `manual/` and `rmcsv/` as Phase 2+ and not present in the tree.
  Doc update only; do **not** add stubs.
- `entity_rules`: state plainly that the schema ships in Phase 1 and rule application is Phase 2+.
  This is **correct as-built**, not a gap.
  Phase 1 assigns accounts `DefaultEntityID` and transactions inherit the account entity.
  The table has no seed rows, no write path, and no CLI to author rules.
- Note the design tension for whoever implements rules later: the composite FK `(account_id, entity_id)` means a transaction cannot diverge from its account's entity without reassigning the account.
  Revisit before implementing `merchant_pattern` rules.

**Also record these, so a future agent does not have to rediscover them:**

- The four [architectural findings](#architectural-findings-not-bugs-document-in-pr5-resolve-in-phase-2) A1 through A4.
  A short subsection under the plan's architecture notes is enough: the finding, why it is inert today, and the moment it becomes expensive.
  Do **not** fix them; just write them down.
- The [D2](#decisions-already-made-do-not-relitigate) carry-forward: the Phase 2 PR that adds `moneta tag` must decide whether a provider recategorization or a user edit wins for `category_id` / `entity_id`, and per-column override flags are the likely answer.
- That Phase 1 is single-entity: `entities` has no seed rows, PR6 adds the bootstrap, and multi-entity routing arrives with `entity_rules` in Phase 2+.

### Acceptance

- No file claims implementation has not started.
- `AGENTS.md`, `README.md`, `moneta-plan.md`, and `product-spec.md` agree on the phase status.
- No false claim that entity rules or a CSV stub are live.
- A1 through A4 and the D2 carry-forward are written down in `moneta-plan.md`.

---

## PR6 - Sync orchestration API (library)

**Severity:** gap, not a bug. Land last.
**Anchor:** new file, suggested `internal/core/sync.go`.

### Problem

`store.SaveProviderItem` exists, but the full path (load ciphertext, decrypt, Plaid Sync, ApplySync, clear plaintext) is assembled only in `//go:build sandbox` tests via raw SQL.
That is easy to misuse, and the README implies an end-to-end path exists.

### Design

Add a small orchestrator, not a CLI command.

**`internal/store/`** - add the missing read side.
Only `SaveProviderItem` exists today (`provider_items.go:21`); there is no getter.

```go
func GetProviderItem(ctx context.Context, db *sql.DB, provider, itemID string) (ProviderItem, error)
```

Return `ItemID`, `Institution`, `AccessTokenEnc`, `SyncCursor`, and the numeric `id` that `SyncTarget.ProviderItemID` needs.

**`internal/core/sync.go`**

Match the **real** existing APIs, which the earlier draft of this plan got wrong:

- Decryption is `(*secret.Cipher).Open(envelope []byte) ([]byte, error)` (`secret.go:124`), constructed via `secret.FromEnvironment()` (`secret.go:46`). There is no `secret.Key` type.
- `ApplySync` is a **method** on `*Ingestor` (`ingest.go:43`), not a free function, and it returns **`error` only**. There is no `ApplyResult` type.
- `Provider.Sync(ctx, cursor) (*SyncBatch, error)` (`canon/provider.go:27`).

So either return plain `error`, or introduce an `ApplyResult` deliberately as part of this PR.
Do not assume it exists.

```go
// SyncProviderItem decrypts the access token, calls provider.Sync, applies the
// batch, and zeroizes the plaintext. Cursor CAS is handled inside ApplySync.
func SyncProviderItem(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	item store.ProviderItem,
	target SyncTarget,
	buildProvider func(accessToken string) (canon.Provider, error),
) error
```

**Blocker found during review: there is no way to create an entity.**
`SyncTarget` needs a `DefaultEntityID`, and `ingest.go:54` rejects anything `<= 0` while `ingest.go:159-167` validates the row exists.
But the migration seeds **zero** rows into `entities`, and `grep "INSERT INTO entities"` finds hits **only in test files** (`ingest_test.go:390`, `provider_test.go:191`, `sandbox_integration_test.go:113`, `store_test.go:262`), every one of them raw SQL.
So on a fresh database `ApplySync` can never succeed: `moneta link` writes a `provider_items` row, and there is no supported way to reach a first sync.
Every green test today passes because it hand-inserts an entity the product cannot create.

This PR must close that gap, or the orchestrator it adds is unusable.

**Decided:** add an entity bootstrap to `internal/store/`, and have the orchestrator resolve the entity rather than taking it on faith.

```go
// EnsureDefaultEntity returns the id of the single personal entity, creating it
// if the database has none. Phase 1 is single-entity; multi-entity routing is
// Phase 2+ (see entity_rules in moneta-plan.md).
func EnsureDefaultEntity(ctx context.Context, db *sql.DB) (int64, error)
```

The `entities` DDL is `kind TEXT CHECK (kind IN ('personal','business'))`, `name TEXT CHECK (name <> '')`, `UNIQUE (kind, name)`, so a deterministic seed such as `('personal', 'Personal')` is safe and idempotent under `INSERT ... ON CONFLICT DO NOTHING`.

Do **not** put a `default_entity_id` column on `provider_items` in this PR.
That is real multi-entity design and belongs with `entity_rules` in Phase 2.
Keep `SyncTarget.DefaultEntityID` as the parameter it already is, and let the caller pass what `EnsureDefaultEntity` returns.

Requirements:

1. Decrypt the access token in memory.
2. `defer` zeroing the plaintext byte slice.
   `Open` returns a fresh `[]byte`, so the caller can wipe it.
3. Call `Provider.Sync(ctx, cursor)`.
4. Call `ApplySync` with the returned batch and the expected cursor.
5. Surface `ErrCursorChanged` (`ingest.go:18`) unwrapped so callers can retry.
   It already exists; do not define a second sentinel.
6. Never log token material.

Unit-test with a fake `canon.Provider` and a temp SQLite DB, following the existing store and core test patterns.
Do **not** require live Plaid for unit tests.
Keep the sandbox integration test as an optional smoke test; having it call the helper instead of raw SQL is a nice-to-have in the same PR.

### Failed `import_runs`, folded in here

The earlier draft proposed a separate PR to write `status = 'failed'` audit rows.
**The current behavior is correct and needs no fix in `ingest.go`.**
The `'succeeded'` insert at `ingest.go:761` is deliberately inside the tx: a row asserting `transactions_added` counts must be atomic with the rows it counts, or a rolled-back batch would leave a `'succeeded'` row claiming writes that never landed.

The real gap is that nothing owns failure recording, because no orchestrator existed.
This PR creates that owner.
**If** you want failure audit now, add it here: after `ApplySync` returns an error, insert `import_runs` with `status = 'failed'` and a redacted `error_detail` on a **separate** short transaction outside the rolled-back one.
Never include token material or raw account identifiers in `error_detail`.
Otherwise leave it for Phase 2 and note it in the plan.

### Acceptance

- A fresh database can reach a first sync without hand-written SQL.
- A library sync path exists and is tested without network access.
- Plaintext tokens are zeroized on every exit path.
- At least one test drives the path against a database bootstrapped **only** by product code, proving the entity gap is closed.
- README can point to this as the supported sync entrypoint for agents and the Phase 2 CLI.

---

## PR7 - Mechanical cleanup (no behavior change)

**Severity:** none. Quality only.
**Why last:** every item is confirmed dead or duplicated by grep, but several touch files PR1 through PR6 edit.
Land after them to avoid merge pain.

Each item below is independently verifiable and should compile with **zero** test changes.
If any item requires touching a test assertion, stop: it is not mechanical, and it does not belong in this PR.

| # | File | Change |
|---|------|--------|
| 1 | `internal/core/dedup.go:33` | Delete the local `hashWriter` interface; it is a verbatim redefinition of `io.Writer`. Its only argument is `sha256.New()`, a `hash.Hash`, which already satisfies `io.Writer`. Change `writeHashField` to take `io.Writer`. |
| 2 | `internal/providers/plaid/provider.go:58` | Replace the hand-rolled access-token check with a call to the package-local `validateOpaqueToken("access token", accessToken)` (`link.go:120`). Verified semantically identical except for a 4096-byte cap that no real token approaches. |
| 3 | `internal/providers/plaid/link.go:121` | Drop the dead `strings.TrimSpace(token) != token` clause from `validateOpaqueToken`; the adjacent `strings.IndexFunc(token, unicode.IsSpace) >= 0` already subsumes it, since `TrimSpace` trims exactly the runes where `unicode.IsSpace` is true. |
| 4 | `internal/providers/plaid/errors.go:21` | Delete `APIError.Type` and `rawItem.ErrorType` plus their assignments (`errors.go:43`, `sdk_gateway.go:194`). Both are written and never read; `Code` / `ErrorCode` drive every decision. Re-add when a caller needs them. |
| 5 | `internal/providers/plaid/normalize.go:221` | Inline `accountLimitToCents`; its body is exactly `return optionalMoneyToCents(account.Limit)` and it has one caller (line 87). |
| 6 | `internal/providers/plaid/provider.go:118` | Delete the `connectionStatus` method and inline field-named `canon.ConnectionStatus{...}` literals at its three call sites (lines 86, 106, 114). It never touches its receiver, and it reorders four same-typed string params `(state, detail, itemID, institution)` against a struct declared `{ID, Institution, State, Detail}`, so transposing two arguments compiles and silently produces wrong output. |
| 7 | `internal/providers/plaid/link.go:104` | Replace the bare `"plaid"` literal with a package-level `const providerName = "plaid"`, also returned by `Provider.Name()` (`provider.go:72`). This string is the join key across `provider_items.provider`, `accounts.provider`, `txn_provider_refs.provider`, and the 16 seeded `category_mappings` rows; if the two literals ever drift, `category()` (`ingest.go:721`) silently returns `sql.ErrNoRows` and every transaction ingests uncategorized instead of failing loudly. |
| 8 | `internal/store/provider_items_test.go:11,76` | Use the existing `openTestDB(t)` helper (`store_test.go:242`, same package, `t.Helper()`-marked) instead of inlining its body twice. The inline copies also lose `t.Helper()`, so failures report the wrong line. |

### Acceptance

- `go build ./... && go vet ./... && go test ./...` green with **no test file edited** except item 8.
- No behavior change; the diff is deletions and call-site swaps only.

---

## Architectural findings (not bugs; document in PR5, resolve in Phase 2)

These four surfaced during review.
None is wrong today, and none blocks this stack.
Each one becomes expensive at a specific, predictable moment, so PR5 records them in `docs/moneta-plan.md` rather than leaving them for a future agent to rediscover.
**Do not fix them in this stack.**

### A1. `provider_items` requires an encrypted token, which token-less providers cannot supply

`provider_items.access_token_enc` is `BLOB NOT NULL CHECK (length(access_token_enc) > 0)` (schema line 15), and `ingest.go:51` hard-requires `ProviderItemID > 0` while `loadAccount` (`ingest.go:693`) filters `AND provider_item_id = ?`.
The plan lists `manual` and `rmcsv` as v1 providers behind the same `canon.Provider` contract, and **neither has an access token**.
To ingest one CSV row, `rmcsv` would have to fabricate a synthetic item with dummy ciphertext, defeating the point of `internal/secret`, or ship a migration relaxing the constraint.
The schema already hints at the tension: `accounts.provider_item_id` is nullable (line 27), so item-less accounts are representable but unreachable, and `upsertAccount`'s `WHERE accounts.provider_item_id = excluded.provider_item_id` (`ingest.go:212`) can never match a NULL.

**Becomes expensive when:** the first token-less provider lands.
**Likely resolution:** move the credential to a `provider_credentials` table keyed by `provider_item_id`, or make the column nullable, leaving `provider_items` as the connection-scoped cursor and status record that core actually uses.

### A2. `canon.Capability` is declared but core never reads it, while the property core does branch on has no bit

`Capabilities()` is on the interface (`canon/provider.go:25`), implemented (`provider.go:75`), and asserted in tests, but **no non-test code ever calls it**.
Meanwhile the one property core actually branches on, native IDs versus id-less hash matching, is inferred ad hoc from a data shape: `if transaction.ProviderTxnID == ""` (`ingest.go:335`, and the gate PR1 fixes at line 351).
A provider whose IDs are not stable across pulls (the RocketMoney CSV re-export case: same transaction, new row id each export) cannot opt out of ID dedup without a new branch inside `findTransaction`, and a provider that populates IDs on only some rows silently switches strategy mid-batch.

**Becomes expensive when:** `rmcsv` lands, or any provider needs a different dedup strategy.
**Likely resolution:** a `CapNativeTxnIDs` bit or a `DedupStrategy()` method on the contract, read once in `newSyncWriter` next to the provider name.
PR1 makes the current inference correct, which is the right move now; A2 is about making it *declared* rather than inferred.

### A3. `canon.Liability` has no kind, so core re-derives it from account type and two schema columns are unreachable

`normalizeLiabilities` (`normalize.go:86-162`) iterates Plaid's distinct `Credit` / `Student` / `Mortgage` lists, then flattens all three into one `canon.Liability` carrying no kind field.
`upsertLiability` (`ingest.go:611`) therefore guesses the kind from `accounts.type` and hard-fails the whole batch (`default:` at line 660) if a liability ever arrives on an investment or asset account, for example a margin loan or a HELOC.
The flattening also makes `loan_terms.origination_cents` and `loan_terms.maturity_date` (schema lines 169-170, both present on Plaid's mortgage payload) permanently unreachable, since no canon field carries them.

**Becomes expensive when:** a mortgage or HELOC shows up, or Phase 3 wants loan amortization.
**Likely resolution:** a kind field on `canon.Liability`, or separate `CreditTerms` / `LoanTerms` slices on `SyncBatch`, so core routes on a declared kind instead of a correlated proxy.

### A4. `balance_snapshots.currency` is a hardcoded literal

`upsertBalance` writes `'USD'` as a SQL literal (`ingest.go:575`) while `accounts.currency` and `transactions.currency` are populated from the provider through `canonicalCurrency` (`ingest.go:791`).
`canon.Balance` has no currency field at all, so a provider cannot report it even if it wanted to.
The column is decorative and always says USD.

**Becomes expensive when:** multi-currency lands.
The fix is not one literal: it is discovering that balance rows never carried a real currency and that net worth has been summing across them.
**Likely resolution:** carry currency on the `accountRecord` that `loadAccount` (`ingest.go:688`) already scans, or drop the column so the account is the single source of currency truth.
Inert today because `normalize.go:208` rejects non-USD at the boundary.

---

## Explicitly deferred (do not do in this stack)

Everything the review surfaced that is **not** a PR above is listed here, so nothing is silently dropped.

| Item | Reason |
|------|--------|
| Optional money as `*int64` | Settled in [D1](#decisions-already-made-do-not-relitigate). Contradicts binding rule `moneta-plan.md:231`; no reader exists yet. Revisit at Phase 3. |
| Provider vs user field ownership on UPDATE | Settled in [D2](#decisions-already-made-do-not-relitigate). `excluded` is sync-owned per the plan doc; the real `category_id` / `entity_id` question belongs to the Phase 2 PR that adds `moneta tag`. |
| Failed `import_runs` as its own PR | Current atomicity is correct by design. Folded into PR6, which creates the component that would own it. |
| Full entity_rules engine | Correct as-built for Phase 1: no seed rows, no write path, no CLI to author rules. Design tension with the composite FK; document only (PR5). |
| Token-less providers blocked by `access_token_enc NOT NULL` | See [A1](#a1-provider_items-requires-an-encrypted-token-which-token-less-providers-cannot-supply). Schema migration; blocks `rmcsv` / `manual`, not this stack. |
| Dedup strategy inferred, not declared | See [A2](#a2-canoncapability-is-declared-but-core-never-reads-it-while-the-property-core-does-branch-on-has-no-bit). PR1 makes the inference correct; declaring it is Phase 2. |
| `canon.Liability` has no kind field | See [A3](#a3-canonliability-has-no-kind-so-core-re-derives-it-from-account-type-and-two-schema-columns-are-unreachable). Contract change; `loan_terms.origination_cents` / `maturity_date` stay unreachable until then. |
| Balance insert hardcoded `'USD'` | See [A4](#a4-balance_snapshotscurrency-is-a-hardcoded-literal). Inert while `normalize.go:208` rejects non-USD. Revisit with multi-currency. |
| Prepared statements in the ingest loop | `ingest.go` passes raw SQL strings per row, and modernc.org/sqlite's legacy `Execer`/`Queryer` recompiles and finalizes each one. ~4 statements per row = ~40k compile cycles per 10k-transaction sync; benchmarked 2.79x slower than preparing once (293ms vs 105ms per 10k rows on 2 statements). Throughput only, no correctness impact, and no `sync` command exists yet to feel it. Revisit when Phase 2 ships sync and volume is real. |
| `NormalizeMerchant` recomputed 2-3x per transaction | `ingest.go:272` computes it, `DedupHash` recomputes it (`dedup.go:29`), and `findTransaction:367` recomputes it again instead of taking the value 95 lines above. ~4ms and ~4.5MB of garbage per 10k-transaction sync. Trivial fix (pass `merchantNormalized` down), but it touches `findTransaction`, which PR1 edits. Fold into the `insertTransaction`/`updateTransaction` consolidation below. |
| `insertTransaction` / `updateTransaction` duplication | `ingest.go:416-427` and `466-477` are byte-identical 12-value blocks; the update path only appends `transactionID`. Every new column must be added in four places. Real maintenance hazard, but PR1 and PR6 both touch this file, and the fix needs a shared `transactionValues(...) []any` helper, so it is not mechanical enough for PR7. Do it as a follow-up once this stack lands. |
| Sequential Plaid `/accounts/get` + `/liabilities/get` | `provider.go:138` serializes two independent round trips, roughly 38% of a typical incremental sync's wall clock. The idiomatic fix is `errgroup`, but `golang.org/x/sync` is **not** a dependency and this stack forbids new ones. A stdlib `sync.WaitGroup` version is doable; deferred as pure latency with no correctness impact. |
| Credit / student liability loop duplication | `normalize.go:86-116` and `117-142` run the same four-conversion sequence over identically-named fields, ~25 duplicated lines. Fixing it well means an embedded `rawLiabilityCommon` struct, which overlaps [A3](#a3-canonliability-has-no-kind-so-core-re-derives-it-from-account-type-and-two-schema-columns-are-unreachable). Do both together in Phase 2, not piecemeal now. |
| `dateDay` / `canonicalPlaidCurrency` duplicate core helpers | `normalize.go:237` reimplements `core.validateDate` down to a byte-identical error string, and `normalize.go:200` reimplements `core.canonicalCurrency`. The right home is `canon` (both packages import it, and `canon.Date` already declares the contract in its doc comment but carries no behavior). Deferred because moving validation into `canon` is a design change, not a cleanup, and it collides with [A3](#a3-canonliability-has-no-kind-so-core-re-derives-it-from-account-type-and-two-schema-columns-are-unreachable) / [A4](#a4-balance_snapshotscurrency-is-a-hardcoded-literal). |
| Down migration leaves its `schema_migrations` row | Unreachable from code: `upMigrations()` filters on `.up.sql` (`migrations.go:61`) and no down-runner exists. Only bites a developer hand-running the file via the sqlite3 CLI, which then makes `store.Open` return a healthy handle over an empty schema. A note for whoever adds a down-runner (mirror the INSERT at `migrations.go:118-124` with a DELETE), not a fix to the SQL file. |
| Split `ingest.go` | Nit; split when Phase 2 touches it. |
| Concurrent multi-writer stress | `MaxOpenConns=1` today; revisit if that changes. |
| manual/rmcsv implementations | Later phases; blocked on [A1](#a1-provider_items-requires-an-encrypted-token-which-token-less-providers-cannot-supply) and [A2](#a2-canoncapability-is-declared-but-core-never-reads-it-while-the-property-core-does-branch-on-has-no-bit) anyway. |

---

## Test and quality bar (every PR)

```bash
go build ./... && go vet ./... && go test ./...
```

- Every correctness PR here (PR1 through PR4) has a **red/green** regression: write the test, watch it fail, then fix.
  A fix without a failing-first test does not close the item.
- Prefer table-driven tests next to the existing ones in `*_test.go`.
- Fake institutions and merchants only (`Test Bank`, `Coffee Shop`, and similar).
- No secrets, real tokens, or personal data in fixtures.
- No em dashes in any file, including Go and SQL comments (`~/.claude/CLAUDE.md`).
- Do not edit the auto-generated CHANGELOG.
- Do not commit unless the maintainer asks; if committing, use the repo-local noreply identity (`git config user.email`).

---

## Suggested execution order

1. **PR5** (docs) first.
   Code-free, zero risk, and it stops the next agent session from acting on a false status.
2. **PR1** (fuzzy gate) alone.
   Highest severity; red/green regression first.
3. **PR2** (Link orphan) and **PR3** (balance date).
   Independent of PR1 and of each other; can run in parallel branches.
4. **PR4** (partial balance).
   One-line fix, cheap.
5. **PR6** (entity bootstrap + orchestration), once ingest is correct.
6. **PR7** (cleanup) last, once the files have stopped moving.

D1 and D2 are already settled; see [Decisions already made](#decisions-already-made-do-not-relitigate).
Nothing in this stack needs maintainer input to execute.

---

## Definition of done for the whole stack

- [ ] Fuzzy matching never claims a row for a transaction carrying a native `ProviderTxnID`; regression test locked.
- [ ] A browser disconnect during Link completion leaves a persisted item, not an orphan at Plaid.
- [ ] Balance snapshots are stamped in the local calendar; an evening sync and the next morning's sync do not collide.
- [ ] No balance snapshot is written without a current balance.
- [ ] `AGENTS.md`, `README.md`, `moneta-plan.md`, and `product-spec.md` agree Phase 1 is implemented and entity_rules application is deferred.
- [ ] `moneta-plan.md` records the four architectural findings (A1 to A4) and the D2 Phase 2 carry-forward.
- [ ] A fresh database can reach a first sync using only product code; no test hand-inserts an entity to make the path work.
- [ ] Documented library path: load item, decrypt, Sync, ApplySync, zeroize plaintext.
- [ ] Confirmed dead code and duplication from PR7 removed, with no behavior change.
- [ ] `go build ./... && go vet ./... && go test ./...` green.
- [ ] No new deps; CGO-free; privacy rules intact.

---

## Copy-paste prompt for an implementing agent

```text
Implement the Phase 1 fix PR plan for Moneta.

Plan file: docs/phase1-review-fix-pr-plan.md

Read AGENTS.md and the binding rules in docs/moneta-plan.md first.
Read "Decisions already made" in the plan: D1 (optional money stays int64) and D2
(excluded is sync-owned) are SETTLED. Do not reopen them, and do not reinstate work
an earlier draft proposed. Every PR is fully specified; you should not need to ask me
anything to execute it.

Start with PR5 (docs drift) - it is code-free and unblocks future agent sessions:
- AGENTS.md:12 still says "Design is approved; implementation has not started." That is false.
- AGENTS.md:11 still names Phase 1 as the Current Goal.
- docs/product-spec.md:18 is stale the same way.
- Fix per the PR5 section. No code changes.

Then PR1 (the blocker):
- Write the regression test FIRST in internal/core/ingest_test.go and confirm it FAILS.
  Name it TestApplySyncDoesNotFuzzyMatchPostedTransactionWithNativeID.
  Three batches, exact values in the PR1 section. Expect count(transactions)==2 and
  sum(amount_cents)==-2400; current code gives 1 and -1200.
- Then gate the fuzzy match at internal/core/ingest.go:351 so it cannot run when
  ProviderTxnID != "". Use the exact condition in the PR1 section; do NOT add a
  PendingTxnID clause (it is dead code, see the note there).
- Run: go build ./... && go vet ./... && go test ./...

Stop after PR1 and report. Do not commit unless I ask. No new dependencies.
After I approve, proceed to PR2, PR3, PR4, PR6, then PR7 as in the plan.

Note on PR6: review found that `entities` has no seed rows and no production creation
path, so a fresh DB cannot sync at all - every green test hand-inserts an entity via
raw SQL. PR6 must close that gap, not just add the orchestrator.
```
