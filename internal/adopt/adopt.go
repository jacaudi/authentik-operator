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

// Package adopt translates the API-object adoption contract
// (docs/design/scope.md, "API-object adoption") into pure decisions for the
// group and user reconcilers.
package adopt

import "github.com/jacaudi/authentik-operator/api/v1alpha1"

// Tracked says whether the object the resource remembers (status) still
// exists in Authentik.
type Tracked string

const (
	// TrackedNone means the resource remembers no object.
	TrackedNone Tracked = ""
	// TrackedFound means the remembered object exists.
	TrackedFound Tracked = "Found"
	// TrackedGone means the remembered object no longer exists.
	TrackedGone Tracked = "Gone"
)

// Target is the outcome of looking the object up by its spec identity.
type Target string

const (
	// TargetUnknown means no lookup was made; Decide never creates or adopts.
	TargetUnknown Target = ""
	// TargetFound means exactly one object matches.
	TargetFound Target = "Found"
	// TargetMissing means no object matches.
	TargetMissing Target = "Missing"
	// TargetAmbiguous means several objects match.
	TargetAmbiguous Target = "Ambiguous"
)

// Facts is everything Decide needs; it performs no I/O.
type Facts struct {
	Policy     v1alpha1.ManagementPolicy
	Provenance v1alpha1.Provenance
	Tracked    Tracked
	Target     Target
	// OwnCreatePending is set while status.pendingCreate records a create
	// whose response may have been lost.
	OwnCreatePending bool
	// OwnCreateProven is set when the found object carries this resource's
	// ownership proof.
	OwnCreateProven bool
	DryRun          bool
	// RecreateVanished is set for kinds that recreate a Created object that
	// vanished (groups); users are never re-invited.
	RecreateVanished bool
}

// Action is what the reconciler does next.
type Action string

const (
	// ActionUseTracked keeps working on the remembered object.
	ActionUseTracked Action = "UseTracked"
	// ActionRecreate creates the vanished object again.
	ActionRecreate Action = "Recreate"
	// ActionTrackedGone reports the remembered object as gone and writes nothing.
	ActionTrackedGone Action = "TrackedGone"
	// ActionRecoverOwnCreate records the found object as Created (a lost create).
	ActionRecoverOwnCreate Action = "RecoverOwnCreate"
	// ActionCreate creates the object.
	ActionCreate Action = "Create"
	// ActionAdopt takes over the found object.
	ActionAdopt Action = "Adopt"
	// ActionObserve reads the found object without writing (ReadOnly).
	ActionObserve Action = "Observe"
	// ActionDryRunAdopt reports that the found object would be adopted.
	ActionDryRunAdopt Action = "DryRunAdopt"
	// ActionDryRunCreate reports that a new object would be created.
	ActionDryRunCreate Action = "DryRunCreate"
	// ActionConflict refuses: the object exists and the policy does not allow
	// taking it, or the lookup was not made.
	ActionConflict Action = "Conflict"
	// ActionTargetNotFound reports AdoptionTargetNotFound and writes nothing.
	ActionTargetNotFound Action = "TargetNotFound"
	// ActionAmbiguous reports several matching objects.
	ActionAmbiguous Action = "Ambiguous"
)

// Decide returns the next action. Precedence, which the scope.md tables
// cannot show: a tracked object wins, then a vanished one by provenance, then
// the lookup by policy.
func Decide(in Facts) Action {
	in.Policy = in.Policy.Resolve()
	switch in.Tracked {
	case TrackedFound:
		return ActionUseTracked
	case TrackedGone:
		switch in.Provenance {
		case v1alpha1.ProvenanceAdopted:
			return ActionTrackedGone
		case v1alpha1.ProvenanceCreated:
			if in.Policy == v1alpha1.ManagementPolicyReadOnly || !in.RecreateVanished {
				return ActionTrackedGone
			}
			return decideRecreate(in)
		}
	case TrackedNone:
	}
	return decideUntracked(in)
}

func decideRecreate(in Facts) Action {
	switch in.Target {
	case TargetFound:
		return foundOwnOrConflict(in)
	case TargetMissing:
		return ActionRecreate
	case TargetAmbiguous:
		return ActionAmbiguous
	case TargetUnknown:
	}
	return ActionConflict
}

// foundOwnOrConflict is the verdict for a found object when only recovering
// this resource's own lost create can excuse it.
func foundOwnOrConflict(in Facts) Action {
	if in.OwnCreatePending && in.OwnCreateProven {
		return ActionRecoverOwnCreate
	}
	return ActionConflict
}

func decideUntracked(in Facts) Action {
	switch in.Target {
	case TargetFound:
		return decideFound(in)
	case TargetMissing:
		return decideMissing(in)
	case TargetAmbiguous:
		return ActionAmbiguous
	case TargetUnknown:
	}
	return ActionConflict
}

func decideFound(in Facts) Action {
	if in.OwnCreatePending {
		return foundOwnOrConflict(in)
	}
	switch {
	case in.Policy == v1alpha1.ManagementPolicyCreate:
		return ActionConflict
	case in.Policy == v1alpha1.ManagementPolicyReadOnly:
		return ActionObserve
	case in.DryRun:
		return ActionDryRunAdopt
	}
	return ActionAdopt
}

func decideMissing(in Facts) Action {
	switch in.Policy {
	case v1alpha1.ManagementPolicyAdoptOrCreate:
		if in.DryRun {
			return ActionDryRunCreate
		}
		return ActionCreate
	case v1alpha1.ManagementPolicyAdopt, v1alpha1.ManagementPolicyReadOnly:
		return ActionTargetNotFound
	case v1alpha1.ManagementPolicyCreate:
	}
	return ActionCreate
}
