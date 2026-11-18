# Migrating from JeffResc/authentik-k8s-operator

This operator uses the API group `authentik.io/v1alpha1`; JeffResc's uses
`goauthentik.io/v1alpha1`. The two can be installed side by side (no CRD
clash), but JeffResc's kinds do not convert in place: one JeffResc object
becomes three here.

| JeffResc (`goauthentik.io`) | This operator (`authentik.io`) |
|---|---|
| `AuthentikApplication` (v0.1.x) or `AuthentikOAuth2Application` (v0.2.x): application + provider + Secret in one object | `AuthentikOAuthProvider` + `AuthentikApplication` (same namespace) + a reference to a cluster-scoped `AuthentikCredentialsTemplate` |
| `AuthentikSAMLApplication` (v0.2.x) | no equivalent (no SAML provider kind); leave it on JeffResc |
| `AuthentikGroup` (v0.2.8+) | no equivalent (no group kind); leave it on JeffResc, or set its `deletionPolicy: Retain` and then delete it. The converter refuses it |

**Supported JeffResc versions:** the converter understands the input fields of
JeffResc 0.1.8 through v0.2.10 (the 0.1.x tags have no `v` prefix). It decodes
strictly, so a field added in a later release is rejected until the converter
learns it. Known gaps for the newest releases are listed in
[TASK-09](roadmap/TASK-09-migration-from-jeffresc.md).

