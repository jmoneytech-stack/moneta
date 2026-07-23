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
`moneta status`, `moneta accounts`, `moneta tx`, `moneta spend`, `moneta cashflow`, `moneta networth`, and `moneta debts` emit TOON for agent consumers and are mirrored as authenticated JSON by `moneta serve`; Phase 2 CI is in place.
The Phase 3 correctness foundation is complete; compute-on-read analytics now include `moneta networth --history Nd` and the `mom`, `merchants`, `utilization`, and `savings` trend metrics without materialized analytics tables.
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

CI runs on pull requests and pushes to `main`: build, vet, tests, CGO-free tests, and race tests.
Full staticcheck and golangci-lint are not CI gates yet because the established Plaid ST1005 / errcheck baseline remains; touched code is checked locally without mass baseline cleanup.

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

## Trends

```sh
go run ./cmd/moneta trends --metric mom [--period 2026-07] [--account checking] [--json] [--limit N | --full]
go run ./cmd/moneta trends --metric merchants [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json] [--limit N | --full]
go run ./cmd/moneta trends --metric utilization [--history 30d | --period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account card] [--json]
go run ./cmd/moneta trends --metric savings [--period 2026-07 | --from 2026-07-01 --to 2026-07-31] [--account checking] [--json]
```

`--metric` is required, and the supported values are `mom`, `merchants`, `utilization`, and `savings`; later trend metrics return a usage error until their own PRs land.
For `mom`, the selected calendar month is the current comparison period, with the immediately preceding calendar month as its baseline.
With no `--period`, `mom` uses the current month in the host's local timezone.
Custom `--from` / `--to` windows are rejected for `mom` so both sides remain calendar months.

For every category present in either month, `mom` reports `spend_this` and `spend_prev` as positive magnitudes of posted, non-excluded outflows, and signed `delta` as `spend_this - spend_prev`.
Categories retain `category_id` identity even when display names match; uncategorized rows share the `Uncategorized` bucket.
Rows sort by absolute delta descending, then category name and ID.

The `merchants` metric uses the current local calendar month by default, an explicit `--period YYYY-MM`, or a complete inclusive `--from` / `--to` pair with the same validation rules as `moneta spend`.
It groups by exact `merchant_norm`, so multiple raw spellings with the same normalized merchant remain one row.
An empty normalized key uses one `Unknown Merchant` bucket rather than splitting by raw spelling.
Rows sort by spend descending, then merchant name and normalized key.

The `utilization` metric returns one carried-forward portfolio point for every calendar day.
It defaults to the 30 local-calendar days ending today, inclusive, and also accepts `--history Nd`, `--period YYYY-MM`, or a complete inclusive `--from` / `--to` pair.
Those window forms cannot be combined.
Explicit `--limit` / `--full` are rejected because utilization has no row-limited per-card table.

Utilization includes credit-card accounts only.
For each card and day, it uses the latest balance snapshot on or before that day.
A non-NULL snapshot `limit_cents` is authoritative; only a NULL snapshot limit falls back to the current `credit_terms.limit_cents`.
Cards without a positive usable limit are excluded from that day's portfolio, and utilization is `null` when no card remains.
A positive balance contributes debt, while a zero or negative credit balance contributes zero debt when the card has a positive limit.
Portfolio utilization is total debt divided by total limits through integer-only `cli.Ratio(..., 4)` arithmetic.
The summary `accounts` count covers matched credit cards, each point's `accounts` covers included cards, and `missing_limit_days` counts days when at least one carried card balance lacks a positive usable limit.
Historical limit values stored as zero before nullable-money PR2 remain indistinguishable from real reported zero and are excluded rather than backfilled or treated as 0% utilization.

The `savings` metric uses the same current-month default, explicit calendar month, or complete inclusive custom date pair as `moneta cashflow` and `merchants`.
It calls the same `store.ReadCashflow` aggregation as `moneta cashflow`, so count, inflow, positive outflow magnitude, signed net, and savings rate cannot use a separate formula.
Savings rate is `cli.Ratio(net, inflow, 4)` and is `null` when inflow is zero.
The metric is summary-only, so `--history`, `--limit`, and `--full` are rejected.

