# TASK-07: Release pipeline -- multi-arch image and OCI Helm chart

## Ticket
- Ticket: none
- Status: Planned
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
- [ ] Helm chart via kubebuilder `helm/v2-alpha` plugin (`dist/chart`), published
      as OCI to `ghcr.io/<owner>/charts`; chart version == app version
- [ ] Chart values (parity with JeffResc): connection URL + token secretRef + CA
      bundle ref (TASK-01), replicaCount, resources, securityContext
      (runAsNonRoot, RuntimeDefault seccomp, drop ALL, read-only rootfs),
      PDB, topologySpread, priorityClass, nodeSelector/tolerations, metrics
      ServiceMonitor, webhook toggles, watchNamespaces, logging
- [ ] CRDs shipped in the chart; upgrade path documented
- [ ] CI smoke: `helm template` + install into kind and wait for Ready using the
      chart's DEFAULT image values
- [ ] Container runs as non-root, static binary, distroless base

## Dependencies
- Requires: TASK-06 (for chart knobs)
- Blocks: migration from JeffResc (TASK-09)

## Completion Checklist
- [ ] v0.1.0 (or first parity tag) published: image + chart pullable anonymously
- [ ] README install section replaces the kubebuilder TODO template
