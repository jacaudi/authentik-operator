# Scope and binding decisions

Decisions made before scaffolding. These are the expensive-to-retrofit ones —
API group, kind names, and the adoption contract. Change them now or not at all.

## Origin

A Go rewrite of `github.com/dsluijk/authentik-operator` (Rust, v0.2.5,
last commit 2022-12-13, archived). The original and its analysis live outside
this repo under `source/` and `docs/refactor/`, both gitignored.

## Framework

Kubebuilder v4 + controller-runtime, matching `cloudflare-operator`,
`nextdns-operator`, `nomad-operator` and `towonel-operator`. `config/` is kept
as the codegen target, consistent with those repos.

## API

- group `authentik.io`, version `v1alpha1`, all kinds namespaced
- `internal/authentik/` holds the Authentik REST client, mirroring
  `towonel-operator`'s `internal/towonel/`

| Kind | Manages |
|---|---|
| `AuthentikDeployment` | the Authentik install itself — Deployment, Service, Ingress, RBAC |
| `AuthentikApplication` | an application in Authentik |
| `AuthentikUser` | a user |
| `AuthentikGroup` | a group |
| `AuthentikOAuthProvider` | an OAuth2 provider |

### Why `AuthentikDeployment` and not `Authentik`

The upstream kind was bare `Authentik`. Renamed so the kind says what it does
and does not read as a catch-all for the whole API group.

## Adoption — `AuthentikDeployment` must take over existing deployments

**Requirement:** an `AuthentikDeployment` pointed at an Authentik that already
exists must adopt it, not fail and not fight it.

The upstream operator could only ever create. It applied every object with
server-side apply, `.force()`, field manager `authentik.ak-operator`, and a
hand-written `ownerReferences` block — so pointing it at an existing install
would either stomp another manager's fields or conflict outright.

What adoption has to cover:

1. **Pre-existing `Deployment`, `Service`, `Ingress`, `ServiceAccount`** with
   the expected names are adopted: labels and an `ownerReference` are added,
   and fields the CR does not speak to are left alone.
2. **Another field manager already owns fields.** Resolve deliberately rather
   than reaching for `.force()` — a blind force is what makes takeover
   destructive.
3. **An existing Authentik with its own admin token.** The operator must accept
   a supplied token (secretRef) instead of requiring the bootstrap-token dance.
4. **Adoption is reversible.** Deleting the CR must be able to release the
   objects rather than cascade-delete an install the operator did not create.

Open: whether adoption is implicit (adopt anything matching by name) or
explicit (`spec.adopt: true`, or an annotation on the target). Explicit is
safer and is the likely answer, but it is not decided yet.

## Known upstream defects this rewrite exists to fix

From `docs/refactor/authentik-operator-code-map.md` §9, condensed:

- bootstrap token is a compile-time constant published in the repo
- plaintext HTTP to a hardcoded in-cluster address, unconfigurable
- generated client IDs/secrets written back into the CR spec in plaintext
- no pagination on any list endpoint
- every object resolved by display name, so renames orphan the CR
- drift detection by `serde_json::to_string` equality
- `400` mapped to `NotFound` on three delete paths
- zero tests

Plus seven Authentik API breaks since 2022 — see
`docs/refactor/authentik-api-drift.md`. The two silent ones
(`Group.parent` → `parents`, `token_validity` split) matter most, because the
upstream code reports success while losing data.
