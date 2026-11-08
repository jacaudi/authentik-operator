#!/usr/bin/env bash
# Regenerate (or check) the plugin-owned parts of dist/chart.
#
# dist/chart is a hybrid. The kubebuilder helm/v2-alpha plugin derives these
# directories from config/ and they are NEVER edited by hand:
#   templates/{crd,rbac,metrics,network-policy,prometheus}
# Everything else is hand-owned and is preserved byte-for-byte: Chart.yaml,
# values.yaml, templates/_helpers.tpl, templates/NOTES.txt, templates/pdb.yaml,
# templates/manager/manager.yaml (the plugin would overwrite it with its generic
# Deployment), tests/, README.md.
#
# The plugin also rewrites PROJECT, Makefile, config/manager/kustomization.yaml
# and adds .github/workflows/test-chart.yml; those side effects are undone.
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
trap 'rm -rf "${tmp:?}"' EXIT

cp -a dist/chart "${tmp}/owned"
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

if ! "${kb}" edit --plugins=helm/v2-alpha --force >"${tmp}/plugin.log" 2>&1; then
  cat "${tmp}/plugin.log" >&2
  restore_side_effects
  exit 1
fi
cp -a dist/chart "${tmp}/fresh"
rm -rf dist/chart
cp -a "${tmp}/owned" dist/chart
restore_side_effects

# A new generated directory (e.g. webhook or certmanager after a PROJECT change)
# must be adopted deliberately, not silently dropped.
for d in "${tmp}"/fresh/templates/*/; do
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
    diff -ru "dist/chart/templates/${d}" "${tmp}/fresh/templates/${d}" || drift=1
  else
    rm -rf "dist/chart/templates/${d}"
    cp -a "${tmp}/fresh/templates/${d}" "dist/chart/templates/${d}"
  fi
done

if [ "${drift}" = 1 ]; then
  echo "dist/chart is out of date with config/. Run 'make chart-regen' and commit." >&2
  exit 1
fi
echo "dist/chart plugin-owned templates: ${mode} ok"
