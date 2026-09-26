# TASK-08: End-to-end tests against a real Authentik

## Ticket
- Ticket: none (Navigator-only)
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Unit tests with a fake API cannot catch Authentik API drift -- the reason the
Rust original silently lost data and JeffResc #279 shipped. e2e must run the
real server.

## Implementation Plan
- [ ] Extend `test/e2e` (kind) to install the official Authentik Helm chart at
      pinned versions: current orac version (2026.2.x) AND latest (>= 2026.8)
- [ ] Bootstrap an API token non-interactively for the test
- [ ] Scenarios:
  - [ ] OAuthProvider + Application + Secret; assert rendered keys for the
        orac template shape (`.ClientID .ClientSecret .IssuerURL .AuthURL
        .TokenURL .UserInfoURL`)
  - [ ] a full OIDC authorization-code login succeeds (guards #279 grant_types)
  - [ ] adoption of a pre-created app + `<slug>-provider` keeps client_id/secret
  - [ ] Retain vs Delete on CR deletion
  - [ ] drift: change a field in Authentik, operator restores it
  - [ ] custom CA: Authentik behind TLS with a private CA
- [ ] Run in CI on PRs (matrix over Authentik versions)

## Dependencies
- Requires: TASK-01..TASK-05
- Blocks: TASK-07 release gate, TASK-09

## Completion Checklist
- [ ] CI matrix green on both Authentik versions
