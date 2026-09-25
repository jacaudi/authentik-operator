# TASK-03: AuthentikApplication kind

## Ticket
- Ticket: none (Navigator-only)
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Parity with the application half of JeffResc's `AuthentikOAuth2Application` /
`AuthentikSAMLApplication`. Here the provider is a separate kind referenced by
name.

## Implementation Plan
### Phase 1: spec (parity)
- [ ] `name` (1-128 chars, required), `slug` (`^[a-z0-9-]+$`, default metadata.name)
- [ ] `providerRef` -- `{kind: AuthentikOAuthProvider|AuthentikSAMLProvider, name}`,
      same namespace; application waits (condition, not error loop) until the
      provider reports Ready
- [ ] `group`, `policyEngineMode` all|any (default any)
- [ ] `metaLaunchUrl` (must be http(s)), `metaDescription`, `metaPublisher`,
      `openInNewTab` (default true)
- [ ] `backchannelProviders` (beyond parity)
- [ ] Status: `applicationUid`, `slug`, `observedGeneration`, conditions

### Phase 2: behaviour
- [ ] Slug change = delete-and-recreate guarded by deletionPolicy (TASK-05);
      document it -- the slug is the application's identity in Authentik URLs.
- [ ] Watch referenced provider; re-reconcile when it changes.

## Dependencies
- Requires: TASK-01, TASK-02
- Blocks: TASK-04 (secret lives on the provider or application -- decide there), TASK-09

## Completion Checklist
- [ ] Unit + envtest coverage for defaults, validation, providerRef not-ready path
- [ ] CRD regenerated; sample in `config/samples/`
