# Moneta

A self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.

Moneta ingests financial data from pluggable providers (Plaid first), normalizes it into a canonical model in SQLite, and exposes it through a token-efficient, agent-ergonomic CLI following the [AXI](https://github.com/kunchenguid/axi) design principles with [TOON](https://github.com/toon-format/toon) output.
A small localhost REST API mirrors the same operations for non-CLI consumers.

## Status

Pre-alpha, building in public.
Phase 1 implementation and post-review hardening are complete pending final maintainer approval: the Go module, canonical provider contract, SQLite schema, transactional sync ingestion, AES-GCM secret encryption, Plaid sync integration, and the runnable Plaid Link loopback flow exist.
The Link flow creates and exchanges Plaid tokens, encrypts permanent access tokens before SQLite persistence, and rejects every non-loopback bind.
The complete Link, transactions, balances, liabilities, encrypted persistence, and atomic ingestion path has been verified against Plaid Sandbox.
The hardening stack addresses the confirmed review findings before Phase 2 begins.
The `moneta link` and `moneta sync` commands run the connection and sync flows; the AXI read surface and REST API are next.
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

## Library sync path

`moneta sync` wraps the library sync path: product code loads a linked connection with `store.GetProviderItem` and passes it to `core.SyncProviderItem` with the secret cipher and provider constructor.
`SyncProviderItem` decrypts the credential in memory, clears the plaintext bytes before returning, syncs from the stored cursor, bootstraps the single Phase 1 personal entity when needed, and applies the batch and cursor atomically.
Fresh databases require no hand-written entity SQL.
A successful sync returns `SyncResult.Skipped`, the merged list of provider and ingest records dropped as single-row poison; an empty list means nothing was dropped.

## License

[Apache-2.0](LICENSE)
