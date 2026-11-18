# authentik-operator
A Kubernetes operator that manages objects in an [Authentik](https://goauthentik.io)
server (applications, OAuth2 providers, groups, people) from custom resources in
the `authentik.io/v1alpha1` API group.

## Description

| Kind | Manages |
|---|---|
| `AuthentikDeployment` | the connection to an Authentik server (`ConnectOnly`); managing the install itself (`Managed`) is not built yet |
| `AuthentikApplication` | an application, and who may open it (`spec.access`) |
| `AuthentikOAuthProvider` | an OAuth2 provider |
| `AuthentikGroup` | a group, its attributes and (optionally) its members |
| `AuthentikUser` | a person: adopted, or invited to enrol |
| `AuthentikCredentialsTemplate` | (cluster-scoped) the keys of rendered credential Secrets |

Every kind except `AuthentikCredentialsTemplate` is namespaced. Examples are in
[config/samples](config/samples); the decisions behind the behaviour are in
[docs/design/scope.md](docs/design/scope.md).

### Application access (breaking change)

`spec.access` on `AuthentikApplication` is **required** (except under
`managementPolicy: ReadOnly`): an application manifest without it is rejected.
Add one of:

```yaml
spec:
  access:
    type: Groups              # bind exactly these Authentik groups
    groups: [immich-users]
  # or: access: {type: Unmanaged}   # never write a binding; status lists the live ones
```

`spec.access` says who may open the application. It is not `spec.group`, the
section label in the library. Under `Groups` the application is not created
until one of the listed groups exists (`Ready=False`, reason `WaitingForGroups`).
An application with no enabled binding is open to every user unless the
Authentik instance setting "Applications with no policies bound can be accessed
by any user" (2026.5+) is off; the operator never changes it. See
[Application access](docs/design/scope.md#application-access-task-10).

### Groups and users

- `AuthentikGroup.spec.members` (Authentik usernames) is authoritative when set
  and never read or written when absent. Attributes are owned per key.
- `AuthentikUser` is for people, not service accounts. With the default
  `managementPolicy` (`Create`) the operator issues a single-use Authentik
  invitation and writes the sign-up link to a Secret (keys `url` and `expires`,
  named `<metadata.name>-invitation` unless `spec.invitation.secretName` is
  set). The link is built from the browser-facing URL (`authentik.externalUrl`
  in the Helm chart, `--authentik-external-url`); without it the resource
  reports `ExternalURLRequired`. Anyone who can read Secrets in that namespace
  can read the link. An unset `deletionPolicy` is `Retain` for every user.
- On the operator's default connection, groups and users are reconciled only in
  namespaces matched by `--default-connection-identity-namespace-selector` (Helm
  value `identity.defaultConnectionNamespaceSelector`), under every
  `managementPolicy`. The default is empty, which denies every namespace
  (`IdentityNotAllowed`). Resources with `spec.authentikRef` are not affected.
  Adoption is gated separately by `--default-connection-adoption-namespace-selector`.

See [Groups](docs/design/scope.md#groups-task-10) and
[Users](docs/design/scope.md#users-task-10).

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/authentik-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/authentik-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/authentik-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/authentik-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

