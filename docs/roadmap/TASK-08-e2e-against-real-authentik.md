# TASK-08: End-to-end tests against a real Authentik

## Ticket
- Ticket: none
- Status: In Progress (suite written and green locally on Authentik 2026.2.3 and 2026.8.5; the CI
  matrix has not run on GitHub; see "Progress")
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Unit tests with a fake API cannot catch Authentik API drift -- the reason the
Rust original silently lost data and JeffResc #279 shipped. e2e must run the
real server.

## Implementation Plan
- [x] Extend `test/e2e` (kind) to install the official Authentik Helm chart at
      pinned versions: an older supported release (2026.2.3) AND latest
      (2026.8.5). The version is `AUTHENTIK_CHART_VERSION`; the operator is
      installed from `dist/chart` (which therefore runs against a real
      Authentik too). PostgreSQL is a plain Deployment (the chart's Bitnami
      subchart is avoided); Redis is not used by either pinned release.
- [x] Bootstrap an API token non-interactively (`authentik.bootstrap_token` /
      `bootstrap_password` chart values, i.e. `AUTHENTIK_BOOTSTRAP_TOKEN` /
      `AUTHENTIK_BOOTSTRAP_PASSWORD`, present in both pinned releases'
      `blueprints/system/bootstrap.yaml`); readiness is "the token
      authenticates", not pod readiness (the worker creates it asynchronously)
- [ ] Scenarios (`test/e2e/e2e_test.go`; helpers in `test/aktest`):
  - [x] OAuthProvider + Application + Secret; assert rendered keys for a
        typical template (`.ClientID .ClientSecret .IssuerURL .AuthURL
        .TokenURL .UserInfoURL`) and that the values equal Authentik's own
  - [x] a full OIDC authorization-code login succeeds (guards #279 grant_types;
        headless, PKCE, id_token `iss`/`aud`/`nonce` checked, userinfo called)
  - [x] adoption of a pre-created app + `<slug>-provider` keeps client_id/secret
        byte for byte (`managementPolicy: Adopt`): dry run (Authentik unchanged,
        no Secret), real adoption that leaves a field the spec does not set at
        its live value, login with the adopted credentials; an Adopt miss
        creates nothing. The chart installs with
        `adoption.defaultConnectionNamespaceSelector` for the test namespaces
        (the default-connection adoption gate)
  - [x] Retain vs Delete on CR deletion, defaults and explicit overrides
        (Created+Retain keeps, Adopted+Delete deletes)
  - [x] drift: change a provider and an application field in Authentik, the
        operator restores them (triggered by annotating the CR, not by
        waiting out the five-minute resync)
  - [x] second reconcile writes nothing (see the TASK-02 note): measured as
        Authentik's own audit events (filtered and counted server side) plus
        the operator's "Updated" log lines (Kubernetes Events are not used:
        identical ones fold into one series), each with a positive control;
        every forced reconcile is waited for via the controller metrics
  - [x] custom CA: the operator's default connection is pointed at an
        nginx TLS terminator with a per-run private CA; refuses without
        `authentik.caBundle` (x509, nothing created), works with it
  - [x] JeffResc migration end to end (TASK-09): JeffResc 0.1.8 (chart fetched
        at the tag's commit, GPL so not vendored; image by digest) installed in
        `authentik-operator-system`, this operator in `authentik-operator`,
        converter + runbook steps 1 to 7, with the runbook's own gated script
        executed. Pre-migration login works on 2026.2 and fails on 2026.8+
        (#279, `invalid_request`), the post-migration login works on both
- [ ] Run in CI on PRs (matrix over Authentik versions): workflow written in
      `test-e2e.yml` (actionlint and zizmor clean); has not run on GitHub

## Progress
Ran locally (kind cluster `ao-e2e`, operator built with podman and loaded as an
image archive; Authentik pulled by the cluster):

| Authentik | Result |
|---|---|
| 2026.2.3 | 26 specs pass, about 280 s after the images are present |
| 2026.8.5 | 26 specs pass, about 328 s |

Operator bugs this suite found against a real 2026.2.3 (both fixed, with unit
tests built from responses recorded from that server):

1. The generated client tracks 2026.8 and rejects responses missing a property
   that release added: every Application reconcile failed on `pbm_uuid` and every
   credentials Secret on `dcr_registration`. Fixed by a response shim in
   `internal/authentik/compat.go`.
2. `grantTypes` was PATCHed on every reconcile (and an `Updated` event emitted)
   because 2026.2 has no `grant_types` property to compare with. Fixed in
   `oauth2ProviderPatch`: not reported means nothing to compare.

Runbook bugs found by running it (fixed in the doc): both charts name their
ClusterRole after the release, so this operator must not be released as
`authentik-operator` beside JeffResc's; and the step 5 script used one variable
for the old operator's namespace and the old CR's.

Gotchas for anyone extending the suite:
- An `Eventually` given a context has no timeout; pass `WithTimeout` (see
  `waitTimeout`).
- Authentik answers a flow-executor POST with a 302 to the same URL; the next
  challenge is on the GET (handled in `aktest`).
- The suite refuses to run unless kubectl's current context is
  `kind-<KIND_CLUSTER>`.

## Dependencies
- Requires: TASK-01..TASK-05
- Blocks: TASK-07 release gate, TASK-09

## Notes from TASK-02 review
- Besides "login works", assert that a second reconcile of every kind issues
  **no writes** against each tested Authentik version (2026.2 and >= 2026.8).
  A field a version omits or normalizes differently (e.g. `grant_types`,
  `refresh_token_threshold`, `redirect_uri_type`, duration strings) would
  otherwise cause a PATCH and an `Updated` event every resync while Ready
  stays True.
- Verify the generated client decodes 2026.2 responses (required-property
  checks, enum values); a decode failure on create is recovered by the
  derived client ID, but on reads it would block reconciliation.

## Completion Checklist
- [ ] CI matrix green on both Authentik versions (cannot be ticked from a local run)
