# TASK-09: Migration path from JeffResc (goauthentik.io) CRs

## Ticket
- Ticket: none
- Status: In Progress
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Different API group (`goauthentik.io/v1alpha1` -> `authentik.io/v1alpha1`) and
split kinds mean no in-place upgrade. JeffResc itself renamed
`AuthentikApplication` -> `AuthentikOAuth2Application` in v0.2.0, so users may
hold either. Target: existing JeffResc OAuth2 users (chart 0.1.x and 0.2.x).

## Implementation Plan
- [x] Field map document (`docs/migration-from-jeffresc.md`): every JeffResc
      field -> provider/application field, defaults that differ, dropped fields
- [x] Converter: `hack/convert-jeffresc` (Go, testable) reading
      AuthentikApplication / AuthentikOAuth2Application / AuthentikSAMLApplication
      (refused) / AuthentikGroup (TASK-10) YAML and emitting
      AuthentikOAuthProvider + AuthentikApplication, or AuthentikGroup (one object
      per file, `<kind>-<name>.yaml`), plus one cluster-scoped
      AuthentikCredentialsTemplate per distinct (template, labels,
      annotations) tuple, with a `namespaceSelector` on
      `kubernetes.io/metadata.name` listing the source namespaces, referenced via
      `spec.secret.templateRef`. Templates are admin-owned (TASK-04), so the
      runbook has the platform admin apply them. Also: `managementPolicy: Adopt`,
      `match` targeting `<slug>-provider`, `deletionPolicy: Retain`; on the
      default connection the platform admin also allows the namespace with
      `--default-connection-adoption-namespace-selector` (an `authentikRef` needs
      no allowance)
- [x] Runbook, zero-downtime, credentials preserved:
  1. install new operator alongside the old (different group, no CRD clash)
  2. apply converted CRs with adoption (+ dryRun first); confirm Ready and that
     the rendered Secret is byte-identical to the old one
  3. point consumers (workloads, secret-sync tools) at the new Secret names if they differ
  4. remove old CRs -- NOT before removing the old operator's finalizer
     behaviour: JeffResc deletes adopted and created objects alike on CR delete.
     Scale the old operator to 0, strip `goauthentik.io/finalizer` from its CRs,
     then delete them, so the Authentik objects survive
  5. uninstall the old chart and CRDs
- [x] Converter tests against representative real-world manifests (golden files)

## Notes
- Converter: `internal/migrate/jeffresc` (+ `hack/convert-jeffresc`); golden
  fixtures in `internal/migrate/jeffresc/testdata`, refreshed with
  `go test ./internal/migrate/... -run Golden -update`. Output is also decoded
  strictly into the typed structs and created in an envtest apiserver.
- Fields JeffResc left unset stay unset in the output: adoption keeps the live
  value of any field the spec does not set (TASK-05), so JeffResc's defaults are
  no longer written out. The converter copies what JeffResc set, and always
  writes `grantTypes`: JeffResc's list (v0.2.7+, deduplicated) or its default
  pair when absent or empty. JeffResc v0.2.6 and older never sent it (#279), so
  adoption would keep an empty live value, which Authentik 2026.5+ rejects every
  login for. Providers back-filled with all 7 grant types (Authentik migration
  0032) are narrowed to the pair, as JeffResc v0.2.7+ does itself.
- Input range: JeffResc 0.1.8 through v0.2.10, decoded strictly.
  `groupBindings` (v0.2.8+) converts to the required `spec.access` (TASK-10):
  a non-empty list becomes `access: {type: Groups, groups: [...]}`, an absent or
  empty one `access: {type: Unmanaged}`. The `--accept-unmanaged-group-bindings`
  flag no longer exists (passing it is a "flag provided but not defined" error).
- JeffResc `AuthentikGroup` (v0.2.8+) converts to an `AuthentikGroup` with
  `managementPolicy: Adopt`, `deletionPolicy: Retain` and no `members`;
  reserved and object-valued attributes, `parents` and `roles` stay in Authentik,
  unmanaged, and the report lists them. On the default connection the identity
  selector must allow the group's namespace as well as the adoption selector.

## Known gaps (open follow-ups, not designed here)
- `AuthentikGroup` (JeffResc v0.2.8+) is closed: it is converted, and runbook
  step 4a sets `deletionPolicy: Retain` on the JeffResc group before deleting it
  (JeffResc's default `Delete` would delete the group and cascade away its
  application bindings).
- JeffResc v0.2.9+ ownership: v0.2.9 binds ownership to status IDs, so the
  runbook's rollback (re-apply a status-stripped backup) can fail with
  `errNotOwned`. The migration e2e covers JeffResc 0.1.8 only.
- Runbook and field map: `docs/migration-from-jeffresc.md`. Finding that shaped
  it: this operator refuses to overwrite a Secret it does not own, and
  JeffResc's Secret is owned by its CR, so the runbook renders the new Secrets
  under a suffix (`--secret-suffix`) to compare and cut over without a gap.

## Dependencies
- Requires: TASK-02..TASK-05, TASK-08
- Blocks: first release recommended to JeffResc users

## Completion Checklist
- [x] Runbook exercised end to end in e2e (old operator installed, migrated,
      removed, login still works with unchanged client credentials): scenario 9
      of `test/e2e`, green locally on Authentik 2026.2.3 and 2026.8.5; runs
      the runbook's marked scripts (`<!-- e2e:... -->`), so a runbook edit that
      breaks them fails the suite
