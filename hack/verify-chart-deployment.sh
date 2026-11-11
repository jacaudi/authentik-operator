#!/usr/bin/env bash
# Assert the chart's default Deployment matches the kustomize install bundle
# (config/default + config/manager): container command, args, ports, probes,
# resources, container and pod securityContext, terminationGracePeriodSeconds.
#
# Known, allowed differences (not compared): the image reference, the chart's
# --zap-* logging flags (production defaults, see values.yaml), names/labels.
#
# Needs: helm, yq (mikefarah v4), bin/kustomize (make kustomize).
# CHART=<dir> compares another chart copy (used to prove the check can fail).
set -euo pipefail

cd "$(dirname "$0")/.."
chart="${CHART:-dist/chart}"
kustomize="${KUSTOMIZE:-bin/kustomize}"
[ -x "${kustomize}" ] || { echo "kustomize not found at ${kustomize}; run: make kustomize" >&2; exit 2; }

pick() { # $1 = yq expression, stdin = multi-doc yaml
  yq "select(.kind == \"Deployment\") | $1" -o=json -I=0
}
exprs=(
  '.spec.template.spec.containers[0].command'
  '.spec.template.spec.containers[0].args | map(select(test("^--zap-") | not)) | sort'
  '.spec.template.spec.containers[0].ports'
  '.spec.template.spec.containers[0].livenessProbe'
  '.spec.template.spec.containers[0].readinessProbe'
  '.spec.template.spec.containers[0].resources'
  '.spec.template.spec.containers[0].securityContext'
  '.spec.template.spec.securityContext'
  '.spec.template.spec.terminationGracePeriodSeconds'
  '.spec.replicas'
)

want_doc=$("${kustomize}" build config/default)
got_doc=$(helm template deploy-check "${chart}" --namespace deploy-check)

fail=0
for e in "${exprs[@]}"; do
  want=$(printf '%s\n' "${want_doc}" | pick "${e}")
  got=$(printf '%s\n' "${got_doc}" | pick "${e}")
  if [ -z "${want}" ] || [ "${want}" != "${got}" ]; then
    echo "MISMATCH ${e}" >&2
    echo "  kustomize: ${want}" >&2
    echo "  chart:     ${got}" >&2
    fail=1
  fi
done
[ "${fail}" = 0 ] && echo "ok: chart Deployment matches config/default (${#exprs[@]} fields)"
exit "${fail}"
