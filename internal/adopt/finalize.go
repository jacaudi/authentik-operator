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

package adopt

import (
	"cmp"

	"github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// FinalizeFacts is everything Finalize needs; it performs no I/O.
type FinalizeFacts struct {
	Policy         v1alpha1.ManagementPolicy
	DeletionPolicy v1alpha1.DeletionPolicy
	Provenance     v1alpha1.Provenance
	// UnsetDefault replaces the by-provenance rule when DeletionPolicy is
	// unset (AuthentikUser: Retain for every provenance). Empty keeps the rule.
	UnsetDefault v1alpha1.DeletionPolicy
	// IdentityAllowed is false when the namespace is outside the default
	// connection's identity selector.
	IdentityAllowed bool
}

// Finalization is what deletion does to the Authentik object.
type Finalization string

const (
	// FinalizeDelete deletes the Authentik object.
	FinalizeDelete Finalization = "Delete"
	// FinalizeDeleteUndecided deletes on an explicit policy although the
	// provenance is undecided: a Warning event comes first.
	FinalizeDeleteUndecided Finalization = "DeleteUndecided"
	// FinalizeRetain leaves the Authentik object.
	FinalizeRetain Finalization = "Retain"
	// FinalizeRetainUndecided retains because the provenance is unknown: a
	// Warning event says so.
	FinalizeRetainUndecided Finalization = "RetainUndecided"
	// FinalizeRetainNotAllowed retains an object not Created by a resource
	// outside the identity selector: a Warning event says so.
	FinalizeRetainNotAllowed Finalization = "RetainNotAllowed"
)

// Finalize decides what deletion does. ReadOnly never deletes, then the
// identity selector confines deletion to Created objects, then the effective
// deletion policy applies.
func Finalize(in FinalizeFacts) Finalization {
	if in.Policy.Resolve() == v1alpha1.ManagementPolicyReadOnly {
		return FinalizeRetain
	}
	if !in.IdentityAllowed && in.Provenance != v1alpha1.ProvenanceCreated {
		return FinalizeRetainNotAllowed
	}
	spec := cmp.Or(in.DeletionPolicy, in.UnsetDefault)
	undecided := in.Provenance == ""
	if v1alpha1.EffectiveDeletionPolicy(in.Policy, spec, in.Provenance) == v1alpha1.DeletionPolicyDelete {
		if undecided {
			return FinalizeDeleteUndecided
		}
		return FinalizeDelete
	}
	if undecided && in.DeletionPolicy == "" {
		return FinalizeRetainUndecided
	}
	return FinalizeRetain
}
