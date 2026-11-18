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
| `AuthentikApplication` | an application in Authentik, and who may open it (`spec.access`); see "Application access (TASK-10)" |
| `AuthentikUser` | a person: adopted, or invited to enrol (the operator never creates a person through the API or manages a password); see "Users (TASK-10)" |
| `AuthentikGroup` | a group; `members` is authoritative when set; see "Groups (TASK-10)" |
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
delete -- applied to objects inside Authentik. `AuthentikGroup` and
`AuthentikUser` follow the same contract through `internal/adopt`, behind the
identity gate (see "Identity gate (TASK-10)", "Groups (TASK-10)" and "Users
(TASK-10)"). Migrating from another operator is the use
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
Two exceptions for identities: an unset `deletionPolicy` on an `AuthentikUser`
is `Retain` whatever its provenance, and outside the identity selector a group
or user is deleted only if it was `Created` (see "Users (TASK-10)" and "Identity
gate (TASK-10)").
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
| `--default-connection-identity-namespace-selector` | | label selector for the namespaces whose `AuthentikGroup` and `AuthentikUser` resources are reconciled on the default connection, under every `managementPolicy` (`Create` included), checked before the adoption gate (reason `IdentityNotAllowed`); empty (the default) denies all; outside the selector deletion removes only `Created` objects, and adopted or undecided ones are retained with a Warning; objects with `authentikRef` are not affected (see "Identity gate (TASK-10)") |

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

## Identity gate (TASK-10)

`AuthentikGroup` and `AuthentikUser` act on Authentik with the operator's own
token, which spans every team, so who may create, change or delete them on the
default connection is an administrator's decision, separate from adoption.

- Flag `--default-connection-identity-namespace-selector` (chart value
  `identity.defaultConnectionNamespaceSelector`): a Kubernetes label selector
  matched against the Namespace's labels, parsed at startup (an invalid selector
  stops the operator). The recommended value, and the owner's, for this and for
  the adoption selector, is `kubernetes.io/metadata.name in (authentik)`.
- On the default connection a group or user reconciles only in a namespace the
  selector matches, **under every `managementPolicy`, `Create` included**: even
  creating a person or a group with the operator's token is gated. An empty
  selector, the default, denies every namespace. A denied resource reports
  `Ready=False`, reason `IdentityNotAllowed`.
- The identity gate is checked **before** the adoption gate, which still applies
  afterwards to `Adopt`, `AdoptOrCreate` and `ReadOnly` (a group that adopts
  needs both selectors).
- `spec.authentikRef` is not gated: that deployment's own token bounds what the
  resource can reach.
- **Outside the selector, deletion removes only `Created` objects.** A group or
  user that was adopted, or whose provenance is undecided, is retained when its
  resource is deleted, with a Warning event, whatever `deletionPolicy` says.
  Like the other namespace selectors there is no Namespace watch: a label change
  takes effect within the 5-minute recheck.

Trust boundaries:

- Kubernetes RBAC decides who may write the resources (the chart's helper roles
  are off by default and not aggregated into the built-in roles).
- Inside an allowed namespace a writer has full power over users and groups in
  Authentik, superuser groups included, and anyone who can read Secrets there
  can read an invitation link (see "Users").
- Authentik creates `ak-<provider name>-client_credentials` users on demand;
  never list them in a group.
- Removing someone from an access group stops new sign-ins only: refresh tokens
  already issued are not re-checked against the bindings (only
  `client_credentials` and token exchange run the policy check). To cut a person
  off at once, deactivate them (`isActive: false`); Authentik deletes a
  deactivated user's tokens.

## Application access (TASK-10)

`AuthentikApplication.spec.access` says who may open the application, and is
required (except under `ReadOnly`, where it is optional and drift is reported):

```yaml
access:
  type: Groups              # or Unmanaged; a union discriminator
  groups: [immich-users]    # Authentik group names, 1 to 64; required iff type Groups
```

It is not the existing `spec.group`, which is the label of the application's
section in the library.

- **`Groups`**: the application's group bindings are exactly the listed groups.
  Each is held `enabled: true`, `negate: false`. The operator adds the missing
  bindings (`order` one past the highest existing) **before** removing unlisted
  or duplicate ones, and removes only from a complete picture: if a listed group
  does not resolve (missing, unreadable to the token, or renamed in the UI) it
  adds what it can, removes nothing, and sets `AccessInSync=False`, reason
  `GroupNotFound`. Group names are resolved on every reconcile, not pinned.
- **`Unmanaged`**: no binding is ever written. `status.access.bindings` lists
  the live ones, and an adoption dry run lists them too.
- User and policy bindings, and group bindings with `expiring: true` (2026.5+
  temporary grants), are never touched; they are listed in status and the dry
  run as "also grants access". A negated group binding follows the live
  `policyEngineMode`: under `any` it admits every user outside the group, so it
  is removed; under `all` it excludes the group, so it is left and reported.
- `policyEngineMode` follows the adoption contract (spec wins, unset keeps live,
  `any` on create). The CRD refuses `all` with more than one listed group; a
  live `all` with several (an adopted application) is left alone, because it only
  narrows access, and reported as `AccessInSync=False`, reason `AccessNarrowed`.
- **Never zero bindings by the operator's hand.** Under `Groups` the operator
  neither creates the application nor takes over an existing one until at least
  one listed group resolves (`Ready=False`, reason `WaitingForGroups`). Any
  application this resource created (new, recreated, or created earlier whose
  listed bindings were since removed) has `Ready=False`, reason `AccessPending`,
  with a one-minute requeue, for as long as none of its listed groups has a
  managed binding, and a Warning event if the binding write fails.
- Conditions: `Ready` keeps its meaning (the application itself); `AccessInSync`
  (`Groups` only; reasons `GroupNotFound`, `AccessNarrowed`, `AuthentikError`,
  `DriftDetected` (under `ReadOnly`, where the bindings differ from the spec),
  `InSync`); `AccessOpen` (`True`, reason `NoBindings`, when the application has no
  enabled binding of any kind, so every user may open it unless the instance
  setting below is off). A binding problem never flips `Ready`, except
  `AccessPending` and `WaitingForGroups`. An `AuthentikGroup` change re-enqueues
  the applications that list its name.
- **Safety of the binding list.** Bindings are listed with `target=<application
  pk>`; an empty pk is an error and never a query (Authentik ignores an empty
  `target=` and would return every binding in the instance), and every delete
  re-checks `binding.target` against the application's pk.
- **Token permissions.** The operator's token must be able to view policy
  bindings for every application it manages, and to add them (`add_policybinding`)
  to bind groups. Without view permission the binding list fails: a `Groups`
  application reports `AccessInSync=False`, reason `AuthentikError`, the
  reconcile backs off and no binding is written (an application just created
  reports `Ready=False`, reason `AccessPending`).
- **Recommended instance setting.** Turn off "Applications with no policies bound
  can be accessed by any user" in Authentik (2026.5+): with it on, an application
  without an enabled binding is open to every user, and `AccessOpen=True` says so.
  The operator does not read or change the setting.

## Groups (TASK-10)

`AuthentikGroup` follows the adoption contract through `internal/adopt`, with
the identity gate above in front of it. There is no `match` field: group names
are unique in Authentik, so the target is the group named `spec.name` (default
`metadata.name`). `spec.name` cannot change once the group is created or adopted
(`InvalidSpec`): renaming it would change every application's bindings by name.

| Field | Meaning |
|---|---|
| `name` | The group's name in Authentik; default `metadata.name`; 1 to 255 characters. |
| `isSuperuser` | `*bool`. Unset: held to `false` on a group this resource **created** (a UI flip to superuser is reverted); the live value on an adopted group. |
| `attributes` | Per-key owned map, below. Group attributes are inherited by members in claims, so this is where a role claim lives. |
| `members` | Authentik usernames. **Absent: membership is never read or written**, whatever the provenance (an exception to "created objects are held to create-time defaults"). **Present, including `[]`: exactly this list.** |

`parents` and `roles` are not fields: their live values are kept and never set
(setting `parents` is itself an access grant).

- **Attributes are owned per key.** Authentik replaces the whole `attributes`
  dict on every write and keeps its own keys there (`settings`,
  `goauthentik.io/...`), so the operator owns only the keys the spec lists. Each
  sync reads the live dict, sets the listed keys, deletes keys it owned
  (`status.managedAttributes`) and no longer lists, keeps every other key, and
  PATCHes only when something differs. Absent `attributes` reads and writes
  nothing (earlier keys are released, not deleted); `{}` removes the owned keys.
  Values are strings, numbers, booleans or lists, top-level keys only, at most
  64. Keys starting `goauthentik.io/`, and `authentik_operator` (the ownership
  proof) and `settings`, are reserved: the resource reports `InvalidSpec` and
  writes nothing. That refusal is made at reconcile, not by CEL, because
  Kubernetes cannot type a map of untyped JSON values.
- **Lost create.** `status.pendingCreate` holds the name while a create is in
  flight. A group found by that name is this resource's only if it carries the
  attribute `authentik_operator.owner_uid: <metadata.uid>` (a nested dict:
  Authentik splits attribute paths on dots), written in the create request. A
  target found while the create is pending but unproven is a `Conflict` under
  every policy, `AdoptOrCreate` included (it could be a squatter's group). The
  provider and application controllers keep their own rules for an unproven
  target (see "Lost creates" above); settling that difference is noted in
  TASK-05 and TASK-10.
- **Members, authoritative when present.** Each username resolves to a user pk.
  An unknown username is reported (`MembersInSync=False`, reason
  `MemberNotFound`) while every other add and every removal still applies. A
  write Authentik refuses (for example an `add_user` the token may not make) is
  reported the same way, reason `AuthentikError`, and does not stop the other
  writes: a person removed in Git still loses membership. A
  username whose pk differs from the one pinned in `status.members` is held out
  (`MemberIdentityChanged`) until the pin is cleared. A username is **held**
  (`MemberHeld`: neither added nor removed) only while an `AuthentikUser` with
  that `spec.username` is still being invited (same Authentik, through the
  identity gate, policy `Create` or `AdoptOrCreate`, provenance not yet decided,
  invitation present). The operator token's own user is never removed. Writes are
  `add_user` and `remove_user` per member, never a PUT and never `users` in a
  group write. A members problem never makes `Ready` false. The adoption dry run
  lists the usernames it would add and remove, and under `ReadOnly` the `Drifted`
  condition does.
- **Deletion** follows `EffectiveDeletionPolicy` and the identity rule above.
  When the effective policy would delete the group, the finalizer first asks
  Authentik what uses it (`GET /core/groups/{uuid}/used_by/`). While any policy
  binding uses it, on any application (one with a resource, `Unmanaged`, or
  existing only in the UI), nothing is deleted: the resource stays terminating
  with `Ready=False`, reason `DeletionBlocked`, naming each binding, and the
  remedy: remove the binding in Authentik or the group from the application's
  `access.groups`, or set `deletionPolicy: Retain`. If `used_by` cannot be read
  the group stays too. `used_by` lists only what the token may see, so unless the
  token's user is a superuser the operator cannot know the list is complete: it
  then retains the group, removes the finalizer and emits the Warning
  `RetainedUnverifiable` (delete the group in Authentik after checking its
  bindings). `Retain` deletes nothing and is never blocked. A group the resource
  created is deleted only if it still carries the resource's ownership proof
  (otherwise a Warning `Retained` is emitted and the group stays).
- A group this resource created that vanishes is recreated; an adopted one that
  vanishes reports `AdoptionTargetNotFound`.
- Recheck: 5 minutes when synced, 1 minute on errors the user can fix.

## Users (TASK-10)

`AuthentikUser` manages **people**. The operator never creates a person through
the API and never manages a password: it invites them, or adopts an account that
exists. `spec.username` is required and immutable; `name`, `email`, `path`,
`type` (`internal` or `external`), `isActive`, `firstName` and `lastName` are
optional, and **unset keeps the live value, for a `Created` account too** (the
person made the account in their own enrolment, so there are no operator
create-time defaults to hold; a stated departure from the adoption contract,
like `members` on groups). `firstName` and `lastName` are written to the
attribute keys `given_name` and `family_name`, which the OIDC `profile` scope
reads, so applications receive them as `given_name` and `family_name`.
`attributes` are per key, as for groups; `given_name` and `family_name` are
reserved there as well, and reserved keys are refused at reconcile with
`InvalidSpec`, not by CEL.

**Create means invite.** `spec.invitation` (`flow`: the enrolment flow's slug;
`expiresAfter`, default 7 days; `secretName`, default `<metadata.name>-invitation`;
`attempt`, default 1) is required when the policy may create (`Create`,
`AdoptOrCreate`) and refused under `Adopt` and `ReadOnly`.

1. The connection needs an explicit browser-facing base URL
   (`--authentik-external-url` / chart `externalUrl` for the default connection,
   `spec.connection.externalURL` on an `AuthentikDeployment`). Without one:
   `Ready=False`, reason `ExternalURLRequired`, and no invitation is made.
2. The operator creates a single-use invitation named `k8s-<metadata.uid>-<attempt>`
   whose fixed data pins the username and carries the ownership proof
   `attributes.authentik_operator.owner_uid`, and writes the link
   `<externalURL>/if/flow/<slug>/?itoken=<pk>` to the Secret (keys `url` and
   `expires`). `status.invitation` records the name and expiry. **The invitation
   pk is the sign-up token: it is written only to the Secret, never to status, a
   condition, an event, a log line or an error.** Anyone who can read Secrets in
   that namespace can read the link.
3. `Ready=False`, reason `InvitationPending`, until an account carrying the
   ownership proof exists (found by an attribute query and re-checked
   client-side). If its username equals `spec.username` the user is `Created`,
   the invitation status is cleared and the Secret deleted. If it differs,
   `EnrolledUnderOtherName`: it is not adopted. More than one account with the
   proof: `MultipleEnrolments`.
4. A person named `username` who appears without the proof while inviting is a
   `Conflict`; the invitation and its Secret are deleted. An invitation that
   disappears before any account carries the proof is `InvitationConsumed`
   (before `status.invitation.expires`) or `InvitationExpired`, with no automatic
   reissue: a normal enrolment passes through that state briefly, and the account
   search keeps running. Several invitations with the same name:
   `InvitationAmbiguous`; the operator deletes none of them. The same reason
   covers a single invitation of that name that does not carry this resource's
   username, single use and ownership proof: the operator did not issue it, so it
   writes no link for it and deletes nothing.
5. **Reissue only by `attempt`.** Increase `spec.invitation.attempt` for a new
   link; the operator deletes any live invitation of the previous attempt and
   rewrites the Secret. Once the user is `Created`, `attempt` does nothing.
6. A person who stopped after the account was made but before enrolling an
   authenticator is still recognised and `Created`; the condition
   `AuthenticatorMissing` (reason `NoAuthenticator`) is `True` and does not
   affect `Ready`.
7. Switching the policy away from `Create` or `AdoptOrCreate`, or deleting the
   resource, while inviting deletes the live invitation. A switch to `ReadOnly`
   (or `Adopt`) therefore performs one Authentik write, the deletion of the
   invitation, in the safe direction. `dryRun` under `AdoptOrCreate` mid-invitation
   does not: the invitation is still live.
8. A resource whose namespace the identity gate stops allowing while inviting has
   its live invitation and its Secret deleted before it reports
   `IdentityNotAllowed`; `status.invitation` stays, so the resource reports
   `InvitationConsumed` once allowed again, and a new link needs a higher
   `attempt`.

**Owner's blueprint change (documented, not automated).** The enrolment flow's
username prompt must be `text_read_only`, so the invitation's pinned username
wins over anything the person types. Without it a person can enrol under another
name and the resource reports `EnrolledUnderOtherName` and adopts nothing. Flows
and stages are blueprints, outside this operator's scope.

**Recommended recovery flow for lost passkeys.** A person who loses their only
passkey cannot be re-invited (the username is taken, and a new invitation would
enrol another account). Have a recovery path ready in Authentik instead: a
recovery flow, set on the brand, that ends in an authenticator-setup stage,
managed in the same blueprint as the enrolment flow, from which an administrator
creates a recovery link on the user's page in the admin UI. The operator does
not create or manage it.

Other behaviour:

- Adopt and ReadOnly find the account by `username`. The operator refuses to
  adopt a `service_account` or `internal_service_account` user (`Conflict`): people
  only. Under `AdoptOrCreate` an existing account is adopted.
- A `Created` user that vanishes from Authentik is **not** re-invited:
  deleting a person in the UI is read as intent. `Ready=False`, reason
  `AdoptionTargetNotFound`, `userPk` kept; clear the status and bump `attempt` to
  invite again.
- **Deletion: an unset `deletionPolicy` resolves to `Retain` for every
  `AuthentikUser`, whatever its provenance.** This is a deliberate per-kind
  exception to `EffectiveDeletionPolicy` (re-enrolling a passkey is costly); an
  explicit `Delete` still deletes, and `ReadOnly` never deletes. An account the
  resource created is deleted only if it still carries the resource's ownership
  proof (otherwise a Warning `Retained` is emitted and the account stays). A
  retained account stays active: set `isActive: false` first to offboard a
  person. The operator never deactivates or deletes its own token's user.
- Writes are PATCH only (a PUT would clear the user's groups and roles); the
  operator never sends `groups` or `roles`. Group membership is managed from
  `AuthentikGroup.spec.members`.
- Gates and rechecks are those of groups: the identity gate first, then the
  adoption gate for `Adopt`, `AdoptOrCreate` and `ReadOnly`.

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