`hack/convert-jeffresc` (`make convert-jeffresc` builds `bin/convert-jeffresc`)
does the conversion. Everything it emits adopts the provider and application
that JeffResc already created in Authentik (`managementPolicy: Adopt`), so
client IDs and secrets do not change and consuming apps keep working. The
adoption contract (management policies, dry run, the adoption gate, deletion
policy, what adoption writes) is in
[scope.md, "API-object adoption"](design/scope.md#api-object-adoption-task-05);
this page does not repeat it.

**The operator admin must allow the namespaces being migrated.** On the
operator's default connection `Adopt` is refused (reason `AdoptionNotAllowed`)
unless the resource's namespace matches the operator flag
`--default-connection-adoption-namespace-selector`, set in the chart as
`adoption.defaultConnectionNamespaceSelector` (empty, the default, denies every
namespace). With `spec.authentikRef` (converter flag `--authentik-ref`) the
objects use a same-namespace `AuthentikDeployment` instead, and no allowance is
needed. The converter's report prints the selector for the namespaces it
converted. Step 1 shows how to set it.

Both CRD sets define kinds called `AuthentikApplication`, so once both are
installed `kubectl get authentikapplication` is ambiguous. **Always use the
fully qualified resource names below** (`authentikapplications.goauthentik.io`
for JeffResc, `authentikapplications.authentik.io` for this operator).

## The converter

```sh
make convert-jeffresc
bin/convert-jeffresc --out out/ jeffresc-backup.yaml          # files and/or directories, multi-document YAML or a List
# re-running into a non-empty --out needs --force
```

Writes one object per file, `<kind-lowercase>-<name>.yaml`, deterministically
(no timestamps; `out/` can be committed), and prints a report on stderr.
It fails, writing nothing, when an object cannot be converted faithfully
(SAML or `AuthentikGroup` kind, unknown JeffResc field, a `groupBindings` entry
`spec.access.groups` cannot hold, template variable or construct this
operator does not support, a template that would render differently).

| flag | default | meaning |
|---|---|---|
| `--out DIR` | required | output directory |
| `--dry-run-adoption` | `true` | `spec.dryRun` on every provider and application: preview what adoption would change, write nothing |
| `--namespace NS` | each source's namespace | namespace of the converted objects; the template's `namespaceSelector` follows it |
| `--authentik-ref NAME` | none | `spec.authentikRef` (an `AuthentikDeployment` in the same namespace); omit to use the operator's default connection |
| `--template-name NAME` | `jeffresc-<hash>` | name of the `AuthentikCredentialsTemplate`; with several distinct templates it is a prefix (`NAME-<hash>`) |
| `--secret-suffix S` | none | appended to every rendered Secret name, so new and old Secrets can exist side by side (recommended, see step 4) |
| `--force` | off | write into a non-empty `--out` |
| `--provider-suffix S` | none | appended to the `AuthentikOAuthProvider` resource names (default: same as the slug) |

### groupBindings become spec.access

JeffResc v0.2.8+ creates a group binding in Authentik for each entry of
`spec.groupBindings` and deletes the others, so the list is an access
allowlist. `AuthentikApplication.spec.access` is the same allowlist here, and it
is required, so every converted application carries one:

- A non-empty `groupBindings` becomes `access: {type: Groups, groups: [...]}`,
  in input order with a name listed twice kept once (the report says so). The
  operator then binds exactly those Authentik groups and removes the bindings
  of other groups, adding before removing and removing nothing while a listed
  group does not exist. The groups must exist in Authentik (they do, JeffResc
  bound them); manage them with `AuthentikGroup` or leave them where they are.
- An absent or empty `groupBindings` becomes `access: {type: Unmanaged}`: the
  operator never writes a binding for that application. Its live bindings are
  unknown offline, so the converted file carries a header comment saying so;
  with `--dry-run-adoption` (the default) the adoption dry run in the Ready
  condition lists them ("also grants access"), without it `status.access.bindings`
  does once applied.
- JeffResc `policyEngineMode: all` together with two or more distinct
  `groupBindings` would require membership of every group, which the CRD
  refuses for `spec.access`. The converter then leaves `policyEngineMode` out:
  adoption keeps the live `all`, behaviour is unchanged, and the application
  reports `AccessInSync=False` with reason `AccessNarrowed`. The report names the
  application. Set `policyEngineMode: any` after the migration if each group
  should grant access on its own.

Turn off the Authentik instance setting "Applications with no policies bound can
be accessed by any user" (2026.5+): without it, an application with no enabled
binding is open to everyone, and the `AccessOpen` condition says when that is
the case. The operator never changes the setting.

Emitted per JeffResc object:

- `AuthentikOAuthProvider` named after the slug: `managementPolicy: Adopt`,
  `dryRun: <flag>`, `match: {name: <slug>-provider}` (JeffResc names the
  Authentik provider `<slug>-provider`), `spec.name: <slug>-provider`,
  `deletionPolicy: Retain`, every provider field JeffResc sets, `secret.name`
  and `secret.templateRef`. The converter never emits `AdoptOrCreate`: a
  typo in the name must not create a provider with new credentials.
- `AuthentikApplication` with `providerRef`, `access` (see above),
  `managementPolicy: Adopt` (same dry-run flag) and `deletionPolicy: Retain`.
- One cluster-scoped `AuthentikCredentialsTemplate` per distinct
  (template body, secret labels, secret annotations). Objects that share all
  three share one template, whose `namespaceSelector` is
  `kubernetes.io/metadata.name In [every namespace that uses it]`. No
  template, labels or annotations in JeffResc means no `templateRef`: the
  default Secret keys (`client-id`, `client-secret`, `issuer-url`,
  `authorization-url`, `token-url`, `userinfo-url`) are the same in both
  operators.

### Field map

JeffResc 0.1.8 and v0.2.5 have the same fields (note that the 0.1.x tags have
no `v` prefix); v0.2.x renames the kind, adds validation and parses rendered
Secret templates differently (see the template section). v0.2.7 adds
`spec.provider.grantTypes` and v0.2.8 adds `spec.groupBindings` and the
`AuthentikGroup` kind; the table covers everything up to v0.2.10. Fields JeffResc leaves
unset take JeffResc's own CRD defaults (listed in the table). The converter
**leaves those fields unset**: adoption writes only the fields the spec sets and
keeps the live value of the others, and the live object already has JeffResc's
default. Fields JeffResc sets are copied, so the new operator holds them to
those values. One difference to know about: JeffResc held every field, defaulted
or not, to its default on each reconcile, whereas a converted object holds only
the fields it names. To keep holding a defaulted field, add it to the spec.

| JeffResc field | New field | Notes |
|---|---|---|
| `metadata.name`, `metadata.namespace` | `AuthentikApplication` `metadata`; provider resource name = slug (+ `--provider-suffix`) | |
| `spec.name` | `AuthentikApplication.spec.name` | |
| `spec.slug` (default `metadata.name`) | `AuthentikApplication.spec.slug` | always written; new limit is 50 characters (JeffResc 128); the converter errors above that |
| `spec.group` | `AuthentikApplication.spec.group` | |
| `spec.policyEngineMode` (default `any`) | `AuthentikApplication.spec.policyEngineMode` | copied when set; unset stays unset |
| `spec.metaLaunchUrl` | `AuthentikApplication.spec.metaLaunchUrl` | must be http(s) here; the converter errors otherwise |
| `spec.metaDescription`, `spec.metaPublisher` | same names | |
| `spec.openInNewTab` (default `true`) | `AuthentikApplication.spec.openInNewTab` | copied when set; unset stays unset |
| `spec.provider.authorizationFlow`, `invalidationFlow` | same names (flow slugs) | |
| `spec.provider.redirectUris[]` (strings) | `redirectUris[].url` | JeffResc sent every URI as `strict` matching; `matchingMode` and `type` are left unset, so an adopted URI keeps the live values of the same `url` |
| `spec.provider.scopes` (default `openid, profile, email`) | `scopes` | copied when set (the spec then owns the provider's whole scope-mapping list); unset stays unset, so the live mappings are kept. An explicit `scopes: []` with `propertyMappings` is emitted as `scopes: []` (only those mappings); with no `propertyMappings` JeffResc leaves the live mappings alone, so `scopes` is omitted. When several scope mappings share a scope name JeffResc takes the first result and this operator prefers the managed (built-in) mapping, which can show up as a `property_mappings` diff in the dry run |
| `spec.provider.propertyMappings` | `propertyMappings` | |
| `spec.provider.clientType` (default `confidential`) | `clientType` | copied when set; unset stays unset |
| `spec.provider.accessCodeValidity` (`minutes=1`), `accessTokenValidity` (`minutes=5`), `refreshTokenValidity` (`days=30`) | same names | copied when set; unset stays unset |
| `spec.provider.subMode` (`hashed_user_id`), `includeClaimsInIdToken` (`true`), `issuerMode` (`per_provider`) | same names | copied when set; unset stays unset |
| `spec.provider.signingKey` (default `authentik Self-signed Certificate`) | `signingKey` | copied when set; unset, or an explicit `signingKey: ""`, stays unset (the live key is kept) |
| `spec.secret.name` (default `<name>-oauth`) | `AuthentikOAuthProvider.spec.secret.name` (+ `--secret-suffix`) | |
| `spec.secret.template` | `AuthentikCredentialsTemplate.spec.template` | variables below |
| `spec.secret.labels`, `spec.secret.annotations` | `AuthentikCredentialsTemplate.spec.labels`, `.annotations` | keys under `authentik.io/` are reserved here |
| `spec.provider.grantTypes` (v0.2.7+; default and empty: `authorization_code, refresh_token`) | `grantTypes` | **always written**: the list copied without duplicates, else `[authorization_code, refresh_token]`; see below |
| `spec.groupBindings` (v0.2.8+) | `AuthentikApplication.spec.access.groups` | `access: {type: Groups, groups: [...]}`; `access: {type: Unmanaged}` when absent; see "groupBindings become spec.access" |
| (none) | `refreshTokenThreshold`, `logoutMethod`, `authenticationFlow`, `encryptionKey`, `logoutUri`, `clientId`, `AuthentikApplication.metaIcon`, `metaHide` | new; left unset, so adoption keeps the live values |
| `status.*` | n/a | not migrated; rebuilt from Authentik on adoption |
| `AuthentikSAMLApplication`, `AuthentikGroup` | none | error |
| Secret label `goauthentik.io/application` | none | JeffResc sets it; this operator does not |
| Secret owner | the `AuthentikOAuthProvider` | JeffResc's Secret is owned by its CR; see step 4 |

**Fields JeffResc has no field for are not written, except `grantTypes`.** This
operator's own defaults (`refreshTokenThreshold`, `logoutMethod`, redirect
`matchingMode` and `type`, `metaHide`) apply only when this operator creates an
object. Adoption never applies them, so a value someone changed in the UI
survives. Set one of these in the spec if you want this operator to enforce it;
the dry run in step 3 then shows the change.

`grantTypes` is the exception. Authentik 2026.5 and later rejects a login whose
grant type is not in the provider's `grant_types`, and JeffResc v0.2.6 and
older never sent the field (its issue #279), so a provider they created on
2026.5+ has none and rejects every login ("Invalid grant_type for provider").
JeffResc v0.2.7+ always sends it, from `spec.provider.grantTypes`, treating an
empty list as `[authorization_code, refresh_token]`. Adoption keeps the live
value of an unset field, which would carry the empty list over, so the converter
always writes `grantTypes`: JeffResc's list when it has one (duplicates
removed), else that pair. The report says which, per provider. Servers before
2026.5 do not report the property and the operator ignores it. Delete the field
from a converted spec to keep the live grant types as they are.

**This can narrow a provider.** Providers that existed before the Authentik 2026.5
upgrade were back-filled with all seven grant types (Authentik migration 0032).
Writing the pair narrows them to `authorization_code` and `refresh_token`,
dropping `implicit`, `hybrid`, `password`, `client_credentials` and the
device-code grant, exactly as JeffResc v0.2.7+ does on its own. That is the
least-privilege direction (RFC 9700 says implicit and password should not be
used), but a client that depends on a dropped grant stops working: see the
dry-run check in step 3.

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
both operators' pipelines on sample values (JeffResc 0.1.x parses the output with
a line parser that strips one pair of quotes and keeps trailing comments; v0.2.x
with a YAML decoder) and fails unless the Secret data is identical, so it never emits a template that renders differently.

