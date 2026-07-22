# Moneta - Architecture Plan

Status: approved design. Phase 1 implementation and post-review hardening are complete. Phase 2 is in progress: the poison skip, production `moneta sync`, and the `moneta status` / `accounts` / `tx` reads are merged, and the post-review hardening stack (`docs/phase2-review-fix-pr-plan.md`) is complete; the remaining AXI reads and REST mirror are next.
Moneta is a self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.
It ingests financial data from pluggable providers, normalizes it into a canonical model in SQLite, and exposes it through a token-efficient AXI CLI (TOON output) and a small REST API.

## Goals

- Local-first and self-hosted: data never leaves the machine except via the CLI/API the owner controls and outbound calls to Plaid (the only permitted third-party dependency, isolated inside the Plaid provider).
- Agent-ergonomic access following the [kunchenguid/axi](https://github.com/kunchenguid/axi) design principles, with [TOON](https://github.com/toon-format/toon) output on stdout.
- Agent-harness agnostic: the CLI and REST surfaces assume nothing about which agent framework consumes them; any harness with shell access or HTTP works.
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
  internal/core/         ingest + sync orchestration; rules deferred
  internal/store/        database/sql, migrations, entity bootstrap
  internal/providers/
    plaid/               plaid-go, Link page via embed, ONLY Plaid-touching code
    manual/              Phase 2+ JSON/write provider, not yet present
    rmcsv/               Phase 2+ CSV provider, not yet present
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
- **txn_provider_refs**: `provider`, `provider_txn_id`, `pending_txn_id`; Plaid dedups on native ids from `/transactions/sync`; id-less providers (CSV) fall back to `dedup_hash` (date + amount + merchant_norm + account) with a ±3-day fuzzy window for pending→posted drift; the hash excludes mutable fields (notably `status`), and a fuzzy match updates the existing row, never inserts a second one (see Binding implementation requirements).
- **categories**: `name`, `parent_id?`, `kind` (income|expense|transfer|ignore); `transfer` rows (card payments, internal transfers) are auto-excluded from spending analytics, never deleted.
- **category_mappings**: `provider`, `source_category`, `category_id`; the user-overridable mapping layer.
- **entity_rules**: the schema ships in Phase 1, but rule creation and application are Phase 2+.
  Phase 1 is single-entity: ingest accepts one default entity, assigns it to accounts, and transactions inherit their account's entity.
  No entity rules are seeded or written yet.
  Before merchant-pattern routing is implemented, revisit the composite `(account_id, entity_id)` foreign key, which currently prevents a transaction from diverging from its account's entity.
- **recurring_items**: `kind` (subscription|bill|income), `cadence`, `expected_cents`, `next_expected_date`, `drift_pct`, `source` (detected|manual); one table covers subscriptions, bills, and income streams.
- **provider_items**: `item_id`, `institution`, `access_token_enc` (AES-GCM blob), `status` (ok|login_required|error), `sync_cursor`, `last_synced_at`.
- **net_worth_snapshots**: per entity (and combined) per day: assets, liabilities, net, per-type breakdown.
- **budgets** (optional v1): entity, category, month, target.
- **import_runs**: successful sync audit rows commit atomically with their batches.
  Failed-run recording with redacted error details is deferred to the Phase 2 service and CLI layer.

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
    Skipped     []SkippedRecord // row-local poison dropped with redacted reasons
}

// SkippedRecord is one dropped row: Kind (account|transaction|balance|liability),
// ID (opaque provider id), Reason (unsupported_currency | unsupported_account_type |
// malformed_record | account_skipped), and a short static Detail (a currency or
// account type code). It never carries amounts, merchants, account names, or
// credentials. SyncResult.Skipped merges provider and ingest skip lists.

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

### Provider roadmap

- **plaid** (primary): Link flow served from the binary via `embed` on localhost; `/transactions/sync` (cursor-based, idempotent); balances; `/liabilities/get` where the institution supports it (coverage varies by institution); Item error states like `ITEM_LOGIN_REQUIRED` surface as `reconnection_needed` in `moneta status`, never silent failure; Sandbox for development, switchable to production via env vars; webhooks deferred, manual/scheduled sync in v1.
- **manual** (Phase 2+; not yet present): structured JSON file plus `moneta add` writes, for assets (vehicles, property), unsupported institutions, and anything a provider does not cover (including loan terms when liabilities data is thin).
- **rmcsv** (Phase 2+; not yet present): a future implementation will bulk-import RocketMoney CSV exports and match accounts so history merges with Plaid data without duplicates.

### Phase 2 architecture carry-forwards

These findings are inert in the current USD-only Plaid path, but must be resolved when the named consumer arrives.

