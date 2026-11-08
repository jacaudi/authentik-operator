# TASK-06: Reconcile semantics, observability, validation

## Ticket
- Ticket: none
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5

## Context
JeffResc v0.2.5 behaviour to match or exceed:
- finalizer; requeue every 5m (`--requeue-interval`), exponential backoff to 10m;
  template errors not requeued; 2 concurrent reconciles; leader election
- conditions: only `Ready` (reasons Succeeded, AuthentikError, SecretError,
  TemplateError, DeletionFailed); `observedGeneration`
- Events: Normal Synced/Deleted; Warning ProviderError, ApplicationError,
  SecretError, TemplateError, AuthentikError
- metrics: `authentik_operator_reconcile_total{crd,result}`,
  `authentik_operator_reconcile_duration_seconds`, API-call metrics
- readiness: Authentik health check, 60s grace
- optional validating webhook (cert-manager) that only checks the template renders
- optional event webhook: registers a transport + notification rule in Authentik;
  any event re-enqueues ALL CRs (bearer shared secret, pre-delete cleanup Job)

## Implementation Plan
- [ ] Finalizers per kind; deletion honours TASK-05 policy
- [ ] Conditions: `Ready`, `Synced`, `Conflict`, `Degraded` with reasons above
- [ ] Events + metrics at parity names (dashboards/alerts written against
      JeffResc keep working); add per-kind API error counters
- [ ] Configurable resync interval, concurrency, backoff
- [ ] CRD validation via CEL/OpenAPI (patterns, enums, MaxLength) -- most of
      JeffResc's webhook checks move here, no cert-manager needed
- [ ] Optional validating webhook only for what CEL cannot do (template render)
- [ ] Beyond parity: event-driven resync that enqueues only the affected CR
      (map Authentik model + pk to the owning CR), not a full re-list
- [ ] `--watch-namespaces` (JeffResc watches all namespaces with cluster-wide
      Secret CRUD -- scope RBAC down when set)

## Dependencies
- Requires: TASK-01..TASK-03
- Blocks: TASK-07 (chart knobs), TASK-08

## Completion Checklist
- [ ] Metrics documented; sample PrometheusRule in `config/prometheus/`
- [ ] envtest covers finalizer, backoff, conditions
