# TASK-05: Adoption and deletion policy for Authentik API objects

## Ticket
- Ticket: none
- Status: Completed (revised after the PR 18 review, see Notes)
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
- [x] `spec.managementPolicy` (`Create` default | `Adopt` | `AdoptOrCreate` |
      `ReadOnly`) on every API kind with a controller (provider and
      application here; group and user followed in TASK-10). Under
      `Create` an existing object with the target identity is a `Conflict`
      condition, not an overwrite. Replaces the earlier `spec.adoption.*`
      booleans.
- [x] `spec.match` (providers) -- how to find the existing object, by name or
      pk (so JeffResc's `<slug>-provider` can be targeted explicitly); an
      application is matched by slug.
- [x] Provenance: Authentik objects cannot carry annotations, so record
      `status.provenance: Created|Adopted` (plus the object pk) and treat a CR
      with no status as "not yet decided" -- never infer Created on a fresh CR
      that found an existing object. A lost application create is `Created`
      only if it links to a provider this resource created.
- [x] Adopt = take over only fields the spec sets; leave others untouched
      (PATCH, not full PUT). The adoption-sensitive fields have no CRD default:
      the controller applies the defaults when it creates, and an unset field
      keeps the live value on an adopted object. Credentials of an adopted
      confidential provider are read from the API and rendered into the Secret
      unchanged.
- [x] `spec.deletionPolicy: Delete|Retain` (default: Delete for Created,
      Retain for Adopted or undecided -- matches scope.md's release-vs-delete;
      an explicit Delete wins and warns on undecided provenance).
- [x] Optional dry-run: `spec.dryRun: true` reports the diff it would apply in
      status/events without writing.
- [x] `ReadOnly`: finds the object, renders the Secret, reports drift in the
      `Drifted` condition, never writes or deletes.
- [x] Adoption trust boundary: Adopt, AdoptOrCreate, ReadOnly (and dryRun) on
      the default connection are refused (`AdoptionNotAllowed`) unless the
      namespace matches `--default-connection-adoption-namespace-selector`
      (empty denies all); an explicit `authentikRef` is allowed.
- [x] Duplicate claims: any holder (terminating ones included) refuses an
      adoption; two holders are ordered Created over Adopted, then lower UID;
      the claim re-check reads the API; the loser deletes its rendered Secret;
      connections are compared by normalized base URL.

## Dependencies
- Requires: TASK-01, TASK-02, TASK-03
- Blocks: TASK-09

## Completion Checklist
- [x] Tests: conflict under Create; adoption preserves client_id/client_secret;
      Retain leaves the object on CR delete; dry-run writes nothing; ReadOnly
      never writes; unset fields keep live values on adoption and take
      defaults on create; the gate; the tie-break and cache-lag race
- [x] scope.md extended with the API-object adoption contract and trust boundary

## Notes
- Implemented here for AuthentikOAuthProvider and AuthentikApplication.
  `AuthentikGroup` and `AuthentikUser` (TASK-10) follow the same contract
  (`APIManagementSpec`, `DeletionPolicy`, `Provenance`, claims and tie-break)
  through the pure `Decide` and `Finalize` of `internal/adopt`; the provider and
  application controllers are not moved onto it yet.
- Deliberate difference to settle when the provider and application move onto
  `internal/adopt`: for a group or user, a target found while the resource's own
  create is pending but unproven (no ownership proof) is a `Conflict` under every
  policy, `AdoptOrCreate` included, because it could be a squatter's object. The
  provider and application controllers keep the rules in scope.md, "Lost
  creates". The move also has to pin one divergence found while writing TASK-10:
  with undecided provenance, policy `Adopt` and a vanished object, the provider
  recreates and the application refuses.
- Contract: docs/design/scope.md, section "API-object adoption".
- Revised after the review of PR 18 (docs/refactor/pr-18-adoption-review.md):
  the `adoption.{enabled,createIfMissing,dryRun,match}` booleans became
  `managementPolicy` + `match` + `dryRun` before v1alpha1 ships; the earlier
  "PATCH only the fields the spec sets" criterion was not met while the CRD
  defaulted a dozen fields (now met); resources created before provenance
  existed are not backfilled (scope.md, "Upgrading a dev cluster").
- Known gap: the adoption gate is by policy; a resource switched from Adopt to
  Create after its namespace was un-allowed keeps managing what it adopted.
- Not covered by a real Authentik: the specs run against `fakeauthentik`; the
  e2e suite (TASK-08) is where adoption meets a live instance.
