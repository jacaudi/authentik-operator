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

// Package controller holds the reconcilers for the authentik.io custom
// resources: one per kind, plus the helpers they share.
package controller

import (
	"context"
	"errors"
	"fmt"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// Condition reasons shared by the kinds that support adoption.
const (
	// reasonAdoptionDryRun marks Ready=False while spec.adoption.dryRun
	// reports what adoption would change.
	reasonAdoptionDryRun = "AdoptionDryRun"
	// reasonAdoptionTargetNotFound marks an explicit spec.adoption.match
	// that names no existing object.
	reasonAdoptionTargetNotFound = "AdoptionTargetNotFound"
)

// conditionError is a problem that retrying cannot fix: a reference that
// does not resolve, a conflict with an object this resource does not own, a
// connection that cannot be built from the cluster state. It is reported in
// a status condition under reason and retried on the slow interval; any
// other error is transient and backs off. The message wraps the cause, so
// errors.Is still sees it.
type conditionError struct {
	reason string
	err    error
}

func (e *conditionError) Error() string { return e.err.Error() }
func (e *conditionError) Unwrap() error { return e.err }

// condErr reports err under reason.
func condErr(reason string, err error) error {
	return &conditionError{reason: reason, err: err}
}

// condErrf reports a formatted message under reason; %w keeps a cause.
func condErrf(reason, format string, args ...any) error {
	return &conditionError{reason: reason, err: fmt.Errorf(format, args...)}
}

// conditionReason is err's condition reason, or fallback for any other error.
func conditionReason(err error, fallback string) string {
	if ce, ok := errors.AsType[*conditionError](err); ok {
		return ce.reason
	}
	return fallback
}

// resolve is the connection boundary: a *connection.Error becomes a
// conditionError here, so the controllers classify one error type and the
// connection package stays independent of this one.
func resolve(ctx context.Context, r *connection.Resolver, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (*connection.Resolved, error) {
	resolved, err := r.ForRef(ctx, namespace, ref)
	if reason := connection.Reason(err); reason != "" {
		return nil, condErr(reason, err)
	}
	return resolved, err
}

// resolveForDeletion resolves the connection for a finalizer. When the
// connection's objects are already gone (namespace teardown deletes the
// token Secret first), it falls back to the last client built for that
// connection so cleanup can still reach Authentik.
func resolveForDeletion(ctx context.Context, r *connection.Resolver, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (*connection.Resolved, error) {
	resolved, err := resolve(ctx, r, namespace, ref)
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
	switch conditionReason(err, "") {
	case connection.ReasonDeploymentNotFound, connection.ReasonConnectionNotConfigured,
		connection.ReasonSecretNotFound:
		return true
	}
	return false
}

// managedField is one field a patch builder compares. apply copies the
// desired value into the patch; it runs only when same is false.
type managedField struct {
	name  string
	same  bool
	apply func()
}

// diff is the managedField for a plain comparable value with a setter.
// Fields that need a bespoke comparison build the struct directly.
func diff[T comparable](name string, want, got T, set func(T)) managedField {
	return managedField{name: name, same: want == got, apply: func() { set(want) }}
}

// changedFields applies every field that differs and returns their names.
func changedFields(fields []managedField) []string {
	var changed []string
	for _, f := range fields {
		if !f.same {
			f.apply()
			changed = append(changed, f.name)
		}
	}
	return changed
}

// sameAuthentikRef reports whether two objects use the same connection.
func sameAuthentikRef(a, b *authentikiov1alpha1.AuthentikRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Name == b.Name
}
