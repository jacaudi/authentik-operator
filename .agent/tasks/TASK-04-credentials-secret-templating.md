# TASK-04: Credentials Secret with Go-template rendering

## Ticket
- Ticket: none (Navigator-only)
- Status: Planned
- Milestone: parity-with-jeffresc-v0.2.5

## Context
This is the feature orac's gitops actually depends on. Each of its four apps
(gitlab, grafana, litellm, vault) renders client credentials plus endpoint URLs
into an app-specific Secret, e.g.

```yaml
secret:
  name: gitlab-oidc-credentials
  template: |
    OIDC_CLIENT_ID: {{ .ClientID }}
    OIDC_CLIENT_SECRET: {{ .ClientSecret }}
```

and ExternalSecrets (Kubernetes-provider ClusterSecretStore) copy them to the
consuming namespace. Variables in use there: `.ClientID .ClientSecret .IssuerURL
.AuthURL .TokenURL .UserInfoURL`.

JeffResc semantics to match (`internal/template/secret.go`):
- `text/template` with `missingkey=error`, no sprig; output parsed as a YAML map
  of string -> string
- OAuth2 vars: `.ClientID .ClientSecret .IssuerURL .AuthURL .TokenURL
  .UserInfoURL .LogoutURL .JWKSURL .ProviderInfoURL .Slug .Name`
- SAML vars: `.Metadata .Slug .Name`
- Defaults when no template: keys client-id, client-secret, issuer-url,
  authorization-url, token-url, userinfo-url (SAML: metadata)
- Secret name default `<cr>-oauth` / `<cr>-saml`; `labels`, `annotations`
- Written to the CR's namespace only; Opaque; owned by the CR (GC on delete);
  labels `app.kubernetes.io/managed-by=authentik-operator` and
  `<group>/application=<cr>`
- Renaming `secret.name` deletes the old Secret; unchanged data is not rewritten

## Implementation Plan
- [ ] Decide which kind carries `spec.secret`: provider (credentials belong to the
      provider) is recommended; application-level URLs (`.Slug`, launch URL) are
      then read via the application that references it.
- [ ] Implement vars + defaults above, identical names, so migrated templates
      render byte-identical output (TASK-09 relies on this).
- [ ] URLs derived from issuer mode + host; configurable external base URL,
      since the in-cluster API URL is NOT what clients should see (orac hit this:
      Grafana/Vault need the browser-facing host).
- [ ] Credentials are never stored in CR spec or status (status may hold clientId).
- [ ] Optional: rotate client secret on an annotation, with the Secret updated
      atomically.
- [ ] Validation: template must parse (admission webhook, TASK-06) and render.

## Security and RBAC (findings from the TASK-01 review)

Writing credential Secrets is the first time the operator needs Secret
**write**. Today (post TASK-01) it holds only cluster-wide `get` on Secrets,
plus a required opt-in label (`authentik.io/connection: "true"`) on any
Secret an `AuthentikDeployment` references. Adding cluster-wide
create/update/delete changes the blast radius from "read a Secret whose name
you know" to "overwrite or plant any Secret in any namespace", which enables
things like replacing a TLS key or an ExternalSecrets source. Decide RBAC
here, not later.

Two separate threats, two separate controls:

| threat | control | enforced by |
|---|---|---|
| a user misuses the operator (confused deputy): makes it read or overwrite a Secret they can't touch themselves | opt-in label / ownership checks in the operator | operator code |
| the operator itself is compromised, or its SA token leaks | least-privilege RBAC (per-namespace Roles) | API server |

The label does not cover the second row: an attacker holding the SA token
talks to the API directly.

How Crossplane handles this (for comparison; verify details against current
Crossplane docs):
- Credentials live on a `ProviderConfig`. It was historically cluster-scoped
  (admin-only); v2 adds a namespaced `ProviderConfig` alongside
  `ClusterProviderConfig`. Tenants reference it by name
  (`providerConfigRef`), so the credential-bearing object is admin-owned.
  There is no opt-in label on Secrets.
- Providers are granted broad, cluster-wide Secret access by the RBAC
  manager, largely to write connection details
  (`writeConnectionSecretToRef`, the direct analogue of this task).
  Per-namespace RBAC is not the default there either.
- Takeaway: the "admin-owned connection object referenced by tenants"
  pattern (a cluster-scoped `ClusterAuthentikConnection`) is the structural
  alternative to the label. It would reopen scope.md's "all kinds
  namespaced" decision, so consider it only if untrusted tenants become a
  requirement.

Decisions to make in this task:
- [ ] **Write scope.** The operator writes Secrets only in the CR's own
      namespace (already JeffResc semantics). Keep that invariant and test it.
- [ ] **Never overwrite a foreign Secret.** Before updating an existing Secret
      at the target name, require it to be controller-owned by this CR (or
      carry `authentik.io/created-by`). Otherwise fail with a condition rather
      than take it over, which matches the opt-in adoption principle. This
      closes the "point `secret.name` at another app's Secret" deputy attack.
- [ ] **Verbs.** create/update/patch/delete on Secrets. Owner references mean
      GC handles deletion, so check whether `delete` is needed at all (it is,
      for the rename-deletes-old-Secret behaviour).
- [ ] **Per-namespace RBAC as an install option.** Ship an alternative
      kustomize overlay / Helm value that replaces the ClusterRole Secret rules
      with a ClusterRole bound per namespace via RoleBinding (namespace owners
      opt in). Keep cluster-wide as the default (simple homelab installs), and
      document the trade-off. The resolver already maps Forbidden to condition
      reason `Forbidden`; do the same for writes.
- [ ] **No cache for Secrets.** Keep using the API reader for Secret reads.
      If the controller needs to react to its own Secrets being edited or
      deleted, watch them with a label selector (`cache.Options.ByObject`) on
      `app.kubernetes.io/managed-by=authentik-operator` instead of cluster-wide.
      Note: this still needs list/watch RBAC on Secrets. Weigh against
      periodic requeue.
- [ ] **Secret contents never in logs, events or status** (clientSecret
      especially); status may hold clientId only.

## Dependencies
- Requires: TASK-02, TASK-03
- Blocks: TASK-08, TASK-09

## Completion Checklist
- [ ] Golden-file tests: each of the four orac templates renders exactly as today
- [ ] missingkey error surfaces as a TemplateError condition, not a crash loop
