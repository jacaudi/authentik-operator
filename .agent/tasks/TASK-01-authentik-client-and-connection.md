# TASK-01: Authentik API client and connection model

## Ticket
- Ticket: none (Navigator-only; project_management = none)
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Every API kind depends on this. The operator we are replacing
(`JeffResc/authentik-k8s-operator` v0.2.5, "JeffResc" below) reads `AUTHENTIK_URL`
and `AUTHENTIK_TOKEN` from the environment, sends a Bearer token, retries 3x with
a 10 req/s rate limit, and has **no TLS/CA option** (system roots only). The
original Rust operator (see `docs/design/scope.md`) used plaintext HTTP to a
hardcoded address and had no pagination. Both are defects to not repeat.

Downstream consumer: orac gitops (`clusters/orac/foundation/authentik-operator/`)
points the current operator at `http://authentik-server.authentik.svc.cluster.local`
with the token in Secret `authentik-operator-token` key `token`.

## Implementation Plan
### Phase 1: client
- [ ] `internal/authentik/` wrapping the official generated client
      `goauthentik.io/api/v3` (JeffResc uses it; pins track Authentik releases).
      Pin to the Authentik version under test; record the pairing in docs.
- [ ] Pagination on every list call.
- [ ] Typed errors: NotFound only on 404 (the Rust original mapped 400 to NotFound).
- [ ] Retry with backoff on 429/5xx; client-side rate limit (parity: 10 rps, burst 20).
- [ ] Lookups by stable identifier: flows by slug, scope mappings by `scope_name`
      (or managed name), certificates by name, property mappings by UUID or name.
      Never by display name alone (renames orphan CRs -- upstream defect).

### Phase 2: connection model (DECISION REQUIRED)
Options -- pick one and record it in `docs/design/scope.md`:
- (a) Operator-level connection (URL, token secretRef, CA bundle) via flags/env,
      like JeffResc. Simplest; one Authentik per operator.
- (b) Per-CR `spec.authentikRef` naming an `AuthentikDeployment`, which gains a
      **connect-only mode** (manages no workloads; supplies URL, token secretRef, CA).
      Supports several Authentiks; reuses scope.md adoption item 3 (supplied token).
- Recommendation: (b) with (a) as a default when `authentikRef` is omitted --
  dual-mode, so a Helm-installed Authentik (orac's case) needs no
  `AuthentikDeployment` managing its pods.
- [ ] Token always from a Secret ref; never logged, never written to CR spec/status.
- [ ] TLS: optional CA bundle (ConfigMap/Secret ref) and `insecureSkipVerify` only
      behind an explicit field; plain HTTP allowed only when the URL says http.
- [ ] Health check endpoint used for readiness (JeffResc: 60s grace period).

## Technical Decisions
- Official client over hand-rolled HTTP: the API drifts every release
  (Group.parent -> parents, token_validity split); regenerate rather than chase.

## Dependencies
- Blocks: TASK-02, TASK-03, TASK-04, TASK-05, TASK-08.

## Completion Checklist
- [ ] Unit tests with an httptest fake (pagination, 400 vs 404, retry, auth header)
- [ ] envtest suite still green; `go vet`, `make lint` clean
- [ ] scope.md updated with the connection decision
