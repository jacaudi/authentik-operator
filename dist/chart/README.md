# authentik-operator Helm chart

Installs the authentik-operator controller, its RBAC and its CRDs
(`AuthentikDeployment`, `AuthentikApplication`, `AuthentikUser`,
`AuthentikGroup`, `AuthentikOAuthProvider`, `AuthentikCredentialsTemplate`).

The chart version always equals the operator version (release-please bumps
both). It is published as an OCI artifact on public releases.

## Install

```bash
helm install authentik-operator oci://ghcr.io/jacaudi/charts/authentik-operator \
  --version X.Y.Z \
  --namespace authentik-operator-system --create-namespace
```

With a default Authentik connection (token read from an existing Secret and
mounted as a file; the token is never part of the values):

```bash
kubectl -n authentik-operator-system create secret generic authentik-api \
  --from-literal=token="$AUTHENTIK_TOKEN"

helm install authentik-operator oci://ghcr.io/jacaudi/charts/authentik-operator \
  --version X.Y.Z -n authentik-operator-system --create-namespace \
  --set authentik.url=https://authentik.example.com \
  --set authentik.tokenSecret.name=authentik-api
```

### Pinning the image digest

Release images are multi-arch (linux/amd64, linux/arm64) with no attestation
entries in the index, but registry mirrors and pull-through caches are often
easier to satisfy with a single-platform digest. Pin one (release notes list
the per-platform digests):

```bash
crane digest --platform linux/amd64 ghcr.io/jacaudi/authentik-operator:vX.Y.Z

helm upgrade --install authentik-operator oci://ghcr.io/jacaudi/charts/authentik-operator \
  --version X.Y.Z --set image.digest=sha256:<digest>
```

When `image.digest` is set the chart renders `repository@digest` and ignores
`image.tag`. If the GHCR package is private, set `imagePullSecrets`.

## CRDs and upgrades

CRDs are shipped as templates (`templates/crd/`), not in Helm's `crds/`
directory, because Helm never upgrades or deletes `crds/` content. As templates:

- `helm upgrade` applies CRD schema changes together with the new controller.
- With `crd.keep=true` (default) each CRD carries `helm.sh/resource-policy: keep`,
  so `helm uninstall` leaves the CRDs, and every custom resource, in place.
  Remove them deliberately with `kubectl delete crd` only when you mean to
  delete all Authentik objects managed through the operator.
