# ADR 0006: Public repo with strict personal-data boundaries

Date: 2026-07-16
Status: accepted

## Context

Moneta is built in public, and the repo is public from its first commit.
The maintainer develops against their own real financial data, and git history is permanent once pushed, so a single leaked commit cannot be fully retracted.

## Decision

Keep a hard boundary between the public repo and personal data:

- All private material (data exports, personal category mappings, local config, notes) lives in gitignored `.local/` or in the local database, never in the repo.
- Committed docs, examples, and test fixtures use the neutral default taxonomy and fake data only.
- Commits use a GitHub noreply git identity, enforced by repo-local git config and GitHub's email-privacy push protection.
- No real email address appears anywhere in the repo; security contact goes through GitHub private vulnerability reporting.

## Consequences

- No history scrubbing should ever be needed; prevention replaces cleanup.
- Contributors and agents get realistic-shaped fake fixtures rather than real data.
- Docs and examples must be written public-safe from the start, which costs a little care per change and removes an entire class of risk.
