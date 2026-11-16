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
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// staleListClient lists nothing, as an informer cache that has not yet seen
// anyone's recent status writes would. Everything else is the real client.
type staleListClient struct{ client.Client }

func (staleListClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return nil
}

func TestClaimOutranks(t *testing.T) {
	created := authentikiov1alpha1.ProvenanceCreated
	adopted := authentikiov1alpha1.ProvenanceAdopted
	cases := []struct {
		name string
		a, b claim
		want bool // a outranks b
	}{
		{"created beats adopted despite a higher uid", claim{uid: "z", provenance: created},
			claim{uid: "a", provenance: adopted}, true},
		{"adopted yields to created despite a lower uid", claim{uid: "a", provenance: adopted},
			claim{uid: "z", provenance: created}, false},
		{"equal provenance: the lower uid wins", claim{uid: "a", provenance: adopted},
			claim{uid: "b", provenance: adopted}, true},
		{"equal provenance: the higher uid loses", claim{uid: "b", provenance: adopted},
			claim{uid: "a", provenance: adopted}, false},
		{"undecided provenance ranks with adopted", claim{uid: "a"}, claim{uid: "b", provenance: adopted}, true},
		{"a claim never outranks itself", claim{uid: "a", provenance: created}, claim{uid: "a", provenance: created}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.outranks(c.b); got != c.want {
				t.Errorf("outranks = %t, want %t", got, c.want)
			}
		})
	}
}

func TestOutrankedBy(t *testing.T) {
	self := claim{namespace: "n", name: "self", uid: "m", provenance: authentikiov1alpha1.ProvenanceAdopted}
	weak := claim{namespace: "n", name: "weak", uid: "z", provenance: authentikiov1alpha1.ProvenanceAdopted}
	low := claim{namespace: "n", name: "low", uid: "a", provenance: authentikiov1alpha1.ProvenanceAdopted}
	owner := claim{namespace: "n", name: "owner", uid: "y", provenance: authentikiov1alpha1.ProvenanceCreated}

	if _, lost := outrankedBy(self, nil); lost {
		t.Error("no other claims: self must keep the object")
	}
	if _, lost := outrankedBy(self, []claim{weak}); lost {
		t.Error("a weaker claim must not displace self")
	}
	got, lost := outrankedBy(self, []claim{weak, low, owner})
	if !lost || got.name != "owner" {
		t.Errorf("outrankedBy = %v, %t; want the Created owner", got, lost)
	}
}
