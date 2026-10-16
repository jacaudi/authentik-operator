# TASK-12: Outposts (and the provider kinds that need them)

## Ticket
- Ticket: none (Navigator-only)
- Status: Planned
- Milestone: beyond-parity

## Context
Proxy, LDAP, RADIUS and RAC providers do nothing until an outpost serves
them. Outposts are Authentik objects (`/api/v3/outposts/instances/`) with a
type (proxy, ldap, radius, rac), a list of provider pks, an optional service
connection and a `config` map. Config keys include authentik_host,
authentik_host_browser, log_level, object_naming_template, refresh_interval
and the kubernetes_* keys: replicas, namespace, service type, ingress class,
annotations and secret, image pull secrets, disabled components, JSON
patches. These are from memory, so verify them against the pinned
`goauthentik.io/api/v3` before designing fields.

Every Authentik install also has a managed "authentik Embedded Outpost".
Assigning proxy providers to it is the most common small-install setup and needs no
extra workload.

JeffResc does not support outposts or non-OAuth/SAML providers, so this is
beyond parity (TASK-10 lists "proxy (+ outposts), LDAP, RADIUS").

## Key design question: who runs the outpost workload?
1. **Authentik's Kubernetes integration** (service connection). Authentik
   itself creates the Deployment, Service and Ingress. This needs Authentik
   to hold cluster RBAC, and Authentik becomes a second controller of those
   objects.
2. **The operator deploys it** ("manual" outpost, no service connection).
   The operator reads the outpost's API token from Authentik, writes it to an
   owned Secret, and renders the Deployment and Service itself, so Authentik
   needs no Kubernetes access. This fits the operator's model: declarative,
   with GitOps-visible objects.
3. **Embedded outpost only**: just manage provider assignment.

Leaning: support 3 first (cheapest, most common), then 2. Reference 1 only
through an explicit `serviceConnectionRef` for users who already run it; the
operator never creates service connections itself. Decide this before
implementing.

## Implementation Plan (draft)
- [ ] Provider kinds first: `AuthentikProxyProvider` (forward auth
      single/domain, proxy mode), then `AuthentikLDAPProvider`, then
      `AuthentikRADIUSProvider`. Each follows TASK-02's ownership pattern
      (pk plus a stable identity check, lost-create recovery, Conflict
      instead of take-over).
- [ ] `AuthentikOutpost` (namespaced): `type`, `providerRefs` (same
      namespace), `config` (typed fields for common keys plus an escape
      hatch), and `embedded: true` to manage provider assignment on the
      embedded outpost without owning it. Needs a merge rule so that
      providers added through the UI or by other CRs are not removed. Adopting
      the embedded outpost has to be explicit (ties into TASK-05).
- [ ] Workload mode (option 2): outpost token into an owned Secret (never in
      status), Deployment and Service for `ghcr.io/goauthentik/<type>` pinned
      to the Authentik server version, health from the outpost's
      `/outposts/instances/{uuid}/health/`.
- [ ] Ingress/forward-auth integration (Traefik middleware, ingress-nginx
      auth annotations, Gateway API): document first, CRDs maybe later.
- [ ] e2e against real Authentik (TASK-08 harness): proxy provider through
      the embedded outpost protecting a test app.

## Dependencies
- Requires: TASK-01..TASK-03 patterns, TASK-05 (adoption of the embedded
  outpost), TASK-08 harness
- Related: TASK-10 (other provider types)

## Completion Checklist
- [ ] Design decision on workload ownership recorded in scope.md
- [ ] Forward-auth login through an operator-managed proxy provider works in e2e
