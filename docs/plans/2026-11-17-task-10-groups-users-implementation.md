# TASK-10 AuthentikGroup, AuthentikUser and Required Application Access Implementation Plan

**Goal:** Replace the `AuthentikGroup` and `AuthentikUser` scaffold stubs with working kinds (groups with an authoritative member list, people who enrol through a single-use invitation) and make every `AuthentikApplication` declare who may open it (`spec.access`), so release v0.1.0 can ship.

**Architecture:** A small pure library `internal/adopt` (`Decide`, `Finalize`) translates scope.md's management-policy and deletion tables; per-kind controllers in `internal/controller` call it and keep everything kind-specific (members, invitations, bindings). New Authentik client calls live in `internal/authentik` (with a 2026.2.3 compat rule for policy bindings) and are faked in `fakeauthentik` for envtest. A new `--default-connection-identity-namespace-selector` gates groups and users on the default connection; `AuthentikApplication.spec.access` owns the application's group bindings.

**Tech Stack:** Go 1.27 toolchain (go.mod `go 1.26.0`, `toolchain go1.27.4`), Kubebuilder v4.16.0 / controller-runtime v0.25.2 / controller-tools v0.22.0, Ginkgo v2 + Gomega with envtest (Kubernetes 1.37), `goauthentik.io/api/v3 v3.2026080.5`, Helm v4 + helm-unittest v1.2.1, go-task, kind; Authentik 2026.2.3 and 2026.8.5.

**Spec:** docs/designs/2026-11-17-task-10-groups-users-design.md

> **For Claude:** REQUIRED EXECUTION WORKFLOW (follow in order):
> 1. `superpowers:using-git-worktrees` — the worktree already exists; verify it, never create a second
> 2. `superpowers:subagent-driven-development` — one builder per task (batched where SDD says so), a fresh critic per task
> 3. `superpowers:test-driven-development` — every builder
> 4. `superpowers:verification-before-completion` — every task
> 5. Per-task review is SDD's own `task-reviewer-prompt.md` (built in)
> 6. After all tasks: whole-branch review by `sr-eng-review` (automatic; see the feature-workflow skill, Phase 4)
> 7. `superpowers:finishing-a-development-branch` — executes the run contract
>
> Skills carry their own model and effort settings. Do not override them; the feature-workflow skill's routing table is the only place a model is chosen.

**Run contract:**
- Integration: (b) push and open a DRAFT pull request (GitHub, `gh`) against `main`.
- PR title: `feat(api)!: AuthentikGroup, AuthentikUser and required application access (TASK-10)`; body per `/pr` from the design's intent plus the run summary (security counts only); no reviewers; no labels; never a claude.ai session link.
- Base branch: `main` (PR target). The feature branch forks from `design/task-10-groups-users`, which holds the approved design commits.
- Remote and forge: `git@github.com:jacaudi/authentik-operator.git`, `gh`.
- Repository owned by the user: yes (`jacaudi/`).
- Direct push to the base branch: never. Never force-push.
- Branch: `feat/task-10-groups-users`; worktree `~/Projects/github/authentik-operator/.worktrees/feat-task-10-groups-users`.
- Sanctioned environment: the devcontainer (`.devcontainer/`), after Task 1 adds pinned go-task, helm and the helm-unittest plugin to `.devcontainer/post-install.sh` (owner decision: "Fix devcontainer first"). `devcontainer up --workspace-folder "$PWD" --mount-git-worktree-common-dir` once; every test run is `devcontainer exec --workspace-folder "$PWD" --mount-git-worktree-common-dir <command>`. Docker is colima: `DOCKER_HOST=unix:///Users/acaudill/.colima/default/docker.sock` on the host.
- Sanctioned test command: `task test` per task; before the PR `task ci` (after `rm -rf bin/trivy`) and `task e2e`, both green.
- New dependencies: only with a present requirement, pinned, named in the task report.
- Boundaries: 8 hours wall clock; dispatch cap 6 x tasks + 12; pause after 2 hours without a task completing.
- Worktree after merge: not applicable (option b); keep it.
- Commit signing: SSH via 1Password; if signing fails, PLAN: PAUSED — signing; never --no-gpg-sign.
- Commits: Conventional Commits subjects, ASCII-only bodies, each ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Global Constraints

Every task's requirements include this section. `$WT` is `/Users/acaudill/Projects/github/authentik-operator/.worktrees/feat-task-10-groups-users`; every command runs from `$WT`.