- **Token-less providers:** `provider_items.access_token_enc` is required and core ingestion requires a provider item, so manual and CSV providers cannot yet represent a connection without fabricating ciphertext.
  Revisit the credential storage shape or nullable constraint before either provider lands.
- **Dedup strategy:** `Capabilities()` is not consumed by core, while native-ID behavior is inferred from whether each transaction carries `ProviderTxnID`.
  Add a declared native-ID capability or dedup strategy before the CSV provider introduces unstable or partially populated IDs.
- **Liability kind:** `canon.Liability` does not carry its source kind, so core infers credit versus loan terms from account type and cannot populate mortgage origination or maturity fields.
  Extend the canonical contract before mortgage, HELOC, margin-loan, or Phase 3 amortization support.
- **Balance currency:** `canon.Balance` has no currency and balance ingestion currently writes the USD literal.
  Carry the account currency or remove the redundant snapshot column before multi-currency work.
- **Provider versus user ownership:** `excluded` remains sync-owned and derived from category kind.
  The Phase 2 change that adds `moneta tag` must decide whether provider recategorization or user edits win for `category_id` and `entity_id`; per-column override flags are the likely schema design.
- **Single-entity bootstrap:** migrations intentionally seed no personal entity.
  Phase 1 hardening adds a product-level bootstrap and sync orchestrator for one deterministic personal entity; multi-entity routing remains coupled to the deferred `entity_rules` work.

### Phase 2 sync policy: single-row poison must not wedge an Item (resolved)

Row-local normalization failures (unsupported or unofficial currency, unexpected account type, malformed fields) no longer fail `Provider.Sync`.
The provider skips the offending record, records it in `SyncBatch.Skipped` (kind, stable reason code, redacted detail; never amounts, merchants, or names), and returns a batch whose cursor can advance.
Records referencing a skipped account are dropped with an `account_skipped` reason so they cannot fail ingest.
Core ingest applies the same policy where routing allows it: a liability for an account type with no terms table is skipped and reported via `IngestResult.Skipped` instead of rolling the batch back.
`core.SyncProviderItem` merges both skip lists into `SyncResult.Skipped` for the CLI and REST layer to surface.
This is not multi-currency support: unsupported-currency rows are skipped, never converted.

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

## Binding implementation requirements

These three requirements are binding for Phase 1 and beyond; code that violates them is a bug regardless of tests passing.

### 1. Money: integer cents, never floats

- All monetary amounts are `int64` cents in SQLite and in every internal Go struct; no `float64` money anywhere in the core.
- Every string money input (CLI flags, REST API, manual/JSON provider) parses to cents without ever passing through a float: split on the decimal point with digit validation.
  `$10.50`, `10.50`, `10.5`, and `-3.07` all parse correctly; currency symbols and negatives are handled; more than 2 decimal places is rejected.
- Plaid boundary: `plaid-go` returns amounts as `float64`.
  Convert to cents exactly once, at the Plaid provider boundary, using round-half-away-from-zero.
  Unit tests must cover the known float-precision traps (e.g., `4.35`, `1.005`, and similar values that misbehave under naive `*100` truncation).
- Cents convert back to display strings only at the output boundary (TOON/REST formatting).

### 2. Dedup survives pending → posted transitions

- The transaction `dedup_hash` excludes mutable fields - specifically `status` - so a pending transaction that later posts matches its earlier record instead of duplicating.
- Plaid path: ID matching is primary.
  Use `transaction_id`; when a posted transaction arrives carrying a `pending_transaction_id`, update/replace the pending row it references.
  Fuzzy logic is a fallback, never the main mechanism.
- ID-less providers (the future RocketMoney CSV import): apply the ±3-day fuzzy window on (amount, account, normalized merchant) to catch pending→posted date shifts.
  On a fuzzy match, update the existing row - never insert a second one.
- Required test case: a pending transaction appears, then posts 2 days later with a shifted date and identical amount → exactly one row exists afterward.

### 3. REST server binds to loopback only

- The `net/http` listener binds explicitly to `127.0.0.1:<port>` - never `0.0.0.0:<port>` and never the bare `:<port>` form, which Go treats as all interfaces.
- The bind address is configurable, but it defaults to loopback and requires an explicit `--listen` flag plus a logged warning to bind anything broader.
- The bound address is logged at startup so exposure is always auditable.

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

- Human web UI, for owners who want to view and review their finances directly rather than only through an agent.
  It will consume the same service layer / REST API, so the current architecture needs no changes to support it; agent-first remains the primary interface.
- Investment holdings and positions (Plaid investments product); v1 tracks investment accounts at balance level only.
- RocketMoney CSV importer implementation (no provider stub exists yet).
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
