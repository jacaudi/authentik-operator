# TASK-03: AuthentikApplication kind

## Ticket
- Ticket: none (Navigator-only)
- Status: Completed (backchannelProviders deferred)
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Parity with the application half of JeffResc's `AuthentikOAuth2Application` /
`AuthentikSAMLApplication`. Here the provider is a separate kind referenced by
name.

## Implementation Plan
### Phase 1: spec (parity)
- [x] `name` (1-255, required), `slug` (Authentik's `^[-a-zA-Z0-9_]+$`, max 50,
      default metadata.name; root CEL rejects a name that is not a valid slug
      unless spec.slug is set)
- [x] `providerRef` -- `{kind: AuthentikOAuthProvider (default), name}`, same
      namespace, optional; waits with ProviderNotFound / ProviderNotReady
      (condition + slow requeue, re-triggered by a provider watch); refuses a
      provider on a different `authentikRef` (ProviderConnectionMismatch).
      SAML kind arrives with TASK-10.
- [x] `group`, `policyEngineMode` all|any (default any)
- [x] `metaLaunchUrl` (must be http(s)), `metaDescription`, `metaPublisher`,
      `metaIcon` (managed only when set), `openInNewTab` (default true),
      `metaHide` (default false)
- [ ] `backchannelProviders` (beyond parity) -- deferred
- [x] Status: `applicationUid`, `slug`, `providerPk`, `observedGeneration`, Ready

### Phase 2: behaviour
- [x] Slug is **immutable** (CEL) until TASK-05 adds deletionPolicy; changing
      it would mean delete-and-recreate, breaking /application/o/<slug>/ URLs.
- [x] Watch referenced provider (field index on spec.providerRef.name).

### Behaviour notes
- Identity: `status.applicationUid` (Authentik UUID) + `status.slug`; the pk
  is re-checked before every PATCH/DELETE, so an application replaced out of
  band (same slug, new pk) is a Conflict and is never modified or deleted.
- Lists use `superuser_full_list=true`; without it Authentik hides
  applications the token's user has no policy access to.
- A pre-existing application with the same slug is a Conflict, except one
  that already holds this resource's provider: a provider belongs to at most
  one application, so that is our own create whose outcome was lost.
  Known gap: a lost create of an application **without** a provider becomes
  a Conflict (no ownership marker available); TASK-05 adoption recovers it.
- A provider already attached to another application is a Conflict
  ("application with this provider already exists").
- Finalizer and connection-loss handling as for providers (last-known client
  fallback; fixable errors block deletion and are reported).

## Dependencies
- Requires: TASK-01, TASK-02
- Blocks: TASK-04 (secret lives on the provider or application -- decide there), TASK-09

## Completion Checklist
- [x] Unit + envtest coverage for defaults, validation, providerRef not-ready path
- [x] CRD regenerated; sample in `config/samples/`
