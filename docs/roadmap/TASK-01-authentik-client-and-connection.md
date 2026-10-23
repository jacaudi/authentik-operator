# TASK-01: Authentik API client and connection model

## Ticket
- Ticket: none
- Status: Completed
- Milestone: parity-with-jeffresc-v0.2.5

## Context
Every API kind depends on this. The operator we are replacing
(`JeffResc/authentik-k8s-operator` v0.2.5, "JeffResc" below) reads `AUTHENTIK_URL`
and `AUTHENTIK_TOKEN` from the environment, sends a Bearer token, retries 3x with
a 10 req/s rate limit, and has **no TLS/CA option** (system roots only). The
original Rust operator (see `docs/design/scope.md`) used plaintext HTTP to a
hardcoded address and had no pagination. Both are defects to not repeat.

Typical installs point the operator at the in-cluster Authentik Service
(e.g. `http://authentik-server.authentik.svc.cluster.local`) with the token in
a Secret.

## Implementation Plan
### Phase 1: client
- [x] `internal/authentik/` wrapping the official generated client
      `goauthentik.io/api/v3` (JeffResc uses it; pins track Authentik releases).
      Pinned: `goauthentik.io/api/v3 v3.2026080.3` <-> Authentik 2026.8.3.
- [x] Pagination on every list call (`ListAll` follows `pagination.next`).
- [x] Typed errors: NotFound only on 404 (the Rust original mapped 400 to NotFound).
      `APIError` carries status + truncated body; `IsNotFound`, `IsConflict`, `StatusCode`.
- [x] Retry with backoff on 429/5xx; client-side rate limit (parity: 10 rps, burst 20).
      3 retries, jittered exponential backoff, honours `Retry-After`. 5xx and transport
      errors are retried only for idempotent methods (never replay a POST that may
      have applied); 429 is retried for any method. 30s per-attempt timeout.
- [x] Lookups by stable identifier (`FlowBySlug`, `ScopeMappingByScopeName`,
      `ScopeMappingByManaged`, `CertificateByName`, `PropertyMappingByName`,
      `PropertyMappingByUUID`; exact match required, >1 match is `ErrAmbiguous`): flows by slug, scope mappings by `scope_name`
      (or managed name), certificates by name, property mappings by UUID or name.
      Never by display name alone (renames orphan CRs -- upstream defect).

### Phase 2: connection model -- DECIDED: (b) with (a) as fallback (see scope.md)
Options -- pick one and record it in `docs/design/scope.md`:
- (a) Operator-level connection (URL, token secretRef, CA bundle) via flags/env,
      like JeffResc. Simplest; one Authentik per operator.
- (b) Per-CR `spec.authentikRef` naming an `AuthentikDeployment`, which gains a
      **connect-only mode** (manages no workloads; supplies URL, token secretRef, CA).
      Supports several Authentiks; reuses scope.md adoption item 3 (supplied token).
- Recommendation: (b) with (a) as a default when `authentikRef` is omitted --
  dual-mode, so a Helm-installed Authentik (the common case) needs no
  `AuthentikDeployment` managing its pods.
- [x] `spec.authentikRef` on all API kinds; `AuthentikDeployment.spec.mode`
      (`Managed` | `ConnectOnly`, required) + `spec.connection`; operator default via
      `--authentik-url` / `AUTHENTIK_URL`, `--authentik-token-file` / `AUTHENTIK_TOKEN`,
      `--authentik-ca-file`. Resolver: `internal/connection`.
- [x] Token always from a Secret ref; never logged, never written to CR spec/status.
- [x] TLS: optional CA bundle (ConfigMap/Secret ref) and `insecureSkipVerify` only
      behind an explicit field; plain HTTP allowed only when the URL says http.
- [x] Health check: per-`AuthentikDeployment` `Connected` condition (users/me),
      rather than operator readiness -- one operator may serve several Authentiks.

## Technical Decisions
- Official client over hand-rolled HTTP: the API drifts every release
  (Group.parent -> parents, token_validity split); regenerate rather than chase.

## Dependencies
- Blocks: TASK-02, TASK-03, TASK-04, TASK-05, TASK-08.

## Completion Checklist
- [x] Unit tests with an httptest fake (pagination, 400 vs 404, retry, auth header, TLS/CA, rate limit)
- [x] envtest suite still green; `go vet`, `make lint` clean
- [x] scope.md updated with the connection decision
