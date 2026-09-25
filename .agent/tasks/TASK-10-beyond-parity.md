# TASK-10: Beyond parity -- SAML, groups/users, AuthentikDeployment

## Ticket
- Ticket: none (Navigator-only)
- Status: Planned
- Milestone: post-parity

## Context
Items that JeffResc either has but orac does not use (SAML), or does not have at
all (groups, users -- its issue #202 is open), plus this repo's own
`AuthentikDeployment` goal from scope.md.

## Implementation Plan
- [ ] `AuthentikSAMLProvider` (JeffResc parity for SAML users): authorizationFlow,
      invalidationFlow, acsUrl, issuer, audience, spBinding redirect|post,
      signingKeypair by name, digest/signature algorithms (sha256 / rsa-sha256
      defaults), propertyMappings by UUID or name; secret var `.Metadata`
- [ ] `AuthentikGroup`: name, `parents` (list -- API changed from `parent`; the
      Rust original lost data here), attributes, superuser flag, members by
      user ref
- [ ] `AuthentikUser`: username, name, email, type, path, groups, attributes,
      is_active; never manage passwords; service accounts + token output to a
      Secret
- [ ] `AuthentikDeployment` per scope.md (adoption contract already decided),
      including connect-only mode (see TASK-01 decision)
- [ ] Other provider types later: proxy (+ outposts), LDAP, RADIUS
- [ ] Out of scope for CRDs: flows, stages, brands -- Authentik blueprints already
      express these declaratively (orac manages its recovery flow, branding and
      login flow as blueprints). Possible thin `AuthentikBlueprint` kind that
      wraps a ConfigMap + BlueprintInstance status, if useful.

## Dependencies
- Requires: parity milestone (TASK-01..TASK-09)
