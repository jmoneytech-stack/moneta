# AGENTS.md - Moneta

## Project Context

Moneta is a self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.
Single static Go binary, SQLite, pluggable providers (Plaid first), AXI-style CLI with TOON output, localhost REST mirror.
Full architecture: `docs/moneta-plan.md`.

## Current Goal

Phase 1 implementation and post-review hardening are complete.
Phase 2a-2j are done: the single-row poison skip, production `moneta sync` on the library path (PR #2), `moneta status` with the shared TOON/JSON output path (`internal/toon`, `internal/cli`), the `moneta accounts` / `moneta tx` / `moneta spend` / `moneta cashflow` / `moneta networth` / `moneta debts` reads, the authenticated loopback `moneta serve` JSON mirror, and GitHub Actions CI for build, vet, tests, CGO-free tests, and race tests.
The post-review hardening stack (`docs/phase2-review-fix-pr-plan.md`) is complete: the confirmed single-row ingest wedge paths are closed, CLI exit codes are uniform (usage = 2), the `tx` aggregate excludes transfers, skip counts persist per import run, reauth failures persist `login_required` so `moneta status` exit 3 is live, and the TOON encoder is hardened.
Phase 2 is complete.
Phase 3 is complete: the D3-1 liability-sign and D3-2 nullable-money foundation, PR3's compute-on-read `networth --history`, PR4-PR8's `mom`, `merchants`, `utilization`, `savings`, and heuristic-v1 `fixed-variable` trend metrics, PR9's credit-card-only `moneta cards`, and PR10's composed `moneta dashboard`; all reads have authenticated REST mirrors.
Loans stay in `moneta debts`; `moneta cards` is `credit_card` only.
The dashboard is the explicit `moneta dashboard` subcommand (R3(b)/B1) - bare `moneta` still prints usage and exits 2.
Its `upcoming_bills` slot follows the detector gate: `null` for `never_run`/`error`, and an honest empty or populated projection for `ok`/`partial`.
Its `anomalies` slot always contains the previous-complete-month anomaly projection with period, count, top three rows, and overflow skips.
A follow-up polish pass moved the dashboard document into `internal/report` so the CLI and REST payloads are one definition and cannot drift.
Phase 4 is complete through PR9: enriched merchant/card due-date ingestion, safe history replay, detector schema/state, the pure recurring detector, post-sync complete/partial persistence, transaction back-links, the `moneta recurring` / `/v1/recurring` read, the `moneta bills` / `/v1/bills` read with dashboard `upcoming_bills` population, the compute-on-read `moneta anomalies` / `/v1/anomalies` category-spike engine, and the final dashboard anomaly projection.
Both dashboard Phase 4 slots are now honest, so `phase4_note` no longer exists.
Do not begin the A2 `expense_class` taxonomy or any later feature until explicitly requested by the maintainer.

## Working Rules

- Inspect the live repository state and local instructions before making changes.
- Read this file and `docs/product-spec.md` before broad changes; check `docs/decisions/` before relitigating a settled choice.
- Preserve existing work and keep diffs focused on the requested task.
- Do not reset, discard, stage, commit, push, or deploy unrelated changes.
- Do not commit or push at all without an explicit request from the maintainer.
- Reproduce bugs through the user-facing workflow before implementing a fix when practical.
- Use existing project conventions and documented commands.
- Run focused tests, lint, and relevant end-to-end checks before handing off work.
- Report uncertainty, remaining risks, and any validation that could not be completed.
- No new dependencies without approval; the core stays CGO-free, with no third-party calls outside the Plaid provider and no telemetry.

## Privacy Rules (public repo)

- This repo is public and its history is permanent; never commit personal data: real transactions, balances, account names, institutions, custom categories, or email addresses.
- All private material lives in gitignored `.local/` or in the local database/config, never in the repo.
- Docs, examples, and test fixtures use the neutral default taxonomy and fake data only.
- Commits use the repo-local noreply git identity; verify with `git config user.email` before committing.
- Secrets (Plaid tokens, API keys, encryption keys) come from env vars only, are never logged, and never appear in code, fixtures, or docs.

## Important Files

- `README.md` - human-facing overview.
- `docs/product-spec.md` - product frame: MVP, current priority, non-goals.
- `docs/moneta-plan.md` - approved architecture: schema, provider interface, AXI commands, phases.
- `docs/decisions/` - ADRs; decisions future agents should preserve.
- `internal/report/` - documents whose CLI and REST payloads must stay identical (the dashboard today). Add a document here instead of writing a second builder in `internal/api`; most commands still keep a per-surface builder, which is fine while the two are allowed to differ.

## Done Means

- The phase deliverable runs locally (or the limitation is documented), with focused tests passing.
- README updated for whatever the phase added (setup, commands, usage).
- Changed files summarized with any remaining risk.

## Ignore For Now

- Investment holdings/positions (balance-level tracking only in v1).
- RocketMoney CSV importer implementation (Phase 2+; no stub exists yet).
- Plaid webhooks, whole-database encryption, multi-currency, brokerages.
- Human-facing web UI (roadmap item, post-v1; agent interfaces come first).
