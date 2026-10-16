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
**get/create/update/patch/delete** on Secrets -- no list, no watch, no cache.
Writes exist for credential Secrets (TASK-04): they go only to the provider's
own namespace, and an existing Secret not controlled by that provider is
never modified or deleted (`SecretConflict`).

Trust boundary for credential Secrets, with personas split as in Crossplane's
XRD/Composition versus claim model:
- Platform administrators own the cluster-scoped `AuthentikCredentialsTemplate`.
  It holds the data template, the labels and the annotations, and its
  `namespaceSelector` limits which namespaces may use it.
- Teams own `AuthentikOAuthProvider`, and choose only the Secret's name and
  which template to reference.

So provider write lets a team make the operator create a Secret, with any name
not already taken, in the team's own namespace. The Secret holds that
provider's client secret, in a shape an administrator defined. Provider write
does not let a team choose the Secret's keys, labels or annotations, so a team
cannot, for example, opt a rendered Secret into `authentik.io/connection` or
into another controller's label selector. Template labels and annotations also
reject the reserved prefixes `authentik.io/`, `kubernetes.io/` and `k8s.io/`.
Templates are restricted (no loops, no nested templates, capped output,
`.ClientSecret` printable only), so even a template author cannot hang or crash
the operator or probe the client secret through render errors.

Checking the namespaceSelector needs `get` on Namespaces. When a template
stops allowing a namespace, the existing Secret is kept, and the provider
reports `TemplateNotAllowed` until the template is fixed or the reference is
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
being reachable, since one operator may serve several. `spec.mode` is
immutable.

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
