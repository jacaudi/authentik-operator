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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// Condition types and reasons shared by the kinds that support adoption.
const (
	// ConditionDrifted reports, under managementPolicy ReadOnly, whether the
	// object in Authentik differs from the spec. It is absent under every
	// other policy, which reconciles the difference instead.
	ConditionDrifted = "Drifted"

	// reasonAdoptionDryRun marks Ready=False while spec.dryRun reports what
	// adoption would change.
	reasonAdoptionDryRun = "AdoptionDryRun"
	// reasonAdoptionTargetNotFound marks an adoption (or ReadOnly) target
	// that names no existing object, or an adopted object that vanished.
	reasonAdoptionTargetNotFound = "AdoptionTargetNotFound"
	// reasonReadOnly marks Ready=True for a ReadOnly resource that found and
	// observes its object.
	reasonReadOnly = "ReadOnly"
	// reasonDriftDetected and reasonInSync are the Drifted reasons.
	reasonDriftDetected = "DriftDetected"
	reasonInSync        = "InSync"
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

// readOnly reports whether the policy is ReadOnly: the resource finds its
// object and observes it, and never writes to Authentik.
func readOnly(mp authentikiov1alpha1.ManagementPolicy) bool {
	return mp.Resolve() == authentikiov1alpha1.ManagementPolicyReadOnly
}

// adoptVerb is what a policy that may adopt does with the object it finds,
// for messages.
func adoptVerb(mp authentikiov1alpha1.ManagementPolicy) string {
	if readOnly(mp) {
		return "observe"
	}
	return "adopt"
}

// recordDrift sets the Drifted condition from the patch that reconciling
// would apply: True with the differing fields (as "field: live -> spec"), or
// False when there is none.
func recordDrift(conds *[]metav1.Condition, generation int64, patch, live any, changed []string) {
	cond := metav1.Condition{
		Type: ConditionDrifted, Status: metav1.ConditionFalse, Reason: reasonInSync,
		Message: "the object in Authentik matches the spec", ObservedGeneration: generation,
	}
	if len(changed) > 0 {
		cond.Status, cond.Reason = metav1.ConditionTrue, reasonDriftDetected
		cond.Message = "the object in Authentik differs from the spec: " + describeChanges(patch, live)
	}
	meta.SetStatusCondition(conds, cond)
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

// sameConnection reports whether two objects, each in its own namespace,
// use the same Authentik: both the operator default, or the same
// AuthentikDeployment.
func sameConnection(nsA string, a *authentikiov1alpha1.AuthentikRef, nsB string,
	b *authentikiov1alpha1.AuthentikRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return nsA == nsB && a.Name == b.Name
}

const (
	maxChangeValueLen   = 100
	maxChangeMessageLen = 2000
)

// describeChanges renders "field: old -> new" for every field a patch sets,
// reading the old value from the object as Authentik has it. The names are
// Authentik's API field names. Values are compact JSON, truncated; the
// client secret is never shown (it is never in a patch, and is skipped
// regardless).
func describeChanges(patch, current any) string {
	toMap := func(v any) map[string]any {
		var m map[string]any
		raw, err := json.Marshal(v)
		if err != nil || json.Unmarshal(raw, &m) != nil {
			return nil
		}
		return m
	}
	render := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			return "?"
		}
		if r := []rune(string(raw)); len(r) > maxChangeValueLen {
			return string(r[:maxChangeValueLen]) + "..."
		}
		return string(raw)
	}
	pm, cm := toMap(patch), toMap(current)
	parts := make([]string, 0, len(pm))
	for _, k := range slices.Sorted(maps.Keys(pm)) {
		if k == "client_secret" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", k, render(cm[k]), render(pm[k])))
	}
	msg := strings.Join(parts, "; ")
	if r := []rune(msg); len(r) > maxChangeMessageLen {
		msg = string(r[:maxChangeMessageLen]) + "..."
	}
	return msg
}
