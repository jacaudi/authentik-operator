# TASK-04: Credentials Secret with Go-template rendering

## Ticket
- Ticket: none
- Status: Completed (per-namespace RBAC overlay, secret rotation and template webhook are follow-ups)
- Milestone: parity-with-jeffresc-v0.2.5

## Context
This is the feature most JeffResc users depend on: each application renders
client credentials plus endpoint URLs into an app-specific Secret shaped for
the consuming app (e.g. its environment variable names), e.g.

```yaml
secret:
  name: myapp-oidc-credentials
  template: |
    OIDC_CLIENT_ID: {{ .ClientID }}
    OIDC_CLIENT_SECRET: {{ .ClientSecret }}
```

Consumers mount the Secret directly or copy it to another namespace with a
secret-sync tool. The most common variables: `.ClientID .ClientSecret
.IssuerURL .AuthURL .TokenURL .UserInfoURL`.

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

**Deliberate deviation from JeffResc**: the template, labels and annotations
are not inline in the provider spec. They live on a cluster-scoped,
administrator-owned `AuthentikCredentialsTemplate` that the provider names
in `spec.secret.templateRef`. See "Admin-owned templates" below. The TASK-09
converter therefore emits one template per distinct inline template.

## Implementation Plan
- [x] `spec.secret` lives on **AuthentikOAuthProvider**. `.Slug`/`.Name` come
      from the provider's `assigned_application_slug/name` in Authentik; the
      provider watches AuthentikApplications so attaching one re-renders
      promptly. Until an application is attached: `WaitingForApplication`.
- [x] Vars + defaults with identical names (`internal/credentials`):
      text/template, `missingkey=error` (data passed as a map so the option
      applies), no function library, output parsed with yaml.v3 into
      map[string]string (scalars become strings), keys validated as Secret
      keys. Byte-identical parity with JeffResc is asserted by TASK-09's
      golden tests; JeffResc's source was not available here.
- [x] URLs: taken from Authentik's own `setup_urls` (correct for issuer mode,
      slug and Authentik version), then the operator's API base URL prefix is
      rewritten to the browser-facing URL: `connection.externalURL` on
      AuthentikDeployment, `--authentik-external-url` /
      `AUTHENTIK_EXTERNAL_URL` for the default connection.
- [x] Credentials are never stored in CR spec or status (status holds clientId
      and secretName only); template/YAML errors never echo values.
- [ ] Optional: rotate client secret on an annotation -- follow-up.
- [ ] Validation: template parse at admission needs a webhook (TASK-06);
      today errors surface as `TemplateError` (event once per change).

## Security and RBAC (findings from the TASK-01 review)

