# Moneta - Architecture Plan

Status: approved design, pre-implementation.
Moneta is a self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.
It ingests financial data from pluggable providers, normalizes it into a canonical model in SQLite, and exposes it through a token-efficient AXI CLI (TOON output) and a small REST API.

## Goals

- Local-first and self-hosted: data never leaves the machine except via the CLI/API the owner controls and outbound calls to Plaid (the only permitted third-party dependency, isolated inside the Plaid provider).
- Agent-ergonomic access following the [kunchenguid/axi](https://github.com/kunchenguid/axi) design principles, with [TOON](https://github.com/toon-format/toon) output on stdout.
- Pluggable providers: no provider-specific fields in the core model, idempotent imports, transaction-level dedup.
- Open source: personal data (exports, custom categories, local mapping config) stays out of the repo; committed docs and fixtures use the neutral default taxonomy and fake data only.

## Tech stack

- Go, latest stable release, pinned via `toolchain` in `go.mod`.
- Module path: `github.com/jmoneytech-stack/moneta`.
- CLI: stdlib `flag` plus a small command router (~18 flat commands need no cobra; stdlib fails loudly on unknown flags and lets us own concise agent-oriented help).
- SQLite via `modernc.org/sqlite` (pure Go, CGO-free), plain `database/sql`, SQL migrations embedded with `embed`.
- REST: stdlib `net/http` with the 1.22+ pattern mux.
- Plaid: official `plaid-go` SDK only.
- TOON: internal encoder package (`internal/toon`), spec-conformant subset, golden-file tests; encode-only, applied at the stdout boundary.
- Secrets: Plaid `access_token`s encrypted at the application layer with AES-256-GCM, key from `MONETA_ENCRYPTION_KEY` (32-byte base64). No SQLCipher: it requires CGO and breaks clean cross-compilation; disk-level encryption (e.g., FileVault) covers the rest.
- Money: signed integer cents (`int64`) everywhere internally and in SQLite; negative = outflow; rendered as dollars at output.
- Distribution: single static binary (`CGO_ENABLED=0`), release matrix for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, plus `go install`. A Dockerfile ships as an optional distribution artifact for containerized self-hosting.

## Configuration (env vars / flags only)

`MONETA_DB_PATH`, `MONETA_API_KEY`, `MONETA_ENCRYPTION_KEY`, `PLAID_CLIENT_ID`, `PLAID_SECRET`, `PLAID_ENV` (`sandbox` or `production`; Plaid retired the Development environment).
Never hardcoded, never logged, never returned by any command or endpoint.

## Package layout

```
moneta/
  cmd/moneta/            main.go (router, flag parsing)
  internal/canon/        canonical DTOs + Provider interface (no deps)
  internal/core/         ingest: dedup, category map, entity rules
  internal/store/        database/sql, embedded SQL migrations
  internal/providers/
    plaid/               plaid-go, Link page via embed, ONLY Plaid-touching code
    manual/              JSON file + write path
    rmcsv/               stub (interface-conforming, no implementation)
  internal/analytics/    precomputed views, refreshed at sync
  internal/toon/         TOON encoder
  internal/cli/          AXI command implementations
  internal/api/          net/http REST (127.0.0.1, X-API-Key)
  internal/secret/       AES-GCM seal/open
```

## Canonical data model (SQLite)

```
entities 1──* accounts 1──* transactions *──1 categories
              │  │                │
              │  ├─0..1 credit_terms / loan_terms
              │  └──* balance_snapshots
              │                   └──* txn_provider_refs
provider_items 1──* accounts
recurring_items 1──* transactions
net_worth_snapshots (per entity per day)
budgets · category_mappings · entity_rules · import_runs
```

- **entities**: `personal` | `business`; every account and transaction belongs to one, and all queries filter by it.
- **accounts**: `entity_id`, `type` (checking|savings|credit_card|loan|investment|asset), `name`, `institution`, `mask`, `provider`, `provider_account_id`, `is_active`.
- **credit_terms** (credit cards): `limit_cents`, `apr`, `statement_day`, `due_day`, `min_payment_cents`, `last_statement_cents`.
- **loan_terms**: `apr`, `min_payment_cents`, `origination_cents`, `maturity_date`.
- **balance_snapshots**: one row per account per sync day; feeds net worth and utilization history.
- **transactions**: `date` (TEXT ISO), `amount_cents` (signed), `merchant_raw`, `merchant_norm`, `category_id`, `status` (pending|posted), `tags`, `notes`, `recurring_id?`, `is_transfer`, `excluded`, `dedup_hash`.
- **txn_provider_refs**: `provider`, `provider_txn_id`, `pending_txn_id`; Plaid dedups on native ids from `/transactions/sync`; id-less providers (CSV) fall back to `dedup_hash` (date + amount + merchant_norm + account) with a ±3-day fuzzy window for pending→posted drift.
- **categories**: `name`, `parent_id?`, `kind` (income|expense|transfer|ignore); `transfer` rows (card payments, internal transfers) are auto-excluded from spending analytics, never deleted.
- **category_mappings**: `provider`, `source_category`, `category_id`; the user-overridable mapping layer.
- **entity_rules**: ordered rules (`account?`, `category?`, `merchant_pattern?`) → entity, applied at ingest; account default is the last rule.
- **recurring_items**: `kind` (subscription|bill|income), `cadence`, `expected_cents`, `next_expected_date`, `drift_pct`, `source` (detected|manual); one table covers subscriptions, bills, and income streams.
- **provider_items**: `item_id`, `institution`, `access_token_enc` (AES-GCM blob), `status` (ok|login_required|error), `sync_cursor`, `last_synced_at`.
- **net_worth_snapshots**: per entity (and combined) per day: assets, liabilities, net, per-type breakdown.
- **budgets** (optional v1): entity, category, month, target.
- **import_runs**: audit trail per sync/import.

### Categories: default taxonomy

Moneta ships a neutral default taxonomy mirroring Plaid's Personal Finance Categories primary groups (~16 top-level groups), stored as seed data rows, not code.
This gives any new user sensible categories on first sync with zero config.
Personalization is data: users add custom categories and remap rules locally (DB + gitignored config), which never enter the repo.

## Provider interface

```go
package canon

type AccountType string   // checking, savings, credit_card, loan, investment, asset
type TxnStatus string     // pending, posted
type Date string          // YYYY-MM-DD; stored as TEXT in SQLite

type Account struct {
    ProviderAccountID string
    Name, Institution, Mask string
    Type     AccountType
    Currency string
}

type Transaction struct {
    ProviderTxnID  string // empty for id-less providers (CSV) → hash dedup
    PendingTxnID   string // links posted txn to its pending predecessor
    AccountRef     string // ProviderAccountID
    Date           Date
    AmountCents    int64  // signed; negative = outflow
    MerchantRaw    string
    SourceCategory string // provider vocabulary; core maps via category_mappings
    Status         TxnStatus
    Currency       string
}

type Balance struct {
    AccountRef string
    Date       Date
    CurrentCents, AvailableCents, LimitCents int64
}

type Liability struct {
    AccountRef string
    APR        float64
    LimitCents, MinPaymentCents, LastStatementCents int64
    StatementDay, DueDay int
}

type ConnectionStatus struct {
    ID, Institution string
    State           string // ok | login_required | error
    Detail          string // e.g. Plaid error code; never contains secrets
}

type SyncBatch struct {
    Accounts    []Account
    Added       []Transaction
    Modified    []Transaction
    Removed     []string // provider txn ids
    Balances    []Balance
    Liabilities []Liability
    NextCursor  string   // core persists this; providers stay stateless
}

type Capability uint8
const (
    CapAccounts Capability = 1 << iota
    CapTransactions
    CapBalances
    CapLiabilities
    CapWrite
)

type Provider interface {
    Name() string
    Capabilities() Capability
    // Connections reports per-Item health; surfaces login_required.
    Connections(ctx context.Context) ([]ConnectionStatus, error)
    // Sync pulls incrementally from cursor. Core owns cursor persistence,
    // dedup, category/entity mapping, and all DB writes.
    Sync(ctx context.Context, cursor string) (*SyncBatch, error)
}
```

### Providers (v1)

- **plaid** (primary): Link flow served from the binary via `embed` on localhost; `/transactions/sync` (cursor-based, idempotent); balances; `/liabilities/get` where the institution supports it (coverage varies by institution); Item error states like `ITEM_LOGIN_REQUIRED` surface as `reconnection_needed` in `moneta status`, never silent failure; Sandbox for development, switchable to production via env vars; webhooks deferred, manual/scheduled sync in v1.
- **manual**: structured JSON file plus `moneta add` writes, for assets (vehicles, property), unsupported institutions, and anything a provider does not cover (including loan terms when liabilities data is thin).
- **rmcsv**: stub only in v1; future implementation bulk-imports RocketMoney CSV exports and matches accounts so history merges with Plaid data without duplicates; adding it touches only the provider registry.

## AXI CLI commands

Conventions on every command: TOON on stdout; one record per line (grep/head friendly); `summary:` pre-computed aggregates before rows; default limit 20 with a truncation line and `--limit`/`--full` escape; definitive empty states with a widening suggestion; `hint:` next-step line; structured errors on stderr; exit codes 0 ok / 1 error / 2 usage / 3 reconnection-needed; no interactive prompts; `moneta help <cmd>` concise reference; `--json` escape hatch on reads.

| Command | Purpose |
|---|---|
| `moneta` | Content-first dashboard: net worth, cash, utilization, upcoming bills, sync health, anomaly count |
| `moneta networth [--entity] [--asof] [--history 90d]` | Current + historical net worth |
| `moneta accounts [--entity] [--type]` | name, type, balance, status (4-field default schema) |
| `moneta tx [--from --to --cat --merchant --account --entity --min --max --search --limit]` | Transactions with aggregate header |
| `moneta spend --period YYYY-MM [--by category\|merchant\|account] [--entity] [--vs prev]` | Spending summary + deltas |
| `moneta cashflow --period [--entity]` | Inflow, outflow, net, savings rate |
| `moneta income --period [--entity]` | Income streams |
| `moneta recurring [--entity] [--kind]` | Subscriptions/bills/income with drift flags |
| `moneta bills [--days 30]` | Upcoming bills + card due dates |
| `moneta debts [--entity]` / `moneta cards` | Liabilities; card utilization/APR/due dates |
| `moneta trends [--metric mom\|merchants\|utilization\|savings\|fixed-variable]` | Precomputed analytics views |
| `moneta anomalies [--period]` | Unusual spend vs trailing baseline |
| `moneta sync [--provider]` / `moneta status` | Incremental sync; Item health incl. `reconnection_needed` |
| `moneta tag <txn-id> [--entity] [--cat] [--note]` | Fix one-off misclassifications rules don't catch |
| `moneta add <balance\|asset\|tx\|debt> ...` | Idempotent manual-provider writes (natural-key upsert) |
| `moneta link` | Serve the embedded Plaid Link page locally to connect an institution |
| `moneta budgets [set]` | Optional v1 |
| `moneta serve` | REST API (JSON, `X-API-Key`, 127.0.0.1) mirroring reads + adds |

Example output shape (fake data):

```
summary:
  count: 142
  total: -4821.37
  avg_per_day: -160.71
  top_category: Food & Drink,-812.40
tx[20]{date,amount,merchant,category,account}:
  2026-06-30,-42.18,Grocery Mart,Food & Drink,checking-1
  2026-06-29,-15.49,StreamCo,Entertainment,card-1
  ...
truncated: 20 of 142 shown (--limit 142 for all; pipe | grep to filter)
hint: moneta spend --period 2026-06 --by category
```

A `SKILL.md` ships with the repo so agents can learn the command surface without trial and error, per AXI's ambient-context principle.

## Analytics (precomputed at sync, not derived by the agent)

Month-over-month category trends, top merchants, average daily spend, fixed vs variable expense split, savings rate, subscription total, credit utilization trend.
Net worth snapshots are computed at sync time (scheduled daily).

## Security

- API-key auth on all REST endpoints; server binds 127.0.0.1 by default.
- Access tokens AES-GCM encrypted at rest, never logged (redaction filter as second line of defense), never exposed via any response.
- No telemetry; no third-party calls outside the Plaid provider.

## Deployment

Recommended: native binary with a scheduled sync via launchd (macOS) or cron/systemd timers (Linux).
Containerized deployment is supported via the optional Dockerfile for server/NAS self-hosting.

## Build phases

1. Core schema + provider interface + Plaid provider (Link flow, transactions sync, liabilities), tested against Sandbox.
2. AXI CLI + REST API.
3. Analytics views.
4. Recurring detection + anomaly detection.

Each phase delivers runnable code and README updates (setup, building or downloading the binary, getting Plaid Sandbox keys, connecting an institution, running a sync, testing the CLI commands).

## Deferred / roadmap

- Investment holdings and positions (Plaid investments product); v1 tracks investment accounts at balance level only.
- RocketMoney CSV importer implementation (provider stub exists from day one).
- Plaid webhooks for push-driven sync.
- Whole-database encryption (deliberate follow-up decision if ever needed; not silently added).
- Brokerage support, if and when needed.

## Flagged assumptions

- USD only.
- Signed integer cents; negative = outflow.
- Subscriptions, bills, and income streams share one `recurring_items` table with a `kind` field.
- Transfers and card payments excluded from analytics via `category.kind`.
- `PLAID_ENV` supports `sandbox`/`production` only.
- Stdlib `flag` over cobra; no SQLCipher (cases documented above).
- REST returns JSON; TOON is a stdout/agent concern.
