# Scope and binding decisions

Decisions made before scaffolding. These are the expensive-to-retrofit ones —
API group, kind names, and the adoption contract. Change them now or not at all.

## Origin

A Go rewrite of `github.com/dsluijk/authentik-operator` (Rust, v0.2.5,
last commit 2022-12-13, archived). The original and its analysis live outside
this repo under `source/` and `docs/refactor/`, both gitignored.

## Framework

Kubebuilder v4 + controller-runtime, matching `cloudflare-operator`,
`nextdns-operator`, `nomad-operator` and `towonel-operator`. `config/` is kept
as the codegen target, consistent with those repos.

## API

- group `authentik.io`, version `v1alpha1`. Every kind is namespaced
  except `AuthentikCredentialsTemplate`, which is cluster-scoped because
  platform administrators own it (see the credential Secret trust boundary below).
- `internal/authentik/` holds the Authentik REST client, mirroring
  `towonel-operator`'s `internal/towonel/`

| Kind | Manages |
|---|---|
| `AuthentikDeployment` | the Authentik install itself — Deployment, Service, Ingress, RBAC |
| `AuthentikApplication` | an application in Authentik |
| `AuthentikUser` | a user |
| `AuthentikGroup` | a group |
| `AuthentikOAuthProvider` | an OAuth2 provider |
| `AuthentikCredentialsTemplate` | (cluster-scoped) the keys, labels and annotations of credential Secrets |

### Why `AuthentikDeployment` and not `Authentik`

The upstream kind was bare `Authentik`. Renamed so the kind says what it does
and does not read as a catch-all for the whole API group.

## Adoption — `AuthentikDeployment` must take over existing deployments

**Requirement:** an `AuthentikDeployment` pointed at an Authentik that already
exists must adopt it, not fail and not fight it.

The upstream operator could only ever create. It applied every object with
server-side apply, `.force()`, field manager `authentik.ak-operator`, and a
hand-written `ownerReferences` block — so pointing it at an existing install
would either stomp another manager's fields or conflict outright.

What adoption has to cover:

1. **Pre-existing `Deployment`, `Service`, `Ingress`, `ServiceAccount`** with
   the expected names are adopted: labels and an `ownerReference` are added,
   and fields the CR does not speak to are left alone.
2. **Another field manager already owns fields.** Resolve deliberately rather
   than reaching for `.force()` — a blind force is what makes takeover
   destructive.
3. **An existing Authentik with its own admin token.** The operator must accept
   a supplied token (secretRef) instead of requiring the bootstrap-token dance.
4. **Adoption is reversible.** Deleting the CR must be able to release the
   objects rather than cascade-delete an install the operator did not create.

### Decided: adoption is explicit, via `spec.adoption.enabled`

```yaml
spec:
  adoption:
    enabled: true   # default false
```

With `enabled` false — the default — a matching object the operator did not
create is an **error**, not something to overwrite. Pointing an
`AuthentikDeployment` at a live install therefore cannot silently reconfigure
it; you have to say so.

Provenance is tracked with two annotations rather than a second spec knob, so
release-vs-delete on teardown needs no user input:

| annotation | meaning | on teardown |
|---|---|---|
| `authentik.io/created-by` | the operator created this object | delete |
| `authentik.io/adopted-by` | names the `AuthentikDeployment` that adopted it | release (drop ownerRef, leave the object) |

The spec field was chosen over an annotation on the target because the decision
belongs to whoever owns the CR, and it stays visible in one place in Git rather
than on a live object the operator is about to take over.

## API-object adoption (TASK-05)

`AuthentikOAuthProvider` and `AuthentikApplication` follow the same contract as
`AuthentikDeployment` adoption above -- explicit, opt-in, release rather than
delete -- applied to objects inside Authentik. (Group and User are stubs and
get it when they get a controller.) Migrating from another operator is the use
case: a provider recreated instead of adopted gets a new client ID and secret
and every consuming app breaks.

```yaml
spec:
  managementPolicy: Adopt   # Create (default) | Adopt | AdoptOrCreate | ReadOnly
  dryRun: false             # report the diff, write nothing; Adopt/AdoptOrCreate only
  match:                    # providers only; exactly one of name or pk
    name: grafana-provider
  deletionPolicy: Retain    # optional: Delete | Retain
```

