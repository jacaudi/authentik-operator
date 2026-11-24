# TASK-10: Beyond parity -- SAML, groups/users, AuthentikDeployment

## Ticket
- Ticket: none
- Status: In Progress (groups, users and application access shipped; SAML and
  the `AuthentikDeployment` Managed mode open)
- Milestone: post-parity

## Context
Items that JeffResc either has but parity does not cover yet (SAML), or does not have at
all (groups, users -- its issue #202 is open), plus this repo's own
`AuthentikDeployment` goal from scope.md.

## Implementation Plan
- [ ] `AuthentikSAMLProvider` (JeffResc parity for SAML users): authorizationFlow,
      invalidationFlow, acsUrl, issuer, audience, spBinding redirect|post,
      signingKeypair by name, digest/signature algorithms (sha256 / rsa-sha256
      defaults), propertyMappings by UUID or name; secret var `.Metadata`
- [x] `AuthentikGroup`: `name` (immutable once tracked), `isSuperuser`,
      `attributes` (owned per key; reserved keys refused), `members` as Authentik
      usernames (authoritative when present, absent means never read or
      written, `MemberHeld` while a user is being invited), the full adoption
      contract through `internal/adopt`, the identity gate, and a deletion guard
      that keeps a group any policy binding still uses (`DeletionBlocked`;
      `RetainedUnverifiable` for a non-superuser token). `parents` and `roles`
      are not fields: their live values are kept. Design:
      `docs/designs/2026-11-17-task-10-groups-users-design.md`; behaviour:
      scope.md, "Groups (TASK-10)"
- [x] `AuthentikUser` (people): `username`, `name`, `email`, `path`, `type`,
      `isActive`, `firstName`, `lastName`, per-key `attributes`; create means
      invite (a single-use invitation, the link in a Secret, the enrolled account
      recognised by an ownership proof, reissue only by raising `attempt`);
      adopt or observe an existing account; never a password; unset
      `deletionPolicy` is `Retain`. scope.md, "Users (TASK-10)"
- [x] `AuthentikApplication.spec.access` (required): `Groups` binds exactly the
      listed Authentik groups, `Unmanaged` writes nothing; `AccessInSync`,
      `AccessOpen`, `WaitingForGroups`, `AccessPending`. The JeffResc converter
      maps `groupBindings` to it and converts JeffResc `AuthentikGroup` objects
      (TASK-09). scope.md, "Application access (TASK-10)"
- [x] Identity gate: `--default-connection-identity-namespace-selector` (chart
      `identity.defaultConnectionNamespaceSelector`) for groups and users on the
      default connection. scope.md, "Identity gate (TASK-10)"
- [ ] `AuthentikDeployment` per scope.md (adoption contract already decided),
      including connect-only mode (see TASK-01 decision)
- [ ] Other provider types later: proxy (+ outposts), LDAP, RADIUS -- see
      [TASK-12](./TASK-12-outposts.md)
- [ ] Out of scope for CRDs: flows, stages, brands -- Authentik blueprints already
      express these declaratively (recovery flows, branding and login flows
      are commonly managed as blueprints). Possible thin `AuthentikBlueprint` kind that
      wraps a ConfigMap + BlueprintInstance status, if useful.

## Follow-ups (from the groups and users design, section 10)
- Migrate the provider and application controllers onto `internal/adopt` after
  v0.1.0, and pin the divergence found while designing groups: with undecided
  provenance, policy `Adopt` and a vanished object, the provider recreates and
  the application refuses. The move also settles the deliberate difference
  recorded in [TASK-05](./TASK-05-api-object-adoption-and-deletion-policy.md): an
  unproven pending create is a `Conflict` under every policy for groups and users.
- Machine accounts and their tokens (service accounts are refused today).
- An `AuthentikInvitation` kind, if invitations are wanted without users.
- `parents` on groups (setting it is itself an access grant).
- A recovery flow in the owner's blueprint, for a lost passkey without a new
  account (scope.md, "Users (TASK-10)", recommends one; the operator does not
  manage flows).
- Revoking an application's refresh tokens when someone is removed from an
  access group (today issued refresh tokens are not re-checked against the
  bindings; deactivate the user to cut them off).

## Dependencies
- Requires: parity milestone (TASK-01..TASK-09)
