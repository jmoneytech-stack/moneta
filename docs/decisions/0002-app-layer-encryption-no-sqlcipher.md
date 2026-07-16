# ADR 0002: Application-layer secret encryption, no SQLCipher

Date: 2026-07-16
Status: accepted

## Context

Plaid `access_token`s must be encrypted at rest, never logged, and never exposed through any response.
Moneta ships as a single static, cross-compiled binary using pure-Go SQLite (`modernc.org/sqlite`), which has no native at-rest encryption.
SQLCipher would provide whole-database encryption but requires CGO, which breaks the clean four-target cross-compile matrix.

## Decision

Encrypt secrets (Plaid access tokens) at the application layer with AES-256-GCM, key supplied via the `MONETA_ENCRYPTION_KEY` env var (32-byte base64).
Do not adopt SQLCipher.

## Consequences

- The binary stays CGO-free and cross-compiles cleanly for macOS/Linux, amd64/arm64.
- The highest-value secret is protected even if the database file leaks.
- Transaction data at rest relies on disk-level encryption (e.g., FileVault), which is the norm for local-first tools.
- Whole-database encryption remains a possible deliberate follow-up decision; it will not be added silently.
