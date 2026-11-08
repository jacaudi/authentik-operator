# TASK-07: Release pipeline -- multi-arch image and OCI Helm chart

## Ticket
- Ticket: none
- Status: In Progress (chart written and unit-tested; OCI publishing and the
  kind smoke are wired but unverified until a release runs on a public repo)
- Milestone: parity-with-jeffresc-v0.2.5 (a cutover needs a release to pin)

## Context
`.github/workflows/` has lint, test and test-e2e but no release. GitOps
consumers (Flux, Argo CD) pin a chart version from an OCI registry and an image.

Lessons from JeffResc to avoid:
- chart 0.2.5 defaults to an image repo that does not exist
  (`ghcr.io/JeffResc/...`, uppercase -> MANIFEST_UNKNOWN); the chart's image
  default must be tested by CI
- its image tag is a multi-arch index with `unknown/unknown` attestation
  entries, which some registry mirrors and pull-through caches cannot sync,
  forcing consumers to pin single-platform digests. Publish per-platform
  digests in release notes so pinning is easy.
- arm64 users of the current operator have no native image; a real arm64
  build removes that.

## Implementation Plan
- [ ] Tag-triggered workflow (semver `vX.Y.Z`): build linux/amd64 + linux/arm64
      with buildx, push to `ghcr.io/<owner>/authentik-operator` (lowercase)
- [ ] Release notes list each per-platform digest
- [x] Helm chart via kubebuilder `helm/v2-alpha` (`dist/chart`, hybrid: CRD/RBAC/
      metrics templates are plugin-owned, `make chart-regen`; Deployment, values
      and helpers are hand-owned; see AGENTS.md); chart version == app version
      (release-please `extra-files` on `dist/chart/Chart.yaml`)
- [ ] Chart published as OCI to `ghcr.io/<owner>/charts` (`ci-chart.yml`, public
      repos only; written, UNVERIFIED until the first public release)
- [x] Chart values: connection URL + token Secret + CA bundle ref, replicaCount,
      resources, securityContext (runAsNonRoot, RuntimeDefault seccomp, drop
      ALL, read-only rootfs), PDB, topologySpread, priorityClass,
      nodeSelector/tolerations/affinity, ServiceMonitor, NetworkPolicy, logging,
      extraArgs/extraEnv, image digest pin, imagePullSecrets
      (50 helm-unittest cases in `dist/chart/tests`)
- [ ] Chart values: webhook toggles and `watchNamespaces`. NOT exposed on purpose:
      the binary has neither a watch-namespaces flag nor an active webhook.
      Follow-up gated on TASK-06.
- [x] CRDs shipped in the chart (templates with `helm.sh/resource-policy: keep`,
      `crd.enabled`/`crd.keep`); upgrade path documented in `dist/chart/README.md`
- [ ] CI smoke: PR pipeline installs the chart into kind with the digest-pinned
      PR image (`ci-smoke.yml`, `task chart:smoke`); the release pipeline
      installs the PACKAGED chart with its DEFAULT image values before pushing
      (`ci-chart.yml`). Written, not yet run in Actions (no local Docker).
- [ ] Container runs as non-root, static binary, distroless base

## Dependencies
- Requires: TASK-06 (for chart knobs)
- Blocks: migration from JeffResc (TASK-09)

## Completion Checklist
- [ ] v0.1.0 (or first parity tag) published: image + chart pullable anonymously
- [ ] README install section replaces the kubebuilder TODO template
