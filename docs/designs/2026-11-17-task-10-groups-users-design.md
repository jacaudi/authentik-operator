# TASK-10 design: AuthentikGroup, AuthentikUser and application access

Status: draft for owner approval (2026-11-17). Decision pages with research and
citations (local, gitignored): `docs/reference/2026-11-15-task10-membership-decision.html`,
`2026-11-16-task10-app-access-decision.html`, `2026-11-17-task10-identity-gate-decision.html`,
`2026-11-17-task10-people-decision.html`, `2026-11-17-task10-build-approach-decision.html`,
`2026-11-15-task10-operator-comparison.html` (all under the main checkout).

Reconciled with the code as built (end-of-build drift check, 2026-11-18). A
passage that changed since approval says so in parentheses and cites the ruling
that allowed it, in the implementation ledger or the plan
(`docs/plans/2026-11-17-task-10-groups-users-implementation.md`).

## 1. Intent

`AuthentikGroup` and `AuthentikUser` ship today as kubebuilder scaffold stubs
(`spec.foo`, a no-op `Reconcile`). Release PR #33 (v0.1.0) is held because it
would publish them. The owner chose to build them rather than remove them.

The owner's use: every application gets at least two Authentik groups, an
**access group** that decides who may open the app (for example
`immich-users`) and a **role group** the app reads from the OIDC `groups` claim
(for example `immich-admins`). The users are people. Membership changes are
made by the Authentik admin through CRs. People sign in with passkeys only, with
no email and no password, and create their own accounts through an invitation
(the owner's blueprint `docs/reference/authentik-passkey-only.yaml`, main
checkout, gitignored).

A group gates nothing unless applications bind to it, and Authentik lets
everyone open an application that has no bindings. So this design also gives
`AuthentikApplication` a required access declaration.

## 2. Goals, non-goals, anti-goals

Goals
- `AuthentikGroup`: create or adopt a group; own its member list from Git.
- `AuthentikUser` for people: issue a single-use invitation that pins the
  username; recognise the account the person enrols; manage display details
  and offboarding (`isActive`).
- `AuthentikApplication.spec.access`: every application states who may open
  it; the operator owns its group bindings.
- Reuse the #18 adoption contract (`docs/design/scope.md`, "API-object
  adoption") unchanged: `managementPolicy`, `dryRun`, `deletionPolicy`,
  `status.provenance`, claims and tie-break, adoption gate.
- Unblock v0.1.0 without touching the shipped provider and application
  adoption flows beyond the access field. (As built, a few shared helpers in
  `internal/controller/common.go` were refactored, behaviour-preserving: see 5.1.)

Non-goals (this design)
- Machine accounts: service accounts, API tokens, app passwords, token Secrets
  (owner: "I don't have a service/machine to machine in mind").
- `AuthentikSAMLProvider` and `AuthentikDeployment` Managed mode: separate
  designs (they share no machinery with groups and users).
- Group `parents`, `roles`; user `roles`,
  passwords, passkeys.
- Migrating the provider and application controllers onto the new library
  (follow-up after v0.1.0, section 10).
- Changing Authentik's instance-wide settings.

Anti-goals (would make this a failure even if it works)
- An application that is open to every user because a field was forgotten,
  a binding failed, or a typo dropped a group.
- A Kubernetes namespace using the operator's shared token to create a
  superuser group or claim someone's username.
- Deleting a person's account (and their passkeys) because a CR was deleted.
- A sign-up link anywhere a non-admin can read it (status, events, logs).

## 3. Constraints

- Kubebuilder rules (`AGENTS.md`): the kinds exist (`PROJECT` already lists
  them); edit `api/v1alpha1/authentik{group,user}_types.go` and the controllers;
  run `make manifests generate`; `make chart-regen` for chart-owned templates;
  never hand-edit generated files.
- #18 contract: no CRD defaults on adoption-sensitive fields; defaults applied
  in the controller on create only; adopt keeps live values for unset fields.
- Authentik versions in the e2e matrix: 2026.2.3 and 2026.8.5
  (`.github/workflows/test-e2e.yml`). Go client `goauthentik.io/api/v3
  v3.2026080.5`.
- Authentik facts this design rests on (all read from 2026.8.5 source,
  cited in the decision pages): group names are unique (`core/models.py:189`);
  usernames are unique; per-member `POST /core/groups/{uuid}/add_user/` and
  `remove_user/`; a PUT clears omitted lists (PATCH only); only enabled
  bindings count, `negate` inverts; deleting a group cascades its bindings;
  instance flag `core_default_app_access` (2026.5+, default on) decides apps
  with no bindings; invitation `fixed_data` pre-fills prompts and a
  `text_read_only` prompt is reset to the context value; deactivating a user
  deletes their OAuth tokens (`providers/oauth2/signals.py:121-130`).

## 4. Approaches (decided with the owner, one decision at a time)

| # | Decision | Chosen | Rejected (why) |
|---|---|---|---|
| 1 | Who owns membership | **Group `spec.members`, authoritative when set** | User `spec.groups` (any user CR can join any group, superuser included); separate membership kind (no need); additive with a status ledger (grants outside Git invisible; ledger lost on CR re-apply) |
| 2 | Who may open an app | **Required `spec.access`: `Groups` or `Unmanaged`** | Optional `groupBindings` (JeffResc; forgotten field = open); group lists its apps (pocket-id-operator; open when empty); first-draft "unlink the provider" fail-closed trick (broke lost-create recovery, did not stop refresh tokens) |
| 3 | Which namespaces manage identities | **New `--default-connection-identity-namespace-selector`**, every policy, empty denies | Field-level gate (leaves username squatting); RBAC only (shared token with no admin opt-in) |
| 4 | How a person's account comes to exist | **The CR issues a single-use invitation pinning the username** (owner's pick over the recommended enrol-then-adopt) | Enrol first, then commit an Adopt CR; operator creates the account plus a recovery link (collides with `always_create`, needs a recovery flow) |
| 5 | Code structure | **Per-kind controllers plus a small shared library** (policy decision function + step helpers), the hand-written-operator norm | Generic engine like ACK/ASO/Crossplane (needs generated code at scale, kind branches here, delays v0.1.0); a third and fourth copy of the adoption flow (copies already drifted once) |

