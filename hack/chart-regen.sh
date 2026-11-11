#!/usr/bin/env bash
# Regenerate (or check) the plugin-owned parts of dist/chart.
#
# dist/chart is a hybrid. The kubebuilder helm/v2-alpha plugin derives these
# directories from config/ and they are NEVER edited by hand:
#   templates/{crd,rbac,metrics,network-policy,prometheus}
# Everything else is hand-owned and is never written by this script: Chart.yaml,
# values.yaml, templates/_helpers.tpl, templates/NOTES.txt, templates/pdb.yaml,
# templates/manager/manager.yaml, tests/, ci/, README.md.
#
# The plugin is pointed at a scratch --output-dir under bin/, so the live
# dist/chart is only touched at the very end, and only in the generated dirs.
#
# CAUTION: before scaffolding, the plugin runs `make manifests generate
# build-installer`. That can rewrite config/crd/bases, config/rbac/role.yaml and
# api/**/zz_generated*.go (the same files `make verify` regenerates; review
# `git status` afterwards). A build-installer failure is only a WARNING in
# kubebuilder v4.16.0 (pkg/plugins/optional/helm/v2alpha/edit.go), so a stale
# dist/install.yaml could be used silently; this script therefore runs
# `make build-installer` itself first and fails hard. The plugin also rewrites
# PROJECT (output:), Makefile and config/manager/kustomization.yaml, and may add
# .github/workflows/test-chart.yml; an EXIT/INT/TERM trap restores all of them.
#
#   hack/chart-regen.sh           regenerate in place
#   hack/chart-regen.sh --check   exit 1 if the plugin-owned parts drift
set -euo pipefail

cd "$(dirname "$0")/.."
mode="${1:-write}"
kb="${KUBEBUILDER:-bin/kubebuilder}"
[ -x "${kb}" ] || { echo "kubebuilder not found at ${kb}; run: make kubebuilder" >&2; exit 2; }

want=$(sed -n 's/^cliVersion: *//p' PROJECT)
have=$("${kb}" version 2>/dev/null | sed -n 's/^KubeBuilder: *//p' | head -1)
[ "${have#v}" = "${want#v}" ] || { echo "kubebuilder '${have}' != PROJECT cliVersion '${want}'" >&2; exit 2; }

generated=(crd rbac metrics network-policy prometheus)
tmp=$(mktemp -d)

cp PROJECT "${tmp}/PROJECT"
cp Makefile "${tmp}/Makefile"
cp config/manager/kustomization.yaml "${tmp}/kustomization.yaml"
had_workflow=0
if [ -e .github/workflows/test-chart.yml ]; then had_workflow=1; fi

restore_side_effects() {
  cp "${tmp}/PROJECT" PROJECT
  cp "${tmp}/Makefile" Makefile
  cp "${tmp}/kustomization.yaml" config/manager/kustomization.yaml
  if [ "${had_workflow}" = 0 ]; then rm -f .github/workflows/test-chart.yml; fi
}
cleanup() {
  restore_side_effects
  rm -rf "${scratch:-bin/chart-regen}" "${tmp:?}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

scratch="bin/chart-regen"
rm -rf "${scratch:?}"
mkdir -p "${scratch}"

# Fail hard where the plugin would only warn.
if ! make build-installer IMG=controller:latest >"${tmp}/installer.log" 2>&1; then
  cat "${tmp}/installer.log" >&2
  echo "make build-installer failed" >&2
  exit 1
fi
if ! "${kb}" edit --plugins=helm/v2-alpha --output-dir="${scratch}" >"${tmp}/plugin.log" 2>&1; then
  cat "${tmp}/plugin.log" >&2
  exit 1
fi
fresh="${scratch}/chart"
[ -d "${fresh}/templates" ] || { echo "plugin produced no chart in ${fresh}" >&2; exit 1; }

# A new generated directory (e.g. webhook or certmanager after a PROJECT change)
# must be adopted deliberately, not silently dropped.
for d in "${fresh}"/templates/*/; do
  name=$(basename "${d}")
  case " ${generated[*]} manager " in
    *" ${name} "*) ;;
    *)
      echo "plugin generated an unknown directory templates/${name}; add it to hack/chart-regen.sh and review its values" >&2
      exit 1
      ;;
  esac
done

drift=0
for d in "${generated[@]}"; do
  if [ "${mode}" = "--check" ]; then
    diff -ru "dist/chart/templates/${d}" "${fresh}/templates/${d}" || drift=1
  else
    rm -rf "dist/chart/templates/${d}"
    cp -a "${fresh}/templates/${d}" "dist/chart/templates/${d}"
  fi
done

if [ "${drift}" = 1 ]; then
  echo "dist/chart is out of date with config/. Run 'make chart-regen' and commit." >&2
  exit 1
fi
echo "dist/chart plugin-owned templates: ${mode} ok"
