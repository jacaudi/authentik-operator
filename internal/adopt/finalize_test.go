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

func TestFinalize(t *testing.T) {
	cases := []struct {
		name string
		in   FinalizeFacts
		want Finalization
	}{
		{
			"ReadOnly never deletes",
			FinalizeFacts{Policy: ro, Provenance: adopted, IdentityAllowed: true},
			FinalizeRetain,
		},
		{
			"Unset, the effective policy is Delete for a Created object",
			FinalizeFacts{Policy: create, Provenance: created, IdentityAllowed: true},
			FinalizeDelete,
		},
		{
			"and Retain for an Adopted one",
			FinalizeFacts{Policy: adoptP, Provenance: adopted, IdentityAllowed: true},
			FinalizeRetain,
		},
		{
			"an undecided provenance never deletes by default (Retain, with a Warning event)",
			FinalizeFacts{Policy: create, IdentityAllowed: true},
			FinalizeRetainUndecided,
		},
		{
			"An explicit Delete wins over provenance",
			FinalizeFacts{
				Policy:          adoptP,
				DeletionPolicy:  v1alpha1.DeletionPolicyDelete,
				Provenance:      adopted,
				IdentityAllowed: true,
			},
			FinalizeDelete,
		},
		{
			"when it does so for undecided provenance a Warning event says so first",
			FinalizeFacts{Policy: adoptP, DeletionPolicy: v1alpha1.DeletionPolicyDelete, IdentityAllowed: true},
			FinalizeDeleteUndecided,
		},
		{
			"Retain removes the finalizer and leaves the Authentik object",
			FinalizeFacts{
				Policy:          create,
				DeletionPolicy:  v1alpha1.DeletionPolicyRetain,
				Provenance:      created,
				IdentityAllowed: true,
			},
			FinalizeRetain,
		},
		{
			"User: unset deletionPolicy resolves to Retain whatever its provenance",
			FinalizeFacts{
				Policy:          create,
				Provenance:      created,
				UnsetDefault:    v1alpha1.DeletionPolicyRetain,
				IdentityAllowed: true,
			},
			FinalizeRetain,
		},
		{
			"User: an explicit Delete still deletes",
			FinalizeFacts{
				Policy:          create,
				DeletionPolicy:  v1alpha1.DeletionPolicyDelete,
				Provenance:      created,
				UnsetDefault:    v1alpha1.DeletionPolicyRetain,
				IdentityAllowed: true,
			},
			FinalizeDelete,
		},
		{
			"Outside the selector, deletion removes only Created objects",
			FinalizeFacts{Policy: create, Provenance: created},
			FinalizeDelete,
		},
		{
			"Outside the selector, adopted objects are retained with a Warning",
			FinalizeFacts{Policy: adoptP, DeletionPolicy: v1alpha1.DeletionPolicyDelete, Provenance: adopted},
			FinalizeRetainNotAllowed,
		},
		{
			"Outside the selector, undecided objects are retained with a Warning",
			FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyDelete},
			FinalizeRetainNotAllowed,
		},
		{
			"Outside the selector, a Created object with Retain is still retained",
			FinalizeFacts{Policy: create, DeletionPolicy: v1alpha1.DeletionPolicyRetain, Provenance: created},
			FinalizeRetain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Finalize(tc.in); got != tc.want {
				t.Errorf("Finalize(%+v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