## 5. Design

### 5.1 Shared adoption library (decision 5)

New package `internal/adopt` (name: builder's call if a clash appears; it
must not import `internal/controller`).

- `Decide(in Facts) Action`: a pure function translating `scope.md`'s
  management-policy table. `Facts` holds: management policy, provenance,
  whether the tracked object was found, adoption target (found / missing /
  ambiguous), own create pending (a create was sent, outcome unknown), own
  create proven (the found target carries this resource's ownership proof),
  `dryRun`, and `RecreateVanished` (a per-kind constant: true for Group,
  false for User). Eight facts, not the six first proposed: the design gate
  showed the User kind needs the last two to avoid kind branches. `Action` is
  a small enum: `UseTracked`, `Recreate`, `TrackedGone` (vanished and not
  recreated), `RecoverOwnCreate`, `Create`, `Adopt`, `Observe` (ReadOnly),
  `DryRunAdopt`, `DryRunCreate`, `Conflict`, `TargetNotFound`, `Ambiguous`.
- One rule differs from the application controller's today, deliberately:
  a target found while the resource's own create is pending but **unproven**
  is a `Conflict` under every policy (never adopted: under `AdoptOrCreate`
  it could be a squatter's account). Recorded for the post-v0.1.0 migration.
- Table test: one row per row and bullet of `scope.md` "Management policy"
  and "Behavior" (the test names quote the doc line they pin).
- Step helpers as plain functions with concrete parameters: the finalize
  decision (`EffectiveDeletionPolicy` with a per-kind default for unset
  `deletionPolicy` passed in, decision 3's "outside the selector delete only
  Created" rule, and the undecided-provenance Warning), the claim
  persist-then-recheck sequence (uses `claims.go` unchanged), and status
  plumbing (`failed`, `setReady`, `patchStatus` equivalents).
  (Changed at build: only the finalize decision is in `internal/adopt`
  ([`finalize.go`](../../internal/adopt/finalize.go), `adopt.Finalize`). The claim
  re-check and the status plumbing (`classifyFailure`, `patchStatus`,
  `recheckClaim`, `syncThenPersist`, `addFinalizer`) are in
  [`internal/controller/identity_status.go`](../../internal/controller/identity_status.go):
  the status plumbing works on controller types, which `internal/adopt` must not
  import (plan Rulings 1-2; pre-flight F2 ruling). `failed` and `setReady` stay
  methods of each reconciler.)
- Guards (owner): concrete parameters, few helpers, no helper added before two
  kinds call it; `Facts` stays at these eight; anything kind-specific
  (members, invitations, bindings) stays in the controller.
- The provider and application controllers are **not** changed to use it in
  this task. (Changed at build: shared helpers in
  [`common.go`](../../internal/controller/common.go) that they call were
  refactored for the new kinds, behaviour-preserving: `adoptionGate.check` goes
  through a new `selectorMatches`; `ParseAdoptionNamespaceSelector` became
  `ParseNamespaceSelector`; `dryRunReport` split into `dryRunChanges` and
  `dryRunMessage`; `recordDrift` delegates to `setDrift`; the application
  controller's dry run goes through `dryRunAdoption`. The provider and
  application messages are unchanged: ledger "Task 7: Ruling: builder also
  edited internal/controller/common.go", verified by the Task 7 critic.)

### 5.2 AuthentikGroup

Spec (all optional unless stated; `APIManagementSpec` inline: `managementPolicy`,
`dryRun`, `deletionPolicy`; `authentikRef`):

| Field | Meaning |
|---|---|
| `name` | Authentik group name; default `metadata.name`; 1 to 255 characters, no control characters. Immutable once the group is tracked, enforced by the controller against `status.name` (CEL cannot see status): a changed name is `InvalidSpec` (renaming would change every app's bindings by name). |
| `isSuperuser` | `*bool`. On a group this resource **created**, unset is held to `false` (#18: Created objects are held to create-time defaults), so a UI flip to superuser is reverted. On an adopted group, unset keeps live. |
| `attributes` | Per-key owned map (5.3a). Group attributes are inherited by members in claims (`user.group_attributes()`), so this is where a role claim lives. |
| `members` | `*[]string` of Authentik usernames, at most 1000, each 1 to 150 characters. **Absent: membership never read or written** (any provenance; an explicit exception to "Created objects are held to create-time defaults"). **Present (incl. `[]`): exactly this list.** |

No `match` field: group names are unique, so the target is the group named
`name`. No `parents` or `roles` (unmanaged, live values kept). `attributes`
as in 5.3a.

Status: `conditions`, `groupPk` (UUID string), `name`, `provenance`,
`pendingCreate`, `observedGeneration`, `members` (list of `{username, pk}`
pinned on first resolution). (`name` added at build: the group's name in
Authentik when it was created or adopted, so the controller can tell a spec
rename from a rename made in the Authentik UI; plan Ruling 3.)

Conditions: `Ready` (sync of the group itself; blocking reasons such as
`Conflict`, `IdentityNotAllowed`, `DeletionBlocked`), `MembersInSync` (only
when `members` is present; `False` with reason `MemberNotFound`,
`MemberIdentityChanged`, `MemberHeld` or `AuthentikError` (a write Authentik
refused), message naming the usernames), and `Drifted` under ReadOnly. A
members problem never makes `Ready` false: the group exists and the other
members are correct.

Behaviour
- Policy flow from `adopt.Decide`. Lost create: `pendingCreate` holds the name;
  a group found by that name is proven ours only if it carries the attribute
  `authentik_operator.owner_uid: <metadata.uid>` (a nested key: Authentik splits
  attribute paths on dots) written in the create request (the
  operator's own key in `attributes`, written on create only and reserved).
- Members (authoritative when present):
  - resolve each username to a pk; unknown -> condition `MemberNotFound`
    naming it; all other adds **and every removal** still apply; a write that
    fails (for example a 403 on `add_user`) is joined with the others and
    reported as `MembersInSync` `False`, reason `AuthentikError`, without
    stopping the remaining writes, and the group is rechecked in a minute;
  - a username whose pk differs from the pinned one -> held out, condition
    `MemberIdentityChanged`, until the pin is cleared from status;
  - a username is **held** (`MemberHeld`: neither added nor removed, not
    pinned) only while an `AuthentikUser` with that `spec.username` is
    **inviting**: on the same Authentik (`sameConnection`), passing the
    identity gate, policy `Create` or `AdoptOrCreate`, provenance still empty,
    `status.invitation` present. Adopted, ReadOnly or gated-out user CRs never
    hold anyone. Indexes: `AuthentikUser` by `spec.username`, and
    `AuthentikGroup` by each `spec.members` entry, so a User change
    re-enqueues the groups listing that name;
  - never removes the operator token's own user (`GET /core/users/me/`);
    reports it if the list omits it;
  - writes per member via `add_user` / `remove_user`; never PUT;
  - adoption `dryRun` lists the usernames it would add and remove; after
    adoption every change emits an event naming added and removed usernames.
- Gates, in order: identity gate (5.5), then the adoption gate for policies
  that may adopt.
- Deletion: `EffectiveDeletionPolicy`, plus decision 3's rule (outside the
  identity selector only `Created` objects are deleted). Finalizers delete only
  the pk in status.
- **Deletion guard (owner, builder check Q1; strengthened by the design
  gate):** when the effective policy would delete the Authentik group, the
  finalizer first asks Authentik what the group is bound to
  (`GET /core/groups/{uuid}/used_by/`, both versions). While any policy
  binding uses the group (on any application, whether it has a CR,
  `Unmanaged`, or exists only in the UI), the group is not deleted: the CR
  stays Terminating with `Ready=False`, reason `DeletionBlocked` (a new
  condition reason; the repo already uses it as an event reason), message
  quoting each `used_by` entry's text verbatim (for example "Binding from
  immich #0 to Group immich-users") and the remedy: remove the binding in
  Authentik or from the app's `access.groups`, or set `deletionPolicy:
  Retain` on this CR. Any policy binding blocks: application, flow, stage or
  source bindings alike. A group the resource created is deleted only if it
  still carries the resource's ownership proof; otherwise a Warning `Retained`
  is emitted and the group stays.
  **Fail closed on visibility:** `used_by` lists only what the token may see
  (`core/api/used_by.py:83`). Unless the token's user is a superuser (from
  `GET /core/users/me/`), the operator cannot know the list is complete, so
  it does **not** delete the group: it retains it, removes the finalizer and
  emits a Warning `RetainedUnverifiable` ("delete it in Authentik after
  checking its bindings").
  This also covers an app CR deleted with `Retain` in the same prune. The
  recheck picks up removed bindings (and `AuthentikApplication` changes
  re-enqueue groups by name). `Retain` deletes nothing in Authentik and is
  never blocked. Mirrors pocket-id-operator's reference-blocking finalizers,
  but on the live bindings rather than CR references.
- Recheck: 5 minutes when synced, 1 minute on user-fixable errors (as today).

Implemented in:
[`api/v1alpha1/authentikgroup_types.go`](../../api/v1alpha1/authentikgroup_types.go),
[`authentikgroup_controller.go`](../../internal/controller/authentikgroup_controller.go)
(reconcile, gates, deletion),
[`group_desired.go`](../../internal/controller/group_desired.go) (fields to
write),
[`group_members.go`](../../internal/controller/group_members.go) (member plan,
`MemberHeld`),
[`internal/authentik/group.go`](../../internal/authentik/group.go) (Authentik
calls) and the ownership-proof single source
[`internal/authentik/owner.go`](../../internal/authentik/owner.go).

### 5.3 AuthentikUser (people)

Spec:

| Field | Meaning |
|---|---|
| `username` | Required, immutable (CEL `self == oldSelf`). A tracked account that is renamed in Authentik is a `Conflict`, see "Create = invite" step 1. |
| `name`, `email`, `path`, `type` (`internal` / `external`) | Optional; unset keeps live. |
| `firstName`, `lastName` | Optional strings; unset keeps live. Written to Authentik's `given_name` and `family_name` attribute keys, which the OIDC `profile` scope reads (`blueprints/system/providers-oauth2.yaml:33-50`), so apps receive them as `given_name` / `family_name`. The owner chose these names over the claim names. |
| `isActive` | `*bool`; unset keeps live. `false` is offboarding. |
| `attributes` | Per-key owned map (5.3a). |
| `invitation` | Required when the policy may create (`Create`, `AdoptOrCreate`); forbidden under `Adopt` / `ReadOnly` (CEL). `{flow: <enrolment flow slug>, expiresAfter: <duration>, secretName?: <name>, attempt?: <int>}`. Bumping `attempt` is the only way to get a new link (see "Reissue"). `flow` is a slug (pattern, at most 50 characters) and `secretName` a DNS name (pattern, at most 253). (Limits added at build: plan Task 9 API step.) |

Status: `conditions` (`Ready`, `Drifted` under ReadOnly), `userPk` (int32),
`provenance`, `observedGeneration`, and while inviting `invitation`:
`{name, attempt, expires, pending, secretName}`. **Never the invitation pk**:
the pk is the sign-up token itself (the link is `?itoken=<pk>` and the stage
looks the invitation up by that value, `stages/invitation/stage.py`
`get_invite`, `api.py:148`), so it lives only in the Secret.

"Unset keeps live" applies to a **Created** user too (`name`, `email`, `path`,
`type`, `isActive`): the person's account was made by their own enrolment, so
there are no operator create-time defaults to hold. A stated departure from
#18, like `members` on groups.

Create = invite (decision 4)
1. No user named `username` exists. If one does and no invitation is in
   progress: `Conflict` under `Create`, adopted under `AdoptOrCreate` (#18,
   `scope.md:115-117`). If one appears while an invitation is in progress
   and lacks the proof: step 6.

   How a User maps onto `Facts`: "tracked found" = `GET` by `status.userPk`
   whose username is still `spec.username` (changed at build: an account
   renamed in Authentik since it was tracked is a `Conflict`, "the account pk N
   is now named X, not Y; refusing to manage it", and nothing is patched; plan
   Ruling 15);
   "own create pending" = `status.invitation` present (the whole inviting
   phase, not only the request in flight); "own create proven" = the
   attribute query of step 5 found exactly one account; "target" = find by
   `spec.username`; `RecreateVanished` = false. The table test includes
   these User rows.
2. The connection must have an explicit browser-facing base URL
   (`AuthentikDeployment` `spec.connection.externalURL`, or
   `--authentik-external-url` for the default connection, both existing
   settings used today for credential Secrets). `connection.Resolved` today
   folds `url` into `ExternalURL` (`internal/connection/resolver.go:131,185`),
   so it gains an explicit `ExternalURLSet` flag. The chart default
   `externalUrl: ""` must be set for invitations. Without one: `Ready=False`,
   reason `ExternalURLRequired`; no invitation is created. (The connection
   `url` is usually an in-cluster address a person cannot open.)
3. The operator creates an Authentik invitation: `single_use: true`, `flow`
   resolved from `invitation.flow` by slug, `name: k8s-<metadata.uid>-<attempt>`,
   `expires` = now + `expiresAfter` (default 7 days, applied in the
   controller), `fixed_data: {"username": <username>, "name": <name if set>,
   "attributes": {"authentik_operator": {"owner_uid": <metadata.uid>}}}` (a
   whole `attributes` dict, merged by the user-write stage,
   `stages/user_write/stage.py:125-129`, same on 2026.2.3). Authentik
   generates the pk, so `status.invitation.pending: true` and the name are
   persisted **before** the request, and a lost response is recovered by
   listing invitations filtered by that exact name. Names are not unique in
   Authentik: several invitations with the name are `Ready=False`, reason
   `InvitationAmbiguous` (the resource issues and deletes none, and deletion
   stays Terminating), and so is a single one that does not carry this
   resource's username, single use and ownership proof, which is neither linked
   nor deleted. The flow slug is resolved before anything is issued or replaced:
   an unknown slug is `Ready=False`, reason `ReferenceNotFound`. (Both reasons
   added at build: plan Ruling 26 and the plan-gate pass 1 minors disposition;
   flow lookup per plan Task 9.)
4. **The pk never leaves the Secret.** Invitation API errors are wrapped so
   the request path (which carries the pk, `internal/authentik/errors.go`
   puts paths into messages) is redacted before an error reaches a
   condition, event or log; a test asserts that no condition, event or log
   line contains the pk across create, delete, recovery and failure paths.
   4. The link `<externalURL>/if/flow/<slug>/?itoken=<pk>` is written to a Secret
   (`invitation.secretName`, default `<metadata.name>-invitation`, keys `url`
   and `expires`), owned by the CR, never written to status, events or logs.
   Existing-Secret rules are those of TASK-04 (`SecretConflict`).
5. `Ready=False`, reason `InvitationPending`, until an account carrying
   `attributes.authentik_operator.owner_uid == metadata.uid` exists, found by
   the attribute query `GET /core/users/?attributes={"authentik_operator__owner_uid":"<uid>"}`
   (Django lookup syntax, `core/api/users.py:598-613`; the server returns
   **every** user on malformed input), and every result is re-checked
   client-side for that exact attribute value, as `internal/authentik/lookup.go`
   already requires. More than one account carrying the proof (an earlier
   attempt enrolled under another name): `Ready=False`, reason
   `MultipleEnrolments`, naming them; nothing adopted. This search runs
   whenever no account is recognised yet, including in the
   `InvitationConsumed` state below. Then:
   - its username equals `spec.username`: provenance `Created`, `userPk`
     recorded, invitation status cleared, Secret deleted (the link is spent);
   - it differs (the enrolment flow's username prompt was not read-only and
     the person typed another name): `Ready=False`, reason
     `EnrolledUnderOtherName`, message naming it; the account is not adopted.
     Fix the blueprint, then delete that account or change `spec.username`
     on a new CR.
6. An account named `username` **without** the proof appears while inviting:
   `Conflict`; the invitation and the Secret are deleted.
7. The invitation is gone before any account carries the proof (opened and
   abandoned on another device, deleted by an admin, or expired: Authentik
   deletes a single-use invitation as soon as its first stage runs):
   `Ready=False`, reason `InvitationConsumed` (gone before
   `status.invitation.expires`) or `InvitationExpired` (past it; decided from
   status, since 2026.2.3 keeps expired rows listed until cleanup); an expired
   invitation that is still listed is deleted in Authentik too (changed at build:
   it serves anti-goal 4, no live sign-up link outside the Secret; plan Task 9
   `checkInvitation`); the Secret is deleted; **no automatic reissue**. A normal enrolment passes
   through this state (Authentik deletes the invitation at its first stage,
   before the account exists), so step 5's search keeps running and moves
   the CR on as soon as the account appears.
8. `attempt` is an integer, default 1, minimum 1; `expiresAfter` is a
   `metav1.Duration`. Bumping `attempt` after the user is `Created` does
   nothing.
8. **Reissue** happens only when `spec.invitation.attempt` is increased
   (GitOps-visible, one link per explicit request): the operator deletes any
   live invitation of the previous attempt, issues `k8s-<uid>-<attempt>`, and
   rewrites the Secret. Recovery after a lost response looks only for the
   current attempt's name.
9. Account created but no authenticator enrolled (the person stopped after
   the user-write stage, which in the owner's blueprint runs before the
   passkey stage): the account is still recognised and `Created`, and a
   condition `AuthenticatorMissing` (Warning) is set while
   `GET /authenticators/admin/all/?user=<pk>` lists none. The remedy is a
   recovery link from the admin UI (section 10: recovery flow), not a
   re-invite (the username is taken).
10. Switching the policy away from `Create` / `AdoptOrCreate`, or deleting
    the CR, while inviting deletes the live invitation in Authentik; the
    Secret goes with it (owner reference). A namespace the identity gate stops
    allowing while inviting has its invitation and Secret deleted before the
    resource reports `IdentityNotAllowed`; `status.invitation` stays. A switch to
    `ReadOnly` or `Adopt` is thus the one Authentik write `ReadOnly` performs, in
    the safe direction; `dryRun` under `AdoptOrCreate` mid-invitation leaves the
    invitation live. Editing `invitation.flow`, `expiresAfter` or `secretName`
    while inviting takes effect on the next attempt.

Owner blueprint change (documented, not automated): the enrolment flow's
username prompt becomes `text_read_only`, so `fixed_data.username` wins
(`stages/prompt/stage.py:121-140`). Without it step 5's
`EnrolledUnderOtherName` applies. The e2e uses a blueprint with this change.

Other behaviour
- A **Created** user that vanishes from Authentik is **not** re-invited
  (`RecreateVanished: false`): deleting a person in the UI is taken as intent
  to remove them. `Ready=False`, reason `AdoptionTargetNotFound`, `userPk`
  kept; clear status and bump `attempt` to invite again.
- Adopt / ReadOnly as #18 (find by `username`). Refuses to adopt
  `service_account` and `internal_service_account` users (`Conflict`, humans
  only).
- Never deactivates the operator token's own user.
- PATCH only (a PUT clears groups and roles).
- Deletion: unset `deletionPolicy` resolves to **`Retain` for every
  `AuthentikUser`, whatever its provenance** (passkeys are costly to
  re-enrol; pocket-id-operator retains users by default); an explicit
  `Delete` still deletes. A retained account stays active: set
  `isActive: false` first to offboard. A documented, per-kind exception to
  `EffectiveDeletionPolicy` (section 8, call 4). An account the resource
  created is deleted only if it still carries the resource's ownership proof;
  otherwise a Warning `Retained` is emitted and the account stays.
- Gates as 5.2.

Implemented in:
[`api/v1alpha1/authentikuser_types.go`](../../api/v1alpha1/authentikuser_types.go),
[`authentikuser_controller.go`](../../internal/controller/authentikuser_controller.go)
(reconcile, gates, deletion),
[`user_desired.go`](../../internal/controller/user_desired.go) (fields and the
invitation request),
[`user_invitation.go`](../../internal/controller/user_invitation.go) (the
invitation lifecycle and its Secret),
[`internal/authentik/user.go`](../../internal/authentik/user.go),
[`invitation.go`](../../internal/authentik/invitation.go) and
[`owner.go`](../../internal/authentik/owner.go) (the ownership-proof single
source).

### 5.3a Attributes: per-key, owned keys only (owner decision)

Authentik replaces the whole `attributes` dict on every API write
(`core/api/utils.py:69-97`) and writes its own keys there (`settings`,
`goauthentik.io/user/...`, sync data), so the operator owns only the keys it
lists, the model kaniop uses for SSH keys:

- `spec.attributes` is `map[string]apiextensionsv1.JSON` (values: string,
  number, bool, list), top-level keys only, at most 64 (on groups and users;
  limit added at build, plan Task 6/9 API steps); a listed key's value is owned
  whole.
- `status.managedAttributes` records the keys the operator owns. Each sync
  reads the live dict, sets the listed keys, deletes keys it owned and no
  longer lists, keeps every other key, and PATCHes only when something
  differs (narrowing the lost-write window: Authentik has no
  compare-and-set; stated, accepted).
- Absent `spec.attributes`: nothing read or written (previously owned keys
  are released, not deleted). `{}`: removes the owned keys.
- Reserved, refused by the reconciler (`Ready=False`, reason `InvalidSpec`,
  nothing written): keys starting `goauthentik.io/`, `authentik_operator`
  (ownership proof), `settings` (the user's own settings), and on users
  `given_name` and `family_name` (owned by `firstName` / `lastName`, one writer
  per key). A spec with a reserved key is therefore admitted and fails at
  reconcile (`reservedAttributes` in
  [`attributes.go`](../../internal/controller/attributes.go)). (Changed at
  build, not CEL: CEL cannot type a map of untyped JSON values; ledger "Task 6:
  Ruling: reserved attribute keys ... refused by the controller".)
- Adoption keeps every live key; the dry run lists keys it would set or
  remove. ReadOnly reports differing owned keys in `Drifted`.
- An Authentik 2026.8 attribute schema (`ObjectAttribute`) can reject a
  value: reported as `InvalidSpec` with Authentik's 400 message.

### 5.4 AuthentikApplication.spec.access (decision 2)

```yaml
access:
  type: Groups        # or Unmanaged
  groups: [immich-users]   # Authentik group names; required iff type Groups
```

- Required (CEL) under every policy except `ReadOnly` (optional there; drift
  reported). `type` is a union discriminator (Kubernetes API conventions).
- Not to be confused with the existing `spec.group` (the library section
  label); the field docs say so.
- **Binding target safety (design gate):** bindings are listed with
  `target=<application pk>` (the application's UUID primary key, which is its
  policy-binding-model id). An empty pk is an error, never a query: on 2026.2.3
  the application response has no `pbm_uuid`, the compat shim makes it empty,
  and django-filter ignores an empty `target=`, which would return **every
  binding in the instance**. Before any delete the operator also checks
  `binding.target == application pk`.
- `access.groups` holds 1 to 64 names of 1 to 255 characters each (limits added
  at build: plan Task 8 API step; the JeffResc converter refuses a source over
  them, 5.6).
- `Groups`: the app's **group** bindings are exactly the listed groups. For
  each, the operator holds `enabled: true`, `negate: false`; adds missing ones
  (`order` = one past the highest existing order) **before** removing
  unlisted ones and duplicates.
- **Removal only from a complete picture:** if any listed group fails to
  resolve (missing, unreadable to the token, or renamed in the UI) the
  operator adds what it can and **removes nothing** in that reconcile;
  `AccessInSync=False`, reason `GroupNotFound`. A binding for a renamed group
  therefore survives until the spec is fixed.
- Group bindings with `expiring: true` (2026.5+ temporary grants) are left
  alone and reported, like user and policy bindings. **Negated** group
  bindings follow the live `policyEngineMode`: under `any` a negated binding
  on G admits every user outside G (it widens access), so it is removed;
  under `all` it excludes G (removing it would widen access), so it is left
  alone and reported. (Corrected at plan time.)
  `policyEngineMode` follows #18: spec wins, unset keeps live, `any` on create;
  CEL rejects a spec `all` with more than one group. A live `all` with more
  than one listed group (adopted) is left alone, since it only narrows access,
  and reported (`AccessInSync=False`, reason `AccessNarrowed`). The same reason
  reports a negated binding of a listed group under a live `all`, which excludes
  that group (changed at build: plan Ruling 13).
- Conditions: `Ready` keeps its meaning (the application itself; blocking
  reasons include the new `WaitingForGroups` and `AccessPending`); a new
  `AccessInSync` (`Groups` only; reasons `GroupNotFound`, `AccessNarrowed`,
  `AuthentikError` (the binding list or a binding write failed), `DriftDetected`
  (under `ReadOnly`, the bindings differ from the spec: ReadOnly access drift is
  reported here, not in `Drifted`, and never as `AccessNarrowed`) and `InSync`);
  and a new `AccessOpen` (`True`, reason `NoBindings`, when the app has no
  enabled binding of any kind, so every user may open it unless the instance
  setting is off; `False`, reason `Bound`). A binding problem never flips
  `Ready`, except `AccessPending` and `WaitingForGroups`. (`AuthentikError`
  and `DriftDetected` added at build: plan Rulings 12 and 13.)
  User and policy bindings are untouched but listed in status and the adoption
  dry run as "also grants access".
- `Unmanaged`: bindings never written; status lists them; with none, a
  `AccessOpen=True`.
- **Never zero bindings by the operator's hand:** under `Groups`, the operator
  does not create the application, and does not take over (adopt-write) an
  existing one, until at least one listed group resolves; until then
  `Ready=False`, reason `WaitingForGroups`. Once created, the bindings are
  written in the same reconcile, right after the create; the seconds between
  the two are the only window, closed by the instance setting on 2026.5+.
  Until a listed group has a managed binding on an application this resource
  created (new, recreated, or created earlier whose listed bindings were since
  removed), and while the bindings of a just-created application cannot be read,
  `Ready=False` with reason `AccessPending`. The requeue is one minute when no
  write failed, and error backoff when the binding write failed, which also
  emits a Warning event. (Changed at build: plan-gate pass 1 S2 disposition and
  plan Ruling 11, AccessPending until the first listed binding exists on a new,
  recreated or Created app; Ready is set once per reconcile, Task 8 fix round.)
- Group names resolved on every reconcile (not pk-pinned).
- Adoption dry run lists bindings to add and remove by group name.
- Watches: an `AuthentikGroup` change re-enqueues applications listing its
  name (field index on `access.groups`).
- The application's existing provider-link flow and lost-create recovery are
  unchanged.
- Docs recommend turning off the instance setting "Applications with no
  policies bound can be accessed by any user" (2026.5+); the operator never
  changes it.

Implemented in:
[`api/v1alpha1/authentikapplication_types.go`](../../api/v1alpha1/authentikapplication_types.go),
[`application_access.go`](../../internal/controller/application_access.go)
(binding plan, conditions, `AccessPending`) called from
[`authentikapplication_controller.go`](../../internal/controller/authentikapplication_controller.go),
and [`internal/authentik/binding.go`](../../internal/authentik/binding.go).

### 5.4a Authentik 2026.2.3 compatibility

- The 2026.8.5 Go client requires `expires` and `expiring` on a policy
  binding; 2026.2.3 does not send them, so every binding read fails to decode
  there. Add a `compatTransport` rule (`internal/authentik/compat.go`) with a
  recorded 2026.2.3 response under `testdata/`, as for the existing rules.
- Every other Authentik call this design adds (groups and `add_user` /
  `remove_user`, `used_by`, users with an `attributes` filter, invitations
  with a `name` filter, authenticators) is checked against 2026.2.3 the same
  way before it is relied on; the e2e leg on 2026.2.3 is the backstop.

### 5.5 Identity gate (decision 3)

- Flag `--default-connection-identity-namespace-selector` (chart value),
  parsed at startup like the adoption selector; invalid stops the operator.
- On the default connection, `AuthentikGroup` and `AuthentikUser` reconcile
  only in matching namespaces, under every policy; empty denies; reason
  `IdentityNotAllowed`, checked before the adoption gate. `spec.authentikRef`
  is not gated.
- Outside the selector, deletion removes only `Created` objects; adopted or
  undecided are retained with a Warning.
- Owner's value: `kubernetes.io/metadata.name in (authentik)` in both this and
  the adoption flag.

Implemented in
[`identity_gate.go`](../../internal/controller/identity_gate.go) (the gate) and
[`identity_status.go`](../../internal/controller/identity_status.go) (the status
plumbing and claim re-check both new kinds share, see 5.1); the selector is
parsed in [`cmd/main.go`](../../cmd/main.go).

### 5.6 JeffResc converter

- `groupBindings: [g...]` -> `access: {type: Groups, groups: [g...]}`; no
  `groupBindings` -> `access: {type: Unmanaged}` with a comment that live
  bindings are unknown offline and the dry run lists them.
  `--accept-unmanaged-group-bindings` is removed.
- JeffResc `AuthentikGroup` -> `AuthentikGroup` with `managementPolicy: Adopt`,
  `deletionPolicy: Retain`, `name`, `isSuperuser` if set, and `spec.dryRun` per
  `--dry-run-adoption`, as for providers and applications; `members` absent
  (JeffResc never managed membership). JeffResc `attributes` convert to
  `attributes` (owned per key, minus reserved keys, reported); a key whose value
  is an object or `null` is dropped and reported too, since a key is owned whole
  and the CRD takes no such value. `parents`, `roles` are
  not converted (they stay live, unmanaged); the report lists them.
- Converter behaviour added at build (plan-gate pass 3 S1 disposition; ledger
  "Task 8: Ruling: converter badGroupBindings ..."):
  - the CRD refuses `policyEngineMode: all` with several listed groups, so a
    JeffResc `all` with more than one `groupBindings` entry is left unset in the
    converted application and the report says the live value is kept and
    reported as `AccessNarrowed`;
  - duplicate `groupBindings` names are collapsed to one, with a note;
  - a source with more than 64 `groupBindings` entries, or one empty or longer
    than 255 characters, is refused (the limits of `spec.access.groups`, 5.4);
  - the report's identity-gate hint names the namespaces of the converted
    groups (for example `kubernetes.io/metadata.name in (media)`), not only the
    owner's recommended `kubernetes.io/metadata.name in (authentik)` (5.5).
- Golden fixtures updated; runbook gains a step for groups.
- `spec.access` becomes required, so `config/samples`, the e2e manifests
  (`test/e2e`), the chart docs and `docs/migration-from-jeffresc.md` examples
  are updated in the same change.

### 5.7 Chart, RBAC, docs

- Chart value and template wiring for the new flag (hand-owned
  `templates/manager/manager.yaml`, `values.yaml`, with a helm-unittest suite
  first).
- RBAC markers: groups/users/policy bindings/invitations are Authentik API
  calls, no new Kubernetes RBAC beyond Secrets already granted;
  `make manifests` + `make chart-regen`.
- `scope.md`: kind table, identity gate, `spec.access`, group and user
  sections; TASK-05 and TASK-10 notes; migration doc.

### 5.8 Test plan

- Unit: `adopt.Decide` table test (row per `scope.md` line); member diffing;
  attribute merging (owned keys set and removed, foreign keys kept, no write
  when equal, reserved keys refused by the reconciler, see 5.3a);
  binding diffing (enabled/negate/duplicates/ordering).
- envtest + `fakeauthentik`: extend the fake with groups (incl. `add_user` /
  `remove_user`, attributes), users (incl. type, is_active), policy bindings,
  invitations and flows by slug. The operator does not read the instance
  setting (it needs admin-settings permission and does not exist on 2026.2);
  `AccessOpen` says "open unless the instance setting is off". Suites per kind mirroring
  the provider adoption suite: create, adopt, ReadOnly, dryRun, conflict,
  lost create, vanished, claims tie-break, gates, deletion policies, members
  (unknown, pinned pk changed, token user kept), invitation lifecycle
  (pending, enrolled with/without owner attribute, enrolled under another
  name, consumed-then-enrolled race, expired, explicit reissue via
  `attempt`, deleted while pending, pk never in conditions/events/logs), access (Groups/Unmanaged, never through zero bindings,
  missing group keeps bindings).
- Real-Authentik e2e on 2026.2.3 and 2026.8.5: a person enrols through the
  blueprint (read-only username) from the operator's invitation and is
  adopted as Created; group membership grants access to an app with
  `access.type: Groups` and a non-member is refused at authorize; JeffResc
  migration with a group and `groupBindings`. Enrolment is driven through the
  flow executor API (`/api/v3/flows/executor/<slug>/`) on a **test-only copy
  of the owner's enrolment flow without the passkey stage** (invitation,
  read-only username prompt, user write, login): the operator's contract ends
  at "invitation -> account carrying the owner proof"; passkey enrolment is
  Authentik's and is not re-tested here.

## 6. Order of work (for the plan)

One release (owner, builder check Q2): v0.1.0 ships everything below
together, once green on 2026.2.3 and 2026.8.5; release PR #33 stays held until
then.


0. Devcontainer tooling (added at build: owner run-contract decision "Fix
   devcontainer first"; the git source build and pinned tools are the owner's
   choice, and Renovate tracks them through a regex manager in
   `.github/renovate.json`; ledger Task 1 rulings). `.devcontainer/post-install.sh`
   builds git from source and installs pinned `task`, `helm`, `helm-unittest`,
   `yq`, `golangci-lint`, `govulncheck`, `hadolint` and `yamllint`, so `task
   test` and `make lint` run the CI contract inside the devcontainer.
1. `internal/adopt` (decision function + helpers, unit tests).
2. `fakeauthentik` extensions.
3. `AuthentikGroup` API + controller.
4. `AuthentikApplication.spec.access` + converter `groupBindings` mapping.
5. Identity gate flag + chart.
6. `AuthentikUser` API + controller (invitation lifecycle).
7. Converter `AuthentikGroup` mapping; samples and e2e manifests for the
   required `spec.access`; docs; e2e.

## 7. Trust boundaries (summary)

- Kubernetes RBAC decides who writes the CRs (chart helper roles off by
  default, not aggregated).
- The identity gate decides whether the operator's shared token acts on group
  and user CRs; the adoption gate as before.
- Inside an allowed namespace: full power over users and groups, superuser
  included, and invitation Secrets readable by anyone who reads Secrets there.
- Residual: Authentik creates `ak-<provider name>-client_credentials` users
  on demand; never list them in groups.
- Removing someone from an access group stops new sign-ins only: refresh
  tokens already issued are not re-checked against bindings (only
  `client_credentials` and token exchange call the policy check). To cut a
  person off at once, deactivate them (below). Revoking an app's refresh
  tokens on removal is a follow-up.
- Deactivating a user deletes their tokens.

## 8. Calls I made (owner: please check)

1. Group fields `name`, `isSuperuser`, `members`, `attributes` (per key, owner
   decision); `parents`, `roles` unmanaged.
2. Group lost-create proof via an `authentik_operator.owner_uid` attribute written
   on create (a reserved key).
3. Group `name` immutable once tracked.
4. `AuthentikUser` deletion default `Retain` regardless of provenance (an
   exception to `EffectiveDeletionPolicy`).
5. Invitation: 7-day default expiry; **reissue only when
   `spec.invitation.attempt` is increased** (changed after the design gate:
   automatic reissue would keep a valid link alive forever); Secret
   `<name>-invitation` with key `url`; Secret deleted once enrolled.
6. (Owner decided, builder check Q1; strengthened after the design gate) A
   group is not deleted while **any live binding in Authentik** uses it, not
   only while a CR lists it: see 5.2 "Deletion guard".
7. JeffResc `AuthentikGroup` converts to `Adopt` + `Retain`, membership
   absent.
8. SAML provider and Deployment Managed mode are separate designs.
9. `policyEngineMode` follows #18 (spec wins, unset keeps live); a live `all`
   that narrows access is reported, not rewritten.
10. `isSuperuser` held to `false` on created groups when unset.
11. A vanished created user is reported, not re-invited.
12. Under `Groups`, an app is not created or adopt-written until a listed group
    resolves (`WaitingForGroups`).
13. Invitation lost-create marker is the name `k8s-<uid>-<attempt>`; ownership proof is a
    nested `attributes` dict.
14. The operator does not read Authentik's instance setting.
15. Invitations need an explicit `externalURL` on the connection
    (`ExternalURLRequired` otherwise).
16. Members whose username is still being invited by an `AuthentikUser` are
    held out (`MemberHeld`) until that user is recognised.
17. The enrolled account is found by its ownership attribute, not by
    username (`EnrolledUnderOtherName` when they differ).
18. `AuthenticatorMissing` warning when a recognised person has no
    authenticator.
20. A group whose bindings cannot be fully seen (token not superuser) is
    retained, not deleted, on CR deletion (`RetainedUnverifiable`).
19. `Facts` grows from six to eight (own create pending, `RecreateVanished`)
    so the User kind needs no branch; a found-but-unproven target during a
    pending create is `Conflict` under every policy.

## 9. Open questions / not verified

- Closed by the second gate pass from source: `used_by` (identical on both
  versions, permission-filtered, entries carry the binding pk and a text
  name), `/authenticators/admin/all/?user=` and the users `attributes` filter
  exist on 2026.2.3.
- Whether a 2026.2.3 invitation response decodes with the 2026.8.5 Go
  client (fields match; not run). Add a compat rule if it does not.

- `fixed_data` with an `attributes` dict on 2026.2.3 (read on 2026.8.5 only;
  the e2e leg on 2026.2.3 verifies it).
- Whether apps end their own sessions when a user is deactivated.
- Runtime behaviour generally: everything above is read from source.

## 10. Follow-ups (not this task)

- Migrate provider and application controllers onto `internal/adopt` after
  v0.1.0; pin the divergence found (undecided provenance + `Adopt` + vanished
  object: provider recreates, application refuses).
- Machine accounts and tokens.
- `AuthentikInvitation` kind if invitations are wanted without users.
- `parents` on groups (setting it is itself an access grant).
- Recovery flow in the owner's blueprint (lost passkey without a new account).
