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

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// Helpers shared by the Authentik API object reconcilers.

// resolveForDeletion resolves the connection for a finalizer. When the
// connection's objects are already gone (namespace teardown deletes the
// token Secret first), it falls back to the last client built for that
// connection so cleanup can still reach Authentik.
func resolveForDeletion(ctx context.Context, r *connection.Resolver, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (*connection.Resolved, error) {
	resolved, err := r.ForRef(ctx, namespace, ref)
	if err != nil && connectionGone(err) {
		if last, ok := r.LastKnown(namespace, ref); ok {
			return last, nil
		}
	}
	return resolved, err
}

// connectionGone reports connection errors caused by the connection's
// objects having been deleted. A missing operator default (NoConnection) is
// a fixable misconfiguration, not "gone".
func connectionGone(err error) bool {
	switch connection.Reason(err) {
	case connection.ReasonDeploymentNotFound, connection.ReasonConnectionNotConfigured,
		connection.ReasonSecretNotFound:
		return true
	}
	return false
}

// sameAuthentikRef reports whether two objects use the same connection.
func sameAuthentikRef(a, b *authentikiov1alpha1.AuthentikRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Name == b.Name
}
