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
      YAML and emitting AuthentikOAuthProvider + AuthentikApplication (one object
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
  no longer written out. The converter only copies what JeffResc set.
- Runbook and field map: `docs/migration-from-jeffresc.md`. Finding that shaped
  it: this operator refuses to overwrite a Secret it does not own, and
  JeffResc's Secret is owned by its CR, so the runbook renders the new Secrets
  under a suffix (`--secret-suffix`) to compare and cut over without a gap.

## Dependencies
- Requires: TASK-02..TASK-05, TASK-08
- Blocks: first release recommended to JeffResc users

## Completion Checklist
- [ ] Runbook exercised end to end in e2e (old operator installed, migrated,
      removed, login still works with unchanged client credentials)
