# Moneta Product Spec

Architecture details live in [moneta-plan.md](moneta-plan.md); this file is the product frame.

## Product

A self-hosted finance data hub for people who want their AI agent to answer money questions.
It ingests accounts, transactions, and liabilities from pluggable providers, normalizes them into one canonical SQLite model split across personal and business entities, and serves them through a token-efficient CLI built for agent consumption.
Audience: technical self-hosters running local AI agents; single-owner deployments.
Agent-harness agnostic: built and verified with one agent harness first, but any harness that can run shell commands or call the REST API is a first-class consumer.

## MVP

Connect real institutions through Plaid, sync transactions/balances/liabilities into the canonical model, and let an agent answer net worth, spending, cash flow, debt, and upcoming-bill questions through the `moneta` CLI, with a REST mirror for non-CLI consumers.

## Current Priority

Phase 1 implementation and post-review hardening are complete, and the single-row poison sync blocker is resolved (skip-with-signal; see moneta-plan.md).
Production `moneta sync` ships on the library path (PR #2).
`moneta status`, `moneta accounts`, and `moneta tx` ship on the shared TOON/JSON output path (`internal/toon`, `internal/cli`).
The post-review hardening stack (`docs/phase2-review-fix-pr-plan.md`) is complete: confirmed ingest wedge paths closed, uniform exit codes, transfer-aware `tx` aggregates, durable skip and reauth state, TOON hardening.
Phase 2 continues with the remaining AXI read commands, then the REST mirror.

## Non-Goals

- Human-facing web UI in v1 (roadmap item, not current scope; see Milestones).
- Multi-user or hosted/SaaS operation.
- Investment holdings/positions in v1 (balances only; deferred by decision).
- Multi-currency in v1 (USD only; schema keeps a currency field).
- Telemetry or any third-party service beyond Plaid.

## Constraints

- Local-first: data leaves the machine only via the owner's CLI/API and outbound Plaid calls isolated inside the Plaid provider.
- Public repo with strict personal-data boundaries (see `docs/decisions/0006`).
- Single static CGO-free Go binary; minimal dependencies.
- Secrets via env vars only; Plaid tokens AES-GCM encrypted at rest, never logged or exposed.
- Agent ergonomics per the AXI principles: TOON output, pre-computed aggregates, truncation with escape hatches, no interactive prompts.

## Milestones

1. Phase 1 - schema + provider interface + Plaid provider, verified against Sandbox.
2. Phase 2 - AXI CLI + REST API, verified by an agent running real command flows.
3. Phase 3 - precomputed analytics views.
4. Phase 4 - recurring detection + anomaly detection.
5. Post-v1 - optional human web UI on top of the REST API, for viewing and reviewing finances without an agent.