`APIManagementSpec` (`managementPolicy`, `dryRun`, `deletionPolicy`) is a
separate type from the deployment's `AdoptionSpec` because API objects need
`dryRun`, an observe-only mode and a way to find the target, which
Kubernetes-object adoption does not.

### Management policy

| policy | finds an existing object | existing object | miss | writes to Authentik | deletes on CR delete |
|---|---|---|---|---|---|
| `Create` (default) | no, never adopts | `Conflict` | creates | yes | `Created` by default |
| `Adopt` | yes | takes it over | `AdoptionTargetNotFound`, nothing written | yes, the fields the spec sets | `Retain` by default |
| `AdoptOrCreate` | yes | takes it over | creates (Normal event "nothing was found to adopt") | yes | by provenance |
| `ReadOnly` | yes | observes it | `AdoptionTargetNotFound` | never | never |

- A resource that **created** its object recreates it if it vanishes, whatever
  the policy and `match` say. An object it **adopted** that vanishes is never
  recreated silently: it reports `AdoptionTargetNotFound` and its pk stays in
  status. `ReadOnly` never recreates anything.
- `ReadOnly` finds the object, claims it (recorded as provenance `Adopted`,
  so the duplicate check and tie-break cover it), renders the credentials
  Secret, and reports in the `Drifted` condition whether the live object
  differs from the spec (`True` with the fields as `field: live -> spec`, or
  `False`). `Ready` is `True` (reason `ReadOnly`) once the object is found, so
  dependents are not blocked by drift. It never writes to Authentik and never
  deletes (`deletionPolicy: Delete` is rejected at admission).
- CEL admission rules: `match` only with `Adopt`, `AdoptOrCreate` or
  `ReadOnly`; `dryRun` only with `Adopt` or `AdoptOrCreate`; `ReadOnly`
  rejects `deletionPolicy: Delete`; `match` has exactly one of `name` or `pk`.
- The policy names follow ACK's adoption policies, but the write semantics
  differ: `Adopt` here patches the fields the spec sets (and leaves the rest),
  where ACK's `adopt` does not update the object to the spec.
- `AdoptOrCreate` removes the protection against minting new client
  credentials through a typo in a name, slug or pk. Treat it as a temporary
  choice, for a disaster-recovery re-bootstrap (see below).

### Adoption trust boundary

Before adoption the operator never read or changed a provider it did not
create, so create-only was the tenant boundary. Adoption lifts it: an adopting
resource can read the provider's client secret (rendered into its Secret),
rewrite its redirect URIs, change `sub`/`iss` semantics and delete it. So who
may adopt is an administrator's decision, by connection:

- **With an explicit `spec.authentikRef`** (a same-namespace
  `AuthentikDeployment`) adoption, observation and `dryRun` are allowed: that
  deployment's own token bounds what the resource can reach in Authentik.
- **On the operator default connection** they are refused with condition reason
  `AdoptionNotAllowed` unless the resource's namespace matches the label
  selector in `--default-connection-adoption-namespace-selector`. The selector
  is matched against the Namespace's labels, so
  `kubernetes.io/metadata.name=team-a` (or `... in (team-a,team-b)`) works. An
  empty selector, the default, denies every namespace. The check runs on every
  reconcile, so removing a namespace stops an adopted resource from writing and
  rendering Secrets; there is no Namespace watch, so a label change takes effect
  within the 5-minute recheck. `Create` is never gated.
- Known gap: the gate is by policy. A resource that adopted while its namespace
  was allowed and is then switched to `Create` keeps managing the object it
  adopted. Removing the namespace from the selector before switching policy
  closes it.
