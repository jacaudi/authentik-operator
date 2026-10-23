# TASK-03: AuthentikApplication kind

## Ticket
- Ticket: none
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
- [x] Effective slug is **immutable** (root CEL transition rule) until TASK-05
      adds deletionPolicy; changing it would mean delete-and-recreate,
      breaking /application/o/<slug>/ URLs.
- [x] Watch referenced provider (field index on spec.providerRef.name).

### Behaviour notes
- Identity: `status.applicationUid` (Authentik UUID) + `status.slug`; the pk
  is re-checked before every PATCH/DELETE, so an application replaced out of
  band (same slug, new pk) is a Conflict and is never modified or deleted.
  The uid is kept until a replacement exists, so a transient 404 cannot lose
  track of the application (or let the finalizer skip deletion).
- Lookups use retrieve-by-slug, not the list endpoint: the list filters by
  the token user's policy access, and `superuser_full_list` is only honoured
  for superusers.
- A pre-existing application with the same slug is a Conflict -- including
  one created by another AuthentikApplication with the same slug/provider.
  The only exception is `status.pendingCreate`: the slug is persisted there
  before every create and cleared on success or a definite 4xx, so the next
  reconcile can recover its own create whose outcome was lost (timeout,
  failed status write), with or without a provider.
- A provider already attached to another application is a Conflict
  ("application with this provider already exists"). A 400 saying the
  provider pk does not exist (provider being recreated) is reported as
  ProviderNotReady, not InvalidSpec.
- `authentikRef` is immutable on applications and providers (switching would
  leave the old object behind in the old Authentik).
- Effective slug (spec.slug or metadata.name) is immutable; spelling out the
  default later is allowed.
- Every managed field has a default (incl. metaIcon ""), so removing it from
  the spec resets it.
- Finalizer and connection-loss handling as for providers (last-known client
  fallback; fixable errors block deletion and are reported).
- Follow-ups: delete-then-check race in deleteOwnedApplication (GET pk, then
  DELETE by slug) is narrow and accepted; status.providerPk is not refreshed
  while waiting on the provider.

## Dependencies
- Requires: TASK-01, TASK-02
- Blocks: TASK-04 (secret lives on the provider or application -- decide there), TASK-09

## Completion Checklist
- [x] Unit + envtest coverage for defaults, validation, providerRef not-ready path
- [x] CRD regenerated; sample in `config/samples/`
