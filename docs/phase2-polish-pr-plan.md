# Phase 2 Polish PR Plan

Small hardening stack from the accepted Phase 2 full review (2026-07-23).
Do **not** start Phase 3 analytics work in this stack; that has its own plan (`docs/phase3-analytics-plan.md`).
Do **not** commit or push unless the maintainer explicitly asks.
No new dependencies.
Keep CGO-free.
Fake data only in tests and fixtures.

**Source:** Phase 2 full review of `a2cae31..52e5ab8`, verified against the tree; line numbers current as of 2026-07-23.
**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md`.

> **Read this first.**
> The review found no critical or high issues; this stack is polish, not firefighting.
> The one MEDIUM finding (M1, liability sign convention) is **deliberately NOT in this stack** - see [Explicitly excluded](#explicitly-excluded) below.
> Every item here is small, independent, and fully specified.

## Required

### P1. Group `spend` categories by `category_id`, not name

**Anchor:** `internal/store/spend.go:104-107,168,182`.
The by-category breakdown groups on the category *name* label.
The schema only enforces name uniqueness among roots and among siblings (`migrations/000001...up.sql:55-56`), so a root "Coffee" and a child "Coffee" under "Food" merge into one overstated row.
The overall summary total is unaffected; only the per-category split is wrong.

Fix: group by `categories.id` and carry the display name alongside (keep the current `ORDER BY spend_cents DESC` with the name as tiebreaker; add `id` as a final tiebreaker for determinism between same-named rows).
Do not change the output shape beyond possibly showing two rows where one merged row appeared before.

Test first (red on current code): seed a root category and a differently-parented child sharing one name, spend in both, assert two rows with the correct per-row sums.

### P2. Deduplicate the scaled-decimal formatter into `cli.Ratio`

**Anchor:** `cmd/moneta/cashflow.go:117-145` vs `internal/cli/number.go`.
`savingsRateNumber`/`formatScaledInteger` reimplement `cli.Ratio`/`scaledNumber` byte-for-byte (same `big.Int.Quo` truncation, same nil-on-non-positive-denominator).
Not a bug today; two copies of money-adjacent formatting will diverge silently.

Fix: delete the local copies and call `cli.Ratio(summary.NetCents, summary.InflowCents, 4)`.
Behavior must be identical: the existing cashflow golden/TOON tests must pass unchanged.
If any test output changes, that is a finding to report, not to paper over.

### P3. Warn when the REST API key is passed via `--api-key`

**Anchor:** `cmd/moneta/serve.go:38-42,62-65`.
A key passed as a flag is visible to other local users via `ps`/`/proc`; `MONETA_API_KEY` is the safe path.

Fix: keep the flag (compatibility), but when it is the source of the key, print one stderr warning:
`WARNING: --api-key is visible to other local users via the process list; prefer MONETA_API_KEY`.
Update the flag help text and the README serve section to name `MONETA_API_KEY` as the recommended mechanism.
Never print the key itself, in the warning or anywhere else.

Test: asserts the warning appears on stderr when `--api-key` is used and does not appear when the env var is used; asserts the key value is absent from all output.

## Optional

### P4. REST defense-in-depth: `WriteTimeout` + panic recovery

**Anchor:** `internal/api/server.go:120-124` (server config), `:71-82` (middleware chain).
Today only `ReadHeaderTimeout` and `IdleTimeout` are set, and there is no explicit recover middleware.
Impact is bounded (net/http recovers handler panics per-connection; the DB connection is released before the response write), so this is hardening, not a fix.

Add `WriteTimeout` (30s is reasonable for loopback reads) and a recover wrapper inside the auth chain that logs the panic value (never request bodies or headers) and returns the fixed 500 body.
Test: a handler that panics yields a 500 on that request while the server keeps serving subsequent requests.

### P5. Add the `Allow` header to 405 responses

**Anchor:** `internal/api/server.go:61-63`.
RFC 7231 compliance: the methodless fallback handler should set `Allow: GET, HEAD` before writing 405.
Test: assert the header on a POST to a known route.

## Explicitly excluded

**M1 (liability credit balances misreported as debt) was intentionally excluded from this stack and is resolved by Phase 3 PR1 (D3-1).**
Plaid documentation and its published Sandbox `/liabilities/get` fixture confirm that credit-card and loan current balances are positive when owed and negative when the institution owes the user.
Phase 3 PR1 makes that the canonical provider-boundary convention and removes the reader `abs()` behavior.
The exclusion below remains as the historical execution boundary for this completed Phase 2 plan.

## Copy-paste agent prompt

> Read `AGENTS.md` and `docs/moneta-plan.md` first.
> Execute `docs/phase2-polish-pr-plan.md`. Required: P1, P2, P3. Optional: P4, P5.
> Do NOT touch the liability `abs()` logic in networth.go/debts.go (M1 is explicitly excluded; it belongs to Phase 3).
> For P1 write the regression test first and watch it fail; for P2 the existing tests must pass unchanged.
> Do not commit, push, or open a PR unless I explicitly ask.
> No new dependencies. Keep CGO-free. Fake data only in tests.
> Stop and report after each item.
