# AGENTS.md - Moneta

## Project Context

Moneta is a self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.
Single static Go binary, SQLite, pluggable providers (Plaid first), AXI-style CLI with TOON output, localhost REST mirror.
Full architecture: `docs/moneta-plan.md`.

## Current Goal

Phase 1 implementation and post-review hardening are complete.
Phase 2a-2f are done: the single-row poison skip, production `moneta sync` on the library path (PR #2), `moneta status` with the shared TOON/JSON output path (`internal/toon`, `internal/cli`), and the `moneta accounts` / `moneta tx` / `moneta spend` / `moneta cashflow` reads.
The post-review hardening stack (`docs/phase2-review-fix-pr-plan.md`) is complete: the confirmed single-row ingest wedge paths are closed, CLI exit codes are uniform (usage = 2), the `tx` aggregate excludes transfers, skip counts persist per import run, reauth failures persist `login_required` so `moneta status` exit 3 is live, and the TOON encoder is hardened.
Next is the remaining AXI read surface (`networth`, `debts`, and friends per `docs/moneta-plan.md`), then the REST mirror.

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

## Done Means

- The phase deliverable runs locally (or the limitation is documented), with focused tests passing.
- README updated for whatever the phase added (setup, commands, usage).
- Changed files summarized with any remaining risk.

## Ignore For Now

- Investment holdings/positions (balance-level tracking only in v1).
- RocketMoney CSV importer implementation (Phase 2+; no stub exists yet).
- Plaid webhooks, whole-database encryption, multi-currency, brokerages.
- Human-facing web UI (roadmap item, post-v1; agent interfaces come first).
