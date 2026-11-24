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
	"testing"

	"github.com/jacaudi/authentik-operator/api/v1alpha1"
)

const (
	create  = v1alpha1.ManagementPolicyCreate
	adoptP  = v1alpha1.ManagementPolicyAdopt
	aoc     = v1alpha1.ManagementPolicyAdoptOrCreate
	ro      = v1alpha1.ManagementPolicyReadOnly
	created = v1alpha1.ProvenanceCreated
	adopted = v1alpha1.ProvenanceAdopted
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   Facts
		want Action
	}{
		// scope.md "Management policy" table
		{
			"Create (default) | no, never adopts | Conflict",
			Facts{Policy: create, Target: TargetFound},
			ActionConflict,
		},
		{
			"Create (default) | miss | creates",
			Facts{Policy: create, Target: TargetMissing},
			ActionCreate,
		},
		{
			"unset policy is Create",
			Facts{Target: TargetMissing},
			ActionCreate,
		},
		{
			"Adopt | existing object | takes it over",
			Facts{Policy: adoptP, Target: TargetFound},
			ActionAdopt,
		},
		{
			"Adopt | miss | AdoptionTargetNotFound, nothing written",
			Facts{Policy: adoptP, Target: TargetMissing},
			ActionTargetNotFound,
		},
		{
			"AdoptOrCreate | existing object | takes it over",
			Facts{Policy: aoc, Target: TargetFound},
			ActionAdopt,
		},
		{
			"AdoptOrCreate | miss | creates",
			Facts{Policy: aoc, Target: TargetMissing},
			ActionCreate,
		},
		{
			"ReadOnly | existing object | observes it",
			Facts{Policy: ro, Target: TargetFound},
			ActionObserve,
		},
		{
			"ReadOnly | miss | AdoptionTargetNotFound",
			Facts{Policy: ro, Target: TargetMissing},
			ActionTargetNotFound,
		},
		// scope.md "Management policy" bullets and "Behavior"
		{
			"A resource that created its object recreates it if it vanishes, whatever the policy (Create)",
			Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionRecreate,
		},
		{
			"A resource that created its object recreates it if it vanishes, whatever the policy (Adopt)",
			Facts{Policy: adoptP, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionRecreate,
		},
		{
			"A resource that created its object recreates it if it vanishes, whatever the policy (AdoptOrCreate)",
			Facts{Policy: aoc, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionRecreate,
		},
		{
			"ReadOnly never recreates anything",
			Facts{Policy: ro, Provenance: created, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionTrackedGone,
		},
		{
			"An object it adopted that vanishes is never recreated silently (AdoptOrCreate)",
			Facts{Policy: aoc, Provenance: adopted, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionTrackedGone,
		},
		{
			"An object it adopted that vanishes is never recreated silently (Create)",
			Facts{Policy: create, Provenance: adopted, Tracked: TrackedGone, Target: TargetMissing, RecreateVanished: true},
			ActionTrackedGone,
		},
		{
			"a recreate is refused when another object took the name",
			Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetFound, RecreateVanished: true},
			ActionConflict,
		},
		{
			"a recreate whose response was lost is recovered",
			Facts{
				Policy:           create,
				Provenance:       created,
				Tracked:          TrackedGone,
				Target:           TargetFound,
				OwnCreatePending: true,
				OwnCreateProven:  true,
				RecreateVanished: true,
			},
			ActionRecoverOwnCreate,
		},
		{
			"A miss never creates unless the policy is AdoptOrCreate (Adopt, dry run)",
			Facts{Policy: adoptP, DryRun: true, Target: TargetMissing},
			ActionTargetNotFound,
		},
		{
			"Lost creates are recovered through status.pendingCreate and classified Created (Create)",
			Facts{Policy: create, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true},
			ActionRecoverOwnCreate,
		},
		{
			"Lost creates are recovered through status.pendingCreate and classified Created (AdoptOrCreate)",
			Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true},
			ActionRecoverOwnCreate,
		},
		{
			"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (Create)",
			Facts{Policy: create, Target: TargetFound, OwnCreatePending: true},
			ActionConflict,
		},
		{
			"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (Adopt)",
			Facts{Policy: adoptP, Target: TargetFound, OwnCreatePending: true},
			ActionConflict,
		},
		{
			"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (AdoptOrCreate)",
			Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true},
			ActionConflict,
		},
		{
			"spec 5.1: found while the own create is pending but unproven is a Conflict under every policy (ReadOnly)",
			Facts{Policy: ro, Target: TargetFound, OwnCreatePending: true},
			ActionConflict,
		},
		{
			"With dryRun: true the operator finds the object and reports (Adopt)",
			Facts{Policy: adoptP, DryRun: true, Target: TargetFound},
			ActionDryRunAdopt,
		},
		{
			"With dryRun: true the operator finds the object and reports (AdoptOrCreate)",
			Facts{Policy: aoc, DryRun: true, Target: TargetFound},
			ActionDryRunAdopt,
		},
		{
			"With nothing to adopt it reports whether a new object would be created",
			Facts{Policy: aoc, DryRun: true, Target: TargetMissing},
			ActionDryRunCreate,
		},
		{
			"dryRun gates only the initial takeover: once adopted, spec edits are applied live",
			Facts{Policy: adoptP, DryRun: true, Provenance: adopted, Tracked: TrackedFound},
			ActionUseTracked,
		},
		{
			"ReadOnly keeps observing the object it found",
			Facts{Policy: ro, Provenance: adopted, Tracked: TrackedFound},
			ActionUseTracked,
		},
		{
			"Several providers with one name is a Conflict",
			Facts{Policy: adoptP, Target: TargetAmbiguous},
			ActionAmbiguous,
		},
		{
			"an unset target never creates",
			Facts{Policy: create},
			ActionConflict,
		},
		{
			"a vanished object of undecided provenance is decided as untracked",
			Facts{Policy: create, Tracked: TrackedGone, Target: TargetMissing},
			ActionCreate,
		},
		// spec 5.3 User rows (RecreateVanished false)
		{
			"User: A Created user that vanishes from Authentik is not re-invited",
			Facts{Policy: create, Provenance: created, Tracked: TrackedGone, Target: TargetMissing},
			ActionTrackedGone,
		},
		{
			"User: no user named username exists: the invitation is issued",
			Facts{Policy: create, Target: TargetMissing},
			ActionCreate,
		},
		{
			"User: invitation in progress and no account yet: keep inviting",
			Facts{Policy: create, Target: TargetMissing, OwnCreatePending: true},
			ActionCreate,
		},
		{
			"User: the account carrying the proof is recognised as Created",
			Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true, OwnCreateProven: true},
			ActionRecoverOwnCreate,
		},
		{
			"User: An account named username without the proof appears while inviting: Conflict",
			Facts{Policy: aoc, Target: TargetFound, OwnCreatePending: true},
			ActionConflict,
		},
		{
			"User: one exists and no invitation is in progress: Conflict under Create",
			Facts{Policy: create, Target: TargetFound},
			ActionConflict,
		},
		{
			"User: one exists and no invitation is in progress: adopted under AdoptOrCreate",
			Facts{Policy: aoc, Target: TargetFound},
			ActionAdopt,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.in); got != tc.want {
				t.Errorf("Decide(%+v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