- A least-privilege `authentikRef` token can adopt and update an object but
  needs Authentik's global `add_*` permission to create one; scope the token
  with that in mind (see Authentik's permissions documentation).

### Behavior

- **Under `Create`**, an existing object with the target identity is a
  `Conflict` (the message names `spec.managementPolicy`). Nothing is written.
- **Identity.** An application is matched by slug. A provider is matched by
  `match.name` or `match.pk`; with no `match` the candidate is the provider
  named like the resource (`spec.name`, else `metadata.name`). `match.name` may
  differ from the desired name: the JeffResc provider `<slug>-provider` is
  adopted with `match: {name: <slug>-provider}`. Several providers with one
  name is a `Conflict`; select by `match.pk`. A `match.pk` needs no search by
  name.
- **A miss never creates unless the policy is `AdoptOrCreate`.** A typo in a
  name, slug or pk therefore cannot mint new client credentials. Under
  `AdoptOrCreate` a miss creates with provenance `Created`, even when an
  explicit `match` found nothing (the new provider takes the resource's desired
  name).
- **Provenance** is recorded in `status.provenance` (`Created` | `Adopted`)
  next to the object's pk, because Authentik objects cannot carry
  annotations. Empty means undecided: a fresh resource that finds an existing
  object is never assumed to have created it. The decision is written to
  status before any write to Authentik.
- **Lost creates.** A create whose response was lost is recovered through
  `status.pendingCreate` and classified `Created`. For a provider the proof is
  the client ID the operator sent (`k8s-<uid>`, or the pinned `spec.clientId`).
  An application has no client ID: it is recovered as `Created` only if it
  links to this resource's `providerRef` provider **and** that provider's
  provenance is `Created`. Otherwise the marker is dropped with a Warning
  event ("create sent, outcome unproven") and the application takes the normal
  path: adopted if the policy allows (through the gate and `dryRun`), else a
  `Conflict`. An application without a `providerRef` cannot prove a lost
  create.
- **What adoption writes.** A PATCH of the fields the spec sets; everything
  else on the object is left alone. A provider's `client_id` and
  `client_secret` are never changed unless `spec.clientId` is set; the
  credentials Secret is rendered from the values read from the API,
  byte-for-byte. An adopted provider keeps its name unless `spec.name` is set,
  and keeps `authenticationFlow`, `encryptionKey`, `logoutUri` and `signingKey`
  if the spec leaves them empty (including no signing key at all). An adopted
  application keeps `group`, `metaLaunchUrl`, `metaDescription`,
  `metaPublisher` and `metaIcon` if the spec leaves them empty, and its
  provider link if `providerRef` is unset; if `providerRef` is set the link
  becomes that provider (a no-op when it is the adopted one). Required fields
  (flows, application `name`) are written.
- **Defaults apply on create only.** The CRD schema carries no defaults for
  the adoption-sensitive fields, so "unset" is visible to the operator:
  - provider: `scopes`, `clientType`, `grantTypes`, `accessCodeValidity`,
    `accessTokenValidity`, `refreshTokenValidity`, `refreshTokenThreshold`,
    `subMode`, `includeClaimsInIdToken`, `issuerMode`, `logoutMethod`, and each
    redirect URI's `matchingMode` and `type`;
  - application: `policyEngineMode`, `openInNewTab`, `metaHide`.

  When the object is created, an unset field takes the documented default
  (`openid profile email`; `confidential`; `authorization_code` and
  `refresh_token`; `minutes=1`, `minutes=5`, `days=30`, `seconds=0`;
  `hashed_user_id`; `true`; `per_provider`; `backchannel`; `strict` and
  `authorization`; `any`, `true`, `false`), and a resource that created its
  object is held to them on every reconcile. When the object is adopted (or
  observed under `ReadOnly`), an unset field keeps the live value; only fields
  the spec sets are written. `redirectUris` is optional in the schema but is
  **required when creating** (`InvalidSpec` otherwise); an adopted provider
  keeps its live redirect URIs when the spec lists none, and a listed URI
  without `matchingMode` or `type` takes them from the live redirect URI with
  the same `url`, else the defaults.
- **Property mappings** are one Authentik field built from `scopes` plus
  `propertyMappings`. With `scopes` unset the live list is kept and any
  `propertyMappings` the spec names are added to it (additive, never removing
  a live mapping); with `scopes` set the spec owns the whole list.
- **Slug and `iss`.** In `issuerMode: per_provider` the `iss` claim contains
  the application's slug, so changing an application's slug changes `iss` for
  every client of its provider. The slug is immutable on the resource; adopting
  an application under a different slug than another operator used is a
  migration decision, not a rename.
- **Dry run.** With `dryRun: true` the operator finds the object, computes the
  PATCH and reports every field it would change as `field: old -> new`
  (Authentik API field names, values truncated; the client secret is never
  shown) in `Ready=False, reason AdoptionDryRun` and an event (event notes are
  capped at 1024 bytes; the condition carries the full text). With nothing to
  adopt it reports whether a new object would be created. It writes nothing to
  Authentik and renders no Secret; `status.provenance` stays empty. Setting
  `dryRun` back to false adopts. dryRun gates only the initial takeover: once
  adopted, spec edits are applied live. `dryRun` is gated like adoption.
- **Order of adoption.** An application waits for its `providerRef` to be
  Ready, so the provider must be adopted (dryRun off) before the application
  can complete adoption; an application in dryRun against a provider still in
  dryRun shows `ProviderNotReady`.

### Disaster-recovery re-bootstrap

Leave `AdoptOrCreate` out of Git. The one legitimate use is a re-bootstrap
after the Authentik database was lost, when nothing exists to adopt:

1. Clear `status.providerPk`, `status.provenance` and `status.clientId` (for
   an application: `status.applicationUid`, `status.slug`,
   `status.provenance`) on the resource, since an adopted object that vanished
   is otherwise never recreated.
2. Set `managementPolicy: AdoptOrCreate` (or `Create`) temporarily.
3. If consumers must keep their client ID, pin `spec.clientId` to the old
   value. The client secret is always new; consumers need the re-rendered
   credentials Secret.
4. Once the object exists, set the policy back to `Adopt`.

### Deletion policy

`spec.deletionPolicy` is `Delete` or `Retain`. Unset, the effective policy is
`Delete` for a `Created` object and `Retain` for an `Adopted` one; an undecided
provenance never deletes **by default** (`Retain`, with a Warning event
"provenance unknown; retaining"). An explicit `Delete` wins over provenance,
so it can remove an object this resource did not create; when it does so for
undecided provenance a Warning event says so first. `ReadOnly` never deletes.
`Retain` removes the finalizer and leaves the Authentik object. The credentials
Secret is a Kubernetes object owned by the provider resource and is
garbage-collected with it under either policy (the credentials stay readable in
Authentik); copy it out first if workloads still mount it.

### Duplicate claims

Two resources must never manage one object. Adopting is refused with a
`Conflict` while any other resource, terminating ones included, tracks the
object on the same Authentik. Two resources can still pass that check together
(a cache that has not seen the other's claim); every tracked resource re-checks
on every reconcile, and the claim re-check right after adoption reads the API,
not the cache. They are ordered by a total order that every holder computes the
same way: `Created` beats `Adopted`, then the lower `metadata.uid` wins. The
loser clears its status, reports a `Conflict`, and deletes the credentials
Secret it rendered (it holds the live client secret). A terminating holder
still counts, so an adopter yields to a `Created` owner that is being deleted
rather than being stranded when the owner's finalizer deletes the object.

Resources are on the same Authentik when they share a connection reference
(both the default connection, or the same `AuthentikDeployment`) or their
normalized base URLs match (lower-case host, default port stripped, trailing
slash and `/api/v3` trimmed). One resource on the default connection and
another on an `AuthentikDeployment` for the same URL are therefore caught. The
comparison is by URL only: a Kubernetes service DNS name and an ingress host
that reach one instance look different, a false negative this cannot close
without asking Authentik which instance it is (Authentik itself limits a
provider to one application).

### Upgrading a dev cluster (no provenance backfill)

There is no backfill. Resources created before provenance existed have an
object pk in status but an empty `status.provenance`, which resolves to
`Retain`, so deleting them leaves their Authentik objects behind. No release
has shipped with that state; on a development cluster either recreate the
resource or mark it as the creator:

```sh
kubectl patch authentikoauthprovider <name> --subresource=status --type=merge \
  -p '{"status":{"provenance":"Created"}}'
```

(same for `authentikapplication`). Only do this for objects the resource really
created.

### Not covered

Provenance is sticky: switching `managementPolicy` later does not turn an
`Adopted` object into a `Created` one. The gap in the trust boundary for a
policy switch is described above.

## Decided: connection model (TASK-01)

API objects (`AuthentikApplication`, `AuthentikOAuthProvider`, `AuthentikUser`,
`AuthentikGroup`) name the Authentik they belong to with an optional
`spec.authentikRef.name`: an `AuthentikDeployment` in the **same namespace**.
Cross-namespace references are not supported, because the referenced
deployment carries an API token and referencing it grants its privileges.

`AuthentikDeployment.spec.mode` is required (no default, so managing
workloads is always asked for explicitly):

| mode | manages | connection |
|---|---|---|
| `ConnectOnly` | nothing; supplies URL, token and CA for an Authentik installed elsewhere (e.g. Helm) | `spec.connection` required |
| `Managed` | the Authentik workloads (not implemented yet) | `spec.connection` optional; derived later |

```yaml
spec:
  mode: ConnectOnly
  connection:
    url: https://authentik.example.com        # http only if the URL says so
    tokenSecretRef: {name: authentik-operator-token, key: token}
    caBundle:                                 # optional; exactly one source
      configMapKeyRef: {name: trust-bundle, key: ca.crt}
    insecureSkipVerify: false                 # mutually exclusive with caBundle
```

When `authentikRef` is omitted, the operator-level **default connection** is
used, configured by flags / environment (compatible with JeffResc):

| flag | env | notes |
|---|---|---|
| `--authentik-url` | `AUTHENTIK_URL` | empty disables the default |
| `--authentik-token-file` | `AUTHENTIK_TOKEN` | token from a mounted Secret file, else the env var (set via `valueFrom.secretKeyRef`); never a flag value; read at startup |
| `--authentik-ca-file` | | optional PEM bundle |
| `--authentik-insecure-skip-verify` | | testing only |
| `--default-connection-adoption-namespace-selector` | | label selector for the namespaces that may use `managementPolicy` Adopt, AdoptOrCreate or ReadOnly on the default connection; empty (the default) denies all; objects with `authentikRef` are not affected (see "Adoption trust boundary") |

With neither a ref nor a default, an object is not reconciled and says so in
its status (reason `NoConnection`).

Token handling: always read from a Secret (or a file/env populated from one),
never logged, never written to a spec or status (`authentik.Config` redacts
the token in `%v`/`%+v`/`%#v`). The HTTP client never follows redirects: the
bearer header is added in the transport, so a followed redirect would hand the
token to whatever host the redirect names. Error bodies are surfaced in
conditions only for 400 (field validation), so a user-chosen URL cannot be
used to read arbitrary responses back through status.

RBAC: the operator has cluster-wide **get** on ConfigMaps and
**get/create/update/delete** on Secrets -- no list, no watch, no cache, no patch.
Writes exist for credential Secrets (TASK-04): they go only to the provider's
own namespace, and an existing Secret not controlled by that provider is
never modified or deleted (`SecretConflict`).

Trust boundary for credential Secrets, with personas split as in Crossplane's
XRD/Composition versus claim model:
- Platform administrators own the cluster-scoped `AuthentikCredentialsTemplate`.
  It holds the data template, the labels and the annotations. Its required
  `namespaceSelector` limits which namespaces may use it; `{}` allows all,
  so opening a template to everyone is an explicit choice.
- Teams own `AuthentikOAuthProvider`, and choose only the Secret's name and
  which template to reference.

So provider write lets a team make the operator create a Secret, with any name
not already taken, in the team's own namespace. The Secret holds that
provider's client secret, in a shape an administrator defined. Provider write
does not let a team choose the Secret's keys, labels or annotations, so a team
cannot, for example, opt a rendered Secret into `authentik.io/connection` or
into another controller's label selector. Template labels and annotations
reject the reserved prefix `authentik.io/`. Variables are inserted into YAML,
so values containing control characters (C0, DEL and C1, which includes NEL)
or the YAML line breaks U+2028 and U+2029 are refused (`spec.clientId` is
restricted to printable ASCII at admission), and an unquoted `.Name` is
caught when the template is validated. Neither a newline nor YAML syntax in a
value can add keys.

The selector is only as strong as control over namespace labels. Plain
namespaced RBAC cannot label a Namespace. But tenants who can create
namespaces, or a multi-tenancy layer (Capsule, Rancher projects, HNC), may be
able to, so match on `kubernetes.io/metadata.name`, which the API server sets
to the namespace's name. `TemplateNotFound` versus `TemplateNotAllowed` tells
a tenant which template names exist, a disclosure accepted like
`SecretConflict`'s.
Templates are restricted (no loops, no nested templates, capped output,
`.ClientSecret` printable only), so even a template author cannot hang or crash
the operator or probe the client secret through render errors.

Checking the namespaceSelector needs `get` on Namespaces; there is no
Namespace watch, so a label change takes effect within the 5-minute recheck.
When a template stops allowing a namespace, or is deleted, the template's
labels and annotations are removed from the existing Secret: what they opted
it into is withdrawn. Its data, that provider's own credentials, is kept so
workloads keep running. The provider reports `TemplateNotAllowed` /
`TemplateNotFound` until the template is fixed or the reference is
removed. Secrets are fetched by name
through the uncached API reader on every resolve, so token/CA rotation takes
effect immediately for API objects; the `Connected` condition catches up on
its next periodic recheck. A Forbidden read is reported as condition reason
`Forbidden`. Narrowing further to per-namespace opt-in (a get-only ClusterRole
bound by namespace owners with a RoleBinding) is an open follow-up.

Trust boundary: the operator reads the Secret named in `tokenSecretRef` with
its own privileges and sends its value to `connection.url`. To stop an
`AuthentikDeployment` author from using that to exfiltrate arbitrary Secrets
in the namespace, **every referenced Secret (token and CA bundle) must carry
the label `authentik.io/connection: "true"`**. Unlabeled Secrets are refused
(condition reason `SecretNotOptedIn`) before any key is read or any request
is sent. Only someone who can write the Secret can opt it in. For
cert-manager CA Secrets, set the label via the Certificate's
`spec.secretTemplate.labels`. The operator-level default connection
(file/env) is unaffected.

The label is enforced by the operator, not the API server: it defends
against misuse *of* the operator, not against a compromised operator or a
leaked ServiceAccount token, which still hold cluster-wide Secret `get`.
Per-namespace RBAC (enforced by the API server) is the defence for that and
is deferred to TASK-04, where the operator first needs Secret *write*. See
the RBAC notes in TASK-04. Operators should also restrict the operator's
egress with a NetworkPolicy, since connection URLs are user input.

The `AuthentikDeployment` controller reports reachability in the `Connected`
condition (checked via `GET /api/v3/core/users/me/` with a 15s overall
deadline, every 5 minutes when healthy, 30 seconds when not) and the resolved
base URL in `status.url`. Operator readiness does not depend on any Authentik
being reachable, since one operator may serve several.

`spec.mode` is mutable. Switching ConnectOnly → Managed in place is the
migration path for an install that starts out Helm-managed: it must be
possible without recreating the resource, and it takes over nothing by itself
because adoption is gated by `spec.adoption.enabled` and is reversible. The
only rule kept across updates is that ConnectOnly requires `spec.connection`.

## Known upstream defects this rewrite exists to fix

From `docs/refactor/authentik-operator-code-map.md` §9, condensed:

- bootstrap token is a compile-time constant published in the repo
- plaintext HTTP to a hardcoded in-cluster address, unconfigurable
- generated client IDs/secrets written back into the CR spec in plaintext
- no pagination on any list endpoint
- every object resolved by display name, so renames orphan the CR
- drift detection by `serde_json::to_string` equality
- `400` mapped to `NotFound` on three delete paths
- zero tests

Plus seven Authentik API breaks since 2022 — see
`docs/refactor/authentik-api-drift.md`. The two silent ones
(`Group.parent` → `parents`, `token_validity` split) matter most, because the
upstream code reports success while losing data.
