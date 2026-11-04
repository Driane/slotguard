# 1. Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

slotguard makes deliberate trade-offs (consistency over availability, database constraints over application checks, idempotency keys on every write). Those choices need to be readable later, with the reasons that led to them.

## Decision

Every significant architecture decision is recorded as an Architecture Decision Record in `docs/adr/`, numbered in order, with its context, the decision and its consequences.

## Consequences

- A reviewer can understand why the code looks the way it does without asking.
- A decision can be revisited by writing a new ADR that supersedes the old one, never by silently changing the code.
