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
	"strconv"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
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

// Field indexes over the status key a claim is recorded in, so the check that
// runs on every reconcile of a tracked resource lists only the resources
// holding that one object instead of copying every resource of the kind. The
// key is the pk alone: which Authentik instance it is on is decided afterwards,
// by sameConnection, because an index function sees only the resource and a
// connection's URL lives in another object.
const (
	indexProviderPK     = ".status.providerPk"
	indexApplicationUID = ".status.applicationUid"
)

// providerPKIndex indexes providers by the pk they track.
func providerPKIndex(o client.Object) []string {
	p, ok := o.(*authentikiov1alpha1.AuthentikOAuthProvider)
	if !ok || p.Status.ProviderPK == 0 {
		return nil
	}
	return []string{strconv.Itoa(int(p.Status.ProviderPK))}
}

// applicationUIDIndex indexes applications by the uid they track.
func applicationUIDIndex(o client.Object) []string {
	a, ok := o.(*authentikiov1alpha1.AuthentikApplication)
	if !ok || a.Status.ApplicationUID == "" {
		return nil
	}
	return []string{a.Status.ApplicationUID}
}

// indexProviderPKs registers providerPKIndex.
func indexProviderPKs(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &authentikiov1alpha1.AuthentikOAuthProvider{}, indexProviderPK, providerPKIndex)
}

// indexApplicationUIDs registers applicationUIDIndex.
func indexApplicationUIDs(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &authentikiov1alpha1.AuthentikApplication{}, indexApplicationUID, applicationUIDIndex)
}

// holders keeps the candidates that hold the same object as self on the same
// Authentik instance; candidates already share the object's pk or uid.
func holders(ctx context.Context, res *connection.Resolver, self claim, candidates []claim) ([]claim, error) {
	var out []claim
	for _, c := range candidates {
		same, err := sameConnection(ctx, res, self, c)
		if err != nil {
			return nil, err
		}
		if same {
			out = append(out, c)
		}
	}
	return out, nil
}

// sameConnection reports whether two claims' resources reach the same
// Authentik instance. The same connection reference (both the operator
// default, or the same AuthentikDeployment) is the same instance without a
// lookup. Otherwise the two are compared by normalized base URL, so a resource
// on the default connection and one on an AuthentikDeployment pointing at the
// same URL are caught. The comparison is by URL only: a service DNS name and
// an ingress host that reach one instance look different, a false negative
// this cannot close without asking Authentik which instance it is.
//
// A connection that cannot be resolved (its deployment is gone or has no URL)
// reaches nothing, so that resource holds nothing on that instance.
func sameConnection(ctx context.Context, res *connection.Resolver, a, b claim) (bool, error) {
	if sameAuthentikRef(a.ref, b.ref) && (a.ref == nil || a.namespace == b.namespace) {
		return true, nil
	}
	urlA, err := res.BaseURL(ctx, a.namespace, a.ref)
	if err != nil {
		return false, unresolvable(err)
	}
	urlB, err := res.BaseURL(ctx, b.namespace, b.ref)
	if err != nil {
		return false, unresolvable(err)
	}
	return urlA == urlB, nil
}

// unresolvable is nil for a connection error that only says a connection
// cannot be resolved, and err itself for anything that may be transient.
func unresolvable(err error) error {
	if connection.Reason(err) != "" {
		return nil
	}
	return err
}

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
