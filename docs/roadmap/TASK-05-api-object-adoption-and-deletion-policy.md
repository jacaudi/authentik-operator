# TASK-05: Adoption and deletion policy for Authentik API objects

## Ticket
- Ticket: none
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5 (required for any migration)

## Context
scope.md defines explicit, opt-in adoption for the Kubernetes objects an
`AuthentikDeployment` manages. The same contract is needed for **Authentik API
objects** (applications, providers, groups, users), and it is the thing that
makes migrating off JeffResc safe.

JeffResc has no adoption feature: if an app with the slug, or a provider named
`<slug>-provider`, already exists, it silently takes it over with a full PUT; on
CR delete it deletes both, adopted or not. No deletion policy.

Why adoption matters for migration: if the new operator recreates a provider,
Authentik issues a new client ID/secret and every consuming app breaks until its
credentials are rotated. Adopting the existing provider keeps them.

## Implementation Plan
- [ ] `spec.adoption.enabled` (default false) on every API kind, mirroring
      AuthentikDeployment. Without it, an existing object with the target
      identity is a `Conflict` condition, not an overwrite.
- [ ] `spec.adoption.match` -- how to find the existing object: application by
      slug; provider by name or pk (so JeffResc's `<slug>-provider` can be targeted
      explicitly).
- [ ] Provenance: Authentik objects cannot carry annotations, so record
      `status.provenance: Created|Adopted` (plus the object pk) and treat a CR
      with no status as "not yet decided" -- never infer Created on a fresh CR
      that found an existing object.
- [ ] Adopt = take over only fields the spec sets; leave others untouched
      (PATCH, not full PUT). Credentials of an adopted confidential provider are
      read from the API and rendered into the Secret unchanged.
- [ ] `spec.deletionPolicy: Delete|Retain` (default: Delete for Created,
      Retain for Adopted -- matches scope.md's release-vs-delete).
- [ ] Optional dry-run: `spec.adoption.dryRun: true` reports the diff it would
      apply in status/events without writing.

## Dependencies
- Requires: TASK-01, TASK-02, TASK-03
- Blocks: TASK-09

## Completion Checklist
- [ ] Tests: conflict without adoption; adoption preserves client_id/client_secret;
      Retain leaves the object on CR delete; dry-run writes nothing
- [ ] scope.md extended with the API-object adoption contract