- `crd.enabled=false` skips them if you manage CRDs separately (for example
  with `kubectl apply -f dist/install.yaml`'s CRDs or GitOps).

Upgrade path: `helm upgrade authentik-operator oci://... --version <new>`. Read
the release notes first for breaking CRD changes. Flux and Argo CD users can
reference the OCI chart directly.

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `nameOverride`, `fullnameOverride` | unset | Override resource naming. |
| `replicaCount` | `1` | Controller replicas (leader election keeps one active). |
| `image.repository` | `ghcr.io/jacaudi/authentik-operator` | Lowercase GHCR repository. |
| `image.tag` | `""` | Defaults to `v<appVersion>` (release images are tagged `v<semver>`). |
| `image.digest` | `""` | When set, renders `repository@digest`; overrides the tag. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | Needed if the GHCR package is private. |
| `leaderElection.enabled` | `true` | `--leader-elect`, as in `config/manager`. |
| `healthProbe.port` | `8081` | `/healthz` and `/readyz`. |
| `logging.level` | `info` | `--zap-log-level`. |
| `logging.devel` | `false` | `--zap-devel`. |
| `logging.encoder` | `json` | `--zap-encoder` (`json` or `console`). |
| `authentik.url` | `""` | `--authentik-url`: default connection for objects without `spec.authentikRef`. Empty disables it. |
| `authentik.externalUrl` | `""` | `--authentik-external-url`: browser-facing URL used in rendered credential Secrets. |
| `authentik.tokenSecret.name` | `""` | Existing Secret holding the API token. Mounted at `/var/run/secrets/authentik/token/token`, passed as `--authentik-token-file`. Read once at startup; restart the pod to rotate. |
| `authentik.tokenSecret.key` | `token` | Key within that Secret. |
| `authentik.caBundle.configMap.{name,key}` | `""`, `ca.crt` | Existing ConfigMap with a PEM CA bundle (set this OR `secret`); passed as `--authentik-ca-file`. |
| `authentik.caBundle.secret.{name,key}` | `""`, `ca.crt` | Same, from a Secret. |
| `authentik.insecureSkipVerify` | `false` | `--authentik-insecure-skip-verify`. Testing only. |
| `extraArgs` | `[]` | Extra flags appended to the manager args (templated with `tpl`). |
| `extraEnv` | `[]` | Extra container environment variables. |
| `extraVolumes`, `extraVolumeMounts` | `[]` | |
| `resources` | 500m/128Mi limits, 10m/64Mi requests | As in `config/manager`. |
| `podSecurityContext` | `runAsNonRoot`, seccomp `RuntimeDefault` | Restricted PSS. |
| `securityContext` | no privilege escalation, drop `ALL`, read-only rootfs | |
| `terminationGracePeriodSeconds` | `10` | |
| `strategy`, `deploymentAnnotations` | unset | |
| `podAnnotations`, `podLabels` | `{}` | Selector labels cannot be overridden. |
| `priorityClassName` | `""` | |
| `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints` | empty | |
| `podDisruptionBudget.enabled` | `false` | Off by default: with one replica a budget would block drains. |
| `podDisruptionBudget.minAvailable` / `.maxUnavailable` | unset / `1` | `minAvailable` wins when set. |
| `serviceAccount.enabled` | `true` | Create the ServiceAccount (the plugin-generated template calls this `enabled`). |
| `serviceAccount.name` | generated | Used whether or not the chart creates the account; required when `enabled=false`. |
| `serviceAccount.annotations`, `.labels` | `{}` | |
| `rbac.helpers.enabled` | `false` | Admin/editor/viewer ClusterRoles per CRD. |
| `rbac.namespaced` | `false` | Plugin-template compatibility; the operator needs cluster-wide access, leave `false`. |
| `crd.enabled` | `true` | Install the CRDs. |
| `crd.keep` | `true` | `helm.sh/resource-policy: keep` on CRDs. |
| `metrics.enabled` | `true` | Metrics server and Service. |
| `metrics.port` | `8443` | |
| `metrics.secure` | `true` | HTTPS with authn/authz (adds the metrics ClusterRoles); `false` serves plain HTTP. |
| `prometheus.enabled` | `false` | Render a `ServiceMonitor` (needs prometheus-operator CRDs). |
| `prometheus.labels`, `.annotations` | `{}` | Extra ServiceMonitor metadata, e.g. `release: kube-prometheus-stack`. |
| `networkPolicy.enabled` | `false` | Allow metrics scrapes only from namespaces labelled `metrics: enabled`. |
| `certManager.enabled` | `false` | Reserved by the generated ServiceMonitor template; the chart ships no Certificate. |

The `ServiceMonitor` skips TLS verification when `metrics.secure=true` because
controller-runtime serves a self-signed certificate (same as `config/default`).

### Not exposed yet

The binary has no webhook server in use and no watch-namespace flag, so the
chart has no `webhook.*` or `watchNamespaces` values. They are follow-ups gated
on TASK-06.

## Development

This chart is a hybrid of kubebuilder `helm/v2-alpha` output and hand-owned
files; see AGENTS.md ("Option 2: Helm Chart") for what is regenerated.

```bash
task chart:lint     # helm lint --strict, kubeconform, version sync, RBAC parity
task chart:test     # helm-unittest (dist/chart/tests)
task chart:drift    # plugin-owned templates match config/
task chart:smoke    # kind install; IMAGE=... overrides the image
```