## Runbook (zero downtime, credentials preserved)

Migrate one application first, then the rest; the commands below name
resources one at a time for that reason. Variables: `NS` is the application's
namespace (`OLD_NS` and `NEW_NS` where `--namespace` makes them differ).

### 1. Install this operator alongside

Install the Helm chart (see `dist/chart/README.md` for every value) **into a
namespace other than JeffResc's**: JeffResc's chart and this project's
kustomize default both use `authentik-operator-system`, and a shared namespace
invites name clashes between the two controllers' objects.

```sh
# authentik-api is a Secret holding a new Authentik API token
helm install jacaudi-authentik-operator oci://ghcr.io/jacaudi/charts/authentik-operator --version X.Y.Z \
  --namespace authentik-operator --create-namespace \
  --set authentik.url=https://authentik.example.com \
  --set authentik.tokenSecret.name=authentik-api \
  --set-string 'adoption.defaultConnectionNamespaceSelector=kubernetes.io/metadata.name in (NS1\, NS2)'
# from a checkout instead of the registry: helm install jacaudi-authentik-operator dist/chart ...
```

`adoption.defaultConnectionNamespaceSelector` is the gate described above: the
namespaces of the JeffResc objects you are migrating (the `NS` of every
converted object; the converter's report prints the exact selector, and with
`--set` every comma in it needs a backslash, as above). It is rendered as the
operator flag `--default-connection-adoption-namespace-selector`;
without it every converted object stays at `AdoptionNotAllowed`. You can add it
later with `helm upgrade --reuse-values --set-string ...` (the pod restarts to
read it). Skip it, and use `--authentik-ref NAME` when converting, if you attach
the migrated objects to an `AuthentikDeployment` in each namespace instead.

