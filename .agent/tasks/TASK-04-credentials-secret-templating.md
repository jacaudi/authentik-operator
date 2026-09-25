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

## Dependencies
- Requires: TASK-02, TASK-03
- Blocks: TASK-08, TASK-09

## Completion Checklist
- [ ] Golden-file tests: each of the four orac templates renders exactly as today
- [ ] missingkey error surfaces as a TemplateError condition, not a crash loop
