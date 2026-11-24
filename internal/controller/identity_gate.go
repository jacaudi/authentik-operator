/*
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
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// reasonIdentityNotAllowed marks an AuthentikGroup or AuthentikUser on the
// default connection from a namespace the administrator has not allowed.
const reasonIdentityNotAllowed = "IdentityNotAllowed"

// identityGate decides which namespaces may manage groups and users on the
// default connection. Unlike adoptionGate it applies under every
// managementPolicy: even creating a user or group with the operator's own
// token is an administrator's call.
type identityGate struct {
	reader   client.Reader // uncached: the operator has get, not list or watch, on Namespaces
	selector labels.Selector
}

// allowed reports whether the resource's namespace may act on the default
// connection. An explicit spec.authentikRef is never gated: it names a
// same-namespace AuthentikDeployment whose own token bounds what the
// resource can reach. A nil selector denies every namespace. A denied
// namespace is (false, nil); only a failed Namespace read is an error.
func (g identityGate) allowed(ctx context.Context, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (bool, error) {
	if ref != nil {
		return true, nil
	}
	return selectorMatches(ctx, g.reader, g.selector, namespace)
}

// check is allowed as an error: a denied namespace is a conditionError under
// reasonIdentityNotAllowed, which retrying cannot fix until the
// administrator changes the selector.
func (g identityGate) check(ctx context.Context, namespace string, ref *authentikiov1alpha1.AuthentikRef) error {
	ok, err := g.allowed(ctx, namespace, ref)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return condErrf(reasonIdentityNotAllowed, "AuthentikGroup and AuthentikUser on the operator's default "+
		"connection are not allowed in namespace %q; ask an administrator to allow the namespace with "+
		"--default-connection-identity-namespace-selector, or set spec.authentikRef", namespace)
}