The `mom`, `merchants`, and `savings` transaction metrics include posted rows only and always apply `excluded = 0`.
The `mom` and `merchants` metrics include outflows only, while `savings` includes both inflows and outflows.
The row-based `mom` and `merchants` metrics truncate to 20 rows by default without changing summary totals.
All four metrics use the same case-insensitive escaped literal `--account` substring behavior.
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
go run ./cmd/moneta networth [--as-of 2026-07-22 | --history 90d] [--json]
```

By default, net worth uses each account's latest available balance and reports the newest selected balance date as `as_of`; it is `null` when no balance snapshots exist.
`--as-of` selects each account's latest balance on or before the inclusive YYYY-MM-DD cutoff and echoes that requested date in the summary.
`--history Nd` returns exactly N daily points ending on today's local-calendar date, inclusive; the start is the end minus N-1 days, so `90d` returns 90 points.
History carries each account's latest balance forward across days without a new snapshot and supports 1 through 3660 days.
`--history` and `--as-of` cannot be combined.
Checking, savings, investment, and asset accounts contribute to assets.
Credit-card and loan balances use the canonical provider-boundary sign: positive means owed and negative means the institution owes the user.
Signed net worth is `assets - liabilities`, so a negative liability credit raises net worth.
Accounts without an eligible snapshot are counted in `missing_balance` and omitted from every money total; a by-type row with no eligible balances renders `balance: null` rather than inventing zero.
All stored accounts participate, including inactive accounts, because the current schema does not track historical active intervals.
Money remains integer cents internally and renders through `cli.Money`.
Exit codes: 0 ok, 1 error, 2 usage.

## Debts

```sh
go run ./cmd/moneta debts [--json]
```

Debts lists every credit-card and loan account using its latest balance snapshot and preserves the canonical balance sign.
A positive balance is owed debt, while a negative balance is a genuine credit owed to the user.
An account without a snapshot remains in the table with `balance: null`, increments `missing_balance`, and contributes nothing to `total_debt`.
Credit-card limit, APR, and due day and loan APR are best-effort values from the existing terms tables; unavailable fields are `null`.
Provider-omitted optional money remains SQL `NULL`, while an explicitly reported `0.00` remains distinguishable as zero.
Utilization is `balance / limit`, truncated toward zero to four decimal places, and is `null` when balance is missing or limit is absent, zero, or negative.
Plaid APR enters SQLite as percentage points; output converts it to a decimal fraction rounded to the nearest basis point, so stored `22.99` renders as `0.2299`.
Money remains integer cents internally and renders through `cli.Money`.
Exit codes: 0 ok, 1 error, 2 usage.

## Read-only REST API

Set the database path and an API key through the environment, then start the loopback server:

```sh
export MONETA_DB_PATH="$HOME/.local/share/moneta/moneta.db"
export MONETA_API_KEY="replace-with-a-long-random-key"
go run ./cmd/moneta serve
```

The default listen address is `127.0.0.1:8080`.
Every route requires `X-API-Key`, and the key is compared in constant time after fixed-length hashing.
The key is never logged; `MONETA_API_KEY` is the recommended mechanism because `--api-key` is visible to other local users through the process list.
Responses are JSON only, set `Cache-Control: no-store`, and render money as exact decimal numbers through the same `cli.Money` boundary as CLI JSON.

```sh
curl -sS \
  -H "X-API-Key: $MONETA_API_KEY" \
  "http://127.0.0.1:8080/v1/networth?as_of=2026-07-22"
```

Read routes:

| Route | Query parameters |
|---|---|
| `GET /v1/status` | `limit`, `full` |
| `GET /v1/accounts` | `type`, `limit`, `full` |
| `GET /v1/transactions` | `from`, `to`, `account`, `search`, `limit`, `full` |
| `GET /v1/spend` | `period` or `from` + `to`, `account`, `limit`, `full` |
| `GET /v1/cashflow` | `period` or `from` + `to`, `account` |
| `GET /v1/networth` | `as_of` or `history=Nd` |
| `GET /v1/debts` | none |
| `GET /v1/trends` | required `metric=mom\|merchants\|utilization\|savings`; `mom`: optional `period`; `merchants`: `period` or `from` + `to`, plus `limit`/`full`; `utilization`: `history`, `period`, or `from` + `to`; `savings`: `period` or `from` + `to`; all: `account` |

Period and date semantics match their CLI counterparts.
Use `full=true` to disable a route's default 20-row limit.
Invalid queries return JSON with status 400; missing or incorrect keys return 401 without revealing authentication details.
The database opens once at process startup, and SIGINT/SIGTERM triggers graceful shutdown.
Exit codes: 0 clean stop, 1 runtime error, 2 usage/configuration.

Non-loopback binding is refused unless both a non-loopback `--listen` address and `--allow-non-loopback` are supplied.
That opt-in logs a prominent exposure warning; Moneta does not provide TLS, public-internet CORS, or write APIs.

## Library sync path

`moneta sync` wraps the library sync path: product code loads a linked connection with `store.GetProviderItem` and passes it to `core.SyncProviderItem` with the secret cipher and provider constructor.
`SyncProviderItem` decrypts the credential in memory, clears the plaintext bytes before returning, syncs from the stored cursor, bootstraps the single Phase 1 personal entity when needed, and applies the batch and cursor atomically.
Fresh databases require no hand-written entity SQL.
A successful sync returns `SyncResult.Skipped`, the merged list of provider and ingest records dropped as single-row poison; an empty list means nothing was dropped.

## License

[Apache-2.0](LICENSE)
