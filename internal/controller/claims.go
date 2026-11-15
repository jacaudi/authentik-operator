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
	"k8s.io/apimachinery/pkg/types"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// A claim is one resource's hold on an Authentik object, as recorded in its
// status (status.providerPk, status.applicationUid). Two resources must never
// manage one object: the pre-adoption check refuses a second claimant, and
// when two claims exist anyway (both passed that check together) every holder
// runs the same tie-break and exactly one keeps the object.
type claim struct {
	namespace, name string
	uid             types.UID
	provenance      authentikiov1alpha1.Provenance
	// ref is the holder's spec.authentikRef, which says which Authentik it
	// talks to (see sameConnection).
	ref *authentikiov1alpha1.AuthentikRef
}

// String names the holder as namespace/name.
func (c claim) String() string { return c.namespace + "/" + c.name }

// outranks reports whether c takes precedence over o. The order is total, so
// any two holders that compare the same two claims agree on the winner:
// Created beats Adopted (a Created resource owns the object's lifecycle, and
// deleting it deletes the object, which an adopter must not be left holding),
// then the lower UID wins. A terminating holder still counts: its finalizer
// may yet delete the object.
func (c claim) outranks(o claim) bool {
	if cc, oc := c.provenance == authentikiov1alpha1.ProvenanceCreated,
		o.provenance == authentikiov1alpha1.ProvenanceCreated; cc != oc {
		return cc
	}
	return c.uid < o.uid
}

// outrankedBy returns the strongest claim in others that outranks self.
func outrankedBy(self claim, others []claim) (claim, bool) {
	var best claim
	found := false
	for _, o := range others {
		if o.outranks(self) && (!found || o.outranks(best)) {
			best, found = o, true
		}
	}
	return best, found
}
