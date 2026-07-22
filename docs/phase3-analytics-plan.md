# Phase 3 Analytics Plan

Planning document only.
No Phase 3 implementation starts until the maintainer explicitly prioritizes it.
Scope follows `docs/moneta-plan.md` build phase 3 ("Analytics views"); recurring and anomaly detection are build phase 4 and are **not** in this plan.

**Constraint docs:** `AGENTS.md`, `docs/moneta-plan.md` (binding: int64 cents, analytics exclusion via `excluded`, TOON/AXI output conventions, REST mirrors reads).
**Inputs:** the Phase 2 full review (M1 carried here), the Phase 1 D1 carry-forward (`optional money as *int64`, parked "revisit at Phase 3"), and the Phase 2 architecture carry-forwards in `moneta-plan.md:182-197`.

## Scope

Per `moneta-plan.md:214-224` and the analytics list at `:252-253`:

1. `moneta trends --metric mom|merchants|utilization|savings|fixed-variable` - month-over-month category trends, top merchants, average daily spend, fixed vs variable split, savings rate, credit utilization trend.
2. `moneta networth --history 90d` - historical net worth series over `balance_snapshots` (current-value `networth` shipped in Phase 2).
3. `moneta` (bare) - the content-first dashboard: net worth, cash, utilization, upcoming bills, sync health, anomaly count (anomaly count reads as 0/absent until phase 4).
4. `moneta cards` - card-focused view (utilization, APR, due dates); largely a projection of the Phase 2 `debts` data.
5. REST mirror: each new read gets a `/v1/...` endpoint under the existing auth/loopback server, same handler conventions as Phase 2.

Everything is read-only over data sync already writes.
No new provider work, no new write paths, no schema changes except where a Decision below requires one.

## Decisions to resolve before or during Phase 3

### D3-1. Liability balance sign convention (M1 from the Phase 2 review)

**Problem.** `internal/store/networth.go:150-152` and `internal/store/debts.go:101-103` take `abs()` of liability balances because the stored data has a mixed sign convention (Plaid `current` is stored as-is at `provider.go:381`; existing fixtures show a loan at `-100000` and a card at `+300000`).
`abs()` normalizes owed balances but also flips a genuine credit balance (institution owes the user, e.g. an overpaid card at `-5000`) into positive debt: net worth swings $100 the wrong way on a $50 credit.

**Decision required.** Define the canonical stored convention for liability `current_cents`, then remove the `abs()` normalization:

- Option A (recommended): normalize at the ingest boundary - liability-type accounts store `current_cents` positive-when-owed, negative-when-in-credit, using per-provider knowledge of each source's convention (Plaid liabilities report owed balances as positive `current`).
  Readers then trust the sign: positive adds to liabilities, negative adds to assets.
  Requires a one-time backfill migration for existing snapshots plus the ingest change.
- Option B: keep provider-as-is storage and push interpretation to every reader.
  Rejected by default: it spreads provider knowledge across all analytics and the next reader reintroduces `abs()`.

**Interacts with:** the balance-currency carry-forward (`moneta-plan.md:192-193`) and the liability-kind carry-forward (`:190-191`); resolve the sign convention in the same design pass even if the currency work stays deferred.

**Product behavior until decided (explicit):** liability balances are reported as positive owed magnitudes; a credit balance on a liability is knowingly counted as debt in `networth` and `debts`.
This is documented in `docs/phase2-polish-pr-plan.md` as excluded from the polish stack; no code touches the `abs()` until this decision lands.

### D3-2. Optional money: zero vs unknown (Phase 1 D1, now live)

Phase 1 deferred the `*int64` refactor because nothing read `limit_cents`/`available_cents`.
That is no longer true: `debts.go:66` reads `credit_terms.limit_cents` for utilization, and the Phase 3 utilization *trend* reads `balance_snapshots.limit_cents` per day.
Today a missing limit is stored as `0`, and `cli.Ratio` nil-guards a non-positive denominator - the right display outcome for the wrong reason ("no limit reported" and "zero limit" are indistinguishable).

**Decision required:** either move the affected canon/store fields to nullable (`*int64` / nullable column) with a doc amendment to the binding rule at `moneta-plan.md:268` ("all monetary amounts are `int64` cents" gains an "optional money is nullable, never sentinel-zero" clause), or explicitly bless `0 = unknown` as the permanent convention and document it where utilization is computed.
Recommendation: nullable, decided at the start of Phase 3, because the utilization trend is the first consumer that would bake the sentinel into stored history.

### D3-3. Precompute at sync vs compute on read

`moneta-plan.md:252-254` says analytics are "precomputed at sync, not derived by the agent" and net worth snapshots are computed at sync.
Phase 2's reads (`spend`, `cashflow`, `networth`) compute on read and are fast at personal-finance scale.

**Decision required:** whether Phase 3 introduces a precompute step (tables written during `moneta sync`) or continues computing on read and amends the plan line.
Recommendation: compute on read for everything except the daily net-worth snapshot (which is genuinely time-dependent state, not a view), and amend the plan wording; introduce precompute tables only if a measured query is slow.
This keeps sync simple and avoids stale-view invalidation logic.

## Explicitly out of scope for Phase 3

- Recurring detection, anomaly detection, `moneta recurring`, drift flags - build phase 4.
- `moneta tag` and the provider-vs-user field ownership decision (Phase 1 D2 carry-forward) - belongs to the PR that adds `tag`; it can be prioritized alongside Phase 3 but is not part of this plan.
- Manual and rmcsv providers, `moneta add`, token-less provider-item storage.
- Multi-currency, webhooks, holdings, web UI, whole-database encryption.

## Sequencing sketch (not a commitment)

1. Resolve D3-1 and D3-2 (design + doc amendments + any migration).
2. `networth --history` (smallest new read; exercises the D3-1 outcome end to end).
3. `trends` metrics, one `--metric` at a time, each with store query + CLI + REST + tests.
4. `cards`, then the bare `moneta` dashboard last (it composes the others).
5. Full review before merge, same gate as Phases 1-2.
