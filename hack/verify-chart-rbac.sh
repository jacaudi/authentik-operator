#!/usr/bin/env bash
# Assert the chart renders the same RBAC rules as config/rbac (the kustomize
# install bundle). Complements hack/chart-regen.sh --check: that one proves the
# generated templates match the plugin; this one proves the rendered result
# matches the source of truth, whatever the plugin does.
#
# Needs: helm, yq (mikefarah v4).
set -euo pipefail

cd "$(dirname "$0")/.."
chart=dist/chart
rendered=$(helm template rbac-check "${chart}" --namespace rbac-check)

# "<kind>|<chart name suffix>|<config file>" -- config files hold one object.
pairs=(
  "ClusterRole|manager-role|config/rbac/role.yaml"
  "Role|leader-election-role|config/rbac/leader_election_role.yaml"
  "ClusterRole|metrics-auth-role|config/rbac/metrics_auth_role.yaml"
  "ClusterRole|metrics-reader|config/rbac/metrics_reader_role.yaml"
)

norm='.rules | sort_by(.apiGroups, .resources, .nonResourceURLs) | map(sort_keys(.))'
fail=0
for p in "${pairs[@]}"; do
  IFS='|' read -r kind suffix file <<<"${p}"
  want=$(yq "${norm}" "${file}" -o=json -I=0)
  got=$(printf '%s\n' "${rendered}" \
    | yq "select(.kind == \"${kind}\" and (.metadata.name | test(\"-${suffix}\$\"))) | ${norm}" -o=json -I=0)
  if [ -z "${got}" ] || [ "${want}" != "${got}" ]; then
    echo "RBAC mismatch for ${kind} *-${suffix} vs ${file}" >&2
    echo "  config: ${want}" >&2
    echo "  chart:  ${got}" >&2
    fail=1
  else
    echo "ok: ${kind} *-${suffix} matches ${file}"
  fi
done
exit "${fail}"
