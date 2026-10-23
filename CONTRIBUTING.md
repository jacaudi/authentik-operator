# Contributing

Thanks for helping improve authentik-operator. This is a Kubebuilder project;
see `AGENTS.md` for the layout and the files you must not edit by hand.

## Development loop

```sh
make verify   # go.mod tidy, generated CRDs/RBAC/DeepCopy and formatting are committed
make lint     # golangci-lint (builds a custom binary with the logcheck plugin)
make test     # unit tests + envtest (real API server and etcd)
make test-e2e # Kind-based smoke test (needs Docker and kind)
```

CI runs the same targets, so a green local run means a green CI run.

After changing `api/**/*_types.go` or kubebuilder markers, run
`make manifests generate` and commit the regenerated files; `make verify`
fails otherwise.

## Pull requests

- PRs are squash-merged; the **PR title becomes the commit message** and must
  follow [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope): description`, e.g. `feat(application): add metaHide`.
  Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`,
  `perf`, `revert`. Mark breaking changes with `!` (`feat(api)!: ...`).
  Release notes and version numbers are generated from these titles.
- Include tests for behaviour changes; reconcilers must stay idempotent
  (a second reconcile must not write to Authentik).
- Never commit secrets, tokens or `.env` files.
- ASCII only in code, strings and scripts.

## Reporting security issues

Please do not open public issues for vulnerabilities; see
[SECURITY.md](SECURITY.md).
