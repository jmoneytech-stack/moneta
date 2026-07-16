# Moneta

A self-hosted personal + business finance data hub whose primary consumer is an AI agent, not a human UI.

Moneta ingests financial data from pluggable providers (Plaid first), normalizes it into a canonical model in SQLite, and exposes it through a token-efficient, agent-ergonomic CLI following the [AXI](https://github.com/kunchenguid/axi) design principles with [TOON](https://github.com/toon-format/toon) output.
A small localhost REST API mirrors the same operations for non-CLI consumers.

## Status

Pre-alpha, building in public.
No runnable code yet; the approved design lives in [docs/moneta-plan.md](docs/moneta-plan.md) and the reasoning behind key choices in [docs/decisions/](docs/decisions/).

## Principles

- Local-first: your data stays on your machine, except outbound calls to Plaid, which are isolated inside the Plaid provider.
- Agent-first: pre-computed aggregates, compact TOON output, grep-friendly lines, definitive empty states, no interactive prompts.
- Pluggable providers: no provider-specific fields leak into the core model; imports are idempotent with transaction-level dedup.
- Single static Go binary, CGO-free, minimal dependencies, no telemetry.

## Planned build phases

1. Core schema + provider interface + Plaid provider (Link flow, transactions sync, liabilities) against Sandbox.
2. AXI CLI + REST API.
3. Analytics views.
4. Recurring + anomaly detection.

## Development context

Agents and contributors: start with [AGENTS.md](AGENTS.md), then [docs/product-spec.md](docs/product-spec.md) for scope and [docs/decisions/](docs/decisions/) for settled choices.

## License

[Apache-2.0](LICENSE)
