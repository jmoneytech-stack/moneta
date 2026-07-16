# ADR 0004: Internal TOON encoder package

Date: 2026-07-16
Status: accepted

## Context

AXI prescribes TOON (Token-Oriented Object Notation) on stdout for ~40% token savings over JSON.
Moneta only ever encodes TOON; it never parses it.
No mature, official TOON library is assumed to exist for Go, and betting a core output path on a young third-party package is a maintenance risk.

## Decision

Implement a small internal `internal/toon` package covering the spec subset Moneta emits: scalars, nested objects, and `[N]{fields}:` tabular arrays with spec-conformant quoting.
Validate with golden-file tests against examples from the official TOON spec.
Internal logic stays on Go structs; TOON conversion happens only at the stdout boundary.

## Consequences

- Zero dependency risk on the primary agent-facing output path.
- The encoder is small (roughly 100-200 lines) because encode-only avoids the hard half of the spec.
- If an official Go TOON library matures, swapping it in is a contained change behind one package boundary.
