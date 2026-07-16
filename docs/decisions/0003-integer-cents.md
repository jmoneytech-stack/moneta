# ADR 0003: Money as signed integer cents

Date: 2026-07-16
Status: accepted

## Context

Floating-point money accumulates rounding drift, and financial aggregates (sums, deltas, utilization) must be exact.
Go has no decimal type in the standard library, and adding a decimal dependency conflicts with the minimal-dependency goal.

## Decision

Store and compute all monetary amounts as signed `int64` cents, everywhere internally and in SQLite.
Negative values mean outflow.
Convert to dollar strings only at the output boundary (TOON/JSON rendering).

## Consequences

- Arithmetic is exact and comparisons are trivial; no float equality hazards.
- Providers must convert source amounts (Plaid decimals, CSV strings) to cents at the adapter boundary.
- USD-only is assumed in v1; a `currency` field exists on canonical records, so multi-currency remains representable later without a schema break.
