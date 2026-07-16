# ADR 0001: Stdlib `flag` over cobra for the CLI

Date: 2026-07-16
Status: accepted

## Context

Moneta's CLI is consumed by AI agents following the AXI design principles: concise agent-oriented help, fail-loud on unknown flags, deterministic and non-interactive behavior, minimal cold-start.
The command surface is roughly 18 flat commands with no nesting.

## Decision

Use the standard library `flag` package with a small (~100 line) command router instead of cobra.

## Consequences

- Zero third-party dependencies in the CLI layer and minimal cold-start cost, which matters because every AXI command runs as a fresh process.
- We own the help text exactly, so `moneta help <cmd>` can stay terse and agent-shaped instead of cobra's verbose human-oriented output.
- `flag` errors on unknown flags by default, satisfying the AXI fail-loud requirement without configuration.
- We give up cobra's nested subcommands and shell completions, neither of which this tool needs.