**Use a release name different from JeffResc's** (above: `jacaudi-authentik-operator`;
JeffResc's chart is usually released as `authentik-operator`). Both charts name their
cluster-scoped objects after the release (`<release>-manager-role` and its binding),
so two releases both called `authentik-operator` make the second install fail with
`invalid ownership metadata` on the ClusterRole, whatever the namespaces.

The CRDs ship with the chart (different API group, so no clash with
JeffResc's). If `authentik.io` CRDs already exist from an earlier install
(kubectl, kustomize), Helm refuses them with `invalid ownership metadata`:
follow "Ownership metadata on kept CRDs" in `dist/chart/README.md` (adopt them
or `--set crd.enabled=false`). The default connection is configured by
`authentik.url` and a token Secret; alternatively use an `AuthentikDeployment`
(`mode: ConnectOnly`) per namespace plus `--authentik-ref` below; see
[scope.md, connection model](design/scope.md). Create the API token in
Authentik first. Check that nothing collides with JeffResc's objects
(`kubectl get deploy -A | grep -i authentik`).

Back up what you are about to change. `kubectl get a,b` fails outright if one
kind's CRD is missing (0.1.x clusters have no `authentikoauth2applications`),
so list the kinds separately, and strip the server-populated fields so the
backup can be re-applied:

<!-- e2e:backup:begin -->
```sh
for r in authentikapplications.goauthentik.io authentikoauth2applications.goauthentik.io; do
  kubectl get crd "$r" >/dev/null 2>&1 || continue
  kubectl get "$r" -A -o json \
    | jq 'del(.items[].metadata | .resourceVersion, .uid, .managedFields, .generation, .creationTimestamp, .selfLink, .ownerReferences)
          | del(.items[].status)' > "jeffresc-backup-$r.json"
done
```
<!-- e2e:backup:end -->

```sh
kubectl -n OLD_NS get secret <old secret names> -o yaml > jeffresc-secrets-backup.yaml   # contains credentials; keep it safe
```

Rollback: uninstall this operator (`helm uninstall`, then delete the CRDs if
you kept them). Nothing has been adopted.

### 2. Convert; the platform admin applies the templates

```sh
bin/convert-jeffresc --out out/ --secret-suffix -v2 jeffresc-backup-*.json     # add --authentik-ref NAME if used
# read the files and the report printed on stderr before applying anything
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
- `scopes` / property mappings: when the spec sets `scopes`, adoption replaces the
  provider's mappings with `scopes` + `propertyMappings`, so a mapping added in
  the UI would be dropped; unset `scopes` keeps the live mappings;
- `grant_types`: read the direction. `[] -> [authorization_code, refresh_token]`
  (Authentik 2026.5+, a provider JeffResc v0.2.6 or older created) is a repair:
  that provider rejects every login today. `[<all seven>] -> [authorization_code,
  refresh_token]` (a provider that predates the 2026.5 upgrade) is a narrowing:
  check first that no client of it uses `client_credentials`, the device-code
  grant, `password`, `implicit` or `hybrid`. If one does, set `grantTypes` in the
  spec to the list it needs (a list set in JeffResc v0.2.7+ is copied as it
  is);
- `accessCodeValidity`, `accessTokenValidity`, `refreshTokenValidity`: only
  listed when the JeffResc object set them. Fields the spec does not set are
  never in the diff: adoption keeps their live values.

`AdoptionNotAllowed` means the namespace is not allowed on the default
connection (see step 1): set the Helm value, or convert with `--authentik-ref`.
`AdoptionTargetNotFound` means no provider named `<slug>-provider` exists
(a name or slug typo, or the wrong Authentik): fix `match`; the converter
never emits `AdoptOrCreate`, so a miss creates nothing. Then take it out of dry
run:

```sh
kubectl -n NS patch authentikoauthproviders.authentik.io gitlab --type merge -p '{"spec":{"dryRun":false}}'
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
release the Authentik object without deleting it. Fields adoption wrote stay
on the object; JeffResc, still running, rewrites the fields it manages on its
next reconcile.

### 4. Confirm the new Secret is byte-identical, then point consumers at it

With `--secret-suffix -v2` the new operator renders `<old name>-v2` next to the
old Secret, in `NEW_NS` (which differs from `OLD_NS` only if you used
`--namespace`). It appears on the provider's next reconcile, which can be up to
a resync interval after adoption. Compare key by key (`.data` is base64 of the
exact bytes, so a different value or trailing newline shows; map order does
not matter):

<!-- e2e:compare:begin -->
```sh
bash -euo pipefail <<'EOF'
# plain assignments: a failing kubectl stops the script (a diff of two process
# substitutions would report "identical" when both kubectl calls fail)
OLD=$(kubectl -n OLD_NS get secret gitlab-oidc-credentials -o json | jq -S .data)
NEW=$(kubectl -n NEW_NS get secret gitlab-oidc-credentials-v2 -o json | jq -S .data)
test -n "$OLD"; test "$OLD" != null
if test "$OLD" = "$NEW"; then echo identical; else diff <(echo "$OLD") <(echo "$NEW"); exit 1; fi
EOF
```
<!-- e2e:compare:end -->

The two Secrets have different labels and owners; only `.data` is compared.

**Endpoint URLs.** JeffResc wrote the URLs exactly as Authentik returned them,
which Authentik builds from the host JeffResc connected to (often an internal
service name). This operator rewrites them from the connection's base URL to
its external URL (`credentials.Rewrite`, `internal/controller/oauthprovider_secret.go`),
set with `authentik.externalUrl` in the chart (`--authentik-external-url`) or
`AuthentikDeployment.spec.connection.externalURL`. If `diff` shows only the
host part of `issuer`/`auth`/`token`/`userinfo` URLs, this is why: set the new
connection URL and external URL so the output matches what JeffResc rendered.
A URL outside the connection's base is reported as `ExternalURLMismatch`.

Point consumers (Deployment `secretKeyRef`s, mounts, secret-sync tools) at the
`-v2` names and roll them. To get back to the original names, once the old
Secrets are gone (step 5) set `spec.secret.name` to the original name: the
operator creates it, then deletes `-v2`; point consumers back before that
second move.

Without `--secret-suffix` the new provider cannot render a Secret under the
old name while JeffResc's CR owns it. This shows on the **credentials
condition** (`CredentialsReady`, reason `SecretConflict`), not on `Ready`, and
the operator does not overwrite the Secret. It is only freed when the old CR is
deleted in step 5 (garbage collection removes it); the new operator then
creates it on its next reconcile, and pods started in between cannot find it.
Prefer the suffix for anything that is not tolerant of that gap.

Rollback: point consumers back at the old Secret names; the old Secrets are
untouched until step 5.

### 5. Retire the old CRs without deleting the Authentik objects

JeffResc deletes the Authentik provider and application when its CR is
deleted, **adopted or not**, and its reconcile re-adds `goauthentik.io/finalizer`
whenever it is missing. Its Secrets have a controller owner reference to their
CR, so deleting a CR also garbage-collects that application's Secret. So:
migrate and retire **one application at a time**; scaling the old operator to
0 once is fine (the unmigrated applications keep their existing Secrets and
Authentik objects, they just stop being reconciled). Only after every
application has been migrated is the cluster-wide variant at the end of this
step acceptable.

Work through these gates in order and stop at the first one that fails.

**Gate 1 (by hand): stop anything that would bring the old operator back.**
Suspend GitOps reconciliation of it, or set replicas to 0 in Git:

```sh
flux suspend helmrelease <old release> -n <ns>        # and/or: flux suspend kustomization <name> -n <ns>
# Argo CD: disable auto-sync for the application (argocd app set <app> --sync-policy none)
```

The script below derives the old operator's pod selector from its Deployment
(`.spec.selector.matchLabels`), so a mistyped selector cannot make the
"pods are gone" check pass vacuously.

**Gates 2 to 6 as one script**, so every failure stops the whole sequence and
your interactive shell is never the one that exits. Fill in the variables at the
top (`OLD_OP_NS` is the namespace of the old operator's Deployment and `OLD_NS` the
namespace of the old CR, which is usually an application namespace, not the operator's;
`R` is `authentikapplications.goauthentik.io` for 0.1.x,
`authentikoauth2applications.goauthentik.io` for v0.2.x; `OLD_WEBHOOK` is the
old ValidatingWebhookConfiguration on v0.2.x, empty on 0.1.x: with no backend
and `failurePolicy: Fail` it would block the finalizer update):

<!-- e2e:retire-one:begin -->
```sh
bash -euo pipefail <<'EOF'
OLD_OP_NS=authentik-operator-system; OLD_DEPLOY=authentik-operator
OLD_NS=authentik-operator-system   # the namespace of the old CR (often an application namespace)
OLD_WEBHOOK=''
R=authentikapplications.goauthentik.io; NAME=gitlab; NEW_NS=authentik-operator

# gate 2: scale to 0 and wait until the pods are really gone (a running pod re-adds finalizers).
# The selector comes from the Deployment itself; an empty one stops the script.
SELECTOR=$(kubectl -n "$OLD_OP_NS" get deploy "$OLD_DEPLOY" \
  -o go-template='{{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}')
SELECTOR=${SELECTOR%,}
test -n "$SELECTOR"
kubectl -n "$OLD_OP_NS" scale deploy/"$OLD_DEPLOY" --replicas=0
for _ in $(seq 24); do
  PODS=$(kubectl -n "$OLD_OP_NS" get pods -l "$SELECTOR" -o name)
  test -z "$PODS" && break
  sleep 5
done
test -z "$PODS"

# gate 3 (v0.2.x): remove the old webhook configuration
if test -n "$OLD_WEBHOOK"; then kubectl delete validatingwebhookconfiguration "$OLD_WEBHOOK"; fi

# gate 4: strip the finalizer (null-tolerant)
kubectl -n "$OLD_NS" get "$R" "$NAME" -o json \
  | jq '.metadata.finalizers = ((.metadata.finalizers // []) - ["goauthentik.io/finalizer"])' \
  | kubectl replace -f -

# gate 5: assert, with plain assignments so a failing kubectl stops the script
F=$(kubectl -n "$OLD_NS" get "$R" "$NAME" -o jsonpath='{.metadata.finalizers}')
test -z "$F"
SPEC=$(kubectl -n "$OLD_OP_NS" get deploy "$OLD_DEPLOY" -o jsonpath='{.spec.replicas}')
test "$SPEC" = 0
STATUS=$(kubectl -n "$OLD_OP_NS" get deploy "$OLD_DEPLOY" -o jsonpath='{.status.replicas}')
test -z "$STATUS" || test "$STATUS" = 0

# gate 6: delete that CR, then show the new objects
kubectl -n "$OLD_NS" delete "$R" "$NAME"
kubectl -n "$NEW_NS" get authentikoauthproviders.authentik.io,authentikapplications.authentik.io
echo GATES-OK
EOF
```
<!-- e2e:retire-one:end -->

Check that `GATES-OK` printed, that the new objects are `Ready` True, and that
the application is still present in Authentik. Repeat for the next application
with its own `NAME` (gate 1 is once; the scale and wait in gate 2 is harmless to
repeat).

After the last application has been migrated, the same can be done for
everything at once, run per kind (`kubectl get a,b` fails when one CRD is
missing), again as one script:

```sh
bash -euo pipefail <<'EOF'
OLD_OP_NS=authentik-operator-system; OLD_DEPLOY=authentik-operator
KINDS="authentikapplications.goauthentik.io authentikoauth2applications.goauthentik.io"
for r in $KINDS; do
  kubectl get crd "$r" >/dev/null 2>&1 || continue
  kubectl get "$r" -A -o json | jq -r '.items[] | "\(.metadata.namespace) \(.metadata.name)"' | while read -r ns name; do
    kubectl -n "$ns" get "$r" "$name" -o json \
      | jq '.metadata.finalizers = ((.metadata.finalizers // []) - ["goauthentik.io/finalizer"])' | kubectl replace -f -
  done
done
# assertion gate: no old CR still carries a finalizer, and the old operator is at zero
for r in $KINDS; do
  kubectl get crd "$r" >/dev/null 2>&1 || continue
  LEFT=$(kubectl get "$r" -A -o json | jq '[.items[] | select((.metadata.finalizers // []) | length > 0)] | length')
  test "$LEFT" = 0
done
SPEC=$(kubectl -n "$OLD_OP_NS" get deploy "$OLD_DEPLOY" -o jsonpath='{.spec.replicas}')
test "$SPEC" = 0
for r in $KINDS; do
  if kubectl get crd "$r" >/dev/null 2>&1; then kubectl delete "$r" -A --all; fi
done
echo GATES-OK
EOF
```

Never scale the old operator back up while an unstripped CR is being deleted.

Rollback: re-apply the backup (`kubectl apply -f jeffresc-backup-<kind>.json`)
and scale the old operator up; it looks the provider up by its `<slug>-provider`
name, so it should manage it again (verify one application before relying on
this). Delete the new objects first, or the two operators fight over the same
Authentik objects.

### 6. Uninstall the old chart and CRDs, verify

<!-- e2e:uninstall:begin -->
```sh
helm -n <old operator namespace> uninstall <old release>
kubectl delete crd authentikapplications.goauthentik.io     # and authentikoauth2applications.goauthentik.io if it exists
```
<!-- e2e:uninstall:end -->

If you have `AuthentikSAMLApplication` objects (this operator cannot take
them), do not do this step: after step 5 has removed every OAuth CR, resume the old
operator (`flux resume helmrelease <old release> -n <ns>` restores its replicas
and, on v0.2.x, the webhook configuration deleted in gate 3; with Argo CD,
re-enable auto-sync). It then only manages the SAML objects. Keep its chart
and the SAML CRD. Delete the SAML CRD only when no SAML objects remain.

Delete the CRDs only after step 5: deleting a CRD deletes its CRs, and a CR
still carrying the finalizer would be processed by any running JeffResc
operator. Then verify SSO login for every application with a real user.
The new objects keep `deletionPolicy: Retain`, so deleting one later releases
the Authentik object instead of deleting it; set `deletionPolicy: Delete`
only if you want the new operator to own its lifecycle.

Rollback: reinstall the old chart and CRDs and re-apply the backups.

### 7. Leave adopt mode

The migration runs in `Adopt` mode: converted objects have `managementPolicy:
Adopt`, never `AdoptOrCreate`. In this mode the operator never creates a
provider or application. If the match target is missing (a typo in
`match.name` or the slug) it reports `AdoptionTargetNotFound` and writes
nothing.

Once SSO login is verified for every application, return every converted
provider and application to the default policy: remove `managementPolicy`,
`match` and `dryRun` from the spec (`Create` is the default; the admission rules
reject `match` and `dryRun` without `Adopt`), commit that to your manifests, and
confirm each resource stays `Ready` with provenance `Adopted` and that its next
reconcile writes nothing (no new events, no change in Authentik). Why: adoption
is a one-time takeover. Afterwards the resources are managed by primary key
(provenance stays `Adopted`, so the default deletion policy remains `Retain`);
leaving `Adopt` in place would let a future CR with a matching name take over
unrelated objects, and it keeps needing the namespace in the selector. After
the switch you can remove the namespaces from
`adoption.defaultConnectionNamespaceSelector`; resources already on `Create`
are not gated.

Rollback: set `managementPolicy: Adopt` and the `match` again (the namespace
must be allowed again too).

## After migration: disaster recovery

With the policy back at the default (step 7):

- **Cluster and Authentik rebuilt, CRs re-applied with no status.** The
  operator creates new providers and applications normally. The client secret
  is new; the client ID too, unless you pin `spec.clientId` beforehand (do so
  if consumers must keep it).
- **Authentik database lost, CR status intact** (provenance `Adopted`, pk
  set). The operator reports `AdoptionTargetNotFound` and never recreates an
  adopted object automatically. To let it create, clear `status.providerPk`,
  `status.provenance` and `status.clientId` (applications: `applicationUid`,
  `provenance`), or delete and re-apply the CR, and set
  `managementPolicy: AdoptOrCreate` temporarily (see scope.md,
  "Disaster-recovery re-bootstrap").

- **Cluster rebuilt, Authentik intact, CRs re-applied without status, policy
  `Create`.** The object already exists, so the operator reports `Conflict`
  and changes nothing (safe). To take it over: allow the namespace again, set
  `managementPolicy: Adopt` with `match` and `dryRun: true`, read the diff, set
  `dryRun: false`, then return to the default policy. Deleting and re-applying
  a provider CR garbage-collects its rendered Secret (the operator renders it
  again once the provider is Ready).

For the management policies and the adoption gate see
[scope.md, "API-object adoption"](design/scope.md#api-object-adoption-task-05).

