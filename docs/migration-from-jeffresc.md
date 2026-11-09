# Migrating from JeffResc/authentik-k8s-operator

This operator uses the API group `authentik.io/v1alpha1`; JeffResc's uses
`goauthentik.io/v1alpha1`. The two can be installed side by side (no CRD
clash), but JeffResc's kinds do not convert in place: one JeffResc object
becomes three here.

| JeffResc (`goauthentik.io`) | This operator (`authentik.io`) |
|---|---|
| `AuthentikApplication` (v0.1.x) or `AuthentikOAuth2Application` (v0.2.x): application + provider + Secret in one object | `AuthentikOAuthProvider` + `AuthentikApplication` (same namespace) + a reference to a cluster-scoped `AuthentikCredentialsTemplate` |
| `AuthentikSAMLApplication` (v0.2.x) | no equivalent (no SAML provider kind); leave it on JeffResc |

`hack/convert-jeffresc` (`make convert-jeffresc` builds `bin/convert-jeffresc`)
does the conversion. Everything it emits adopts the provider and application
that JeffResc already created in Authentik, so client IDs and secrets do not
change and consuming apps keep working. The adoption contract (dry run,
`createIfMissing`, deletion policy, what adoption writes) is in
[scope.md, "API-object adoption"](design/scope.md#api-object-adoption-task-05);
this page does not repeat it.

Both CRD sets define kinds called `AuthentikApplication`, so once both are
installed `kubectl get authentikapplication` is ambiguous. **Always use the
fully qualified resource names below** (`authentikapplications.goauthentik.io`
for JeffResc, `authentikapplications.authentik.io` for this operator).

## The converter

```sh
make convert-jeffresc
bin/convert-jeffresc --out out/ jeffresc-backup.yaml          # files and/or directories, multi-document YAML or a List
```

Writes one object per file, `<kind-lowercase>-<name>.yaml`, deterministically
(no timestamps; `out/` can be committed), and prints a report on stderr.
It fails, writing nothing, when an object cannot be converted faithfully
(SAML kind, unknown JeffResc field, template variable or construct this
operator does not support, a template that would render differently).

| flag | default | meaning |
|---|---|---|
| `--out DIR` | required | output directory |
| `--dry-run-adoption` | `true` | `adoption.dryRun` on every provider and application |
| `--namespace NS` | each source's namespace | namespace of the converted objects; the template's `namespaceSelector` follows it |
| `--authentik-ref NAME` | none | `spec.authentikRef` (an `AuthentikDeployment` in the same namespace); omit to use the operator's default connection |
| `--template-name NAME` | `jeffresc-<hash>` | name of the `AuthentikCredentialsTemplate`; with several distinct templates it is a prefix (`NAME-<hash>`) |
| `--secret-suffix S` | none | appended to every rendered Secret name, so new and old Secrets can exist side by side (recommended, see step 4) |
| `--provider-suffix S` | none | appended to the `AuthentikOAuthProvider` resource names (default: same as the slug) |

Emitted per JeffResc object:

- `AuthentikOAuthProvider` named after the slug: `adoption: {enabled: true, dryRun: <flag>, match: {name: <slug>-provider}}`
  (JeffResc names the Authentik provider `<slug>-provider`), `spec.name:
  <slug>-provider`, `deletionPolicy: Retain`, every provider field mapped,
  `secret.name` and `secret.templateRef`. `createIfMissing` is never set.
- `AuthentikApplication` with `providerRef`, adoption enabled (same dry-run
  flag) and `deletionPolicy: Retain`.
- One cluster-scoped `AuthentikCredentialsTemplate` per distinct
  (template body, secret labels, secret annotations). Objects that share all
  three share one template, whose `namespaceSelector` is
  `kubernetes.io/metadata.name In [every namespace that uses it]`. No
  template, labels or annotations in JeffResc means no `templateRef`: the
  default Secret keys (`client-id`, `client-secret`, `issuer-url`,
  `authorization-url`, `token-url`, `userinfo-url`) are the same in both
  operators.

### Field map

JeffResc v0.1.8 and v0.2.5 have the same fields (v0.2.x only renames the kind
and adds validation). Fields JeffResc leaves unset take JeffResc's own CRD
defaults; the converter writes those defaults out so that adoption keeps the
live value.

| JeffResc field | New field | Notes |
|---|---|---|
| `metadata.name`, `metadata.namespace` | `AuthentikApplication` `metadata`; provider resource name = slug (+ `--provider-suffix`) | |
| `spec.name` | `AuthentikApplication.spec.name` | |
| `spec.slug` (default `metadata.name`) | `AuthentikApplication.spec.slug` | always written; new limit is 50 characters (JeffResc 128); the converter errors above that |
| `spec.group` | `AuthentikApplication.spec.group` | |
| `spec.policyEngineMode` (default `any`) | `AuthentikApplication.spec.policyEngineMode` | |
| `spec.metaLaunchUrl` | `AuthentikApplication.spec.metaLaunchUrl` | must be http(s) here; the converter errors otherwise |
| `spec.metaDescription`, `spec.metaPublisher` | same names | |
| `spec.openInNewTab` (default `true`) | `AuthentikApplication.spec.openInNewTab` | |
| `spec.provider.authorizationFlow`, `invalidationFlow` | same names (flow slugs) | |
| `spec.provider.redirectUris[]` (strings) | `redirectUris[].url` | JeffResc sent every URI as `strict` matching; here `matchingMode: strict`, `type: authorization` (the defaults) |
| `spec.provider.scopes` (default `openid, profile, email`) | `scopes` | same default; replaces the provider's scope mappings |
| `spec.provider.propertyMappings` | `propertyMappings` | |
| `spec.provider.clientType` (default `confidential`) | `clientType` | |
| `spec.provider.accessCodeValidity` (`minutes=1`), `accessTokenValidity` (`minutes=5`), `refreshTokenValidity` (`days=30`) | same names | same defaults |
| `spec.provider.subMode` (`hashed_user_id`), `includeClaimsInIdToken` (`true`), `issuerMode` (`per_provider`) | same names | same defaults |
| `spec.provider.signingKey` (default `authentik Self-signed Certificate`) | `signingKey` | this operator has no schema default (an adopted provider keeps its key); the converter writes JeffResc's default explicitly |
| `spec.secret.name` (default `<name>-oauth`) | `AuthentikOAuthProvider.spec.secret.name` (+ `--secret-suffix`) | |
| `spec.secret.template` | `AuthentikCredentialsTemplate.spec.template` | variables below |
| `spec.secret.labels`, `spec.secret.annotations` | `AuthentikCredentialsTemplate.spec.labels`, `.annotations` | keys under `authentik.io/` are reserved here |
| (none) | `grantTypes`, `refreshTokenThreshold`, `logoutMethod`, `authenticationFlow`, `encryptionKey`, `logoutUri`, `clientId`, `AuthentikApplication.metaIcon`, `metaHide` | new. Those with a CRD default are **written by adoption**: see below |
| `status.*` | n/a | not migrated; rebuilt from Authentik on adoption |
| `AuthentikSAMLApplication` | none | error |
| Secret label `goauthentik.io/application` | none | JeffResc sets it; this operator does not |
| Secret owner | the `AuthentikOAuthProvider` | JeffResc's Secret is owned by its CR; see step 4 |

**Defaults adoption will write that JeffResc has no field for** (listed per
object in the converter's report): `grantTypes: [authorization_code,
refresh_token]`, `refreshTokenThreshold: seconds=0`, `logoutMethod:
backchannel`, redirect `matchingMode: strict` / `type: authorization`, and
`metaHide: false` on the application. A drift test checks these against
`config/crd/bases`. They may differ from the live provider (for example
grant types someone changed in the UI): that is exactly what the dry run in
step 3 shows.

### Template variables

JeffResc executes secret templates with Go `text/template` over these
variables; each maps to the variable of the same name here.

| JeffResc | here |
|---|---|
| `.ClientID` `.ClientSecret` `.IssuerURL` `.AuthURL` `.TokenURL` `.UserInfoURL` | same |
| `.LogoutURL` `.JWKSURL` `.ProviderInfoURL` `.Slug` `.Name` | same |

Any other variable is an error that lists the unsupported names. This
operator also restricts templates to text, `{{ .Var }}`, `if`/`else`,
comparisons, `print` and `printf` with `%s`/`%q`/`%v`, rejects an unquoted
`{{ .Name }}` (quote it: `{{ printf "%q" .Name }}`), and refuses variable
values containing control characters. The converter renders each template with
both operators' pipelines on sample values and fails unless the Secret data
is identical, so it never emits a template that renders differently.

## Runbook (zero downtime, credentials preserved)

Do one application first, then the rest. Variables: `NS` is the application's
namespace.

### 1. Install this operator alongside

Install this operator (CRDs and manager) next to JeffResc's; the API groups
differ, so the CRDs do not clash. Give it access to Authentik, either with the
default connection (`--authentik-url`, `--authentik-token-file`) or with an
`AuthentikDeployment` (`mode: ConnectOnly`, `connection.tokenSecretRef`) in each
namespace plus `--authentik-ref` below; see
[scope.md, connection model](design/scope.md). Create the API token for it in
Authentik. Check that the install does not collide with JeffResc's objects
(`kubectl get deploy -A | grep -i authentik`).

Back up what you are about to change:

```sh
kubectl get authentikapplications.goauthentik.io,authentikoauth2applications.goauthentik.io -A -o yaml > jeffresc-backup.yaml
kubectl get secret -n NS <old secret names> -o yaml > jeffresc-secrets-backup.yaml
```

Rollback: uninstall this operator and its CRDs. Nothing has been adopted.

### 2. Convert; the platform admin applies the templates

```sh
bin/convert-jeffresc --out out/ --secret-suffix -v2 jeffresc-backup.yaml     # add --authentik-ref NAME if used
less out/*.yaml; stderr report: read it
```

`AuthentikCredentialsTemplate` is cluster-scoped and administrator-owned. A
platform admin reviews and applies those files first:

```sh
kubectl apply -f out/authentikcredentialstemplate-*.yaml
kubectl get authentikcredentialstemplates.authentik.io     # Ready=True
```

Rollback: `kubectl delete -f out/authentikcredentialstemplate-*.yaml`.

### 3. Adopt providers, then applications (dry run first)

The converted objects have `dryRun: true`. Providers first: an application
waits for its provider to be Ready.

```sh
kubectl apply -f out/authentikoauthprovider-*.yaml
kubectl -n NS get authentikoauthproviders.authentik.io
kubectl -n NS get authentikoauthproviders.authentik.io gitlab \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}{"\n"}{.status.conditions[?(@.type=="Ready")].message}{"\n"}'
```

Reason `AdoptionDryRun` lists each field that adoption would change as
`field: old -> new` (Authentik API field names). Edit the spec
(`kubectl edit`, or edit the file and re-apply) until the only changes listed
are ones you intend. Watch especially:

- `signing_key`: must not change unless you mean it (tokens would be signed by another key);
- `sub_mode`: changing it changes every user's `sub` and breaks existing sessions and account links;
- `scopes` / property mappings: adoption replaces the provider's mappings with
  `scopes` + `propertyMappings`; a mapping added in the UI would be dropped;
- `grantTypes`, `accessCodeValidity`, `accessTokenValidity`, `refreshTokenValidity`, `refreshTokenThreshold`, `logoutMethod`.

`AdoptionTargetNotFound` means no provider named `<slug>-provider` exists
(a name or slug typo, or the wrong Authentik): fix `adoption.match`; the
converter never sets `createIfMissing`. Then take it out of dry run:

```sh
kubectl -n NS patch authentikoauthproviders.authentik.io gitlab --type merge -p '{"spec":{"adoption":{"dryRun":false}}}'
kubectl -n NS get authentikoauthproviders.authentik.io gitlab    # PROVENANCE Adopted, READY True
```

Repeat with the applications (`authentikapplications.authentik.io`):
dry run, read `AdoptionDryRun`, then `dryRun: false`. An application still in
dry run against a provider in dry run shows `ProviderNotReady`.

While both operators run they both reconcile the same Authentik objects.
After a clean dry run they agree on every field that matters, but do not leave
this state longer than needed, and re-check the provider after both operators
have reconciled.

Rollback: before `dryRun: false`, delete the new objects (nothing was
written). After it, `deletionPolicy: Retain` makes deleting a new object
release the Authentik object without deleting it; fields adoption wrote stay
until JeffResc (still running) reconciles them back.

### 4. Confirm the new Secret is byte-identical, then point consumers at it

With `--secret-suffix -v2` the new operator renders `<old name>-v2` next to the
old Secret. Compare, key by key (`.data` is base64 of the exact bytes, so a
different key value or trailing newline shows; map order does not matter):

```sh
diff <(kubectl -n NS get secret gitlab-oidc-credentials -o json | jq -S .data) \
     <(kubectl -n NS get secret gitlab-oidc-credentials-v2 -o json | jq -S .data) && echo identical
```

The two Secrets have different labels and owners; only `.data` is compared.
Point consumers (Deployment `secretKeyRef`s, mounts, secret-sync tools) at the
`-v2` names and roll them. To get back to the original names, once the old
Secrets are gone (step 5) set `spec.secret.name` to the original name: the
operator creates it, then deletes `-v2`; point consumers back before that
second move.

Without `--secret-suffix` the new provider cannot render a Secret under the
old name while JeffResc's CR owns it: it reports `SecretConflict` and does not
overwrite it. The Secret is only freed when the old CR is deleted in step 5
(garbage collection removes it), and the new operator then creates it on its
next reconcile; pods started in between cannot find it. Prefer the suffix for
anything that is not tolerant of that gap.

Rollback: point consumers back at the old Secret names; the old Secrets are
untouched until step 5.

### 5. Retire the old CRs without deleting the Authentik objects

JeffResc deletes the Authentik provider and application when its CR is
deleted, **adopted or not**. So first stop it, then strip its finalizer, then
delete:

```sh
kubectl -n <old operator namespace> scale deploy/<old operator deployment> --replicas=0
for r in authentikapplications.goauthentik.io authentikoauth2applications.goauthentik.io; do
  kubectl get $r -A -o json | jq -r '.items[] | "\(.metadata.namespace) \(.metadata.name)"' | while read ns name; do
    kubectl -n "$ns" get $r "$name" -o json \
      | jq '.metadata.finalizers -= ["goauthentik.io/finalizer"]' | kubectl replace -f -
  done
done
# only now:
kubectl delete authentikapplications.goauthentik.io,authentikoauth2applications.goauthentik.io -A --all
```

Never scale the old operator back up while an unstripped CR is being deleted.
Deleting the old CRs also deletes the old Secrets (they are owned by them),
which is why consumers were moved first. Check that the objects survived:

```sh
kubectl -n NS get authentikoauthproviders.authentik.io,authentikapplications.authentik.io   # still READY True
```

and the applications still exist in Authentik.

Rollback: re-apply `jeffresc-backup.yaml` and scale the old operator up; it
finds the existing provider by its `<slug>-provider` name and manages it
again. Remove the new objects first or the two operators fight over the same
objects.

### 6. Uninstall the old chart and CRDs, verify

```sh
helm -n <old operator namespace> uninstall <old release>
kubectl delete crd authentikapplications.goauthentik.io authentikoauth2applications.goauthentik.io authentiksamlapplications.goauthentik.io
```

Delete the CRDs only after step 5: deleting a CRD deletes its CRs, and a CR
still carrying the finalizer would be processed by any running JeffResc
operator. Then verify SSO login for every application with a real user.
Optionally set `adoption.enabled: false` on the new objects later; `status.provenance`
stays `Adopted` and deleting them still retains the Authentik object unless
you set `deletionPolicy: Delete`.

Rollback: reinstall the old chart and CRDs and re-apply `jeffresc-backup.yaml`.

## Disaster recovery

Adoption needs the Authentik objects to exist. After losing the Authentik
database, `adoption.createIfMissing: true` (set deliberately, never by the
converter) lets a fresh provider be created, with new client credentials.
See [scope.md, "API-object adoption"](design/scope.md#api-object-adoption-task-05).