**Command shorthands (used in every `Run:` line):**
- `dx <cmd>` means `DOCKER_HOST=unix:///Users/acaudill/.colima/default/docker.sock devcontainer exec --workspace-folder "$PWD" --mount-git-worktree-common-dir <cmd>`. A "no running container" error means run `DOCKER_HOST=unix:///Users/acaudill/.colima/default/docker.sock devcontainer up --workspace-folder "$PWD" --mount-git-worktree-common-dir` once and retry; any other devcontainer failure is a stop.
- `envtest "<FOCUS>"` means `dx sh -c 'KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.37 --bin-dir bin -p path)" go test ./internal/controller/ -count=1 -ginkgo.focus="<FOCUS>"'`. Run `dx make manifests generate` first whenever `api/` changed (envtest loads `config/crd/bases`).
- Every task ends green on `dx task test` AND `dx make lint` (AGENTS.md: lint after editing Go). `baseline-failures:` from Task 1 are the only failures allowed.
- Never run `make`, `go`, `task`, `helm` or `kind` on the host: `bin/` is shared with the container and must hold linux binaries. Git (including signed commits) runs on the host.
- Long commands: `dx task test`, `dx task ci`, `dx task e2e` (about 15-50 minutes), the Task 4 recording stack, and any `envtest` run of the whole controller package are started with the Bash tool's `run_in_background: true`; wait for the completion notification, then read the output. Never run them in the foreground with a timeout above 10 minutes. Their `Expected:` lines apply to the finished output.
- envtest and unit suites talk to `fakeauthentik` through `&authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1}` (no rate limit, no retries), so an injected 5xx reaches the code under test exactly once.
- Commit template (every task's last step; the task gives subject and body): `git add <paths> && git commit -m "<subject>" -m "<body>" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"`, on the host. Check: `git cat-file commit HEAD | grep -c '^gpgsig'` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

**Kubebuilder and generated files (AGENTS.md):**
- Never hand-edit `config/crd/bases/*.yaml`, `config/rbac/role.yaml`, `**/zz_generated.*.go`, `PROJECT`. After editing `api/**/*_types.go` or any `+kubebuilder` marker: `dx make manifests generate`, then `dx make chart-regen`, and commit the regenerated files (`config/crd/bases`, `api/v1alpha1/zz_generated.deepcopy.go`, `config/rbac/role.yaml`, `dist/chart/templates/crd`, `dist/chart/templates/rbac`).
- RBAC (documented divergence from design 5.7, accepted): besides Secrets, the new watches add read-only RBAC markers on the operator's own kinds: the group reconciler `authentikapplications` and `authentikusers` get;list;watch (Tasks 8, 11), the application reconciler `authentikgroups` get;list;watch (Task 8), the group and user reconcilers `authentikdeployments` get, `namespaces` get and `events.k8s.io` events create;patch. These verbs already exist in `config/rbac/role.yaml` for other reconcilers, so the generated role is expected not to grow; if it does, the diff is committed and named in the report.
- Never remove `// +kubebuilder:scaffold:*` markers. Do not move files. The kinds already exist in `PROJECT`; do not run `kubebuilder create`.
- Chart: plugin-owned templates change only through `make chart-regen`; hand-owned files (`values.yaml`, `templates/manager/manager.yaml`, `tests/`, `README.md`, `ci/`) are edited directly, helm-unittest test first. Never edit `version:`/`appVersion:` in `Chart.yaml`.
- `go mod tidy` after adding an import: `k8s.io/apiextensions-apiserver` moves from indirect to direct at the existing `v0.37.0` (Task 6). No other new module dependency is expected; any other one is a stop-and-report.

**Adoption contract (#18, `docs/design/scope.md` "API-object adoption"), unchanged except where stated:**
- `managementPolicy`, `dryRun`, `deletionPolicy` via the inline `APIManagementSpec`; `status.provenance`; claims and tie-break through `internal/controller/claims.go` unchanged (`holders`, `outrankedBy`, `sameConnection`); adoption gate `adoptionGate` unchanged for Adopt/AdoptOrCreate/ReadOnly on the default connection.
- No CRD `+kubebuilder:default` on adoption-sensitive fields (group `isSuperuser`, `attributes`, `members`; every user field; application `access`, `policyEngineMode`). Create-time defaults are applied in the controller; an adopted object keeps live values for unset fields.
- Stated departures (spec): group `members` absent = never read or written, any provenance; user `name`, `email`, `path`, `type`, `isActive` unset keep live even when `Created`; `AuthentikUser` unset `deletionPolicy` = `Retain` for every provenance; a target found while the own create is pending but unproven is `Conflict` under every policy.
- The provider and application controllers are NOT migrated onto `internal/adopt`; `authentikapplication_controller.go` changes only for `spec.access` (Tasks 8).

**Authentik writes:**
- PATCH only, never PUT. Never send `users` in a group create or PATCH (membership only through `add_user` / `remove_user`). Never send `groups` or `roles` in a user PATCH. Never send `parents` or `roles` for a group.
- Authentik replaces the whole `attributes` dict on every write: the operator reads, sets owned keys, deletes keys it owned and no longer lists, keeps every other key, and PATCHes only when something differs.
- Both Authentik versions: 2026.2.3 and 2026.8.5 (e2e matrix, `.github/workflows/test-e2e.yml`). Go client `goauthentik.io/api/v3 v3.2026080.5`.
- Ownership proof (single source: `internal/authentik/owner.go`): attributes `{"authentik_operator": {"owner_uid": "<metadata.uid>"}}` (a nested dict; Authentik splits attribute paths on dots).
- Reserved attribute keys, refused by CEL: keys starting `goauthentik.io/`, `authentik_operator`, `settings`; on users also `given_name`, `family_name`.
- Policy bindings are listed with `target=<application pk>`; an empty target is an error (`authentik.ErrNoTarget`), never a query; every delete re-checks `binding.Target == application pk`.

**Secrets of people:**
- The invitation pk is the sign-up token. It is never written to status, a condition, an event, a log line or a returned error; only to the invitation Secret (`url` key). Invitation client errors redact it (`<redacted>`).

**Identity gate:** On the default connection, `AuthentikGroup` and `AuthentikUser` reconcile only in namespaces matching `--default-connection-identity-namespace-selector` (empty denies; reason `IdentityNotAllowed`), under every policy, checked BEFORE the adoption gate. `spec.authentikRef` is not gated. Outside the selector, deletion removes only `Created` objects.

**Conditions and timing:** `Ready` keeps its meaning. Member and access problems never flip `Ready` (only `AccessPending` and `WaitingForGroups` on applications do). Recheck `syncedRecheck` (5 minutes) when synced, `userErrorRecheck` (1 minute) on user-fixable errors and while an invitation is pending. Reconcilers stay idempotent: a second reconcile of an in-sync object sends no write to Authentik.

**Code conventions (AGENTS.md, CONTRIBUTING.md, go-standards):** ASCII only in code, strings, scripts and docs. Errors wrapped with `%w`, never logged and returned. Log messages: capital first letter, no trailing period, past tense, object type named, balanced key-value pairs. String enums are named `string` types (go-standards 4.3). `revive` argument-limit 5, receiver names at most 2 letters, gocyclo 15. Use `encoding/json` (as the rest of the module). Conventional Commits subjects; ASCII bodies; `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Kubernetes YAML: one object per file.

**Failure and rollback (every task):** A task touches only the files it lists. If its verification fails after the SDD fix rounds, the breaker rules; nothing is force-pushed or rewritten. Roll back a finished task with `git revert <sha>` (a new signed commit). Generated files are always regenerated by their `make` target, never hand-fixed. A step marked STOP ends the task with a BLOCKED report quoting the failing command and its last 30 lines of output.

## Review Focus

Uncovered input classes most likely to bite a person; each has its test added to the owning task.

1. An explicit `spec.attributes: {}` (as opposed to absent) must survive the typed Go client and the CRD, remove every owned key, and reach Authentik as `"attributes": {}` rather than an omitted field. Test: Task 6, `It("removes every owned key with attributes: {} and sends an explicit empty object")`.
2. Authentik answering 503 to the deletion guard's `used_by` must leave the group in Authentik, keep the finalizer and report the error, never fall through to a delete. Test: Task 6, `It("keeps the resource terminating and deletes nothing when used_by fails")`.
3. Every manifest in `config/samples` must still pass admission after `spec.access` becomes required and the stub samples are replaced (people copy samples). Test: Task 8, `samples_test.go` `It("accepts every manifest in config/samples")`. It is part of every later `dx task test`; Task 10, which replaces the user sample, also runs it explicitly (`envtest "AuthentikUser|Samples"`).
4. An `externalURL` with a path prefix and a trailing slash must give `https://sso.example.com/auth/if/flow/enrol/?itoken=<pk>` (no `//`). Test: Task 10, `TestInvitationLink`.
5. `spec.invitation.expiresAfter` of `0s` or a negative duration must be refused at admission, not produce an instantly expired invitation. Test: Task 10, `It("rejects a zero or negative expiresAfter at admission")`.

## File Structure

| Path | Responsibility | Task |
|---|---|---|
| `.devcontainer/post-install.sh` | pinned go-task, helm, helm-unittest, and the `task ci` tools | 1 |
| `internal/adopt/{adopt,finalize}.go` | pure `Decide` and `Finalize` | 2 |
| `internal/authentik/{group,user,binding,invitation,owner}.go` | new Authentik calls | 3 |
| `internal/authentik/fakeauthentik/{identity,bindings,invitations}.go` | fake groups, users, bindings, invitations | 3 |
| `internal/authentik/compat.go`, `testdata/2026.2.3/*`, `record_test.go` | 2026.2.3 decode compatibility | 4 |
| `internal/controller/identity_gate.go` | identity gate | 5 |
| `internal/controller/identity_status.go`, `attributes.go` | shared group/user failure classification and per-key attributes | 6 |
| `internal/controller/authentikgroup_controller.go`, `group_desired.go`, `group_members.go` | group reconciler | 6, 7, 11 |
| `internal/controller/application_access.go` | `spec.access` bindings | 8 |
| `internal/controller/authentikuser_controller.go`, `user_desired.go`, `user_invitation.go` | user reconciler | 10, 11 |
| `internal/migrate/jeffresc/*` | converter: groupBindings, AuthentikGroup | 9, 12 |
| `test/e2e/identity_test.go`, `test/aktest/enrol.go` | real-Authentik e2e | 13 |

---

### Task 1: Devcontainer with pinned go-task, helm, helm-unittest and the task ci tools
Kind: rewrite

**Files:**
- Modify: `.devcontainer/post-install.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: a devcontainer whose PATH has `task` v3.54.0, `helm` v4.3.1 with the `unittest` plugin v1.2.1, `yq` v4.54.1, `golangci-lint` 2.15.0, `govulncheck` v1.9.0, `hadolint` v2.15.1, `yamllint` (Debian package), and git `safe.directory '*'`; the `dx` / `envtest` shorthands of Global Constraints work; the `baseline-failures:` list (test names, or `none`).

TDD exemption (test-driven-development's configuration exception): this task changes a provisioning script with no unit-testable behaviour; its test is the container build (Step 2), the tool versions (Step 3), the docker-in-docker probe (Step 4) and a green `task test` inside it (Step 5).

- [ ] **Step 1: Add the pinned tools block to `.devcontainer/post-install.sh`**

Insert a section `Installing pinned tools...` after the kubectl completion block and before "Configuring Docker environment". Each pin is a variable with a Renovate comment in the style of `.taskfiles/*.yml` (`# renovate: datasource=github-releases depName=<owner/repo>`), installed unconditionally (the script runs once, on create). Exact values:

| Tool | Variable = value | Install (`${ARCH}` is `amd64` or `arm64` from the existing detection) |
|---|---|---|
| go-task | `TASK_VERSION=v3.54.0` | `curl -sSfL "https://github.com/go-task/task/releases/download/${TASK_VERSION}/task_linux_${ARCH}.tar.gz" \| tar xz -C /usr/local/bin task` |
| helm | `HELM_VERSION=v4.3.1` | `curl -sSfL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz" \| tar xz --strip-components=1 -C /usr/local/bin "linux-${ARCH}/helm"` |
| helm-unittest | `HELM_UNITTEST_VERSION=v1.2.1` (must equal `.taskfiles/chart.yml`) | `helm plugin install https://github.com/helm-unittest/helm-unittest --version "${HELM_UNITTEST_VERSION}" --verify=false` |
| yq | `YQ_VERSION=v4.54.1` | `curl -sSfLo /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${ARCH}" && chmod +x /usr/local/bin/yq` |
| golangci-lint | `GOLANGCI_LINT_VERSION=2.15.0` (equals Makefile `GOLANGCI_LINT_VERSION`) | `curl -sSfL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${ARCH}.tar.gz" \| tar xz --strip-components=1 -C /usr/local/bin "golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${ARCH}/golangci-lint"` |
| govulncheck | `GOVULNCHECK_VERSION=v1.9.0` | `GOBIN=/usr/local/bin go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"` |
| hadolint | `HADOLINT_VERSION=v2.15.1` | asset `hadolint-linux-x86_64` when `ARCH=amd64`, `hadolint-linux-arm64` when `ARCH=arm64`: `curl -sSfLo /usr/local/bin/hadolint "https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/<asset>" && chmod +x /usr/local/bin/hadolint` |
| yamllint | Debian package | `apt-get update && apt-get install -y --no-install-recommends yamllint` |
| git | n/a | `git config --global --add safe.directory '*'` (the worktree is owned by the host uid) |

Append to the "Verifying installations" block: `task --version`, `helm version --short`, `helm plugin list`, `yq --version`, `golangci-lint --version`, `govulncheck -version`, `hadolint --version`, `yamllint --version`.

- [ ] **Step 2: Create the container**

Run: `DOCKER_HOST=unix:///Users/acaudill/.colima/default/docker.sock devcontainer up --workspace-folder "$PWD" --mount-git-worktree-common-dir`
Expected: JSON with `"outcome":"success"`; the log contains `DevContainer ready!`. If the output says an existing container was reused (post-install did not re-run), run the same command with `--remove-existing-container` added.

- [ ] **Step 3: Verify the tools**

Run: `dx sh -c 'task --version && helm version --short && helm plugin list && yq --version && golangci-lint --version && govulncheck -version && hadolint --version && yamllint --version && git status --short'`
Expected: `3.54.0`, `v4.3.1`, a `unittest` row with `1.2.1`, `v4.54.1`, `2.15.0`, `v1.9.0`, `2.15.1`, a yamllint version, and git status listing only `.devcontainer/post-install.sh`.

- [ ] **Step 4: Prove docker-in-docker can run kind (needed by `task e2e` and Task 4)**

Run: `dx sh -c 'docker info --format "{{.ServerVersion}} {{.MemTotal}}" && kind create cluster --name ao-dind-probe --wait 120s && kind delete cluster --name ao-dind-probe'`
Expected: a server version and memory total, then `Deleted nodes`. Record MemTotal in the report. If this fails: STOP (report BLOCKED with the failing command); do not commit.

- [ ] **Step 5: Baseline**

Run: `dx task test`
Expected: exit 0. If anything fails, record each failing test name as `baseline-failures:` in the report (or `none`) and continue; do not fix them.

- [ ] **Step 6: Commit**

```bash
git add .devcontainer/post-install.sh
git commit -m "build(devcontainer): pin go-task, helm, helm-unittest and the task ci tools" \
  -m "The sanctioned environment runs task test and task ci; the image lacked task, helm, the unittest plugin, yq, golangci-lint, govulncheck, hadolint and yamllint." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 2: internal/adopt -- the management-policy and deletion decisions

**Files:**
- Create: `internal/adopt/adopt.go`, `internal/adopt/finalize.go`, `internal/adopt/adopt_test.go`, `internal/adopt/finalize_test.go`

**Interfaces:**
- Consumes: `api/v1alpha1` (`ManagementPolicy`, `Provenance`, `DeletionPolicy`, `EffectiveDeletionPolicy`). Must not import `internal/controller`.
- Produces (package `adopt`, string enums):
  - `type Tracked string`: `TrackedNone Tracked = ""`, `TrackedFound Tracked = "Found"`, `TrackedGone Tracked = "Gone"`.
  - `type Target string`: `TargetUnknown Target = ""`, `TargetFound = "Found"`, `TargetMissing = "Missing"`, `TargetAmbiguous = "Ambiguous"`.
  - `type Facts struct { Policy v1alpha1.ManagementPolicy; Provenance v1alpha1.Provenance; Tracked Tracked; Target Target; OwnCreatePending bool; OwnCreateProven bool; DryRun bool; RecreateVanished bool }` (exactly these eight).
  - `type Action string`: `ActionUseTracked = "UseTracked"`, `ActionRecreate = "Recreate"`, `ActionTrackedGone = "TrackedGone"`, `ActionRecoverOwnCreate = "RecoverOwnCreate"`, `ActionCreate = "Create"`, `ActionAdopt = "Adopt"`, `ActionObserve = "Observe"`, `ActionDryRunAdopt = "DryRunAdopt"`, `ActionDryRunCreate = "DryRunCreate"`, `ActionConflict = "Conflict"`, `ActionTargetNotFound = "TargetNotFound"`, `ActionAmbiguous = "Ambiguous"`.
  - `func Decide(in Facts) Action`
  - `type FinalizeFacts struct { Policy v1alpha1.ManagementPolicy; DeletionPolicy v1alpha1.DeletionPolicy; Provenance v1alpha1.Provenance; UnsetDefault v1alpha1.DeletionPolicy; IdentityAllowed bool }` (`UnsetDefault` empty = scope.md's by-provenance rule).
  - `type Finalization string`: `FinalizeDelete = "Delete"`, `FinalizeDeleteUndecided = "DeleteUndecided"` (explicit Delete on undecided provenance: Warning first), `FinalizeRetain = "Retain"`, `FinalizeRetainUndecided = "RetainUndecided"` (Warning "provenance unknown; retaining"), `FinalizeRetainNotAllowed = "RetainNotAllowed"` (outside the identity selector, not Created: Warning).
  - `func Finalize(in FinalizeFacts) Finalization`

- [ ] **Step 1: Write the failing table tests**

`internal/adopt/adopt_test.go` (stdlib `testing`, table-driven, `t.Run(tc.name, ...)`, failure message `Decide(%+v) = %s, want %s`). Shorthands: `create, adoptP, aoc, ro` for the four policies, `created, adopted` for the provenances. Names quote the scope.md line they pin:

```go
func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   Facts
		want Action
	}{
		// scope.md "Management policy" table
		{"Create (default) | no, never adopts | Conflict", Facts{Policy: create, Target: TargetFound}, ActionConflict},
		{"Create (default) | miss | creates", Facts{Policy: create, Target: TargetMissing}, ActionCreate},
		{"unset policy is Create", Facts{Target: TargetMissing}, ActionCreate},
		{"Adopt | existing object | takes it over", Facts{Policy: adoptP, Target: TargetFound}, ActionAdopt},
		{"Adopt | miss | AdoptionTargetNotFound, nothing written", Facts{Policy: adoptP, Target: TargetMissing}, ActionTargetNotFound},
		{"AdoptOrCreate | existing object | takes it over", Facts{Policy: aoc, Target: TargetFound}, ActionAdopt},
		{"AdoptOrCreate | miss | creates", Facts{Policy: aoc, Target: TargetMissing}, ActionCreate},
		{"ReadOnly | existing object | observes it", Facts{Policy: ro, Target: TargetFound}, ActionObserve},
		{"ReadOnly | miss | AdoptionTargetNotFound", Facts{Policy: ro, Target: TargetMissing}, ActionTargetNotFound},
		// scope.md "Management policy" bullets and "Behavior"
		{"A resource that created its object recreates it if it vanishes, whatever the policy (Create)", Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionRecreate},
		{"A resource that created its object recreates it if it vanishes, whatever the policy (Adopt)", Facts{Policy: adoptP, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionRecreate},
		{"A resource that created its object recreates it if it vanishes, whatever the policy (AdoptOrCreate)", Facts{Policy: aoc, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionRecreate},
		{"ReadOnly never recreates anything", Facts{Policy: ro, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionTrackedGone},
		{"An object it adopted that vanishes is never recreated silently (AdoptOrCreate)", Facts{Policy: aoc, Provenance: adopted, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionTrackedGone},
		{"An object it adopted that vanishes is never recreated silently (Create)", Facts{Policy: create, Provenance: adopted, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true}, ActionTrackedGone},
		{"a recreate is refused when another object took the name", Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetFound, RecreateVanished: true}, ActionConflict},
		{"a recreate whose response was lost is recovered", Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true, RecreateVanished: true}, ActionRecoverOwnCreate},
		{"A miss never creates unless the policy is AdoptOrCreate (Adopt, dry run)", Facts{Policy: adoptP, DryRun: true, Target: TargetMissing}, ActionTargetNotFound},
		{"Lost creates are recovered through status.pendingCreate and classified Created (Create)", Facts{Policy: create, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true}, ActionRecoverOwnCreate},
		{"Lost creates are recovered through status.pendingCreate and classified Created (AdoptOrCreate)", Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true}, ActionRecoverOwnCreate},
		{"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (Create)", Facts{Policy: create, Target: TargetFound, OwnCreatePending: true}, ActionConflict},
		{"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (Adopt)", Facts{Policy: adoptP, Target: TargetFound, OwnCreatePending: true}, ActionConflict},
		{"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (AdoptOrCreate)", Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true}, ActionConflict},
		{"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (ReadOnly)", Facts{Policy: ro, Target: TargetFound, OwnCreatePending: true}, ActionConflict},
		{"With dryRun: true the operator finds the object and reports (Adopt)", Facts{Policy: adoptP, DryRun: true, Target: TargetFound}, ActionDryRunAdopt},
		{"With dryRun: true the operator finds the object and reports (AdoptOrCreate)", Facts{Policy: aoc, DryRun: true, Target: TargetFound}, ActionDryRunAdopt},
		{"With nothing to adopt it reports whether a new object would be created", Facts{Policy: aoc, DryRun: true, Target: TargetMissing}, ActionDryRunCreate},
		{"dryRun gates only the initial takeover: once adopted, spec edits are applied live", Facts{Policy: adoptP, DryRun: true, Provenance: adopted, Tracked: TrackedFound}, ActionUseTracked},
		{"ReadOnly keeps observing the object it found", Facts{Policy: ro, Provenance: adopted, Tracked: TrackedFound}, ActionUseTracked},
		{"Several providers with one name is a Conflict", Facts{Policy: adoptP, Target: TargetAmbiguous}, ActionAmbiguous},
		{"an unset target never creates", Facts{Policy: create}, ActionConflict},
		{"a vanished object of undecided provenance is decided as untracked", Facts{Policy: create, Tracked: TrackedGone, Target: TargetMissing}, ActionCreate},
		// spec 5.3 User rows (RecreateVanished false)
		{"User: A Created user that vanishes from Authentik is not re-invited", Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetMissing}, ActionTrackedGone},
		{"User: no user named username exists: the invitation is issued", Facts{Policy: create, Target: TargetMissing}, ActionCreate},
		{"User: invitation in progress and no account yet: keep inviting", Facts{Policy: create, Target: TargetMissing, OwnCreatePending: true}, ActionCreate},
		{"User: the account carrying the proof is recognised as Created", Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true}, ActionRecoverOwnCreate},
		{"User: An account named username without the proof appears while inviting: Conflict", Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true}, ActionConflict},
		{"User: one exists and no invitation is in progress: Conflict under Create", Facts{Policy: create, Target: TargetFound}, ActionConflict},
		{"User: one exists and no invitation is in progress: adopted under AdoptOrCreate", Facts{Policy: aoc, Target: TargetFound}, ActionAdopt},
	}
	// loop: t.Run(tc.name, ...); if got := Decide(tc.in); got != tc.want { t.Errorf(...) }
}
```

`internal/adopt/finalize_test.go`:

```go
func TestFinalize(t *testing.T) {
	cases := []struct {
		name string
		in   FinalizeFacts
		want Finalization
	}{
		{"ReadOnly never deletes", FinalizeFacts{Policy: ro, Provenance: adopted, IdentityAllowed: true}, FinalizeRetain},
		{"Unset, the effective policy is Delete for a Created object", FinalizeFacts{Policy: create, Provenance: created, IdentityAllowed: true}, FinalizeDelete},
		{"and Retain for an Adopted one", FinalizeFacts{Policy: adoptP, Provenance: adopted, IdentityAllowed: true}, FinalizeRetain},
		{"an undecided provenance never deletes by default (Retain, with a Warning event)", FinalizeFacts{Policy: create, IdentityAllowed: true}, FinalizeRetainUndecided},
		{"An explicit Delete wins over provenance", FinalizeFacts{Policy: adoptP, DeletionPolicy: v1alpha1.DeletionPolicyDelete, Provenance: adopted, IdentityAllowed: true}, FinalizeDelete},
		{"when it does so for undecided provenance a Warning event says so first", FinalizeFacts{Policy: adoptP, DeletionPolicy: v1alpha1.DeletionPolicyDelete, IdentityAllowed: true}, FinalizeDeleteUndecided},
		{"Retain removes the finalizer and leaves the Authentik object", FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyRetain, Provenance: created, IdentityAllowed: true}, FinalizeRetain},
		{"User: unset deletionPolicy resolves to Retain whatever its provenance", FinalizeFacts{Policy: create, Provenance: created, UnsetDefault: v1alpha1.DeletionPolicyRetain, IdentityAllowed: true}, FinalizeRetain},
		{"User: an explicit Delete still deletes", FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyDelete, Provenance: created, UnsetDefault: v1alpha1.DeletionPolicyRetain, IdentityAllowed: true}, FinalizeDelete},
		{"Outside the selector, deletion removes only Created objects", FinalizeFacts{Policy: create, Provenance: created}, FinalizeDelete},
		{"Outside the selector, adopted objects are retained with a Warning", FinalizeFacts{Policy: adoptP, DeletionPolicy: v1alpha1.DeletionPolicyDelete, Provenance: adopted}, FinalizeRetainNotAllowed},
		{"Outside the selector, undecided objects are retained with a Warning", FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyDelete}, FinalizeRetainNotAllowed},
		{"Outside the selector, a Created object with Retain is still retained", FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyRetain, Provenance: created}, FinalizeRetain},
	}
	// same loop shape as TestDecide
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `dx go test ./internal/adopt/ -count=1`
Expected: FAIL, build error `undefined: Decide` / `undefined: Finalize`.

- [ ] **Step 3: Implement `Decide` in `internal/adopt/adopt.go` and `Finalize` in `internal/adopt/finalize.go`**

Package doc: "Package adopt translates the API-object adoption contract (docs/design/scope.md, "API-object adoption") into pure decisions for the group and user reconcilers." `Decide` resolves `in.Policy.Resolve()` first. Precedence (the table cannot show it; implement in this order):
1. `Tracked == TrackedFound` -> `UseTracked`.
2. `Tracked == TrackedGone` and `Provenance == Adopted` -> `TrackedGone`.
3. `Tracked == TrackedGone`, `Provenance == Created`, and (policy ReadOnly or `!RecreateVanished`) -> `TrackedGone`.
4. `Tracked == TrackedGone`, `Provenance == Created` (recreating): `TargetFound` -> `RecoverOwnCreate` if pending and proven, else `Conflict`; `TargetMissing` -> `Recreate`; `TargetAmbiguous` -> `Ambiguous`; `TargetUnknown` -> `Conflict`.
5. Otherwise (nothing tracked, or gone with empty provenance) by target: `TargetUnknown` -> `Conflict`; `TargetAmbiguous` -> `Ambiguous`; `TargetFound` -> pending and proven: `RecoverOwnCreate`; pending: `Conflict`; Create: `Conflict`; ReadOnly: `Observe`; DryRun: `DryRunAdopt`; else `Adopt`; `TargetMissing` -> Create: `Create`; Adopt or ReadOnly: `TargetNotFound`; AdoptOrCreate: `DryRunCreate` if DryRun else `Create`.

`Finalize` order: ReadOnly -> `Retain`; `!IdentityAllowed` and provenance not Created -> `RetainNotAllowed`; effective = spec if set, else `UnsetDefault` if set, else `EffectiveDeletionPolicy`'s provenance rule; effective Retain -> `RetainUndecided` when both spec and provenance are empty, else `Retain`; effective Delete -> `DeleteUndecided` when provenance is empty, else `Delete`.

- [ ] **Step 4: Run the tests to see them pass**

Run: `dx go test ./internal/adopt/ -count=1 -v`
Expected: PASS, every row listed.

- [ ] **Step 5: Full suite and lint**

Run: `dx task test && dx make lint`
Expected: exit 0 both.

- [ ] **Step 6: Commit**

```bash
git add internal/adopt
git commit -m "feat(adopt): pure management-policy and deletion decisions for groups and users" \
  -m "Decide and Finalize translate the scope.md adoption tables with eight facts; a target found during an unproven pending create is a Conflict under every policy." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 3: Authentik client calls for groups, users, bindings and invitations, with fakeauthentik support
Kind: rewrite

**Files:**
- Create: `internal/authentik/group.go`, `internal/authentik/user.go`, `internal/authentik/binding.go`, `internal/authentik/invitation.go`, `internal/authentik/owner.go`, `internal/authentik/identity_test.go`, `internal/authentik/fakeauthentik/identity.go`, `internal/authentik/fakeauthentik/bindings.go`, `internal/authentik/fakeauthentik/invitations.go`
- Modify: `internal/authentik/fakeauthentik/server.go`

**Interfaces:**
- Consumes: `api` (goauthentik.io/api/v3), existing `Check`, `ListAll`, `PageSize`, `exactlyOne`, `ErrNotFound`, `ErrAmbiguous`, `APIError`.
- Produces (package `authentik`):
  - `const OwnerAttribute = "authentik_operator"`, `const OwnerUIDKey = "owner_uid"`; `func OwnerProof(uid string) map[string]any` (returns `{"authentik_operator": {"owner_uid": uid}}`); `func HasOwnerProof(attrs map[string]any, uid string) bool`.
  - `func (c *Client) GetGroup(ctx context.Context, pk string) (*api.Group, error)` (membership included); `FindGroupByName(ctx, name string) (*api.Group, error)` (list `name=`, `include_users=false`, exact client-side match, `ErrNotFound`/`ErrAmbiguous`); `CreateGroup(ctx, req api.GroupRequest) (*api.Group, error)`; `PatchGroup(ctx, pk string, req api.PatchedGroupRequest) (*api.Group, error)`; `DeleteGroup(ctx, pk string) error` (404 is success); `AddUserToGroup(ctx, groupPK string, userPK int32) error`; `RemoveUserFromGroup(ctx, groupPK string, userPK int32) error`; `GroupUsedBy(ctx, pk string) ([]api.UsedBy, error)`.
  - `GetUser(ctx, pk int32) (*api.User, error)`; `FindUserByUsername(ctx, username string) (*api.User, error)`; `FindUsersByOwnerUID(ctx, uid string) ([]api.User, error)` (query `attributes={"authentik_operator__owner_uid":"<uid>"}`, then keeps only users with `HasOwnerProof(attrs, uid)`); `PatchUser(ctx, pk int32, req api.PatchedUserRequest) (*api.User, error)`; `DeleteUser(ctx, pk int32) error` (404 success); `Me(ctx) (*api.UserSelf, error)`; `UserAuthenticators(ctx, userPK int32) ([]api.Device, error)`.
  - `var ErrNoTarget = errors.New("authentik: policy binding target is empty")`; `ListBindings(ctx, target string) ([]api.PolicyBinding, error)` (empty target -> `ErrNoTarget` without a request; drops results whose `Target != target`); `CreateBinding(ctx, req api.PolicyBindingRequest) (*api.PolicyBinding, error)`; `PatchBinding(ctx, pk string, req api.PatchedPolicyBindingRequest) (*api.PolicyBinding, error)`; `DeleteBinding(ctx, b *api.PolicyBinding, target string) error` (error without a request unless `target != ""` and `b.Target == target`; 404 success).
  - `FindInvitationByName(ctx, name string) (*api.Invitation, error)`; `CreateInvitation(ctx, req api.InvitationRequest) (*api.Invitation, error)`; `DeleteInvitation(ctx, pk string) error` (404 success; every error has the pk replaced by `<redacted>` in `Error()`, in `APIError.Path` and `APIError.Body`, keeping `StatusCode`).
- Produces (package `fakeauthentik`): `Request` gains `Query string` (raw query). Constants `FlowEnrollmentPK = "00000000-0000-0000-0000-00000000f004"`, `FlowEnrollmentSlug = "default-enrollment-flow"` (seeded flow), `TokenUserPK int32 = 1`, `TokenUsername = "akadmin"`. Methods: `FailNextOn(method, pathContains string, status int)`; groups `SeedGroup(name string, fields map[string]any) string`, `Group(pk string) map[string]any`, `GroupByName(name string) map[string]any`, `SetGroupField(pk, field string, v any)`, `DeleteGroup(pk string)`, `GroupCount() int`, `GroupMemberPKs(pk string) []int32`, `AddGroupMember(groupPK string, userPK int32)`, `LoseNextGroupCreate()`; users `SeedUser(username string, fields map[string]any) int32`, `User(pk int32) map[string]any`, `UserByUsername(username string) map[string]any`, `SetUserField(pk int32, field string, v any)`, `DeleteUser(pk int32)`, `SetTokenUserSuperuser(on bool)`, `IgnoreUserAttributeFilter(on bool)`, `AddAuthenticator(userPK int32)`; bindings `SeedBinding(target string, fields map[string]any) string`, `Bindings(target string) []map[string]any` (sorted by order, then pk), `DeleteBinding(pk string)`; invitations `Invitations() []map[string]any` (sorted by name), `InvitationByName(name string) map[string]any`, `IssuedInvitationPKs() []string` (every pk ever created, deleted ones included), `SeedInvitation(name string, fields map[string]any) string`, `ConsumeInvitation(name string)`, `EnrolFromInvitation(name, username string) int32`, `LoseNextInvitationCreate()`.

Fake behaviour to implement (decisions, not code):
- The token user (pk 1, `akadmin`, `is_superuser` true unless `SetTokenUserSuperuser(false)`) is a stored user; `/core/users/me/` is built from it.
- Group JSON carries every property the client requires: `pk` (UUID string, counter `00000000-0000-0000-0001-%012d`), `num_pk`, `name`, `is_superuser` (default false), `parents` `[]`, `parents_obj` `[]`, `users` (member pks), `users_obj` (partial users `pk, username, name, uid`), `attributes` (default `{}`), `roles` `[]`, `roles_obj` `[]`, `inherited_roles_obj` `[]`, `children` `[]`, `children_obj` `[]`. `include_users=false` gives `users_obj: null`. Names are unique (400 `{"name":["group with this name already exists."]}`); `name=` filters exactly. A PATCH copies the body over the stored group (attributes replaced whole, like Authentik; a `users` key replaces membership). `add_user`/`remove_user` take `{"pk": <user pk>}`, answer 204, 404 for an unknown group or user. `used_by` lists, for each binding of the group, `{"app":"authentik_policies","model_name":"policybinding","pk":<binding pk>,"name":"Binding from <application name> #<order> to Group <group name>","action":"cascade"}`, and for each member `{"app":"authentik_core","model_name":"user","pk":"<pk>","name":"<username>","action":"cascade_many"}`. Deleting a group (API or `DeleteGroup`) deletes its bindings.
- User JSON: `pk`, `username`, `name`, `is_active` (true), `last_login` null, `date_joined`, `is_superuser` false, `groups` (group pks), `groups_obj`, `roles` `[]`, `roles_obj` `[]`, `email` `""`, `avatar` `""`, `attributes` `{}`, `uid`, `path` `"users"`, `type` `"internal"`, `uuid`, `password_change_date`, `last_updated`. `username=` filters exactly. `attributes=<json>` parses the JSON object, splits each key on `__` into a path inside `attributes` and keeps users whose value at that path equals the given value; malformed JSON answers 400 `{"attributes":["filter: failed to parse JSON"]}`; with `IgnoreUserAttributeFilter(true)` the filter is ignored and every user is returned (as real Authentik does on a lookup it cannot apply). Deleting a user removes it from groups and deletes its user bindings.
- Binding JSON: `pk` (UUID), `policy`, `group`, `user`, `policy_obj` null, `group_obj` (partial group `pk, num_pk, name`) or null, `user_obj` or null, `target`, `negate` false, `enabled` true, `order`, `timeout` 30, `failure_result` false, `expires` null, `expiring` false. `GET /policies/bindings/?target=X` filters by target; an empty or absent `target` returns every binding (django-filter ignores an empty filter). POST requires `target` (an existing application pk, else 400) and `order`, and exactly one of `policy`/`group`/`user`. Deleting an application deletes its bindings.
- Invitation JSON: `pk` (UUID counter `00000000-0000-0000-0002-%012d`), `name` (regex `^[-a-zA-Z0-9_]+$`, else 400; names are NOT unique, as in real Authentik: `SeedInvitation(name string, fields map[string]any) string` adds another invitation with the same name), `expires`, `fixed_data`, `created_by` (partial token user), `single_use`, `flow`, `flow_obj` (the flow map). `name=` filters exactly. `ConsumeInvitation` deletes it (what Authentik does when the first stage runs). `EnrolFromInvitation(name, username)` consumes the invitation and creates a user from `fixed_data` (`username`, `name`, `attributes` deep-copied), using the `username` argument instead when it is non-empty; returns the new pk, or 0 if no such invitation.
- `GET /authenticators/admin/all/?user=<pk>` returns a JSON list of devices `{verbose_name, verbose_name_plural, meta_model_name, pk, name, type, confirmed, created, last_updated, last_used, extra_description, external_id}` added with `AddAuthenticator`.
- `FailNextOn` fails the first later request whose method matches and whose path contains `pathContains`, with a `{"detail":"injected failure"}` body; it is consumed once.

- [ ] **Step 1: Write the failing client tests in `internal/authentik/identity_test.go`**

Tests use `fakeauthentik.New("t")` and `New(Config{URL: fake.URL, Token: "t", RPS: -1, MaxRetries: -1})`, except where a hand-written handler (`newTestClient`) is named.

```go
func TestOwnerProof(t *testing.T) {
	// OwnerProof("u-1") deep-equals map[string]any{"authentik_operator": map[string]any{"owner_uid": "u-1"}}
	// HasOwnerProof(OwnerProof("u-1"), "u-1") == true; with "u-2" == false;
	// HasOwnerProof(nil, "u-1") == false; HasOwnerProof(map[string]any{"authentik_operator": "x"}, "u-1") == false
}
func TestFindGroupByNameIsExact(t *testing.T) {
	// fake.SeedGroup("Immich Users & Admins", nil); pk := fake.SeedGroup("Immich Users", nil)
	// g, err := c.FindGroupByName(ctx, "Immich Users") -> err nil, g.Pk == pk
	// _, err = c.FindGroupByName(ctx, "immich users") -> IsNotFound(err)
}
func TestCreateGroupSendsNoUsers(t *testing.T) {
	// c.CreateGroup(ctx, api.GroupRequest{Name: "team", Attributes: OwnerProof("u-1")})
	// last POST body has no "users" key; HasOwnerProof(created.Attributes, "u-1")
}
func TestGroupMembershipCalls(t *testing.T) {
	// u := fake.SeedUser("bob", nil); g := fake.SeedGroup("team", nil)
	// AddUserToGroup(ctx, g, u) -> fake.GroupMemberPKs(g) == []int32{u}; request POST path ends "/core/groups/"+g+"/add_user/", body["pk"] == float64(u)
	// RemoveUserFromGroup(ctx, g, u) -> GroupMemberPKs(g) is empty
	// AddUserToGroup(ctx, g, 999) -> IsNotFound(err)
}
func TestGroupUsedByListsBindings(t *testing.T) {
	// fake.SeedApplication("immich", nil); g := fake.SeedGroup("immich-users", nil)
	// appPK := fake.Application("immich")["pk"].(string)
	// fake.SeedBinding(appPK, map[string]any{"group": g, "order": float64(0)})
	// used contains an entry with App "authentik_policies", ModelName "policybinding", Name "Binding from immich #0 to Group immich-users"
}
func TestFindUsersByOwnerUIDRechecksClientSide(t *testing.T) {
	// SeedUser("a", {"attributes": OwnerProof("uid-1")}); SeedUser("b", {"attributes": OwnerProof("uid-2")}); SeedUser("c", nil)
	// fake.IgnoreUserAttributeFilter(true)
	// users -> exactly one, Username "a"
	// the GET request Query, URL-decoded, contains `attributes={"authentik_operator__owner_uid":"uid-1"}`
}
func TestFindUserByUsernameIsExact(t *testing.T) { /* SeedUser("Alice") then FindUserByUsername("alice") -> IsNotFound */ }
func TestMeReportsSuperuser(t *testing.T) {
	// me.Pk == 1, me.Username == "akadmin", me.IsSuperuser == true
	// fake.SetTokenUserSuperuser(false) -> me.IsSuperuser == false
}
func TestListBindingsRefusesEmptyTarget(t *testing.T) {
	// _, err := c.ListBindings(ctx, "") -> errors.Is(err, ErrNoTarget); fake.Requests(false) is empty
}
func TestListBindingsDropsOtherTargets(t *testing.T) {
	// newTestClient handler answering GET /api/v3/policies/bindings/ with a page of two bindings,
	// targets "app-a" and "app-b" (every required property present)
	// ListBindings(ctx, "app-a") -> one binding, Target "app-a"
}
func TestDeleteBindingChecksTarget(t *testing.T) {
	// b := &api.PolicyBinding{Pk: pk, Target: "app-a"}
	// DeleteBinding(ctx, b, "app-b") -> error, no DELETE recorded
	// DeleteBinding(ctx, b, "") -> errors.Is(err, ErrNoTarget), no DELETE recorded
	// DeleteBinding(ctx, b, "app-a") -> nil, DELETE recorded, fake.Bindings("app-a") empty
}
func TestInvitationLifecycle(t *testing.T) {
	// inv := CreateInvitation(name "k8s-u1-1", SingleUse true, Flow FlowEnrollmentPK, FixedData {"username": "dave"})
	// FindInvitationByName("k8s-u1-1").Pk == inv.Pk; DeleteInvitation(inv.Pk) nil; FindInvitationByName -> IsNotFound;
	// DeleteInvitation(inv.Pk) again -> nil
	// fake.SeedInvitation("dup", nil) twice -> FindInvitationByName("dup") -> errors.Is(err, ErrAmbiguous)
}
func TestDeleteInvitationRedactsThePK(t *testing.T) {
	// inv created; fake.FailNextOn(http.MethodDelete, "/stages/invitation/invitations/", 500)
	// err := DeleteInvitation(ctx, inv.Pk): err != nil; !strings.Contains(err.Error(), inv.Pk);
	// strings.Contains(err.Error(), "<redacted>"); StatusCode(err) == 500
	// transport failure: client to a closed server -> err != nil and !strings.Contains(err.Error(), pk)
}
func TestUserAuthenticators(t *testing.T) {
	// u := SeedUser("dave"); 0 devices; fake.AddAuthenticator(u) -> 1 device; another user -> 0
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `dx go test ./internal/authentik/ -count=1 -run 'TestOwnerProof|TestFindGroupByNameIsExact|TestCreateGroupSendsNoUsers|TestGroupMembershipCalls|TestGroupUsedByListsBindings|TestFindUsersByOwnerUIDRechecksClientSide|TestFindUserByUsernameIsExact|TestMeReportsSuperuser|TestListBindings|TestDeleteBindingChecksTarget|TestInvitationLifecycle|TestDeleteInvitationRedactsThePK|TestUserAuthenticators'`
Expected: FAIL, build errors (`undefined: OwnerProof`, `fake.SeedGroup undefined`, ...).

- [ ] **Step 3: Implement the client methods and the fake**

Client methods follow `application.go` / `lookup.go`: generated call, `Check(resp, err)`, `ListAll` for lists with `PageSize`. `DeleteInvitation` applies an unexported `redact(err error, secret string) error`: for an `*APIError` it returns a copy with `Path` and `Body` redacted; for any other error `fmt.Errorf("%s", strings.ReplaceAll(err.Error(), secret, "<redacted>"))`. Fake: `server.go` gains the new maps, the seeded `FlowEnrollment` flow and token user in `New`, `Query` recording, `FailNextOn`, routing (`/core/groups/`, `/core/users/` other than `me`, `/policies/bindings/`, `/stages/invitation/invitations/`, `/authenticators/admin/all/`) to handlers in the three new files, and binding cascade on application delete.

- [ ] **Step 4: Run them to see them pass**

Run: `dx go test ./internal/authentik/... -count=1`
Expected: PASS.

- [ ] **Step 5: Full suite and lint**

Run: `dx task test && dx make lint`
Expected: exit 0 both (existing controller suites still pass on the extended fake).

- [ ] **Step 6: Commit**

```bash
git add internal/authentik
git commit -m "feat(authentik): group, user, policy binding and invitation calls" \
  -m "Bindings are never listed without a target and deletes re-check it; the owner proof lookup re-checks every result; invitation errors redact the pk. fakeauthentik models the same endpoints." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 4: Authentik 2026.2.3 recorded responses and compat rules
Kind: rewrite

**Files:**
- Create: `internal/authentik/record_test.go` (`//go:build record`), `internal/authentik/testdata/2026.2.3/{bindings_list,binding,group,groups_list,group_used_by,user,users_list,users_owner_list,users_me,invitation,invitations_list,authenticators}.json`
- Modify: `internal/authentik/compat.go`, `internal/authentik/compat_test.go`

**Interfaces:**
- Consumes: Task 3's client methods and `OwnerProof`.
- Produces: `compatRule` covers `/policies/bindings/` (`expires`, `expiring`, sentinel `target`) and any further rule the recordings prove necessary; `serveRecorded` serves the twelve recorded files.

- [ ] **Step 1: Write the recorder `internal/authentik/record_test.go`**

`//go:build record`; `func TestRecord20262Responses(t *testing.T)`. Reads `AK_RECORD_URL` and `AK_RECORD_TOKEN` (fatal if unset). Raw `net/http` with `Authorization: Bearer`, JSON bodies; every non-2xx is fatal except step 8. Sequence (names fixed):
1. Poll `GET /api/v3/flows/instances/?slug=default-authentication-flow` every 5 seconds for up to 5 minutes until one result; keep its `pk`.
2. `POST /api/v3/core/applications/` `{"name":"rec-app","slug":"rec-app"}`; keep `pk`.
3. `POST /api/v3/core/groups/` `{"name":"rec-group","attributes":OwnerProof("rec-uid")}`; keep `pk`.
4. `POST /api/v3/core/users/` `{"username":"rec-user","name":"Rec User","path":"users","attributes":OwnerProof("rec-uid")}`; keep `pk`.
5. `POST /api/v3/core/groups/<group>/add_user/` `{"pk":<user>}`.
6. `POST /api/v3/policies/bindings/` `{"group":"<group>","target":"<app>","order":0}` -> save body as `binding.json`.
7. `POST /api/v3/stages/invitation/invitations/` `{"name":"rec-invitation","single_use":true,"flow":"<flow>","expires":"<now+168h RFC3339>","fixed_data":{"username":"rec-invitee","attributes":OwnerProof("rec-uid-2")}}` -> save as `invitation.json`.
8. `POST /api/v3/authenticators/admin/static/` `{"name":"rec-device","user":<user>}`; on non-2xx `t.Logf` the status and continue.
9. GET and save: `bindings_list.json` (`/policies/bindings/?target=<app>`), `group.json` (`/core/groups/<group>/`), `groups_list.json` (`/core/groups/?name=rec-group&include_users=false`), `group_used_by.json` (`/core/groups/<group>/used_by/`), `user.json` (`/core/users/<user>/`), `users_list.json` (`/core/users/?username=rec-user`), `users_owner_list.json` (`/core/users/?attributes=` + URL-encoded `{"authentik_operator__owner_uid":"rec-uid"}`), `users_me.json` (`/core/users/me/`), `invitations_list.json` (`/stages/invitation/invitations/?name=rec-invitation`), `authenticators.json` (`/authenticators/admin/all/?user=<user>`).
Files are written to `testdata/2026.2.3/` pretty-printed with `json.Indent` (two spaces) and a trailing newline. The doc comment states the throwaway instance and the docker commands of Step 2.

- [ ] **Step 2: Start a throwaway Authentik 2026.2.3 inside the devcontainer**

Run:
```bash
dx sh -c 'set -e
docker network create ak-rec
E="-e AUTHENTIK_POSTGRESQL__HOST=ak-rec-pg -e AUTHENTIK_POSTGRESQL__USER=authentik -e AUTHENTIK_POSTGRESQL__NAME=authentik -e AUTHENTIK_POSTGRESQL__PASSWORD=rec-postgres-not-a-secret -e AUTHENTIK_SECRET_KEY=rec-secret-key-not-a-secret-0123456789abcdef -e AUTHENTIK_BOOTSTRAP_TOKEN=rec-token-not-a-secret-0123456789 -e AUTHENTIK_BOOTSTRAP_PASSWORD=rec-password-not-a-secret -e AUTHENTIK_BOOTSTRAP_EMAIL=rec@example.invalid"
docker run -d --name ak-rec-pg --network ak-rec -e POSTGRES_USER=authentik -e POSTGRES_DB=authentik -e POSTGRES_PASSWORD=rec-postgres-not-a-secret postgres:16-alpine
sleep 10
docker run -d --name ak-rec-server --network ak-rec -p 127.0.0.1:9000:9000 $E ghcr.io/goauthentik/server:2026.2.3 server
docker run -d --name ak-rec-worker --network ak-rec $E ghcr.io/goauthentik/server:2026.2.3 worker
for i in $(seq 1 120); do curl -fsS -H "Authorization: Bearer rec-token-not-a-secret-0123456789" http://127.0.0.1:9000/api/v3/core/users/me/ >/dev/null && exit 0; sleep 5; done
docker logs --tail 30 ak-rec-server; exit 1'
```
Expected: exit 0 within 10 minutes. On exit 1: run the cleanup of Step 4, then STOP.

- [ ] **Step 3: Record**

Run: `dx env AK_RECORD_URL=http://127.0.0.1:9000 AK_RECORD_TOKEN=rec-token-not-a-secret-0123456789 go test -tags record -run TestRecord20262Responses ./internal/authentik/ -count=1 -v`
Expected: PASS; the twelve files exist under `internal/authentik/testdata/2026.2.3/`.

- [ ] **Step 4: Remove the throwaway instance (always, also after a failure)**

Run: `dx sh -c 'docker rm -f ak-rec-server ak-rec-worker ak-rec-pg; docker network rm ak-rec'`
Expected: the container names echoed, then `ak-rec`.

- [ ] **Step 5: Write the decode tests in `compat_test.go`**

Extend `serveRecorded`'s switch (method, path, query) to: GET `/policies/bindings/` -> `bindings_list.json`; POST `/policies/bindings/` -> `binding.json`; GET path ending `/used_by/` under `/core/groups/` -> `group_used_by.json`; GET `/core/groups/` exactly -> `groups_list.json`; GET `/core/groups/<x>/` -> `group.json`; GET `/core/users/me/` -> `users_me.json`; GET `/core/users/` with an `attributes` query -> `users_owner_list.json`; GET `/core/users/` otherwise -> `users_list.json`; GET `/core/users/<pk>/` -> `user.json`; POST `/stages/invitation/invitations/` -> `invitation.json`; GET `/stages/invitation/invitations/` -> `invitations_list.json`; GET `/authenticators/admin/all/` -> `authenticators.json`. Keys read from the recordings (target, pks) are read from the files, never hard-coded.

```go
func TestDecodesAuthentik20262Bindings(t *testing.T)       // ListBindings(target from bindings_list.json results[0].target): len 1, Expiring false, GroupObj set
func TestDecodesAuthentik20262BindingCreate(t *testing.T)  // CreateBinding: no error, Order 0
func TestDecodesAuthentik20262Group(t *testing.T)          // GetGroup: Name "rec-group", HasOwnerProof(Attributes, "rec-uid"), len(UsersObj) == 1
func TestDecodesAuthentik20262GroupByName(t *testing.T)    // FindGroupByName("rec-group"): no error
func TestDecodesAuthentik20262GroupUsedBy(t *testing.T)    // GroupUsedBy: an entry App "authentik_policies", ModelName "policybinding"
func TestDecodesAuthentik20262User(t *testing.T)           // GetUser: Username "rec-user"
func TestDecodesAuthentik20262UserByUsername(t *testing.T) // FindUserByUsername("rec-user"): no error
func TestDecodesAuthentik20262UsersByOwnerUID(t *testing.T) // FindUsersByOwnerUID("rec-uid"): len 1
func TestDecodesAuthentik20262Me(t *testing.T)             // Me: IsSuperuser true, Username "akadmin"
func TestPingDecodesAuthentik20262(t *testing.T)           // Ping (same endpoint, used by the AuthentikDeployment reconciler): returns "akadmin", no error
func TestDecodesAuthentik20262InvitationCreate(t *testing.T) // CreateInvitation: Name "rec-invitation", FixedData["attributes"] present
func TestDecodesAuthentik20262InvitationByName(t *testing.T) // FindInvitationByName("rec-invitation"): no error
func TestDecodesAuthentik20262Authenticators(t *testing.T) // UserAuthenticators(user pk from user.json): no error
```

- [ ] **Step 6: Run them; act on each outcome**

Run: `dx go test ./internal/authentik/ -count=1 -run TestDecodesAuthentik20262 -v`
Expected: one of the outcomes below, each with its action:
- `TestDecodesAuthentik20262Bindings` and `...BindingCreate` FAIL with `no value given for required property expires` (or `expiring`): add the rule in Step 7. If they PASS, do not add the binding rule and say so in the report.
- `TestDecodesAuthentik20262Me` and `TestPingDecodesAuthentik20262` FAIL (expected from the 2026.8.5 source: `core/api/users.py` returns only `user` and `original` from `/core/users/me/` and its `UserSelf` serializer has no `is_current`, while the client requires top-level `users` (`model_session_user.go`) and `user.is_current` (`model_user_self.go`)): add the `/core/users/me/` rule of Step 7. If they PASS, add no me rule and say so in the report.
- Any other test FAILING with `no value given for required property <p>`: add a rule for that path with `keys: []string{<p>...}` and a sentinel key the object always has (`num_pk` for groups, `username` for users, `single_use` for invitations, `meta_model_name` for devices, `model_name` for used_by).
- Any test FAILING for another reason (type mismatch, a value the client rejects): STOP.

- [ ] **Step 7: Add the rules to `compatRule` in `compat.go`**

```go
case strings.Contains(path, "/policies/bindings/"):
	return &compat{keys: []string{"expires", "expiring"},
		fill: func(o map[string]any) { addMissing(o, "target", "expires"); addMissing(o, "target", "expiring") }}
```
The `/core/users/me/` rule (two fills, one function), matched with `strings.HasSuffix(path, "/core/users/me/")` and placed before any `/core/users/` case: `keys: []string{"users", "is_current"}`; fill on the top-level document: (1) top level, sentinel `user`: when `users` is absent set `doc["users"] = []any{doc["user"]}` (an array holding the `user` object, not null); (2) inside `doc["user"]` (a map): when `is_current` is absent set it to `true` (the token's own user is the current one). Nothing present is changed. Add `TestCompatMeRuleLeavesPresentValues`: a body that already has `users: [{...}]` and `user.is_current: false` is unchanged.

plus any rule Step 6 required, placed before broader cases. Extend the type doc comment's list ("Found by ... 2026.2.3") with each property added. Add `TestCompatBindingRuleLeavesPresentValues`: `patchJSON([]byte(`{"target":"x","expires":"2026-01-01T00:00:00Z","expiring":true}`), rule.fill)` keeps both values.

The me rule also repairs the existing `Ping` (`internal/authentik/client.go`, used by `authentikdeployment_controller.go` to report `Connected`), which today fails to decode on 2026.2.3; say so in the commit body.

- [ ] **Step 8: Run to see them pass**

Run: `dx go test ./internal/authentik/ -count=1`
Expected: PASS.

- [ ] **Step 9: Full suite and lint**

Run: `dx task test && dx make lint`
Expected: exit 0 both.

- [ ] **Step 10: Commit**

```bash
git add internal/authentik/record_test.go internal/authentik/testdata/2026.2.3 internal/authentik/compat.go internal/authentik/compat_test.go
git commit -m "fix(authentik): decode 2026.2.3 policy bindings and record the new endpoints" \
  -m "Responses recorded from a throwaway Authentik 2026.2.3 (record_test.go, build tag record). Policy bindings there lack expires and expiring, which the pinned client requires." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 5: Identity gate, its flag and its chart value
Kind: rewrite

**Files:**
- Create: `internal/controller/identity_gate.go`, `internal/controller/identity_gate_test.go`
- Modify: `internal/controller/common.go`, `internal/controller/adoption_gate_test.go`, `cmd/main.go`, `internal/controller/authentikgroup_controller.go`, `internal/controller/authentikuser_controller.go`, `dist/chart/values.yaml`, `dist/chart/templates/manager/manager.yaml`, `dist/chart/tests/deployment_test.yaml`, `dist/chart/ci/full-values.yaml`, `dist/chart/README.md`

**Interfaces:**
- Consumes: `adoptionGate`, `condErrf`, `conditionReason`.
- Produces:
  - `ParseNamespaceSelector(s string) (labels.Selector, error)` (renamed from `ParseAdoptionNamespaceSelector`, same behaviour; error text `parse namespace selector %q: %w`).
  - `func selectorMatches(ctx context.Context, rd client.Reader, sel labels.Selector, namespace string) (bool, error)` (nil selector -> false; Namespace read with `rd`); `adoptionGate.check` uses it (behaviour unchanged).
  - `const reasonIdentityNotAllowed = "IdentityNotAllowed"`.
  - `type identityGate struct { reader client.Reader; selector labels.Selector }`; `func (g identityGate) allowed(ctx context.Context, namespace string, ref *authentikiov1alpha1.AuthentikRef) (bool, error)` (ref != nil -> true); `func (g identityGate) check(ctx context.Context, namespace string, ref *authentikiov1alpha1.AuthentikRef) error` (denied -> `condErrf(reasonIdentityNotAllowed, ...)`).
  - Flag `--default-connection-identity-namespace-selector` (options field `identityNamespaces string`), help text: "Label selector for the namespaces whose AuthentikGroup and AuthentikUser resources the operator reconciles on the default connection, under every managementPolicy. Empty denies every namespace. Resources with spec.authentikRef are not affected."; an invalid value fails startup with `--default-connection-identity-namespace-selector: <parse error>`.
  - `AuthentikGroupReconciler` and `AuthentikUserReconciler` gain fields `Recorder events.EventRecorder`, `APIReader client.Reader`, `AdoptionNamespaceSelector labels.Selector`, `IdentityNamespaceSelector labels.Selector`, wired in `setupControllers(mgr ctrl.Manager, connections *connection.Resolver, adoptionNamespaces, identityNamespaces labels.Selector) error` (recorders `mgr.GetEventRecorder("authentikgroup")` / `("authentikuser")`). The stub `Reconcile` bodies stay as they are.
  - Chart value `identity.defaultConnectionNamespaceSelector: ""` rendered as one argument `--default-connection-identity-namespace-selector=<value>` only when non-empty.

- [ ] **Step 1: Write the failing tests**

`adoption_gate_test.go`: rename `TestParseAdoptionNamespaceSelector` to `TestParseNamespaceSelector` and its calls to `ParseNamespaceSelector` (cases unchanged). `identity_gate_test.go` (Ginkgo, namespace `default`, `k8sClient`):

```go
var _ = Describe("Identity gate", func() {
	It("denies every namespace on the default connection when the selector is empty", func() {
		err := identityGate{k8sClient, mustSelector("")}.check(ctx, "default", nil)
		Expect(conditionReason(err, "")).To(Equal(reasonIdentityNotAllowed))
		Expect(err.Error()).To(ContainSubstring("--default-connection-identity-namespace-selector"))
	})
	It("treats a nil selector as deny", func() { /* identityGate{k8sClient, nil}.check -> reason IdentityNotAllowed */ })
	It("allows a namespace the selector matches", func() {
		Expect(identityGate{k8sClient, mustSelector("kubernetes.io/metadata.name in (default)")}.check(ctx, "default", nil)).To(Succeed())
	})
	It("refuses a namespace the selector does not match", func() { /* "kubernetes.io/metadata.name=other" -> IdentityNotAllowed */ })
	It("never gates a resource with spec.authentikRef", func() {
		ref := &authentikiov1alpha1.AuthentikRef{Name: "main"}
		Expect(identityGate{k8sClient, nil}.check(ctx, "default", ref)).To(Succeed())
		ok, err := identityGate{k8sClient, nil}.allowed(ctx, "default", ref)
		Expect(err).NotTo(HaveOccurred()); Expect(ok).To(BeTrue())
	})
	It("reports a denied namespace as not allowed without an error", func() { /* allowed(...) == false, err nil */ })
})
```
(`mustSelector` is a local helper calling `ParseNamespaceSelector` and failing on error.)

`dist/chart/tests/deployment_test.yaml`, two new tests after the adoption ones:
```yaml
  - it: passes identity.defaultConnectionNamespaceSelector as the identity selector flag
    set:
      identity.defaultConnectionNamespaceSelector: kubernetes.io/metadata.name=authentik
    asserts:
      - contains:
          path: spec.template.spec.containers[0].args
          content: --default-connection-identity-namespace-selector=kubernetes.io/metadata.name=authentik
  - it: renders a set-based identity selector as a single argument
    set:
      identity.defaultConnectionNamespaceSelector: kubernetes.io/metadata.name in (authentik)
    asserts:
      - contains:
          path: spec.template.spec.containers[0].args
          content: --default-connection-identity-namespace-selector=kubernetes.io/metadata.name in (authentik)
```
The existing default-args `equal` test must keep passing unchanged (the flag is absent by default).

- [ ] **Step 2: Run to see them fail**

Run: `envtest "Identity gate"` then `dx task chart:test`
Expected: FAIL (`undefined: identityGate`, `undefined: ParseNamespaceSelector`); chart: the two new tests fail.

- [ ] **Step 3: Implement**

`identity_gate.go` as in Interfaces; denial message: `AuthentikGroup and AuthentikUser on the operator's default connection are not allowed in namespace %q; ask an administrator to allow the namespace with --default-connection-identity-namespace-selector, or set spec.authentikRef`. `common.go`: rename, extract `selectorMatches`. `cmd/main.go`: flag, parse, pass to `setupControllers`, wire the four fields on the group and user reconcilers. Chart: `values.yaml` block after `adoption:` (comment mirrors adoption's: "Empty (default) denies every namespace: such resources report IdentityNotAllowed ... Read at startup; changing it restarts the pod."; mention the recommended value `kubernetes.io/metadata.name in (authentik)`), template line after the adoption flag `{{- with .Values.identity.defaultConnectionNamespaceSelector }}\n        - {{ printf "--default-connection-identity-namespace-selector=%s" . | quote }}\n        {{- end }}`, `ci/full-values.yaml` sets `identity.defaultConnectionNamespaceSelector: kubernetes.io/metadata.name in (authentik)`, `README.md` gets a values-table row and an install example like the adoption one.

- [ ] **Step 4: Run to see them pass**

Run: `envtest "Identity gate" && dx go test ./internal/controller/ -run TestParseNamespaceSelector -count=1 && dx task chart:test && dx go build ./...`
Expected: PASS; build ok.

- [ ] **Step 5: Full suite, chart checks, lint**

Run: `dx task test && dx task chart:ci && dx make lint`
Expected: exit 0 all.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/identity_gate.go internal/controller/identity_gate_test.go internal/controller/common.go internal/controller/adoption_gate_test.go internal/controller/authentikgroup_controller.go internal/controller/authentikuser_controller.go cmd/main.go dist/chart
git commit -m "feat(controller): identity gate for groups and users on the default connection" \
  -m "--default-connection-identity-namespace-selector (chart value identity.defaultConnectionNamespaceSelector) names the namespaces whose groups and users the shared token acts on; empty denies." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 6: AuthentikGroup API and lifecycle (policy flow, attributes, deletion guard)
Kind: rewrite

**Files:**
- Create: `internal/controller/identity_status.go`, `internal/controller/identity_status_test.go`, `internal/controller/attributes.go`, `internal/controller/attributes_test.go`, `internal/controller/group_desired.go`, `internal/controller/authentikgroup_test.go`
- Modify: `api/v1alpha1/authentikgroup_types.go`, `internal/controller/authentikgroup_controller.go`, `internal/controller/authentikgroup_controller_test.go` (delete the scaffold spec: `git rm`), `internal/controller/claims_index_test.go`, `config/samples/v1alpha1_authentikgroup.yaml`, `go.mod`, `go.sum`
- Modify (regenerated by `make manifests generate` / `make chart-regen`, never by hand): `config/crd/bases/authentik.io_authentikgroups.yaml`, `api/v1alpha1/zz_generated.deepcopy.go`, `config/rbac/role.yaml`, `dist/chart/templates/crd/authentikgroups.authentik.io.yaml`, `dist/chart/templates/rbac/manager-role.yaml`

**Interfaces:**
- Consumes: Task 2 (`adopt.Decide`, `adopt.Finalize`), Task 3 (group calls, `Me`, `OwnerProof`, `HasOwnerProof`, fake), Task 5 (`identityGate`, reconciler fields), existing `adoptionGate`, `resolve`, `resolveForDeletion`, `connectionGone`, `claim`, `holders`, `outrankedBy`, `dryRunReport`, `recordDrift`, `describeChanges`, `managedField`, `changedFields`, `emit`, `FinalizerName`, `ConditionReady`, `ConditionDrifted`, reason constants.
- Produces:
  - API: `AuthentikGroupSpec{ APIManagementSpec (inline); AuthentikRef *AuthentikRef; Name string; IsSuperuser *bool; Attributes map[string]apiextensionsv1.JSON }` and `AuthentikGroupStatus{ Conditions []metav1.Condition; GroupPK string; Name string; Provenance Provenance; PendingCreate string; ManagedAttributes []string; ObservedGeneration int64 }` (json: `groupPk`, `name`, `provenance`, `pendingCreate`, `managedAttributes`, `observedGeneration`).
  - `func classifyFailure(err error) (reason string, result ctrl.Result, retErr error, warn bool)`.
  - `func specAttributes(spec map[string]apiextensionsv1.JSON) (map[string]any, error)`; `type attributeMerge struct { Merged map[string]any; Owned []string; Set []string; Removed []string }`; `func mergeAttributes(live, want map[string]any, owned []string) attributeMerge`.
  - `func groupName(g *authentikiov1alpha1.AuthentikGroup) string` (spec.name, else metadata.name).
  - `const indexGroupPK = ".status.groupPk"`; `func groupPKIndex(o client.Object) []string`.
  - Reasons/conditions: `reasonDeletionBlocked = "DeletionBlocked"`; event reasons `RetainedUnverifiable`, `Retained`, `GroupMissing`.
  - `func (r *AuthentikGroupReconciler) finalize(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup) (ctrl.Result, error)`.

API markers (pinned):
- Spec type: `+kubebuilder:validation:XValidation:rule="has(self.authentikRef) == has(oldSelf.authentikRef) && (!has(self.authentikRef) || self.authentikRef.name == oldSelf.authentikRef.name)",message="authentikRef is immutable"` (same rule as the provider and application).
- `name`: `MinLength=1`, `MaxLength=255`, ``Pattern=`^[^\x00-\x1F\x7F]+$` ``, optional; doc: "Authentik group name; defaults to metadata.name; cannot change once the group is created or adopted (InvalidSpec)".
- `isSuperuser`: optional `*bool`; doc: "Unset: held to false on a group this resource created, the live value on an adopted group."
- `attributes`: `json:"attributes,omitzero"` (keeps `{}` distinct from absent), `MaxProperties=64`, `+kubebuilder:validation:XValidation:rule="self.all(k, !k.startsWith('goauthentik.io/') && k != 'authentik_operator' && k != 'settings')",message="attribute keys starting with goauthentik.io/, and authentik_operator and settings, are reserved"`; doc covers per-key ownership (spec 5.3a) and that values are strings, numbers, booleans or lists. If envtest fails to install the CRD with a CEL cost error, lower `MaxProperties` to 32, then 16; still failing: STOP.
- Root: `+kubebuilder:subresource:status`; print columns `Name` (`.status.name`), `Ready`, `Provenance`, `Reason` (priority 1), `Age`, as on `AuthentikApplication`. Remove the scaffold `Foo` field and comments.

Reconcile behaviour (decisions; the tests below pin each):
1. Deleting: `finalize`; return its result. Otherwise add `FinalizerName` with a merge patch, then `sync` with a `persist` closure, as the application reconciler does.
2. `sync` order: identity gate, adoption gate (`adoptionGate.check`), name immutability (`status.groupPk != ""` and `status.name != ""` and `status.name != groupName(g)` -> `InvalidSpec`: `spec.name changed from %q to %q after the group was created or adopted; renaming would change every application's bindings by name. Set spec.name back to %q`), `specAttributes` (error -> `InvalidSpec` naming the key), `resolve`, then observe and act. Errors go through `failed` = `classifyFailure` + `setReady` + a Warning `InvalidSpec` event when `warn` and the condition changed.
3. Facts: Tracked from `GetGroup(status.groupPk)` (404 -> Gone); target from `FindGroupByName(groupName)` only when not TrackedFound (`ErrAmbiguous` -> Ambiguous); `OwnCreatePending = status.pendingCreate == groupName(g)`; `OwnCreateProven = target found && HasOwnerProof(target.Attributes, string(g.UID))`; `RecreateVanished = true`.
4. Actions: `UseTracked` -> claim re-check (`yieldIfOutranked`, cached) then update (or drift under ReadOnly); `Create`/`Recreate` -> persist `pendingCreate = name`, `CreateGroup` with `name`, `is_superuser` (spec, default false) and attributes = spec attributes plus `OwnerProof(uid)`, 4xx clears `pendingCreate`, success records `Created`, `Recreate` first emits Warning `GroupMissing`; `Create` under `AdoptOrCreate` (a miss) first emits Normal `NothingToAdopt` (`Creating a new group because nothing was found to adopt (name %q)`); `RecoverOwnCreate` -> `Created`, Normal `Recovered`; `Adopt`/`Observe` -> pre-check claimants (cached), record `Adopted` + pk + name, persist, re-check with `APIReader`, event `Adopted`/`Observing`, then update/drift; `DryRunAdopt` -> `dryRunReport` with subject `group %q`, no writes; `DryRunCreate` -> `AdoptionDryRun` "would be created"; `Conflict` (message names `spec.managementPolicy`; for a pending unproven create the message is `a create of group %q was sent, but the group now named %q does not carry this resource's ownership proof, so it is not treated as this resource's group under any managementPolicy. If that group should be managed by this resource, clear status.pendingCreate and use managementPolicy Adopt (it is then adopted, not created); otherwise choose another spec.name or remove that group from Authentik`); `TargetNotFound`/`TrackedGone` -> `AdoptionTargetNotFound` (pk kept); `Ambiguous` -> `Conflict`.
5. Update (not ReadOnly): `name` always; `is_superuser` = spec if set, else `false` when `Created`, else untouched; attributes only when `spec.attributes != nil`: `mergeAttributes(live, want, status.managedAttributes)`, PATCH `attributes` = `Merged` (non-nil map) when `Set` or `Removed` is non-empty, `status.managedAttributes = Owned`; `spec.attributes == nil` -> attributes untouched and `status.managedAttributes = nil`. One PATCH when anything differs; Normal `Updated` event and log `Updated Group` listing field names (`attributes.<key>`).
6. ReadOnly: same comparison, no write, `recordDrift`; Ready True reason `ReadOnly`.
7. Success: `pendingCreate = ""`, `groupPk`, `name`, `observedGeneration`; Ready True `Synced`; requeue `syncedRecheck`.
8. `finalize`: no finalizer -> return; `status.groupPk == ""` -> release. `allowed` from the identity gate (error -> return it). `adopt.Finalize{UnsetDefault: ""}`: `Retain` -> Normal `Retained`; `RetainUndecided` -> Warning `Retained` "provenance unknown; retaining"; `RetainNotAllowed` -> Warning `Retained` naming the selector flag; `DeleteUndecided` -> Warning `DeletingUndecided` then delete; delete path: `resolveForDeletion` (`connectionGone` -> Warning `Orphaned`, release); `Me()` (error -> `Ready=False` `AuthentikError` "Deletion blocked: ...", return error); not superuser -> Warning `RetainedUnverifiable` with note `Group %q left in Authentik: the operator's token user is not a superuser, so it cannot see every binding that uses the group; delete it in Authentik after checking its bindings`, release, no `used_by` call; `GroupUsedBy` (404 -> release; other error -> as `Me()`); entries with `App == "authentik_policies"` and `ModelName == "policybinding"` -> `Ready=False` `DeletionBlocked`, message `deletion blocked: the group is still used by %d policy binding(s): %s; remove the binding in Authentik or the group from the application's spec.access.groups, or set spec.deletionPolicy: Retain on this resource` (`%s` = each entry's `name` quoted with `%q`, joined `, `), Warning event `DeletionBlocked` once per change, return `ctrl.Result{RequeueAfter: userErrorRecheck}, nil` keeping the finalizer; none -> `DeleteGroup(status.groupPk)`, Normal `Deleted`, release.
9. `SetupWithManager`: register `indexGroupPK`; `For(&AuthentikGroup{})`, `MaxConcurrentReconciles: 2`, `Named("authentikgroup")`. RBAC markers: existing three plus `authentikdeployments` get, `namespaces` get, `events.k8s.io` events create;patch.

- [ ] **Step 1: Write the failing unit tests**

`identity_status_test.go`:
```go
func TestClassifyFailure(t *testing.T) {
	// condErr("Conflict", errors.New("x")) -> ("Conflict", ctrl.Result{RequeueAfter: userErrorRecheck}, nil, false)
	// &authentik.APIError{StatusCode: 409}  -> (reasonConflict, RequeueAfter userErrorRecheck, nil, false)
	// &authentik.APIError{StatusCode: 400, Body: `{"attributes":["bad"]}`} -> (reasonInvalidSpec, RequeueAfter userErrorRecheck, nil, true)
	// errors.New("boom") -> (reasonAuthentikError, ctrl.Result{}, that error, false)
}
```
`attributes_test.go`:
```go
func TestSpecAttributes(t *testing.T) {
	// {"s": "x", "n": 1, "b": true, "l": ["a"]} decode to "x", float64(1), true, []any{"a"}
	// {"nested": {"a": 1}} -> error containing `"nested"`; {"n": null} -> error containing `"n"`
}
func TestMergeAttributes(t *testing.T) {
	// live {"foreign": "f", "role": "old"}, want {"role": "admin"}, owned nil
	//   -> Merged {"foreign": "f", "role": "admin"}, Set ["role"], Removed [], Owned ["role"]
	// live {"a": "1", "b": "2"}, want {"a": "1"}, owned ["a", "b"] -> Merged {"a": "1"}, Removed ["b"], Set []
	// live {"a": float64(1)}, want {"a": float64(1)} -> no Set, no Removed
	// live {"a": "1"}, want {} (empty, non-nil), owned ["a"] -> Merged {} (non-nil, len 0), Removed ["a"]
	// live nil, want {"a": "1"} -> Merged {"a": "1"}
	// Owned is always sorted
}
```

- [ ] **Step 2: Write the failing envtest suite `authentikgroup_test.go`**

Setup as `authentikapplication_adoption_test.go`: `fake := fakeauthentik.New(testAPIToken)`, `recorder := events.NewFakeRecorder(100)`, `groups := &AuthentikGroupReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(), Connections: &connection.Resolver{Reader: k8sClient, Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1}}, Recorder: recorder, APIReader: k8sClient, AdoptionNamespaceSelector: labels.Everything(), IdentityNamespaceSelector: labels.Everything()}`; helpers `newGroup(name, policy, mutate)`, `reconcileGroup`, `get`, `ready`, `writes := func() []fakeauthentik.Request { return fake.Requests(true) }`. Add `indexGroupPK: groupPKIndex` to `testIndexes` in `claims_index_test.go`, and in the same file `type staleCache struct{ client.Client }` whose `List` returns the list emptied (`meta.SetList(list, nil)`) for any call with field selectors and delegates otherwise: it models an informer cache that has not seen another holder's claim yet (Tasks 6 and 10 use it).

```go
var _ = Describe("AuthentikGroup", func() {
	It("creates the group with the owner proof and records Created", func() {
		// newGroup("team-a", Create); reconcile
		// fake.GroupByName("team-a")["attributes"] == map{"authentik_operator": map{"owner_uid": string(g.UID)}}
		// fake.GroupByName("team-a")["is_superuser"] == false
		// status: Provenance Created, GroupPK == fake pk, Name "team-a", PendingCreate ""; Ready True reason "Synced"
	})
	It("stays a Conflict under Create when a group with the name exists", func() {
		// fake.SeedGroup("team-b", nil); Ready reason "Conflict", message contains "spec.managementPolicy"; writes() empty
	})
	It("adopts by name and patches only what the spec sets", func() {
		// SeedGroup("legacy", {"is_superuser": true, "attributes": {"legacy": "x"}}); Adopt, spec.attributes {"role": "admin"}
		// live is_superuser true; live attributes == {"legacy": "x", "role": "admin"}; Provenance Adopted; ManagedAttributes ["role"]
	})
	It("holds isSuperuser to false on a group it created when the spec leaves it unset", func() {
		// create; fake.SetGroupField(pk, "is_superuser", true); reconcile -> live false; recorder has a Normal "Updated" event
	})
	It("reports AdoptionTargetNotFound under Adopt when nothing matches and writes nothing", func() {})
	It("creates under AdoptOrCreate when nothing matches, with a NothingToAdopt event", func() {})
	It("reports in a dry run the fields and attribute keys adoption would change and writes nothing", func() {
		// SeedGroup("dry", {"is_superuser": true}); Adopt, dryRun, isSuperuser false, attributes {"role": "admin"}
		// Ready reason "AdoptionDryRun"; message contains "is_superuser: true -> false" and "role"; writes() empty; Provenance ""
	})
	It("reports a would-create in a dry run with nothing to adopt under AdoptOrCreate", func() {})
	It("observes under ReadOnly, reports drift in Drifted and never writes", func() {
		// SeedGroup("ro", {"is_superuser": true}); ReadOnly, isSuperuser false
		// Ready True reason "ReadOnly"; Drifted True message contains "is_superuser"; writes() empty
	})
	It("recovers its own create whose response was lost as Created", func() {
		// fake.LoseNextGroupCreate(); first reconcile returns an error; status.PendingCreate == "lost"
		// second reconcile: Provenance Created, GroupPK set, PendingCreate ""; a "Recovered" event; fake.GroupCount() == 1
	})
	It("treats a group found during a pending create without the proof as a Conflict under every policy", func() {
		// AdoptOrCreate; status.pendingCreate = "squat" written via Status().Update; SeedGroup("squat", nil)
		// Ready reason "Conflict"; Provenance ""; PendingCreate "squat" kept; writes() empty
	})
	It("recreates a group it created that vanished", func() {
		// create; fake.DeleteGroup(pk); reconcile -> new GroupPK != old; Warning "GroupMissing"; Provenance Created
	})
	It("never recreates an adopted group that vanished", func() {
		// adopt; fake.DeleteGroup(pk); reconcile -> reason "AdoptionTargetNotFound"; GroupPK unchanged; GroupCount() == 0
	})
	It("refuses a changed spec.name once the group is tracked and writes nothing", func() {
		// create "rename-me"; set spec.name "renamed"; reconcile -> reason "InvalidSpec", message contains `"rename-me"`; writes() unchanged
	})
	It("restores a name changed in the Authentik UI", func() {
		// create; fake.SetGroupField(pk, "name", "ui-name"); reconcile -> live name back to spec; a PATCH with body["name"]
	})
	It("refuses to adopt a group another resource already tracks", func() {
		// two Adopt resources for one seeded group; second -> reason "Conflict", message contains "already managed by default/<first>"
	})
	It("settles two holders: Created beats Adopted, then the lower UID", func() {
		// A created group pk P (Provenance Created); B: Adopt resource whose status is set (Status().Update) to {GroupPK: P, Name, Provenance: Adopted}
		// reconcile B -> reason "Conflict", message contains "default/A" and "takes precedence"; B's GroupPK and Provenance cleared; A unchanged
		// two Adopted holders of one pk (statuses set the same way): reconcile the one with the higher metadata.uid -> it releases; the lower keeps it
	})
	It("re-checks a fresh claim through the API reader when the cache missed the other holder", func() {
		// holder A already tracks the seeded group (status set); adopter B uses Client: staleCache{indexedClient{k8sClient}}
		// (a test wrapper whose List returns an empty list), APIReader: k8sClient
		// reconcile B -> its pre-check passes, Provenance Adopted is persisted, then the API-reader re-check yields:
		// final reason "Conflict", B's GroupPK cleared, and no PATCH reached Authentik (writes() empty)
	})
	It("rejects reserved attribute keys at admission", func() {
		// for each of "settings", "authentik_operator", "goauthentik.io/x": k8sClient.Create -> error containing "reserved"
	})
	It("reports an attribute whose value is an object as InvalidSpec", func() {
		// attributes {"nested": {"a": 1}} -> reason "InvalidSpec", message contains `"nested"`; writes() empty
	})
	It("removes every owned key with attributes: {} and sends an explicit empty object", func() { // Review Focus 1
		// SeedGroup("own", {"attributes": {"a": "1"}}); Adopt, attributes {"a": "1"}; reconcile -> ManagedAttributes ["a"]
		// set spec.attributes = map[string]apiextensionsv1.JSON{} (Update); re-Get: Spec.Attributes != nil && len == 0
		// reconcile -> last PATCH body["attributes"] == map[string]any{}; live attributes == {}; ManagedAttributes empty
	})
	It("releases owned keys without deleting them when spec.attributes is removed", func() {
		// ... then spec.attributes = nil -> no PATCH body has "attributes"; live keeps "a"; ManagedAttributes empty
	})
	It("reports an Authentik attribute rejection as InvalidSpec with the 400 message", func() {
		// fake.FailNextWith("PATCH", 400, {"attributes": ["value does not match schema"]})
		// reason "InvalidSpec", message contains "value does not match schema"; a Warning "InvalidSpec" event
	})
	It("sends no writes on a second reconcile", func() {})
	It("refuses every policy outside the identity selector and reads nothing", func() {
		// groups.IdentityNamespaceSelector = labels.Nothing(); each of the four policies -> reason "IdentityNotAllowed"; fake.Requests(false) empty
	})
	It("checks the identity gate before the adoption gate", func() {
		// both selectors labels.Nothing(), policy Adopt -> reason "IdentityNotAllowed"
	})
	It("applies the adoption gate when the identity gate passes", func() {
		// identity Everything, adoption Nothing, policy Adopt -> reason "AdoptionNotAllowed"
	})
	Context("deletion", func() {
		It("deletes a group it created when nothing binds it", func() { /* Delete CR; reconcile; fake.Group(pk) nil; CR gone */ })
		It("retains an adopted group by default", func() { /* group remains; CR gone; Normal "Retained" */ })
		It("blocks deletion while a policy binding uses the group, quoting used_by", func() {
			// create "immich-users"; fake.SeedApplication("immich", nil); fake.SeedBinding(appPK, {"group": pk, "order": float64(0)})
			// Delete CR; res, err := reconcile -> err nil, res.RequeueAfter == time.Minute
			// CR exists with finalizer; Ready False reason "DeletionBlocked"; message contains `"Binding from immich #0 to Group immich-users"` and "deletionPolicy: Retain"
			// group still in fake; a Warning "DeletionBlocked" event
			// fake.DeleteBinding(bindingPK); reconcile -> group gone, CR gone
		})
		It("retains without deleting when the token user is not a superuser", func() {
			// fake.SetTokenUserSuperuser(false); create; Delete CR; reconcile -> group remains; CR gone
			// Warning event reason "RetainedUnverifiable", note contains "delete it in Authentik after checking its bindings"
			// no request path contains "/used_by/"
		})
		It("never blocks Retain", func() { /* deletionPolicy Retain with a binding -> CR gone, no "/used_by/" request */ })
		It("keeps the resource terminating and deletes nothing when used_by fails", func() { // Review Focus 2
			// create; fake.FailNextOn("GET", "/used_by/", 503); Delete CR; reconcile -> err != nil
			// CR still has the finalizer; fake.Group(pk) != nil; Ready False reason "AuthentikError"
		})
		It("deletes only a Created group outside the identity selector", func() {
			// selector Nothing: Created group -> deleted; adopted group with deletionPolicy Delete -> retained, Warning "Retained" note contains "--default-connection-identity-namespace-selector"
		})
	})
})
```

- [ ] **Step 3: Run to see them fail**

Run: `dx go test ./internal/controller/ -count=1 -run 'TestClassifyFailure|TestSpecAttributes|TestMergeAttributes'` then `dx make manifests generate` then `envtest "AuthentikGroup"`
Expected: FAIL (undefined functions and fields).

- [ ] **Step 4: Implement the API types, `make manifests generate`, `go mod tidy`, and the controller**

Write the API types first, then:
Run: `dx make manifests generate && dx go mod tidy`
Expected: CRD regenerated; `go.mod` lists `k8s.io/apiextensions-apiserver v0.37.0` as a direct requirement.
Then implement `identity_status.go`, `attributes.go`, `group_desired.go`, `authentikgroup_controller.go` per the decisions above. `config/samples/v1alpha1_authentikgroup.yaml`: an access group `immich-users` with `name: immich-users`, commented `attributes`, `isSuperuser`, and the policy/dryRun/deletionPolicy comments in the style of the application sample.

- [ ] **Step 5: Run to see them pass**

Run: `dx go test ./internal/controller/ -count=1 -run 'TestClassifyFailure|TestSpecAttributes|TestMergeAttributes' && envtest "AuthentikGroup"`
Expected: PASS.

- [ ] **Step 6: Regenerate the chart, full suite, lint**

Run: `dx make chart-regen && dx task test && dx make lint`
Expected: exit 0; `git status` shows the regenerated CRD, deepcopy, RBAC and chart files.

- [ ] **Step 7: Commit**

```bash
git add api config dist go.mod go.sum internal/controller
git commit -m "feat(group): AuthentikGroup creates, adopts and deletes Authentik groups" \
  -m "Policy flow from internal/adopt; owner-proof lost-create recovery; per-key attributes; deletion blocked while a policy binding uses the group and retained when the token cannot see every binding." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 7: AuthentikGroup members
Kind: rewrite

**Files:**
- Create: `internal/controller/group_members.go`, `internal/controller/group_members_test.go`
- Modify: `api/v1alpha1/authentikgroup_types.go`, `internal/controller/authentikgroup_controller.go`, `internal/controller/authentikgroup_test.go`, `config/samples/v1alpha1_authentikgroup.yaml`
- Modify (regenerated): `config/crd/bases/authentik.io_authentikgroups.yaml`, `api/v1alpha1/zz_generated.deepcopy.go`, `dist/chart/templates/crd/authentikgroups.authentik.io.yaml`

**Interfaces:**
- Consumes: Task 3 (`FindUserByUsername`, `GetGroup`, `AddUserToGroup`, `RemoveUserFromGroup`, `Me`), Task 6 reconciler.
- Produces:
  - API: spec `Members *[]string` (`json:"members,omitempty"`, `+listType=set`, `MaxItems=1000`, `+kubebuilder:validation:items:MinLength=1`, `+kubebuilder:validation:items:MaxLength=150`; doc: "Authentik usernames. Absent: membership is never read or written. Present, including []: exactly these members (the operator token's own user is never removed)."); status `Members []GroupMemberStatus` (`+listType=map`, `+listMapKey=username`); `type GroupMemberStatus struct { Username string json:"username"; PK int32 json:"pk" }`.
  - `const ConditionMembersInSync = "MembersInSync"`; reasons `reasonMemberNotFound = "MemberNotFound"`, `reasonMemberIdentityChanged = "MemberIdentityChanged"`, `reasonMemberHeld = "MemberHeld"`.
  - `type memberInputs struct { Spec []string; Resolved map[string]int32; Live []api.PartialUser; Pins []authentikiov1alpha1.GroupMemberStatus; Held map[string]bool; TokenUserPK int32 }`; `type memberPlan struct { Add []api.PartialUser; Remove []api.PartialUser; NotFound []string; IdentityChanged []string; Held []string; Pins []authentikiov1alpha1.GroupMemberStatus; TokenUserKept bool }`; `func planMembers(in memberInputs) memberPlan`.

Rules (`planMembers`): `want` = listed usernames that resolved, are not held, and whose pin (if any) equals the resolved pk. `Add` = want not live. `keep` = want + token user + resolved pks of held and identity-changed usernames + pinned pks of identity-changed usernames; `Remove` = live not in keep. `NotFound` = listed, unresolved. `IdentityChanged` = listed, resolved, pinned to another pk. `Held` = listed and `Held[u]`. `Pins` = existing pins for usernames still listed, plus a new pin for each username in `want` without one; sorted by username. `TokenUserKept` = token user is live and not listed. All name lists sorted.

Controller (after the update step, not under ReadOnly): `members == nil` -> remove `MembersInSync`, `status.members = nil`, no member reads. Otherwise resolve each username (`FindUserByUsername`; NotFound -> unresolved; other errors returned), read `GetGroup(pk)` (the adoption target came from a lookup without members), `Me()` for the token pk, `planMembers` with `Held: nil` (Task 11 fills it), then every `AddUserToGroup` before any `RemoveUserFromGroup`, then `status.members = Pins`. Event and log when anything changed: Normal `MembersUpdated`, note `Updated members of group %q: added [%s], removed [%s]` (usernames, comma-separated). `MembersInSync`: True reason `InSync` message `members match spec.members` plus, when `TokenUserKept`, `; the operator's own user %q is a member and is never removed`; False with reason by priority `MemberIdentityChanged` > `MemberNotFound` > `MemberHeld` and a message listing every non-empty category: `not found: [%s]`, `identity changed (pk differs from status.members; remove the entry from status.members to accept the new account): [%s]`, `held while an AuthentikUser invitation is pending: [%s]` (quoted names, `; ` between categories). `Ready` is unaffected. Dry-run adoption: when members are present, the message gets `; members: would add [%s], would remove [%s]` inserted before `; nothing was written` (no writes); `announce` is recomputed against this FINAL message (the Ready condition's current message differs from it), not taken from `dryRunReport`, so an unchanged dry run emits its event once. ReadOnly: a member difference sets `Drifted=True`; its message ends with `members: would add [..], would remove [..]`.

- [ ] **Step 1: Write the failing unit tests `group_members_test.go`**

```go
func TestPlanMembers(t *testing.T) {
	// listed+resolved, not live -> Add; live, not listed -> Remove
	// unknown "ghost" -> NotFound ["ghost"], the other adds and every removal still planned
	// pin {carol, 10}, resolved carol=11, live has pk 10 -> IdentityChanged ["carol"]; 11 not added; 10 not removed
	// Held {"dave": true}, live dave -> neither added nor removed; Held ["dave"]; no pin for dave
	// token user (pk 1) live and not listed -> not in Remove; TokenUserKept true
	// Spec [] with live [1, 7] -> Remove [7] only
	// pins: new pin for bob; pin for "gone" (not listed) dropped; existing pin kept
}
```

- [ ] **Step 2: Write the failing envtest specs (append a `Context("members", ...)` to `authentikgroup_test.go`)**

```go
It("never reads or writes membership when members is absent", func() {
	// fake.AddGroupMember out of band; reconcile twice -> no request path contains "add_user" or "remove_user"; MembersInSync absent
})
It("removes everyone except the operator's own user for members: []", func() {
	// group with live members akadmin (1) and bob -> bob removed, 1 kept; MembersInSync True, message contains `"akadmin"`
})
It("writes per member through add_user and remove_user, never a users list", func() {
	// members [bob]; live [eve] -> POST .../add_user/ before POST .../remove_user/; no PATCH or POST to /core/groups/ has body["users"]
})
It("reports MemberNotFound naming unknown usernames and still applies the rest", func() {
	// members [bob, ghost] -> bob added; MembersInSync False reason "MemberNotFound" message contains `"ghost"`; Ready True
})
It("holds out a username whose pk changed until the pin is cleared", func() {
	// pin carol; fake.DeleteUser(carolOld); SeedUser("carol") new pk -> not added; reason "MemberIdentityChanged" message contains `"carol"`
	// clear status.members (Status().Update) -> next reconcile adds the new pk
})
It("lists in a dry run the usernames adoption would add and remove", func() {
	// Adopt dryRun, members [bob], live [eve] -> message contains `would add ["bob"]` and `would remove ["eve"]`; no writes
	// exactly one "AdoptionDryRun" event in the recorder; a second reconcile adds no further "AdoptionDryRun" event
})
It("emits an event naming added and removed usernames", func() {
	// event "MembersUpdated" containing "added [bob]" and "removed [eve]"
})
It("reports member drift under ReadOnly and writes nothing", func() {})
It("rejects duplicate usernames at admission", func() { /* members [bob, bob] -> Create error */ })
It("sends no writes on a second reconcile with members", func() {})
```

- [ ] **Step 3: Run to see them fail**

Run: `dx go test ./internal/controller/ -count=1 -run TestPlanMembers` then `dx make manifests generate` then `envtest "AuthentikGroup"`
Expected: FAIL (undefined `planMembers`, `Members` field).

- [ ] **Step 4: Implement** per Interfaces and rules; update the sample (`members: [alice, bob]` with a comment on absent vs `[]`).

- [ ] **Step 5: Run to see them pass**

Run: `dx go test ./internal/controller/ -count=1 -run TestPlanMembers && envtest "AuthentikGroup"`
Expected: PASS.

- [ ] **Step 6: Regenerate, full suite, lint**

Run: `dx make chart-regen && dx task test && dx make lint`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add api config dist internal/controller
git commit -m "feat(group): authoritative spec.members through add_user and remove_user" \
  -m "Absent members are never touched; unknown usernames, changed identities and the token user are reported in MembersInSync without flipping Ready." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 8: AuthentikApplication.spec.access
Kind: rewrite

**Files:**
- Create: `internal/controller/application_access.go`, `internal/controller/application_access_test.go`, `internal/controller/authentikapplication_access_test.go`, `internal/controller/samples_test.go`
- Modify: `api/v1alpha1/authentikapplication_types.go`, `internal/controller/authentikapplication_controller.go`, `internal/controller/authentikgroup_controller.go`, `internal/controller/claims_index_test.go`, `internal/controller/authentikapplication_controller_test.go`, `internal/controller/authentikapplication_adoption_test.go`, `internal/controller/adoption_gate_test.go`, `internal/controller/oauthprovider_secret_test.go`, `config/samples/v1alpha1_authentikapplication.yaml`, `config/samples/v1alpha1_authentikuser.yaml`, `test/e2e/helpers_test.go`
- Modify (regenerated): `config/crd/bases/authentik.io_authentikapplications.yaml`, `api/v1alpha1/zz_generated.deepcopy.go`, `config/rbac/role.yaml`, `dist/chart/templates/crd/authentikapplications.authentik.io.yaml`, `dist/chart/templates/rbac/manager-role.yaml`

**Interfaces:**
- Consumes: Task 3 (`ListBindings`, `CreateBinding`, `PatchBinding`, `DeleteBinding`, `FindGroupByName`, `ErrNoTarget`), Task 6 (`groupName`, group reconciler).
- Produces:
  - API: `type AccessType string` (`+kubebuilder:validation:Enum=Groups;Unmanaged`; `AccessTypeGroups`, `AccessTypeUnmanaged`); `type ApplicationAccess struct { Type AccessType json:"type" (+required, +unionDiscriminator); Groups []string json:"groups,omitempty" (+listType=set, MaxItems=64, items MinLength=1, items MaxLength=255) }` with `+kubebuilder:validation:XValidation:rule="self.type == 'Groups' ? (has(self.groups) && size(self.groups) > 0) : !has(self.groups)",message="groups is required when type is Groups and not allowed when type is Unmanaged"`; spec field `Access *ApplicationAccess json:"access,omitempty"` (doc: who may open the application; not the library section label `spec.group`); spec rules `+kubebuilder:validation:XValidation:rule="has(self.access) || (has(self.managementPolicy) && self.managementPolicy == 'ReadOnly')",message="spec.access is required unless managementPolicy is ReadOnly"` and `+kubebuilder:validation:XValidation:rule="!(has(self.policyEngineMode) && self.policyEngineMode == 'all' && has(self.access) && self.access.type == 'Groups' && size(self.access.groups) > 1)",message="policyEngineMode all with more than one access group would require membership of every group; use any"`; status `Access *ApplicationAccessStatus json:"access,omitempty"` with `Bindings []string json:"bindings,omitempty"` (every live binding described by `describeBinding`).
  - Conditions/reasons: `ConditionAccessInSync = "AccessInSync"`, `ConditionAccessOpen = "AccessOpen"`; `reasonGroupNotFound = "GroupNotFound"`, `reasonAccessNarrowed = "AccessNarrowed"`, `reasonWaitingForGroups = "WaitingForGroups"`, `reasonAccessPending = "AccessPending"`, `reasonNoBindings = "NoBindings"`, `reasonBound = "Bound"`.
  - `type accessGroups struct { pks map[string]string; missing []string }`; `type accessState struct { client *authentik.Client; groups accessGroups; created bool }`; `func describeBinding(b api.PolicyBinding) string` (`group <name>`, `user <username>`, `policy <name>`, suffixes ` (negated)`, ` (expiring)`, ` (disabled)` in that order); `type bindingPlan struct { Add []string; Enable []api.PolicyBinding; Remove []api.PolicyBinding; NextOrder int32 }`; `func planBindings(live []api.PolicyBinding, groups accessGroups, mode api.PolicyEngineMode) bindingPlan` (`mode` = the application's live `policy_engine_mode`).
  - Indexes: `const indexAccessGroup = ".spec.access.groups"` (`accessGroupIndex`, registered by the application reconciler), `const indexGroupName = ".spec.name"` (`groupNameIndex` = `groupName(g)`, registered by the group reconciler). Map functions `func (r *AuthentikApplicationReconciler) applicationsForGroup(ctx context.Context, o client.Object) []reconcile.Request` (all namespaces), `func (r *AuthentikGroupReconciler) groupsForApplication(ctx context.Context, o client.Object) []reconcile.Request` (all namespaces; one lookup per listed name).

Rules (`planBindings`): a binding is managed when it has a group, `negate` false and `expiring` false. For each listed group that resolved: if no managed binding exists -> `Add` (orders `NextOrder`, `NextOrder+1`, ... with `NextOrder` = highest live order + 1, 0 when none); if several -> keep the one with the lowest order (ties: lowest pk), `Remove` the rest; the kept one with `enabled` false -> `Enable`. Managed bindings for unlisted groups -> `Remove`. Negated group bindings depend on `mode`: under `any` a negated binding on group G passes for every user **outside** G (it widens access), so every negated group binding, listed or not, goes to `Remove`; under `all` it excludes G, so negated bindings are never in any list. When `groups.missing` is non-empty, `Remove` is empty. Expiring, user and policy bindings are never in any list.

Controller changes (only these; the provider link and lost-create flows stay unchanged):
1. `syncApplication` returns named results `(got *api.Application, access accessState, err error)`; `accessState` also carries `created bool`, true when this reconcile created or recreated the application. Right after `resolve`, when `Access.Type == Groups` and the policy is not ReadOnly, resolve each listed name with `FindGroupByName` (NotFound -> missing; other errors returned).
2. WaitingForGroups guards every write that could leave an application open, at the two write entry points: `createApplication` (every create, including the recreate of a vanished `Created` application) and `adoptApplication` before it records `Adopted` (not in its dry-run branch). When `Access.Type == Groups` and no listed group resolved, each returns `condErrf(reasonWaitingForGroups, "none of the groups in spec.access.groups exist in Authentik yet (%s); the application is not created or taken over until one does, so it is never open to every user", quotedNames)` before persisting `pendingCreate` or provenance. Both take `want desiredApp` (`createApplication` and `adoptApplication` previously took `*api.ApplicationRequest`; parameter counts stay at 5). `desiredApp` gains `access *accessState`.
3. `adoptApplication`'s dry-run branch appends, before `; nothing was written`, `; access: would bind groups [%s]; would remove group bindings [%s]; also grants access: %s` (`%s` = the described unmanaged bindings joined `, `, or `none` when there are none), or `; access: groups not found [%s], nothing would be removed`; it computes `announce` against this final message (not `dryRunReport`'s).
4. `sync`, after the existing status and Ready handling, calls `r.syncAccess(ctx, a, got, access) (ctrl.Result, error)` and returns its result when non-empty or an error. `syncAccess`, in this order:
   a. `ListBindings(got.Pk)`. On error (`ErrNoTarget` included): if `access.created` -> `Ready=False` reason `AccessPending`; else `AccessInSync=False` reason `AuthentikError`; return the error (backoff).
   b. `Unmanaged` or no access: no writes. ReadOnly with Groups: `AccessInSync=False` reason `DriftDetected` when the plan is non-empty, no writes. Groups otherwise: `planBindings(live, groups, mode)` then apply `Add` (`CreateBinding` group, target, order, enabled true, negate false), then `Enable` (`PatchBinding` enabled true), then `Remove` (`DeleteBinding(b, got.Pk)`), stopping at the first failed write; Normal event `AccessUpdated` naming bound and unbound group names when any write happened.
   c. When any write was attempted, re-list with `ListBindings(got.Pk)` (an error here is handled as in a.). `status.access.bindings` (described, sorted) and `AccessOpen` are computed from this final list, never from the pre-write one: `AccessOpen` True reason `NoBindings` message `the application has no enabled policy binding, so every user may open it unless the instance setting "Applications with no policies bound can be accessed by any user" is off` when no binding is enabled (nil `enabled` counts as enabled), else False reason `Bound`.
   d. AccessPending (Groups, not ReadOnly): when the application is newly created or recreated (`access.created`), or its provenance is `Created`, at least one listed group resolved, and the final list holds no managed binding of a listed group -> `Ready=False` reason `AccessPending` (`waiting for the first binding of spec.access.groups; until it exists every user may open the application unless the instance setting is off`); with a failed write also a Warning event `AccessBindingFailed` and the error is returned (backoff); without one, requeue after `userErrorRecheck`.
   e. Otherwise a failed write -> `AccessInSync=False` reason `AuthentikError`, return the error. After success `AccessInSync`: False `GroupNotFound` naming the missing names; else False `AccessNarrowed` when the live `policy_engine_mode` is `all` and more than one group is listed; else True `InSync`; `Unmanaged` or no access removes `AccessInSync`.
5. Watches: the application reconciler registers `indexAccessGroup` and `Watches(&AuthentikGroup{}, applicationsForGroup)`; the group reconciler registers `indexGroupName` and `Watches(&AuthentikApplication{}, groupsForApplication)`. RBAC markers: application reconciler `authentikgroups` get;list;watch; group reconciler `authentikapplications` get;list;watch.
6. Existing tests: every `AuthentikApplicationSpec{...}` literal in the four listed controller test files and `test/e2e/helpers_test.go` `newApplication` gains `Access: &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged}` (no assertion changes). `testIndexes` gains `indexAccessGroup` and `indexGroupName`.
7. Samples: `v1alpha1_authentikapplication.yaml` gets `access: {type: Groups, groups: [immich-users]}` with a comment (Unmanaged alternative; turn off the instance setting); `v1alpha1_authentikuser.yaml` gets `spec: {}` (stub, replaced in Task 10).

- [ ] **Step 1: Write the failing unit tests `application_access_test.go`**

```go
func TestPlanBindings(t *testing.T) {
	// listed "a" resolved, no binding, live max order 3 -> Add ["a"], NextOrder 4; two adds -> orders 4 and 5
	// managed binding for unlisted "old" -> Remove it
	// two managed bindings for "a" (orders 1 and 2) -> keep order 1, Remove order 2
	// managed "a" with enabled false -> Enable it
	// mode any: negated group bindings (listed or unlisted) -> Remove
	// mode all: negated group bindings -> in no list
	// expiring, user and policy bindings -> in no list (both modes)
	// groups.missing ["ghost"] with an unlisted "old" binding -> Remove empty
}
func TestDescribeBinding(t *testing.T) {
	// group "a" -> "group a"; negated -> "group a (negated)"; user bob -> "user bob"; policy p disabled -> "policy p (disabled)"
}
```

- [ ] **Step 2: Write the failing envtest suites**

`samples_test.go`:
```go
var _ = Describe("Samples", func() {
	It("accepts every manifest in config/samples", func() { // Review Focus 3
		// for every *.yaml in ../../config/samples except kustomization.yaml: decode into unstructured,
		// set namespace "default", k8sClient.Create(ctx, obj, client.DryRunAll) -> Succeed (failure message names the file)
	})
})
```
If a sample other than the application, group and user sample is rejected, fix that sample to satisfy its CRD and name it in the report.

`authentikapplication_access_test.go` (setup and helpers as `authentikapplication_adoption_test.go`, plus `writes := func() []fakeauthentik.Request { return fake.Requests(true) }`; groups seeded with `fake.SeedGroup`; every application literal sets `Access`):
```go
It("rejects an application without spec.access unless ReadOnly", func() { /* error contains "spec.access is required"; ReadOnly without access is accepted */ })
It("rejects type Groups without groups and Unmanaged with groups", func() {})
It("rejects policyEngineMode all with more than one group", func() {})
It("binds the listed groups right after creating the application", func() {
	// SeedGroup("immich-users"); Groups [immich-users]; reconcile
	// fake.Bindings(appPK) has one binding: group == group pk, enabled true, negate false, order 0
	// Ready True; AccessInSync True; AccessOpen False reason "Bound"
})
It("does not create the application until a listed group exists", func() {
	// no group -> Ready False reason "WaitingForGroups"; fake.ApplicationCount() == 0
	// SeedGroup; reconcile -> created and bound
})
It("does not adopt-write an existing application until a listed group exists", func() {
	// SeedApplication("legacy"); Adopt, Groups [missing] -> "WaitingForGroups"; Provenance ""; writes() empty
})
It("does not recreate a vanished Created application while no listed group exists", func() {
	// create and bind (group "immich-users"); fake.DeleteApplication(slug) (cascades its bindings); fake.DeleteGroup(groupPK)
	// reconcile -> Ready False "WaitingForGroups"; fake.ApplicationCount() == 0; status.pendingCreate == ""
	// SeedGroup("immich-users"); reconcile -> recreated and bound in the same reconcile
})
It("adds missing bindings before removing unlisted and duplicate ones, one past the highest order", func() {
	// adopted app with bindings: "old" (order 3), "immich-users" (orders 1, 2); spec [immich-users, new]
	// write order: POST binding "new" with order 4 precedes every DELETE; "old" and the order-2 duplicate deleted; order-1 kept
})
It("removes nothing while a listed group does not resolve", func() {
	// spec [immich-users, ghost]; binding "old" survives; AccessInSync False "GroupNotFound" contains `"ghost"`; Ready True
})
It("re-enables a disabled binding of a listed group", func() { /* PATCH body["enabled"] == true */ })
It("removes negated group bindings under policyEngineMode any", func() {
	// live mode any; bindings: "group immich-users" (managed), "group x (negated)"
	// after sync the negated binding is deleted; status.access.bindings == ["group immich-users"]
})
It("leaves negated bindings under mode all, and expiring, user and policy bindings always, and lists them", func() {
	// live mode all: status.access.bindings contains "group x (negated)", "group y (expiring)", "user bob", "policy p"; no write touches them
})
It("reports a live policyEngineMode all with several groups as AccessNarrowed and leaves it", func() {})
It("never writes bindings under Unmanaged and reports AccessOpen when there are none", func() {
	// AccessOpen True reason "NoBindings", message contains "unless the instance setting"; AccessInSync absent; no binding writes
})
It("lists bindings in the adoption dry run", func() {
	// message contains `would bind groups ["new"]`, `would remove group bindings ["old"]`, `also grants access: user bob`
	// no writes; a second reconcile emits no new AdoptionDryRun event
	// an app with only group bindings: message contains `also grants access: none`
})
It("lists bindings by the application pk, never by pbm_uuid", func() {
	// bound app; fake.SetApplicationField(slug, "pbm_uuid", "") (what the 2026.2.3 compat shim yields); reconcile
	// every request to "/policies/bindings/" has a Query containing "target="+appPK; AccessInSync True
	// (the empty-target refusal itself is TestListBindingsRefusesEmptyTarget, Task 3)
})
It("reports AccessPending and backs off when the first binding write fails on a new application", func() {
	// fake.FailNextOn("POST", "/policies/bindings/", 500); reconcile -> err != nil; Ready False "AccessPending"; Warning "AccessBindingFailed"
	// reconcile -> bound; Ready True
})
It("reports AccessPending when listing bindings fails right after the create", func() {
	// fake.FailNextOn("GET", "/policies/bindings/", 500); reconcile of a new Groups app -> err != nil; the app exists;
	// Ready False "AccessPending"; reconcile -> bound; Ready True; AccessInSync True
})
It("computes AccessOpen and status.access.bindings from the bindings after the writes", func() {
	// new Groups app: after the first reconcile AccessOpen False "Bound" and status.access.bindings == ["group immich-users"]
	// (the pre-write list was empty, so a value computed before the writes would say NoBindings)
})
It("re-enqueues applications listing a group, and groups listed by an application", func() {
	// applicationsForGroup(group "immich-users") -> the app's request; groupsForApplication(app) -> the group's request
})
It("reports access drift under ReadOnly and writes nothing", func() {})
It("sends no writes on a second reconcile", func() {})
```

- [ ] **Step 3: Run to see them fail**

Run: `dx go test ./internal/controller/ -count=1 -run 'TestPlanBindings|TestDescribeBinding'` then `dx make manifests generate` then `envtest "AuthentikApplication|Samples"`
Expected: FAIL (undefined symbols; existing app specs fail admission until step 5 of the rules is applied).

- [ ] **Step 4: Implement** the API, `application_access.go`, the controller changes 1-5, the test-literal updates (rule 6) and the samples (rule 7).

- [ ] **Step 5: Run to see them pass**

Run: `dx go test ./internal/controller/ -count=1 -run 'TestPlanBindings|TestDescribeBinding' && envtest "AuthentikApplication|Samples|AuthentikGroup"`
Expected: PASS, including every pre-existing application spec unchanged in its assertions.

- [ ] **Step 6: Regenerate, full suite, lint, e2e compiles**

Run: `dx make chart-regen && dx task test && dx make lint && dx go vet -tags e2e ./test/...`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add api config dist internal/controller test/e2e/helpers_test.go
git commit -m "feat(application)!: required spec.access owns the application's group bindings" \
  -m "Groups binds exactly the listed groups (add before remove, nothing removed from an incomplete picture, never through zero bindings); Unmanaged writes none. AccessInSync and AccessOpen report the result." \
  -m "BREAKING CHANGE: AuthentikApplication.spec.access is required unless managementPolicy is ReadOnly." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 9: Converter maps JeffResc groupBindings to spec.access
Kind: rewrite

**Files:**
- Modify: `internal/migrate/jeffresc/build.go`, `internal/migrate/jeffresc/convert.go`, `internal/migrate/jeffresc/convert_test.go`, `internal/migrate/jeffresc/golden_test.go`, `hack/convert-jeffresc/main.go`, `hack/convert-jeffresc/main_test.go`, `docs/migration-from-jeffresc.md`
- Modify (regenerated by `go test -run TestGolden -update`): every file under `internal/migrate/jeffresc/testdata/<case>/golden/`
- Delete: `internal/migrate/jeffresc/testdata/err-group-bindings/` (now converts)
- Rename: `internal/migrate/jeffresc/testdata/group-bindings-accepted/` -> `internal/migrate/jeffresc/testdata/group-bindings/`

**Interfaces:**
- Consumes: Task 8 API (`ApplicationAccess`, `AccessTypeGroups`, `AccessTypeUnmanaged`).
- Produces: `Options.AcceptUnmanagedGroupBindings` and the `--accept-unmanaged-group-bindings` flag removed; `applicationObject` sets `Spec.Access`; the header comment for Unmanaged is exactly `# spec.access is Unmanaged: this application's live bindings are unknown offline; the adoption dry run lists them.`

Rules: non-empty `groupBindings` -> `access: {type: Groups, groups: [...]}` in input order with duplicates dropped (each drop reported as a note `groupBindings lists %q more than once; kept once`); empty or absent -> `access: {type: Unmanaged}` plus the header line. `bindingsNotice`, the `validateSource` groupBindings error and the "accepted with" note are removed.

- [ ] **Step 1: Write the failing tests**

`convert_test.go` (replace the accept-flag tests that cover the removed behaviour):
```go
func TestConvertGroupBindingsToAccess(t *testing.T) {
	// groupBindings [media-users, media-admins, media-users] -> app Spec.Access == {Groups, [media-users, media-admins]}
	// no "NOT managed" text in the file; the report has the "more than once" note
}
func TestConvertWithoutGroupBindingsIsUnmanaged(t *testing.T) {
	// no groupBindings -> Spec.Access == {Unmanaged}; the file contains the exact Unmanaged header line
}
```
`hack/convert-jeffresc/main_test.go`: the groupBindings input converts with exit 0 and no flag; passing `--accept-unmanaged-group-bindings` exits 2 with stderr containing `flag provided but not defined`. `golden_test.go`: drop the `err-group-bindings` case and `acceptFlag`; rename the accepted case to `{name: "group-bindings", opts: dry}`.

- [ ] **Step 2: Run to see them fail**

Run: `dx go test ./internal/migrate/jeffresc/ ./hack/convert-jeffresc/ -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**, rename and delete the fixtures, then regenerate the goldens

Run: `dx go test ./internal/migrate/jeffresc/ -run TestGolden -update -count=1`
Expected: PASS; `git diff --stat` shows every application golden gaining `access`, and `group-bindings/golden/authentikapplication-immich.yaml` with `type: Groups` and both groups. Read the diff: any change other than `access` and the removed notices is a defect to fix before continuing.

- [ ] **Step 4: Update `docs/migration-from-jeffresc.md`**: replace "groupBindings fail open after migration" with how `groupBindings` map to `spec.access` (and Unmanaged when absent, with the dry-run listing), remove the flag row and the flag from the refusals paragraph, and update the field-map row `spec.groupBindings` -> `AuthentikApplication.spec.access.groups`.

- [ ] **Step 5: Run to see them pass**

Run: `dx go test ./internal/migrate/jeffresc/ ./hack/convert-jeffresc/ -count=1`
Expected: PASS.

- [ ] **Step 6: Full suite and lint**

Run: `dx task test && dx make lint`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add -A internal/migrate/jeffresc hack/convert-jeffresc docs/migration-from-jeffresc.md
git commit -m "feat(migrate)!: convert JeffResc groupBindings to spec.access" \
  -m "groupBindings become access type Groups; objects without them become Unmanaged, whose live bindings the adoption dry run lists." \
  -m "BREAKING CHANGE: convert-jeffresc no longer accepts --accept-unmanaged-group-bindings." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 10: AuthentikUser API, lifecycle and invitation happy path
Kind: rewrite

**Files:**
- Create: `internal/controller/user_desired.go`, `internal/controller/user_invitation.go`, `internal/controller/user_invitation_test.go`, `internal/controller/authentikuser_test.go`
- Modify: `api/v1alpha1/authentikuser_types.go`, `internal/controller/authentikuser_controller.go`, `internal/controller/authentikuser_controller_test.go` (delete the scaffold spec: `git rm`), `internal/connection/resolver.go`, `internal/connection/resolver_test.go`, `internal/controller/claims_index_test.go`, `config/samples/v1alpha1_authentikuser.yaml`, `dist/chart/README.md`, `dist/chart/values.yaml` (comment only)
- Modify (regenerated): `config/crd/bases/authentik.io_authentikusers.yaml`, `api/v1alpha1/zz_generated.deepcopy.go`, `config/rbac/role.yaml`, `dist/chart/templates/crd/authentikusers.authentik.io.yaml`, `dist/chart/templates/rbac/manager-role.yaml`

**Interfaces:**
- Consumes: Tasks 2, 3, 5, 6 (`classifyFailure`, `specAttributes`, `mergeAttributes`), existing `FlowBySlug`, `secretWriteError`, `reasonSecretConflict`, `reasonReferenceNotFound`, `labelManagedBy`, `labelManagedByValue`.
- Produces:
  - `connection.Resolved` gains `ExternalURLSet bool` (true when `DefaultExternalURL` is non-empty for the default connection, or `spec.connection.externalURL` is non-empty for a deployment).
  - API: `type UserType string` (`Enum=internal;external`); `type InvitationSpec struct { Flow string json:"flow" (+required, Pattern ^[-a-zA-Z0-9_]+$, MaxLength=50); ExpiresAfter *metav1.Duration json:"expiresAfter,omitempty" (+kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="expiresAfter must be positive"); SecretName string json:"secretName,omitempty" (MaxLength=253, DNS-subdomain pattern ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$); Attempt int32 json:"attempt,omitempty" (Minimum=1) }`; `AuthentikUserSpec{ APIManagementSpec; AuthentikRef *AuthentikRef; Username string json:"username" (+required, MinLength=1, MaxLength=150, +kubebuilder:validation:XValidation:rule="self == oldSelf",message="username is immutable"); Name string (MaxLength=255); Email string (MaxLength=254); Path string (MaxLength=255); Type UserType; FirstName string json:"firstName,omitempty" (MaxLength=255); LastName string json:"lastName,omitempty" (MaxLength=255); IsActive *bool json:"isActive,omitempty"; Attributes map[string]apiextensionsv1.JSON json:"attributes,omitzero" (MaxProperties=64, the group rule plus && k != 'given_name' && k != 'family_name', message also naming given_name and family_name); Invitation *InvitationSpec json:"invitation,omitempty" }` with spec rules authentikRef immutable (as groups) and `+kubebuilder:validation:XValidation:rule="(has(self.managementPolicy) && self.managementPolicy in ['Adopt', 'ReadOnly']) ? !has(self.invitation) : has(self.invitation)",message="invitation is required when managementPolicy may create (Create, AdoptOrCreate) and not allowed under Adopt and ReadOnly"`; `type InvitationStatus struct { Name string json:"name"; Attempt int32 json:"attempt"; Expires metav1.Time json:"expires"; Pending bool json:"pending,omitempty"; SecretName string json:"secretName" }`; `AuthentikUserStatus{ Conditions; UserPK int32 json:"userPk,omitempty"; Provenance; ManagedAttributes []string; Invitation *InvitationStatus json:"invitation,omitempty"; ObservedGeneration int64 }`; print columns `Username` (`.spec.username`), `Ready`, `Provenance`, `Reason` (priority 1), `Age`.
  - `const indexUserPK = ".status.userPk"`, `func userPKIndex(o client.Object) []string`.
  - `const defaultInvitationExpiry = 7 * 24 * time.Hour`; `func invitationName(uid types.UID, attempt int32) string` (`k8s-<uid>-<attempt>`); `func invitationAttempt(u *authentikiov1alpha1.AuthentikUser) int32` (spec attempt, minimum 1); `func invitationSecretName(u *authentikiov1alpha1.AuthentikUser) string` (`spec.invitation.secretName`, else `<metadata.name>-invitation`); `func invitationLink(externalURL, flowSlug, pk string) string` (`strings.TrimRight(externalURL, "/") + "/if/flow/" + flowSlug + "/?itoken=" + url.QueryEscape(pk)`).
  - Reasons: `reasonExternalURLRequired = "ExternalURLRequired"`, `reasonInvitationPending = "InvitationPending"`, `reasonInvitationConsumed = "InvitationConsumed"`, `reasonInvitationExpired = "InvitationExpired"`.

User reconcile behaviour:
1. Same skeleton as groups (finalizer, persist, `failed` via `classifyFailure`); gates identity then adoption; `specAttributes`; `resolve`.
2. Facts: `userPk != 0` -> `GetUser` (found with `Username != spec.username` -> `Conflict` `the account pk %d is now named %q, not %q; refusing to manage it`; 404 -> Gone); else target `FindUserByUsername(spec.username)`; a found target of type `service_account` or `internal_service_account` -> `Conflict` (`... is a service account; AuthentikUser manages people only`). `OwnCreatePending = status.invitation != nil`; `OwnCreateProven` = pending and `FindUsersByOwnerUID(uid)` returns exactly one account whose username equals `spec.username`; `RecreateVanished = false`.
3. Actions: `UseTracked` -> update or drift; `TrackedGone` -> `AdoptionTargetNotFound` (`... was deleted in Authentik; it is not re-invited. To invite again, clear status.userPk and status.provenance and increase spec.invitation.attempt`), `userPk` kept; `Adopt`/`Observe`/`DryRunAdopt`/`Conflict`/`TargetNotFound`/`Ambiguous` as groups (claims on `indexUserPK`); `DryRunCreate` -> `AdoptionDryRun` `dry run: no user named %q exists; an invitation would be issued; nothing was written`; `RecoverOwnCreate` -> recognise; `Create` -> invite.
4. Update (not ReadOnly), PATCH only fields the spec sets: `name`, `email`, `path`, `type`, `is_active` (unset keeps live, every provenance); attributes are computed in two independent parts on the live dict: (a) `spec.attributes`: when non-nil, `mergeAttributes` as for groups, owning `status.managedAttributes`; when nil, no key is set or removed by this part and `status.managedAttributes = nil`; (b) `firstName` / `lastName`: when set, the live keys `given_name` / `family_name` are set to them, whether or not `spec.attributes` is nil; when unset (including after being cleared), the live value is left as it is. These two keys are never in `status.managedAttributes` and never removed by the operator (CEL reserves them in `spec.attributes`, so each key has one writer). When either part changes a key, the PATCH carries the whole resulting dict (every live key kept, since Authentik replaces `attributes` whole); otherwise no `attributes` in the PATCH. One PATCH when anything differs; `isActive: false` on the token user's pk (`Me()`) -> `InvalidSpec` `refusing to deactivate the operator's own user %q`, nothing written.
5. invite (Create): connection without `ExternalURLSet` -> `ExternalURLRequired` (`invitations need a browser-facing URL: set spec.connection.externalURL on the AuthentikDeployment, or --authentik-external-url (Helm value authentik.externalUrl) for the default connection`), nothing created. No `status.invitation`: `FlowBySlug(spec.invitation.flow)` (NotFound -> `reasonReferenceNotFound`); set `status.invitation = {Name: invitationName(uid, attempt), Attempt: attempt, Expires: now + expiresAfter (default 7 days), Pending: true, SecretName: invitationSecretName}`; persist; `CreateInvitation{Name, SingleUse: true, Flow: flow pk, Expires, FixedData: {"username": spec.username, "name": spec.name (only when set), "attributes": OwnerProof(uid)}}`; a 4xx clears `status.invitation`; success -> `Pending = false`, write the Secret, Normal event `InvitationIssued` (`Issued invitation %q; the link is in Secret %q`). With `status.invitation` present and not pending: `FindInvitationByName(status.invitation.name)`: found and `now < expires` -> ensure the Secret (rewrite when missing or different) -> `InvitationPending` (`invitation %q expires %s; the sign-up link is in Secret %q, key url`); found and expired -> delete it in Authentik, then as not found; not found -> delete the Secret, then `InvitationExpired` when `now >= status.invitation.expires` else `InvitationConsumed` (`the invitation is gone before an account carrying this resource's ownership proof appeared (opened, deleted or abandoned); no new link is issued automatically: increase spec.invitation.attempt`). `status.invitation` stays (the search keeps running). Ready False for all of these, requeue `userErrorRecheck`.
6. Secret: name `status.invitation.secretName`, type Opaque, label `labelManagedBy: labelManagedByValue`, controller owner reference to the user, keys `url` (`invitationLink(resolved.ExternalURL, invitation.FlowObj.Slug, invitation.Pk)`) and `expires` (RFC 3339 UTC); read with `APIReader`; an existing Secret not controlled by this user -> `reasonSecretConflict`; write errors through `secretWriteError`; deletes with UID and resourceVersion preconditions.
7. recognise (`RecoverOwnCreate`): `provenance = Created`, `userPk` = the account, delete the live invitation if `FindInvitationByName` finds it, delete the Secret, `status.invitation = nil`, Normal event `Enrolled` (`Recognised account %q enrolled from the invitation`), then the update step.
8. `finalize`: `adopt.Finalize{UnsetDefault: DeletionPolicyRetain}`; Retain notes say `AuthentikUser retains accounts unless deletionPolicy is Delete; set isActive: false first to offboard`; delete path `DeleteUser(userPk)`, except the token user's pk (Warning `NotDeleted`, release).
9. `SetupWithManager`: register `indexUserPK`; `MaxConcurrentReconciles: 2`; `Named("authentikuser")`. RBAC markers: secrets get;create;update;delete, authentikdeployments get, namespaces get, events create;patch.

- [ ] **Step 1: Write the failing tests**

`internal/connection/resolver_test.go`: `TestResolvedExternalURLSet` -- default connection with `DefaultExternalURL ""` -> `ExternalURLSet` false and `ExternalURL` equals the API base; with `"https://sso.example.com/"` -> true, `"https://sso.example.com"`; a deployment with and without `spec.connection.externalURL` likewise.

`user_invitation_test.go`:
```go
func TestInvitationLink(t *testing.T) { // Review Focus 4
	// invitationLink("https://sso.example.com/auth/", "enrol", "p-1") == "https://sso.example.com/auth/if/flow/enrol/?itoken=p-1"
	// invitationLink("https://sso.example.com", "enrol", "a b") == "https://sso.example.com/if/flow/enrol/?itoken=a+b"
}
func TestInvitationNaming(t *testing.T) {
	// invitationName("0b1c-uid", 2) == "k8s-0b1c-uid-2"; invitationAttempt with spec 0 -> 1, 3 -> 3
	// invitationSecretName: unset -> "dave-invitation"; set "x" -> "x"
}
```

`authentikuser_test.go` (setup as groups; connection `DefaultExternalURL: "https://sso.example.com"`; `invite := &InvitationSpec{Flow: fakeauthentik.FlowEnrollmentSlug}`):
```go
It("rejects an invitation under Adopt and its absence under Create at admission", func() {})
It("rejects a zero or negative expiresAfter at admission", func() { // Review Focus 5
	// expiresAfter "0s" and "-1h" -> Create error containing "expiresAfter must be positive"
})
It("rejects a changed username and reserved attribute keys (given_name, family_name, settings) at admission", func() {})
It("adopts an existing person by username and patches only what the spec sets", func() {
	// SeedUser("alice", {"name": "Alice", "email": "a@x", "attributes": {"foreign": "f"}}); Adopt, firstName "Alice", attributes {"team": "ops"}
	// PATCH body has "attributes" == {"foreign": "f", "given_name": "Alice", "team": "ops"} and no "name", "email", "groups" or "roles"
	// Provenance Adopted; ManagedAttributes ["team"]
})
It("writes firstName to given_name even when spec.attributes is absent", func() {
	// SeedUser("carl", {"attributes": {"foreign": "f"}}); Adopt, firstName "Carl", no attributes
	// PATCH body["attributes"] == {"foreign": "f", "given_name": "Carl"}; ManagedAttributes empty
})
It("leaves the live given_name when firstName is cleared", func() {
	// ... then firstName "" (Update); reconcile -> no PATCH carries "attributes"; live given_name still "Carl"
})
It("refuses to adopt a service account", func() { /* SeedUser("svc", {"type": "service_account"}) -> reason "Conflict", message contains "people only" */ })
It("refuses every policy outside the identity selector and reads nothing", func() {
	// users.IdentityNamespaceSelector = labels.Nothing(); Create (with invitation), Adopt, AdoptOrCreate, ReadOnly -> reason "IdentityNotAllowed"; fake.Requests(false) empty
})
It("applies the adoption gate after the identity gate", func() {
	// identity Everything, adoption Nothing: Adopt -> "AdoptionNotAllowed"; both Nothing -> "IdentityNotAllowed"
})
It("reports in a dry run what adoption would change and writes nothing", func() {
	// SeedUser("erin", {"name": "Old"}); Adopt, dryRun, name "New" -> reason "AdoptionDryRun", message contains `name: "Old" -> "New"`; writes() empty; Provenance ""
})
It("refuses to adopt a person another resource already tracks", func() {
	// two Adopt resources with username "frank" -> the second: reason "Conflict", message contains "already managed by default/<first>"
})
It("settles two holders: Created beats Adopted, then the lower UID", func() {
	// as the group spec: statuses set to the same userPk; Adopted loses to Created; between two Adopted the higher UID releases
})
It("re-checks a fresh claim through the API reader when the cache missed the other holder", func() {
	// adopter with Client: staleCache{indexedClient{k8sClient}}, APIReader: k8sClient -> final reason "Conflict", UserPK cleared, writes() empty
})
It("deletes only a Created user outside the identity selector", func() {
	// selector Nothing: Created user with deletionPolicy Delete -> DELETE sent; adopted user with deletionPolicy Delete -> retained,
	// Warning "Retained" note contains "--default-connection-identity-namespace-selector"
})
It("offboards with isActive false and never deactivates the token user", func() {
	// adopted bob, isActive false -> live is_active false
	// a user resource for "akadmin" with isActive false -> reason "InvalidSpec"; live akadmin still active
})
It("observes under ReadOnly and reports drift", func() {})
It("reports ExternalURLRequired and creates no invitation without an explicit external URL", func() {
	// connection without DefaultExternalURL; Create with invitation -> reason "ExternalURLRequired"; fake.Invitations() empty
})
It("issues a single-use invitation pinning the username and writes the link only to the Secret", func() {
	// Create "dave" (name "Dave") -> fake invitation "k8s-<uid>-1": single_use true, flow FlowEnrollmentPK,
	//   fixed_data == {"username": "dave", "name": "Dave", "attributes": {"authentik_operator": {"owner_uid": uid}}}
	// Secret "dave-invitation": data["url"] == "https://sso.example.com/if/flow/default-enrollment-flow/?itoken=" + pk, data["expires"] parses as RFC 3339
	//   about 7 days ahead; controlled by the user resource
	// status.invitation {Name "k8s-<uid>-1", Attempt 1, Pending false, SecretName "dave-invitation"}; Ready False "InvitationPending"
	// no status field, condition message or event contains pk
})
It("refuses to overwrite a Secret it does not own", func() { /* pre-create "dave-invitation" -> reason "SecretConflict" */ })
It("recognises the enrolled account as Created and deletes the Secret", func() {
	// issue; fake.EnrolFromInvitation(name, "") -> reconcile: Provenance Created, UserPK == new pk, status.invitation nil,
	// Secret gone, event "Enrolled"; Ready True
})
It("passes through InvitationConsumed during a normal enrolment", func() {
	// issue; fake.ConsumeInvitation(name); reconcile -> "InvitationConsumed", Secret deleted, status.invitation kept
	// fake.SeedUser("dave", {"attributes": OwnerProof(uid)}); reconcile -> Created
})
It("reports InvitationExpired from status once past expires", func() {
	// issue; set status.invitation.expires 1h in the past (Status().Update) -> "InvitationExpired"; live invitation deleted; Secret deleted
})
It("never re-invites a Created user that vanished", func() { /* enrol; fake.DeleteUser -> "AdoptionTargetNotFound"; no new invitation */ })
It("retains a Created user on deletion by default and deletes it with an explicit Delete", func() {})
It("sends no writes on a second reconcile", func() {})
```
Also rerun the samples spec after replacing the sample.

- [ ] **Step 2: Run to see them fail**

Run: `dx go test ./internal/connection/ ./internal/controller/ -count=1 -run 'TestResolvedExternalURLSet|TestInvitationLink|TestInvitationNaming'` then `dx make manifests generate` then `envtest "AuthentikUser|Samples"`
Expected: FAIL.

- [ ] **Step 3: Implement** the resolver flag, the API, `user_desired.go`, `user_invitation.go`, the controller; sample `v1alpha1_authentikuser.yaml`: a person `dave` with `invitation: {flow: passkey-enrollment, expiresAfter: 168h}` and comments on `attempt`, `secretName`, `isActive: false` to offboard, and that the enrolment flow's username prompt must be `text_read_only`; chart `README.md` and the `values.yaml` comment on `authentik.externalUrl`: required for `AuthentikUser` invitations (`ExternalURLRequired` otherwise).

- [ ] **Step 4: Run to see them pass**

Run: `dx go test ./internal/connection/ ./internal/controller/ -count=1 -run 'TestResolvedExternalURLSet|TestInvitationLink|TestInvitationNaming' && envtest "AuthentikUser|Samples"`
Expected: PASS.

- [ ] **Step 5: Regenerate, full suite, chart, lint**

Run: `dx make chart-regen && dx task test && dx task chart:ci && dx make lint`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add api config dist internal/connection internal/controller
git commit -m "feat(user): AuthentikUser adopts people and invites new ones" \
  -m "A Create user issues a single-use invitation pinning the username; the link lives only in an owned Secret; the account carrying the owner proof is recognised as Created. Accounts are retained on deletion unless deletionPolicy is Delete." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 11: Invitation failure paths, AuthenticatorMissing and MemberHeld
Kind: rewrite

**Files:**
- Create: `internal/controller/authentikuser_invitation_test.go`
- Modify: `internal/controller/authentikuser_controller.go`, `internal/controller/user_invitation.go`, `internal/controller/authentikgroup_controller.go`, `internal/controller/group_members.go`, `internal/controller/authentikgroup_test.go`, `internal/controller/claims_index_test.go`
- Modify (regenerated): `config/rbac/role.yaml`, `dist/chart/templates/rbac/manager-role.yaml`

**Interfaces:**
- Consumes: Tasks 3, 7 (`planMembers` `Held`), 10.
- Produces:
  - Reasons `reasonMultipleEnrolments = "MultipleEnrolments"`, `reasonEnrolledUnderOtherName = "EnrolledUnderOtherName"`, `reasonInvitationAmbiguous = "InvitationAmbiguous"`; `const ConditionAuthenticatorMissing = "AuthenticatorMissing"`, reason `reasonNoAuthenticator = "NoAuthenticator"`; event reason `InvitationWithdrawn`.
  - Indexes `const indexUserUsername = ".spec.username"` (`userUsernameIndex`), `const indexGroupMember = ".spec.members"` (`groupMemberIndex`, one key per listed username), both registered by the group reconciler; `func (r *AuthentikGroupReconciler) groupsForUser(ctx context.Context, o client.Object) []reconcile.Request`; `func (r *AuthentikGroupReconciler) heldUsernames(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup, usernames []string) (map[string]bool, error)`.

Behaviour:
1. Proof query outcomes while inviting, before `Decide`: more than one account -> `MultipleEnrolments` naming every username, nothing adopted; exactly one with another username -> `EnrolledUnderOtherName` (`the invitation was used to enrol %q, not %q (the enrolment flow's username prompt is not read-only); fix the blueprint, then delete that account or use a new resource with that username`), not adopted.
2. Lost create response (`status.invitation.pending` true): `FindInvitationByName(name)` found -> `pending = false`, write the Secret; not found -> re-send `CreateInvitation` with the persisted name and expiry; recovery looks only for the current attempt's name.
3. Reissue: `invitationAttempt(spec) > status.invitation.attempt` while no account is recognised -> delete the live invitation of the previous attempt (by its name), delete the previous Secret when its name differs, then issue `k8s-<uid>-<new attempt>` (new expiry, `pending` true, persisted before the request). A lower or equal attempt does nothing; after `Created` the attempt is ignored.
4. Conflict while inviting (`Decide` -> `Conflict` with `OwnCreatePending`): delete the live invitation and the Secret; keep `status.invitation` (so an `AdoptOrCreate` resource never adopts the squatter).
5. Policy switched to `Adopt`/`ReadOnly` with `status.invitation` present: delete the live invitation and the Secret, clear `status.invitation`, Normal event `InvitationWithdrawn`, then decide normally.
6. Deletion with `status.invitation` present: the finalizer deletes the live invitation first (error -> deletion blocked, retried); the Secret goes with the owner reference.
7. `AuthenticatorMissing` (provenance `Created` only): `UserAuthenticators(userPk)` empty -> condition `AuthenticatorMissing` True reason `NoAuthenticator` (`the account has no authenticator; send the person a recovery link from the Authentik admin interface (a new invitation cannot be used: the username is taken)`), Warning event once per change; removed when a device exists. `Ready` unaffected.
8. Invitation names are not unique in Authentik. Every `FindInvitationByName` in the user reconciler (status checks, lost-response recovery, reissue, withdrawal, deletion) treats `authentik.ErrAmbiguous` the same way: `Ready=False` reason `InvitationAmbiguous` (`several invitations in Authentik are named %q; this resource cannot tell which one it issued, so it issues and deletes none. Delete the extra ones in Authentik`), Warning event `InvitationAmbiguous` once per change, no invitation created or deleted, the Secret left as it is, requeue `userErrorRecheck`. In the finalizer it blocks deletion the same way (retried).
9. MemberHeld: `heldUsernames` lists `AuthentikUser` resources by `indexUserUsername` in every namespace; a username is held when one of them: is on the same Authentik (`sameConnection` of the two claims), passes the identity gate for its own namespace and ref, has policy `Create` or `AdoptOrCreate`, empty provenance, and `status.invitation` present. The group passes the result as `planMembers` `Held`. The group reconciler watches `AuthentikUser` with `groupsForUser` (groups listing `spec.username`, by `indexGroupMember`); RBAC marker `authentikusers` get;list;watch.

- [ ] **Step 1: Write the failing tests `authentikuser_invitation_test.go` and group specs**

```go
It("recovers an invitation whose create response was lost without issuing a second one", func() {
	// fake.LoseNextInvitationCreate(); reconcile -> error, status.invitation.pending true
	// reconcile -> pending false, Secret written, len(fake.Invitations()) == 1
})
It("re-sends the create when the lost request never reached Authentik", func() {
	// status.invitation {pending true, name current} with no invitation in the fake -> one invitation created with that name
})
It("issues a new link only when attempt is increased", func() {
	// issue (attempt 1); reconcile twice -> still one invitation "k8s-<uid>-1"
	// spec attempt 2 -> "k8s-<uid>-1" gone, "k8s-<uid>-2" present, Secret url changed; attempt back to 1 -> nothing reissued
})
It("reports a Conflict and withdraws the invitation when someone else takes the username", func() {
	// issue; SeedUser("dave", nil) without proof; AdoptOrCreate -> "Conflict"; invitation and Secret deleted; Provenance ""; status.invitation kept
})
It("reports EnrolledUnderOtherName and adopts nothing", func() {
	// fake.EnrolFromInvitation(name, "david") -> "EnrolledUnderOtherName", message contains `"david"`; UserPK 0
})
It("reports MultipleEnrolments naming every account", func() {
	// two SeedUser with OwnerProof(uid) -> "MultipleEnrolments" naming both
})
It("withdraws the invitation when the policy changes to Adopt", func() {
	// issue; spec policy Adopt and invitation removed -> invitation and Secret gone; status.invitation nil; event "InvitationWithdrawn"
})
It("deletes the live invitation when the resource is deleted while inviting", func() {})
It("reports InvitationAmbiguous and issues nothing when two invitations share its name", func() {
	// issue; fake.SeedInvitation(status.invitation.name, nil) (a second one with the same name)
	// reconcile -> Ready False "InvitationAmbiguous"; a Warning "InvitationAmbiguous" event; len(fake.Invitations()) == 2 (none deleted, none added)
})
It("sets AuthenticatorMissing on a recognised person with no authenticator and clears it", func() {
	// enrol -> AuthenticatorMissing True reason "NoAuthenticator"; Ready True
	// fake.AddAuthenticator(pk); reconcile -> condition absent
})
It("never writes the invitation pk to a condition, event, log line or returned error", func() {
	// ctx with logf.IntoContext(ctx, zap.New(zap.WriteTo(&buf))); drive: lost create + recovery, Secret conflict,
	// fake.FailNextOn("DELETE", "/stages/invitation/invitations/", 500) during a reissue, withdrawal, deletion
	// for every pk in fake.IssuedInvitationPKs(): no condition message, no recorder event, no returned error string, nothing in buf contains it
})
```
In `authentikgroup_test.go` (`Context("members held by invitations")`):
```go
It("holds a username another resource is inviting, then adds it once recognised", func() {
	// user resource "dave" (Create, inviting); group members [dave]; fake has no dave
	// MembersInSync False reason "MemberHeld" contains `"dave"`; no add_user; Ready True
	// enrol + user reconcile (Created) -> group reconcile adds dave's pk
})
It("does not hold for an Adopt user resource or one outside the identity selector", func() { /* reason "MemberNotFound" */ })
It("re-enqueues groups listing a username when that user resource changes", func() { /* groupsForUser(user "dave") -> the group's request */ })
```

- [ ] **Step 2: Run to see them fail**

Run: `envtest "AuthentikUser|AuthentikGroup"`
Expected: FAIL.

- [ ] **Step 3: Implement** per Behaviour; add `indexUserUsername` and `indexGroupMember` to `testIndexes`.

- [ ] **Step 4: Run to see them pass**

Run: `envtest "AuthentikUser|AuthentikGroup"`
Expected: PASS.

- [ ] **Step 5: Regenerate, full suite, lint**

Run: `dx make manifests generate && dx make chart-regen && dx task test && dx make lint`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add config dist internal/controller
git commit -m "feat(user): invitation recovery, reissue by attempt, and held group members" \
  -m "Lost responses are recovered by name; a new link only on a higher attempt; squatted usernames withdraw the invitation; the sign-up token never reaches a condition, event, log or error. Groups hold a username while its invitation is pending." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 12: Converter maps JeffResc AuthentikGroup; documentation
Kind: rewrite

**Files:**
- Modify: `internal/migrate/jeffresc/input.go`, `internal/migrate/jeffresc/convert.go`, `internal/migrate/jeffresc/build.go`, `internal/migrate/jeffresc/report.go`, `internal/migrate/jeffresc/convert_test.go`, `internal/migrate/jeffresc/golden_test.go`, `docs/design/scope.md`, `docs/migration-from-jeffresc.md`, `docs/roadmap/TASK-05-api-object-adoption-and-deletion-policy.md`, `docs/roadmap/TASK-09-migration-from-jeffresc.md`, `docs/roadmap/TASK-10-beyond-parity.md`
- Rename: `internal/migrate/jeffresc/testdata/err-group-kind/` -> `internal/migrate/jeffresc/testdata/group-adopt/` (input extended, goldens regenerated)

**Interfaces:**
- Consumes: Tasks 6, 7 API (`AuthentikGroupSpec`).
- Produces: `type jeffGroupSpec struct { Name string json:"name,omitempty"; IsSuperuser *bool json:"isSuperuser,omitempty"; Parents []string json:"parents,omitempty"; Roles []string json:"roles,omitempty"; Attributes map[string]json.RawMessage json:"attributes,omitempty"; DeletionPolicy string json:"deletionPolicy,omitempty" }` (JeffResc v0.2.10 `AuthentikGroupSpec`, decoded strictly); output file `authentikgroup-<metadata.name>.yaml`.

How a group flows through `Convert` (decisions):
- `input.go`: `source` gains `group *jeffGroupSpec` (nil for applications). In `parseEnvelope`, the `kindGroup` case no longer errors: after the existing `meta.Name == ""` check it strict-decodes `env.Spec` into a `jeffGroupSpec` (`yaml.UnmarshalStrict`, error `%s: %s: spec: %w` as for applications) and returns `[]source{s}` with `s.group` set and `s.spec` zero. The function's other cases are unchanged.
- `convert.go`: sources stay in one sorted slice; the loop calls `b.addGroup(src)` when `src.group != nil`, else the existing `b.add(src)`. The empty-input error becomes `no AuthentikApplication, AuthentikOAuth2Application or AuthentikGroup (goauthentik.io) found in the input`, so a group-only input converts (it emits no provider, application or template).
- `build.go`: `func (b *builder) addGroup(src source) error` builds the object per the rules below, adds the file via `addFile` (duplicate-file error unchanged), appends the namespace to `b.namespaces`, records an `objectReport{ref, files, notes}`, and sets `b.groups = true` (new `builder` field).
- `report.go`: when `b.groups` and `--authentik-ref` is unset, after the adoption-gate paragraph add `Identity gate: groups on the default connection are refused (reason IdentityNotAllowed) unless the operator admin allows their namespaces with --default-connection-identity-namespace-selector, or the Helm value identity.defaultConnectionNamespaceSelector, set to:` followed by the same `kubernetes.io/metadata.name in (...)` line.

Rules: `managementPolicy: Adopt`, `deletionPolicy: Retain` (whatever JeffResc's was), `dryRun` per `--dry-run-adoption`, `name` = JeffResc `spec.name` else `metadata.name` (always written), `isSuperuser` when set, `authentikRef` per `--authentik-ref`, namespace per `--namespace`; `members` never written. `attributes`: each top-level key except reserved keys (prefix `goauthentik.io/`, `authentik_operator`, `settings`) and except keys whose value is a JSON object or null; every skipped key is a report note, one of two distinct texts: `attribute %q is reserved for Authentik or this operator; left in Authentik, unmanaged` (reserved key) or `attribute %q has an object or null value, which this operator cannot own per key; left in Authentik, unmanaged` (object or null value). Other notes: `parents [..] and roles [..] are not converted; they stay as they are in Authentik, unmanaged` (only the non-empty lists named); `members are not managed: JeffResc never managed membership, so spec.members is absent`.

- [ ] **Step 1: Write the failing tests**

`convert_test.go`: delete `TestGroupKindIsRefusedWithGuidance` (the refusal is gone) and add:
```go
func TestConvertGroupAdoptsAndRetains(t *testing.T) {
	// JeffResc AuthentikGroup media/media-users {isSuperuser: true, deletionPolicy: Delete,
	//   attributes {"role": "admin", "settings": {...}, "nested": {"a": 1}, "goauthentik.io/x": "y"}, parents ["p"], roles ["r"]}
	// -> AuthentikGroup: ManagementPolicy Adopt, DeletionPolicy Retain, Name "media-users", IsSuperuser true,
	//    Attributes only {"role": "admin"}, Members nil
	// report notes: "settings" and "goauthentik.io/x" with the "is reserved" text, "nested" with the "object or null value" text,
	//   parents ["p"] and roles ["r"]
	// the input holds only this group (no application): Convert returns no error and exactly one file, authentikgroup-media-users.yaml
}
func TestConvertGroupDefaultsNameToMetadata(t *testing.T) { /* spec without name -> Spec.Name == metadata.name */ }
func TestConvertRejectsUnknownGroupField(t *testing.T) { /* spec {users: [a]} -> error containing "unknown field" */ }
func TestConvertStillRefusesEmptyInput(t *testing.T) { /* a non-goauthentik object only -> error containing "or AuthentikGroup" */ }
func TestConvertGroupReportNamesIdentitySelector(t *testing.T) { /* report contains "--default-connection-identity-namespace-selector" */ }
```
`golden_test.go`: replace `{name: "err-group-kind", ...}` with `{name: "group-adopt", opts: dry}`. The renamed fixture's `input.yaml` becomes the `TestConvertGroupAdoptsAndRetains` group (media/media-users with every field above) plus a second group `media/media-admins` with no `spec.name` and no attributes; delete its old `golden/error.txt`.

- [ ] **Step 2: Run to see them fail**

Run: `dx go test ./internal/migrate/jeffresc/ -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement, rename the fixture, regenerate its goldens**

Run: `dx go test ./internal/migrate/jeffresc/ -run TestGolden -update -count=1`
Expected: PASS; `group-adopt/golden/` holds `authentikgroup-media-admins.yaml`, `authentikgroup-media-users.yaml` and `report.txt` matching the rules (the report has the identity-gate paragraph); no other golden changes.

- [ ] **Step 4: Documentation** (ASCII; facts from the spec, citing its sections):
- `docs/design/scope.md`: kind table rows for `AuthentikGroup` (members authoritative when set) and `AuthentikUser` (people, invitations); the "API-object adoption" intro no longer calls Group/User stubs; new sections "Identity gate" (spec 5.5, trust boundaries of spec 7, owner's value `kubernetes.io/metadata.name in (authentik)` for both selectors), "Application access" (spec 5.4, and the recommendation to turn off the instance setting "Applications with no policies bound can be accessed by any user"; the operator never changes it), "Groups" (spec 5.2, 5.3a, deletion guard), "Users" (spec 5.3, the owner's blueprint change: the enrolment flow's username prompt becomes `text_read_only`, otherwise `EnrolledUnderOtherName`; refresh tokens survive removal from an access group, deactivate to cut a person off).
- `docs/migration-from-jeffresc.md`: `AuthentikGroup` row now converts (Adopt + Retain, members absent); field map for the group kind; a runbook step for groups: apply the converted `AuthentikGroup`, wait for `Ready`, then set `deletionPolicy: Retain` on the JeffResc `AuthentikGroup` before deleting it (JeffResc's default `Delete` deletes the group and cascades its bindings); add the identity selector next to the adoption selector.
- `docs/roadmap/TASK-05-...md`: note that groups and users follow the contract through `internal/adopt`, and the deliberate difference (unproven pending create is a Conflict under every policy) to settle when the provider and application move onto it.
- `docs/roadmap/TASK-09-...md`: the `AuthentikGroup` known gap is closed (converted, runbook step); `groupBindings` convert to `spec.access`.
- `docs/roadmap/TASK-10-...md`: check off groups and users with what shipped; list the follow-ups of spec section 10 and the provider/application divergence (undecided provenance + `Adopt` + vanished object).

- [ ] **Step 5: Run to see them pass**

Run: `dx go test ./internal/migrate/jeffresc/ ./hack/convert-jeffresc/ -count=1 && dx task test && dx make lint && dx sh -c 'git diff -U0 HEAD -- docs | grep "^+" | LC_ALL=C grep -P "[^\x00-\x7F]"; test $? -eq 1'`
Expected: PASS; the ASCII check (added lines only; scope.md already holds a few non-ASCII characters that stay) prints nothing and exits 0.

- [ ] **Step 6: Commit**

```bash
git add -A internal/migrate/jeffresc docs
git commit -m "feat(migrate): convert JeffResc AuthentikGroup; document groups, users and access" \
  -m "JeffResc groups become Adopt + Retain AuthentikGroups without members; reserved and object-valued attributes, parents and roles stay unmanaged and are reported. scope.md, the migration runbook and the roadmap describe the identity gate, spec.access, groups and users." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

### Task 13: Real-Authentik e2e for enrolment, access and group migration
Kind: rewrite

**Files:**
- Create: `test/e2e/identity_test.go`, `test/aktest/enrol.go`, `test/aktest/enrol_test.go`
- Modify: `test/e2e/install_test.go`, `test/e2e/config_test.go`, `test/e2e/helpers_test.go`, `test/e2e/e2e_suite_test.go`, `test/aktest/api.go`, `test/aktest/oidc.go`, `test/aktest/oidc_test.go`

**Interfaces:**
- Consumes: everything above; existing `aktest.API.Do`, `CreateOAuthProvider`, `CreateApplication`, `ModelWriteCount`, `login` executor helpers, e2e `installOperator`, `newProvider`, `newApplication`, `eventuallySecret`, `deleteAndWait`, `pokeAndWait`.
- Produces:
  - `config_test.go`: `identitySelector = adoptionSelector`; `installOperator` always passes `--set-string identity.defaultConnectionNamespaceSelector=<identitySelector, commas escaped>`.
  - `aktest`: `func (a *API) EnsureEnrolmentFlow(ctx context.Context, slug string) error`. It returns nil when `GET /api/v3/flows/instances/?slug=<slug>` has a result; otherwise it creates, in this order, each with `a.Do` (paths verified against the generated client `goauthentik.io/api/v3@v3.2026080.5`; required properties from its `*Request` models), keeping each response's `pk`:
    1. `POST /api/v3/flows/instances/` `{"name": slug, "slug": slug, "title": "Create your account", "designation": "enrollment", "authentication": "require_unauthenticated"}`
    2. `POST /api/v3/stages/invitation/stages/` `{"name": slug+"-invitation", "continue_flow_without_invitation": false}`
    3. `POST /api/v3/stages/prompt/prompts/` `{"name": slug+"-username", "field_key": "username", "label": "Username", "type": "text_read_only", "required": true, "placeholder": "Username", "order": 0}`
    4. `POST /api/v3/stages/prompt/stages/` `{"name": slug+"-prompt", "fields": [<prompt pk>]}`
    5. `POST /api/v3/stages/user_write/` `{"name": slug+"-write", "user_creation_mode": "always_create", "user_type": "internal"}`
    6. `POST /api/v3/stages/user_login/` `{"name": slug+"-login"}`
    7. `POST /api/v3/flows/bindings/` `{"target": <flow pk>, "stage": <stage pk>, "order": N}` for the stages of 2, 4, 5, 6 with N = 5, 10, 20, 100 (the owner's enrolment flow without the passkey stage).
    Any non-2xx is returned as an error naming the path.
  - More `aktest.API` helpers: `func (a *API) CreateUserWithPassword(ctx context.Context, username, password string) (int, error)` (`POST /api/v3/core/users/` `{"username", "name": username, "path": "users"}`, then `POST /api/v3/core/users/<pk>/set_password/` `{"password"}`); `func (a *API) CreateGroup(ctx context.Context, name string) (string, error)` (`POST /api/v3/core/groups/` `{"name"}`); `func (a *API) CreateGroupBinding(ctx context.Context, groupPK, appPK string, order int) (string, error)` (`POST /api/v3/policies/bindings/` `{"group", "target", "order"}`); `func (a *API) BindingPKs(ctx context.Context, appPK string) ([]string, error)` (`GET /api/v3/policies/bindings/?target=<appPK>`, sorted pks; an empty `appPK` is an error).
  - `aktest`: `var ErrPermissionDenied = errors.New("aktest: permission denied")`; `AuthorizationCodeLogin` returns an error wrapping it when a GET answers HTTP 200 whose body contains `Permission denied` (what Authentik renders for a signed-in user a policy refuses, `policies/views.py` and `policies/denied.py`), in place of the generic `expected a redirect` error; covered by `TestLoginReportsPermissionDenied` in `oidc_test.go`.
  - `func Enrol(ctx context.Context, rt http.RoundTripper, inviteURL string) error` in `test/aktest/enrol.go`: parse the link (path `/if/flow/<slug>/`, query `itoken`), drive `GET /api/v3/flows/executor/<slug>/?query=itoken%3D<pk>` with a cookie jar through the existing `login.executor`; answer `ak-stage-prompt` with each field's `initial_value` keyed by `field_key`; stop at `xak-flow-redirect`; `ak-stage-access-denied` or any other component is an error naming it.
  - `helpers_test.go`: `loginAs(ctx, c creds, redirect, username, password string)`; `login` calls it with the bootstrap user.
  - `e2e_suite_test.go`: the failure dump also prints `authentikgroups` and `authentikusers`.

- [ ] **Step 1: Write the failing unit test `test/aktest/enrol_test.go`**

```go
func TestEnrolAnswersThePromptWithItsInitialValues(t *testing.T) {
	// httptest server: GET executor (query "itoken=p1") -> {"component":"ak-stage-prompt","fields":[{"field_key":"username","initial_value":"dave"}]}
	// POST -> 302 to executor; next GET -> {"component":"xak-flow-redirect","to":"/"}
	// Enrol(ctx, nil, srv.URL+"/if/flow/enrol/?itoken=p1") == nil; the POST body had username "dave"
}
func TestEnrolReportsAccessDenied(t *testing.T) { /* first challenge ak-stage-access-denied -> error containing "access denied" */ }
```
`test/aktest/oidc_test.go`:
```go
func TestLoginReportsPermissionDenied(t *testing.T) {
	// httptest server: the authorize URL answers HTTP 200 with an HTML body containing "Permission denied"
	// AuthorizationCodeLogin -> errors.Is(err, ErrPermissionDenied)
	// a 200 without that text still yields the existing "expected a redirect" error and not ErrPermissionDenied
}
```

- [ ] **Step 2: Write the e2e specs `test/e2e/identity_test.go`** (`Describe("groups, users and access against a real Authentik", Ordered, ...)`, namespace `appsNS`, flow slug `e2e-enrolment`):

```go
BeforeAll: ak.EnsureEnrolmentFlow(ctx, "e2e-enrolment"); ak.CreateUserWithPassword("e2e-member", pw); ak.CreateUserWithPassword("e2e-outsider", pw)
It("enrols a person from the operator's invitation and recognises the account as Created", func(ctx SpecContext) {
	// AuthentikUser "e2e-person" {username "e2e-person", invitation {flow "e2e-enrolment"}} -> Secret "e2e-person-invitation" url
	// aktest.Enrol(ctx, clusterTransport(), url) succeeds
	// eventually: Provenance Created, UserPK != 0, status.invitation nil, Secret gone
	// the Authentik user "e2e-person" has attributes.authentik_operator.owner_uid == string(user.UID)
})
It("adds group members, including the enrolled person", func(ctx SpecContext) {
	// AuthentikGroup "e2e-access" members [e2e-member, e2e-person] -> MembersInSync True; Authentik group has both pks
})
It("lets a member open an application bound to the group and refuses a non-member at authorize", func(ctx SpecContext) {
	// provider + application "e2e-groups" with access {Groups, [e2e-access]} -> AccessInSync True, AccessOpen False
	// loginAs(..., "e2e-member", pw) succeeds; loginAs(..., "e2e-outsider", pw) returns err with errors.Is(err, aktest.ErrPermissionDenied)
})
It("blocks deleting a group an application still binds", func(ctx SpecContext) {
	// delete AuthentikGroup "e2e-access" -> Ready False reason DeletionBlocked; the group still exists in Authentik
	// delete the AuthentikApplication "e2e-groups" (Created, so its application and bindings are deleted)
	// -> within waitTimeout plus one minute the AuthentikGroup is gone and the Authentik group no longer exists
})
It("migrates a JeffResc group and its groupBindings without touching the bindings", func(ctx SpecContext) {
	// pre-create the way JeffResc would: group "e2e-jr-users", provider "e2e-jr-bound-provider", application "e2e-jr-bound" with that provider, one group binding
	// before := ak.BindingPKs(appPK); writes0 := ak.ModelWriteCount(ctx, "authentik_policies", "policybinding")
	// write a JeffResc v0.2 YAML (kind AuthentikGroup e2e-jr-users; kind AuthentikOAuth2Application e2e-jr-bound with groupBindings [e2e-jr-users])
	// go run ./hack/convert-jeffresc --out <tmp> --namespace e2e-migrated --dry-run-adoption=false <input>; kubectl apply -f <tmp>
	// eventually: AuthentikGroup Provenance Adopted, Ready True; AuthentikApplication Provenance Adopted, AccessInSync True
	// ak.BindingPKs(appPK) == before; ModelWriteCount unchanged
})
AfterAll: delete every CR this file created (deleteAndWait); the AuthentikUser keeps its account (Retain by default) and the migrated objects are Retain: the cluster is throwaway, so nothing else is cleaned
```

- [ ] **Step 3: Run the unit tests to see them fail, implement, and pass**

Run: `dx go test ./test/aktest/ -count=1`
Expected: first FAIL (`undefined: Enrol`); after implementing `enrol.go`, the `api.go` helpers and the e2e wiring: PASS. Then `dx go vet -tags e2e ./test/...` exits 0.

- [ ] **Step 4: Run the e2e suite on 2026.8.5**

Before EVERY e2e run (the first included), delete any cluster a previous or failed run left: the helpers are not idempotent (the enrolled account is retained, users and groups already exist) and `make test-e2e` reuses an existing cluster.
Run: `dx sh -c 'bin/kind delete cluster --name authentik-operator-test-e2e || kind delete cluster --name authentik-operator-test-e2e || true'`
Expected: `Deleting cluster "authentik-operator-test-e2e" ...` or nothing to delete; exit 0. (`authentik-operator-test-e2e` is the Makefile's `KIND_CLUSTER` default, which `task e2e` uses.)

Run: `dx task e2e` with the Bash tool's `run_in_background: true` (about 15-50 minutes; wait for the completion notification)
Expected: the output ends with `Ran N of N Specs ... SUCCESS!` and exit 0. On failure: read the dumped operator logs and CRs in the output; fix the cause in the task's files (or, for a product bug, in the owning file with a regression envtest first), delete the cluster as above, and re-run.

- [ ] **Step 5: Run the e2e suite on 2026.2.3**

Run: `dx sh -c 'bin/kind delete cluster --name authentik-operator-test-e2e || true'`
Expected: exit 0.

Run: `dx env AUTHENTIK_CHART_VERSION=2026.2.3 task e2e` with `run_in_background: true` (about 15-50 minutes; wait for the notification)
Expected: the output ends with `SUCCESS!` and exit 0. A decode failure on 2026.2.3 means a missing compat rule: add it with a recorded response (Task 4's procedure) and a decode test, then delete the cluster and re-run both versions.

- [ ] **Step 6: Full suite and lint**

Run: `dx task test && dx make lint`
Expected: exit 0.

- [ ] **Step 7: Commit**

```bash
git add test
git commit -m "test(e2e): enrolment by invitation, group access at authorize, JeffResc group migration" \
  -m "A test-only copy of the owner's enrolment flow without the passkey stage is driven through the flow executor; a member opens a Groups application and a non-member is refused; a JeffResc group with groupBindings migrates without binding writes. Runs on 2026.2.3 and 2026.8.5." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Run: the commands above, on the host, from `$WT`.
Expected: one new commit; `git cat-file commit HEAD | grep -c "^gpgsig"` prints `1`. A signing failure is `PLAN: PAUSED — signing` (never `--no-gpg-sign`).

---

## Rulings made while writing this plan (spec ambiguities)

1. Status plumbing that needs controller-package types (`conditionError`, the recorder) lives in `internal/controller/identity_status.go` (`classifyFailure`), not `internal/adopt`; `internal/adopt` holds the pure decisions. Reason: the spec forbids `adopt` importing `internal/controller` and forbids changing the provider and application controllers, where `conditionError` lives.
2. Claims for groups and users use per-kind `claimants`/`yieldIfOutranked` methods over `claims.go` unchanged, as the provider does; no generic claim helper. Reason: listing differs per kind and the spec says `claims.go` unchanged.
3. `AuthentikGroup.status.name` added. Reason: "name immutable once tracked, enforced by the controller" needs the tracked name to tell a spec rename (InvalidSpec) from a UI rename (restored).
4. `authentikRef` is immutable on groups and users (CEL), as on providers and applications. Reason: a changed ref would point a tracked pk at another Authentik.
5. A vanished tracked object with empty provenance is decided as untracked (`Decide` step 5). Reason: scope.md is silent; this matches the application controller's by-slug fallback.
6. An unset `Target` passed to `Decide` is a `Conflict`. Reason: fail closed on a caller bug; never create from an unknown lookup.
7. Group pending-create marker is kept on an unproven find (Conflict persists until cleared). Reason: dropping it would let `AdoptOrCreate` adopt a squatter on the next reconcile, which spec 5.1 forbids.
8. `spec.attributes` uses `json:"attributes,omitzero"`. Reason: spec 5.3a gives `{}` and absent different meanings; `omitempty` would drop `{}` in every typed client.
9. Attribute values that are JSON objects or null are `InvalidSpec` (controller), not CEL. Reason: the spec lists string, number, bool, list; CEL over preserve-unknown-fields values is costly.
10. (Controller ruling before the plan gate, replacing the plan writer's first ruling) Negated group bindings follow the live `policy_engine_mode`: under `any` they are removed (a negated binding on G admits every user outside G, so keeping it fails open); under `all` they are left alone (there they exclude G, and removing one would widen access). A listed group whose only binding is negated gets a non-negated one added; under `any` the negated one is then removed. Reason: deny by default (spec section 2, anti-goal 1; design 5.4 corrected the same way).
11. (Revised after plan gate pass 1, S1/S2) `WaitingForGroups` guards every create, including the recreate of a vanished `Created` application, and every adopt-write, at `createApplication` and `adoptApplication`. `AccessPending` (design 5.4) holds `Ready=False` until the first listed binding exists on a newly created or recreated application or a `Created` one, including when listing bindings fails right after the create; with no listed group resolvable on an existing application, only `AccessInSync=False GroupNotFound`. Reason: design 5.4 says the app is never created until a listed group resolves and is not Ready until its first listed binding exists.
12. ReadOnly access drift is reported in `AccessInSync` (reason `DriftDetected`), not `Drifted`. Reason: `Drifted` carries the application's field patch; keeps one writer per condition.
13. Binding write failures set `AccessInSync=False` reason `AuthentikError` and back off. Reason: the spec lists only `GroupNotFound`/`AccessNarrowed` and requires a binding problem never to flip Ready.
14. `MembersInSync` stays True with a note when the token user is kept but unlisted. Reason: the spec's False reasons do not include it; it is informational.
15. A tracked user whose username changed in Authentik is a `Conflict`, not renamed back. Reason: people's accounts; the spec does not say to restore usernames, and a silent rename could hand an identity to someone else.
16. The operator never deletes its own token user, even with explicit `Delete`. Reason: same intent as "never deactivates the operator token's own user".
17. `AuthenticatorMissing` applies to `Created` users only. Reason: adopted users may sign in with a password, which is not an authenticator device.
18. Invitation `attempt` and `expiresAfter` defaults are applied in the controller (no CRD default); `expiresAfter` must be positive (CEL). Reason: one place for invitation defaults; a non-positive expiry would mint a dead link.
19. The JeffResc group-migration e2e pre-creates objects the way JeffResc would instead of running JeffResc v0.2.x. Reason: the existing migration e2e pins JeffResc 0.1.8, which has no group kind; the contract under test is converter output plus adoption.
20. The test-only enrolment flow is created through the REST API (`EnsureEnrolmentFlow`), not a blueprint import. Reason: these object endpoints are stable across 2026.2.3 and 2026.8.5; the blueprint import endpoint is unverified on 2026.2.3.
21. The identity flag reuses the adoption selector's parser, renamed `ParseNamespaceSelector`. Reason: identical grammar and empty-denies semantics; the old name would put "adoption" in the identity flag's error.
22. Task order puts the identity gate (Task 5) before the group controller, and the application/group watches in Task 8. Reason: dependency order (the group controller needs the gate; the watches need `spec.access`).
23. (Plan gate pass 1, B1) `/core/users/me/` gets a compat rule: on 2026.2.3 it returns only `user` (and `original`) and its user object has no `is_current`, while the pinned client requires top-level `users` and `user.is_current`. The rule adds `users: [user]` and `user.is_current: true` when absent. Reason: `Me()` (deletion guard, token-user checks) must decode on 2026.2.3; it also repairs the existing `Ping` used by the AuthentikDeployment reconciler, which fails to decode there today.
24. (Plan gate pass 1, S4) A non-member refused at authorize is detected by a small `aktest` change: `AuthorizationCodeLogin` returns an error wrapping `aktest.ErrPermissionDenied` when a page answers HTTP 200 with "Permission denied". Reason: Authentik renders that page (200, HTML) for a signed-in user a policy refuses, which the walker otherwise reports only as "expected a redirect"; an explicit sentinel keeps the e2e assertion from passing on any other unexpected page.
25. (Plan gate pass 1, S9) `firstName`/`lastName` write `given_name`/`family_name` whenever set, independently of `spec.attributes` (absent included); clearing them leaves the live value; the keys are never in `status.managedAttributes`. Reason: design 5.3 says unset keeps live, and CEL already gives each key a single writer.
26. (Plan gate pass 1, minor) Invitation names are not unique in Authentik; an ambiguous lookup is `InvitationAmbiguous` and the reconciler then creates and deletes nothing. Reason: deleting or trusting the wrong invitation could leak or kill someone else's sign-up link.
27. (Plan gate pass 1, S7) A JeffResc input holding only `AuthentikGroup` objects converts (the empty-input error now also counts groups). Reason: groups are migrated before or apart from applications in the runbook.
