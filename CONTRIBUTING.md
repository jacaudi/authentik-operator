# Contributing

Thanks for helping improve authentik-operator. This is a Kubebuilder project;
see `AGENTS.md` for the layout and the files you must not edit by hand.

## Development loop

```sh
make verify   # go.mod tidy, generated CRDs/RBAC/DeepCopy and formatting are committed
make lint     # golangci-lint (builds a custom binary with the logcheck plugin)
make test     # unit tests + envtest (real API server and etcd)
make test-e2e # installs a real Authentik (official chart) in kind and runs test/e2e (needs docker, kind, helm)
```

CI runs the same targets, so a green local run means a green CI run.

### End-to-end suite

`make test-e2e` builds the operator image, creates a kind cluster, installs
PostgreSQL and the official Authentik chart, installs the operator from
`dist/chart`, and runs the specs in `test/e2e` against that real server.
The Authentik version is a parameter: `AUTHENTIK_CHART_VERSION=2026.2.3 make
test-e2e` (CI runs a matrix over 2026.2.3 and 2026.8.5). Other knobs:
`KIND_CLUSTER` (the suite refuses to run unless kubectl's current context is
`kind-<KIND_CLUSTER>`), `E2E_IMAGE`, `E2E_SKIP_BUILD=true` (image already
loaded), `CONTAINER_TOOL=podman`. Budget 10-15 minutes: the first Authentik
start migrates its database. The helpers in `test/aktest` (headless OIDC login,
REST helper, private CA) have ordinary unit tests under `make test`.

After changing `api/**/*_types.go` or kubebuilder markers, run
`make manifests generate` and commit the regenerated files; `make verify`
fails otherwise.

## Pull requests

- PRs are squash-merged; the **PR title becomes the commit message** and must
  follow [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): description`, e.g. `feat(application): add metaHide`.
  Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`,
  `perf`, `revert`, `deps` (Renovate's `deps(renovate): ...`). Mark breaking
  changes with `!` (`feat(api)!: ...`).
  Release notes and version numbers are generated from these titles.
- Include tests for behaviour changes; reconcilers must stay idempotent
  (a second reconcile must not write to Authentik).
- Never commit secrets, tokens or `.env` files.
- ASCII only in code, strings and scripts.

## Reporting security issues

Please do not open public issues for vulnerabilities; see
[SECURITY.md](SECURITY.md).