Writing credential Secrets is the first time the operator needs Secret
**write**. Today (post TASK-01) it holds only cluster-wide `get` on Secrets,
plus a required opt-in label (`authentik.io/connection: "true"`) on any
Secret an `AuthentikDeployment` references. Adding cluster-wide
create/update/delete changes the blast radius from "read a Secret whose name
you know" to "overwrite or plant any Secret in any namespace", which enables
things like replacing a TLS key or a secret-sync source. Decide RBAC
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
- [x] **Write scope.** Secrets are written only in the CR's own namespace
      (the name is the only input; namespace is always the CR's).
- [x] **Never overwrite a foreign Secret.** An existing Secret must be
      controlled (ownerReference, controller=true) by this CR, else
      `SecretConflict` and no write. Rename/removal deletes the previous
      Secret only if still controlled by this CR. Tested.
- [x] **Verbs.** get/create/update/patch/delete on Secrets (delete for
      rename/removal; GC handles CR deletion). Still no list/watch.
- [ ] **Per-namespace RBAC as an install option.** Follow-up PR (kustomize
      overlay now, Helm value with TASK-07). Ship an alternative
      kustomize overlay / Helm value that replaces the ClusterRole Secret rules
      with a ClusterRole bound per namespace via RoleBinding (namespace owners
      opt in). Keep cluster-wide as the default (simple single-tenant installs), and
      document the trade-off. The resolver already maps Forbidden to condition
      reason `Forbidden`; do the same for writes.
- [x] **No cache for Secrets.** Reads use the API reader; no watch, so an
      out-of-band edit or deletion is repaired on the 5-minute resync (tested).
      Unchanged data is not rewritten.
- [x] **Secret contents never in logs, events or status** (tested).

## Admin-owned templates (decided after the first review)

The first version put `template`, `labels` and `annotations` inline in
`spec.secret`. That made provider write equal to Secret write in that
namespace: any data, and any non-reserved labels or annotations. We had no
users yet, so instead of documenting this we split the personas the way
Crossplane does. Platform engineers own XRDs and Compositions; tenants own
claims and fill in only what the platform exposes.

- `AuthentikCredentialsTemplate` (cluster-scoped) holds `template`,
  `labels`, `annotations` and a required `namespaceSelector` (`{}` = all
  namespaces, so this is never the default by accident).
- A provider's `spec.secret` holds only `name` and `templateRef`. Without
  `templateRef`, it gets the default keys and no extra metadata.
- A small reconciler validates each template by rendering it with example
  values, and reports the result in the template's `Ready` condition
  (`Valid` / `TemplateError` / `InvalidMetadata` / `InvalidNamespaceSelector`). Administrators
  see their mistakes on the template, not on every provider using it.
- New provider `CredentialsReady` reasons: `TemplateNotFound` and
  `TemplateNotAllowed` (and `InvalidNamespaceSelector`). On these, the
  template's labels and annotations are removed from the existing Secret and
  its data is kept. A template spec change (generation, not status)
  re-renders every provider that
  references it (watch plus list; no field index, since templates change
  rarely).
- RBAC added: templates `get;list;watch` and status `update;patch`;
  namespaces `get`. The namespace is read through the API reader, uncached.
- This does not change what a compromised operator could do. It still has
  cluster-wide Secret write, so per-namespace RBAC remains the follow-up for
  that.

### Review findings addressed (independent review of the template split)
- **Blocking, fixed**: YAML key injection through `spec.clientId`, e.g.
  `"x\nEXTRA: y"`. `clientId` is now printable ASCII without spaces
  (admission), and `Render` refuses any variable that contains control
  characters, including values from Authentik. The error names the variable,
  never the value. The sample quotes values with `printf "%q"`.
- `namespaceSelector` is required (no default-open). Its doc and scope.md
  explain that it is only as strong as control over namespace labels, and
  recommend `kubernetes.io/metadata.name`.
- Revocation withdraws the template's labels and annotations; see above.
- The template reconciler validates labels and annotations with the API
  server's own rules (`InvalidMetadata`). `Check` also renders with a
  YAML-hostile application name, so an unquoted `.Name` fails on the
  template, with a hint.
- `GenerationChangedPredicate` on the template watch; no Namespace GET for
  `{}`; same `InvalidNamespaceSelector` reason on template and provider.
- The reserved-prefix rule on templates is narrowed to `authentik.io/`:
  admins legitimately set `app.kubernetes.io/*`, and the operator's own keys
  win anyway.
- Accepted: TemplateNotFound/NotAllowed discloses template names. A label key
  that both a template and another controller set is removed with the
  template's key (documented on `mergeMetadata`).
- Tests added: templateRef removal, template switch, template deletion,
  revocation stripping (idempotent), invalid selector on the provider,
  `clientId` admission, metadata validation, `Check` unit tests.

## Review findings addressed (independent review of the first commit)
- **Deadlock fixed**: Secret state moved to its own `CredentialsReady`
  condition (SecretSynced / WaitingForApplication / TemplateError /
  SecretConflict / SecretInvalid / ExternalURLMismatch). `Ready` describes
  only the provider in Authentik, so applications (which gate on Ready) can
  attach and the Secret renders afterwards. Tested with both reconcilers on
  real CRs.
- **Template DoS closed**: templates are restricted by a parse-tree walk to
  text, `{{ .Var }}`, if/else, comparisons, print and width-free printf
  (`%s %q %v`); range/with/define/template/variables are rejected before
  execution; output capped at 1 MiB.
- `.ClientSecret` may only be printed (no comparisons/conditions), so render
  success/failure cannot be used as an oracle on it.
- Error messages never echo rendered content (invalid/duplicate keys are
  reported by entry and line).
- Reserved Secret label/annotation prefixes (`authentik.io/`,
  `kubernetes.io/`, `k8s.io/` and subdomains) rejected by CEL -- blocks
  self-opting a rendered Secret into `authentik.io/connection`.
- Rename deletes the old Secret only after the new one exists.
- Permanent API rejections -> `SecretInvalid`, no backoff loop; provider name
  moved from a label to an annotation (no 63-char limit); default-name length
  and DNS-subdomain name pattern enforced by CEL.
- Labels/annotations merged, not replaced: keys added by mutators are kept,
  keys removed from the spec are removed (tracked in
  `authentik.io/applied-metadata`).
- URL rewrite parses URLs (case-insensitive host, default ports, subpaths);
  a mismatch is `ExternalURLMismatch` instead of silently writing internal
  URLs. External URL flag rejects userinfo/query/fragment; URL patterns
  reject whitespace.
- Application display names reject control characters (no newline key
  injection); docs recommend `{{ printf "%q" .Name }}`.
- Multi-document output rejected; deletes use UID/resourceVersion
  preconditions; empty issuer/authorize/token -> WaitingForApplication.

Accepted/documented, not changed:
- ~~Creating/editing an AuthentikOAuthProvider is equivalent to creating
  Secrets in that namespace~~: superseded by admin-owned templates (above).
  Provider write now means "create a Secret with an unused name holding this
  provider's credentials, in an administrator-defined shape".
- SecretConflict vs success reveals whether a Secret name exists.
- The provider watch on AuthentikApplications has no predicate (extra API
  calls on application status writes); revisit with TASK-06 metrics.

## Dependencies
- Requires: TASK-02, TASK-03
- Blocks: TASK-08, TASK-09

## Completion Checklist
- [ ] Golden-file tests: representative JeffResc templates render exactly as
      they do there -- moved to TASK-09
- [x] missingkey error surfaces as a TemplateError condition, not a crash loop
