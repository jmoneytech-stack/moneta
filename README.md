# Moneta

A self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.

Moneta ingests financial data from pluggable providers (Plaid first), normalizes it into a canonical model in SQLite, and exposes it through a token-efficient, agent-ergonomic CLI following the [AXI](https://github.com/kunchenguid/axi) design principles with [TOON](https://github.com/toon-format/toon) output.
A small localhost REST API mirrors the same operations for non-CLI consumers.

## Status

Pre-alpha, building in public.
Phase 1 and Phase 2 are merged: the Go module, canonical provider contract, SQLite schema, transactional sync ingestion, AES-GCM secret encryption, Plaid sync integration, and the runnable Plaid Link loopback flow exist.
The Link flow creates and exchanges Plaid tokens, encrypts permanent access tokens before SQLite persistence, and rejects every non-loopback bind.
The complete Link, transactions, balances, liabilities, encrypted persistence, and atomic ingestion path has been verified against Plaid Sandbox.
The post-review hardening stack in `docs/phase2-review-fix-pr-plan.md` closes the confirmed single-row ingest wedges, aligns CLI exit codes, excludes transfers from the `tx` aggregate, persists skip counts and reauth state, and hardens the TOON encoder.
The `moneta link` and `moneta sync` commands run the connection and sync flows.
`moneta status`, `moneta accounts`, `moneta tx`, `moneta spend`, `moneta cashflow`, and `moneta networth` are the first AXI reads, emitting TOON for agent consumers; the remaining AXI reads and the REST API are next.
The approved design lives in [docs/moneta-plan.md](docs/moneta-plan.md) and the reasoning behind key choices in [docs/decisions/](docs/decisions/).

## Principles

- Local-first: your data stays on your machine, except outbound calls to Plaid, which are isolated inside the Plaid provider.
- Agent-first: pre-computed aggregates, compact TOON output, grep-friendly lines, definitive empty states, no interactive prompts.
- Harness-agnostic: any AI agent that can run shell commands (or call the REST API) can consume Moneta; no agent-framework lock-in.
- Pluggable providers: no provider-specific fields leak into the core model; imports are idempotent with transaction-level dedup.
- Single static Go binary, CGO-free, minimal dependencies, no telemetry.

## Planned build phases

1. Core schema + provider interface + Plaid provider (Link flow, transactions sync, liabilities) against Sandbox.
2. AXI CLI + REST API.
3. Analytics views.
4. Recurring + anomaly detection.

Post-v1 roadmap highlights: an optional human web UI on top of the REST API, investment holdings, and the RocketMoney CSV importer (see [docs/moneta-plan.md](docs/moneta-plan.md)).

## Development context

Agents and contributors: start with [AGENTS.md](AGENTS.md), then [docs/product-spec.md](docs/product-spec.md) for scope and [docs/decisions/](docs/decisions/) for settled choices.

The module pins its Go toolchain in `go.mod`.
Run the current schema and canonical-contract checks with:

```sh
go test ./...
```

## Plaid Sandbox Link

Keep credentials in the shell environment and never commit them.

```sh
export PLAID_CLIENT_ID='your-sandbox-client-id'
export PLAID_SECRET='your-sandbox-secret'
export PLAID_ENV='sandbox'
export MONETA_ENCRYPTION_KEY='your-base64-encoded-32-byte-key'
export MONETA_DB_PATH="$HOME/.local/share/moneta/moneta.db"
mkdir -p "$(dirname "$MONETA_DB_PATH")"
go run ./cmd/moneta link
```

Generate `MONETA_ENCRYPTION_KEY` once with `openssl rand -base64 32`, store it in a password manager, and reuse the same key for that database.
Open the printed `http://127.0.0.1:<port>` URL in a browser.
The temporary server always binds explicitly to `127.0.0.1`; broader addresses are rejected.

## Syncing

After linking, sync with the same environment (`PLAID_CLIENT_ID`, `PLAID_SECRET`, `PLAID_ENV`, `MONETA_ENCRYPTION_KEY`, `MONETA_DB_PATH`):

```sh
go run ./cmd/moneta sync
```

`moneta sync` pulls incremental transactions, balances, and liabilities for every linked Plaid item, or one item with `--item <item-id>`.
Each item prints a one-line summary, including the count of single-row poison records skipped so the sync could still advance.
Batches and cursors commit atomically, so re-running after a failure is safe.

## Status

After linking and syncing, inspect connection health with the same environment (`MONETA_DB_PATH`, or `--db`):

```sh
go run ./cmd/moneta status
```

`moneta status` reads only the local database and prints TOON on stdout: a summary block (item, account, and needs-attention counts), one row per linked item with institution, stored health, account and transaction counts, and last-sync time, then a next-step hint.
With nothing linked it says so and points at `moneta link`.
Flags: `--json` emits compact JSON instead of TOON, and `--limit` / `--full` control row truncation (default 20).
Exit codes follow the AXI convention: 0 ok, 1 error, 2 usage, and 3 when an item reports `login_required` and needs reconnection.
Output never includes amounts, account names, or credentials.

## Accounts

```sh
go run ./cmd/moneta accounts [--type credit_card] [--json] [--limit N | --full]
```

`moneta accounts` prints the plan's four-field schema (name, type, balance, status) as a TOON table, with a summary block of total/active/per-type counts.
Balance is the latest synced snapshot in dollars, `null` when the account has none yet.
`--type` filters to one canonical type (`checking`, `savings`, `credit_card`, `loan`, `investment`, `asset`).
`--entity` is deferred: Phase 1 is single-entity, so it would be a no-op today.
Exit codes: 0 ok, 1 error, 2 usage.

## Transactions

```sh
go run ./cmd/moneta tx [--from 2026-07-01 --to 2026-07-31] [--account checking] [--search grocery] [--json] [--limit N | --full]
```

`moneta tx` prints an aggregate summary over every match (count, excluded_count, signed total, inflow, outflow in dollars), then a TOON table of date, amount, merchant, status, account, newest first, 20 rows by default with a truncation line.
The listing shows every matching row, but the money totals follow the analytics-exclusion rule and omit `excluded` rows (transfers and card payments); `excluded_count` reports how many rows the totals omitted.
`--from`/`--to` are inclusive YYYY-MM-DD dates, `--account` is a case-insensitive account-name substring, and `--search` is a case-insensitive merchant substring.
With no matches, the hint suggests widening the filters.
Exit codes: 0 ok, 1 error, 2 usage.
Deferred plan filters for later slices: `--cat`, `--merchant`, `--entity`, `--min`/`--max`.

## Spending

```sh
go run ./cmd/moneta spend [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json] [--limit N | --full]
```

With no period flags, `moneta spend` uses the current calendar month in the host's local timezone.
`--period` accepts a calendar month in YYYY-MM form; custom `--from` / `--to` dates are inclusive and must be supplied together.
`--account` is a case-insensitive literal account-name substring.

The summary reports period bounds, posted spending transaction count, and positive `total_spend` in dollars.
Spend includes posted outflows only and always applies `excluded = 0`, so pending rows, transfers, card payments, and inflows do not affect the totals or breakdowns.
Source outflows remain negative cents in SQLite; the spend command deliberately presents them as positive spend.
Category and merchant tables are ordered by spend, use an `Uncategorized` bucket when needed, and show 20 groups each by default with independent truncation lines.
Exit codes: 0 ok, 1 error, 2 usage.

## Cash flow

```sh
go run ./cmd/moneta cashflow [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json]
```

Cash flow uses the same period and account-filter contract as spend: current local calendar month by default, or an explicit YYYY-MM month / inclusive custom date pair.
It includes posted rows with `excluded = 0`; refunds and other positive rows count as inflow, while negative rows are presented as positive outflow magnitude.
The summary reports count, inflow, outflow, signed net (`inflow - outflow`), and `savings_rate`.
Savings rate is `net / inflow`, truncated toward zero to four decimal places (`0.1234` means 12.34%); it is `null` when inflow is zero.
Money remains integer cents internally and renders through `cli.Money`; rate construction uses integer arithmetic, never float64.
Exit codes: 0 ok, 1 error, 2 usage.

## Net worth

```sh
go run ./cmd/moneta networth [--as-of 2026-07-22] [--json]
```

By default, net worth uses each account's latest available balance and reports the newest selected balance date as `as_of`; it is `null` when no balance snapshots exist.
`--as-of` selects each account's latest balance on or before the inclusive YYYY-MM-DD cutoff and echoes that requested date in the summary.
Checking, savings, investment, and asset accounts contribute to assets.
Credit-card and loan balances contribute to liabilities as positive debt magnitude, and signed net worth is `assets - liabilities`.
Accounts without an eligible snapshot are counted in `missing_balance` and omitted from every money total; a by-type row with no eligible balances renders `balance: null` rather than inventing zero.
All stored accounts participate, including inactive accounts, because the current schema does not track historical active intervals.
Money remains integer cents internally and renders through `cli.Money`.
Exit codes: 0 ok, 1 error, 2 usage.

## Library sync path

`moneta sync` wraps the library sync path: product code loads a linked connection with `store.GetProviderItem` and passes it to `core.SyncProviderItem` with the secret cipher and provider constructor.
`SyncProviderItem` decrypts the credential in memory, clears the plaintext bytes before returning, syncs from the stored cursor, bootstraps the single Phase 1 personal entity when needed, and applies the batch and cursor atomically.
Fresh databases require no hand-written entity SQL.
A successful sync returns `SyncResult.Skipped`, the merged list of provider and ingest records dropped as single-row poison; an empty list means nothing was dropped.

## License

[Apache-2.0](LICENSE)
