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

package v1alpha1

// This file holds the types shared by the kinds that manage Authentik API
// objects (applications, providers, groups, users): the management policy (create, adopt or
// only observe a pre-existing object), provenance, and the deletion policy.
// See docs/design/scope.md, section "API-object adoption".

// Provenance records how a resource came to manage its Authentik object.
// Authentik objects cannot carry annotations, so this is kept in status.
// An empty value means "not yet decided": a fresh resource that finds an
// existing object is never assumed to have created it. A ReadOnly resource
// that found its object records Adopted: the object existed first.
// +kubebuilder:validation:Enum=Created;Adopted
type Provenance string

const (
	// ProvenanceCreated means this resource created the Authentik object.
	ProvenanceCreated Provenance = "Created"
	// ProvenanceAdopted means the Authentik object existed first and this
	// resource took it over (or, under ReadOnly, observes it).
	ProvenanceAdopted Provenance = "Adopted"
)

// ManagementPolicy says how a resource relates to the Authentik object it
// describes: whether it may create it, take over one that already exists, or
// only observe one.
//
// The names follow ACK's adoption policies, but the write semantics differ:
// Adopt here takes over an existing object by patching the fields the spec
// sets and leaving the rest alone, where ACK's adopt does not update the
// object to the spec.
// +kubebuilder:validation:Enum=Create;Adopt;AdoptOrCreate;ReadOnly
type ManagementPolicy string

const (
	// ManagementPolicyCreate creates the object. An object that already
	// exists is a Conflict, never overwritten. A resource that created its
	// object recreates it if it vanishes. This is the default.
	ManagementPolicyCreate ManagementPolicy = "Create"
	// ManagementPolicyAdopt takes over an existing object. A miss is reported
	// as AdoptionTargetNotFound and nothing is written; an adopted object
	// that vanishes is never recreated.
	ManagementPolicyAdopt ManagementPolicy = "Adopt"
	// ManagementPolicyAdoptOrCreate adopts the object if it exists and
	// creates it if it does not.
	ManagementPolicyAdoptOrCreate ManagementPolicy = "AdoptOrCreate"
	// ManagementPolicyReadOnly finds an existing object, renders the kind's
	// Secret (if it has one) and reports drift from the spec, but never writes
	// to Authentik (an AuthentikUser switched to it while inviting deletes its
	// live invitation) and never deletes the object.
	ManagementPolicyReadOnly ManagementPolicy = "ReadOnly"
)

// Resolve returns the policy in force: an unset policy is Create.
func (m ManagementPolicy) Resolve() ManagementPolicy {
	if m == "" {
		return ManagementPolicyCreate
	}
	return m
}

// MayAdopt reports whether the policy lets a resource take over, or observe,
// an object it did not create. These are the policies the default-connection
// adoption gate guards.
func (m ManagementPolicy) MayAdopt() bool {
	switch m.Resolve() {
	case ManagementPolicyAdopt, ManagementPolicyAdoptOrCreate, ManagementPolicyReadOnly:
		return true
	case ManagementPolicyCreate:
		return false
	}
	return false
}

// MayCreate reports whether the policy lets a resource create its object when
// none exists.
func (m ManagementPolicy) MayCreate() bool {
	switch m.Resolve() {
	case ManagementPolicyCreate, ManagementPolicyAdoptOrCreate:
		return true
	case ManagementPolicyAdopt, ManagementPolicyReadOnly:
		return false
	}
	return false
}

// DeletionPolicy says what happens to the Authentik object when the
// resource is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	// DeletionPolicyDelete deletes the Authentik object with the resource.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyRetain leaves the Authentik object in place.
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// EffectiveDeletionPolicy resolves an optional spec.deletionPolicy. ReadOnly
// never deletes. Otherwise an explicit value wins; unset, it is Delete for a
// Created object and Retain for an Adopted one (release, not delete -- the
// same contract as AuthentikDeployment adoption). An undecided provenance
// also resolves to Retain by default: if the operator cannot say it created
// the object, it does not delete it unless told to.
func EffectiveDeletionPolicy(mp ManagementPolicy, spec DeletionPolicy, prov Provenance) DeletionPolicy {
	switch {
	case mp.Resolve() == ManagementPolicyReadOnly:
		return DeletionPolicyRetain
	case spec != "":
		return spec
	case prov == ProvenanceCreated:
		return DeletionPolicyDelete
	default:
		return DeletionPolicyRetain
	}
}

// APIManagementSpec holds the fields the kinds that manage Authentik API
// objects share: how the resource relates to a pre-existing object, whether
// the first takeover is only previewed, and what deletion does.
//
// It is a separate type from AdoptionSpec (AuthentikDeployment), which
// governs Kubernetes objects and has no dry run or API-side match.
//
// What adoption writes: the operator PATCHes the fields the spec sets and
// leaves the rest alone. A field the spec leaves unset keeps the value the
// existing object has; when the object is created the same fields take their
// documented defaults. The client ID and client secret of a provider are
// never changed unless spec.clientId is set (dryRun shows exactly what would
// change).
// +kubebuilder:validation:XValidation:rule="!has(self.dryRun) || !self.dryRun || (has(self.managementPolicy) && self.managementPolicy in ['Adopt', 'AdoptOrCreate'])",message="dryRun requires managementPolicy Adopt or AdoptOrCreate"
// +kubebuilder:validation:XValidation:rule="!(has(self.managementPolicy) && self.managementPolicy == 'ReadOnly' && has(self.deletionPolicy) && self.deletionPolicy == 'Delete')",message="managementPolicy ReadOnly never deletes; deletionPolicy Delete is not allowed"
type APIManagementSpec struct {
	// managementPolicy is how this resource relates to the object in
	// Authentik. Create (the default) creates it and treats an existing one as
	// a Conflict. Adopt takes over an existing object and never creates one.
	// AdoptOrCreate adopts if the object exists and creates it if not.
	// ReadOnly finds the existing object, renders the kind's Secret (if it has
	// one) and reports drift in the Drifted condition, and never writes to
	// Authentik (an AuthentikUser switched to it while inviting deletes its
	// live invitation) or deletes the object. Adopt, AdoptOrCreate and ReadOnly on the
	// operator's default connection need an administrator to allow the
	// namespace (--default-connection-adoption-namespace-selector); with
	// spec.authentikRef they are always allowed. Treat AdoptOrCreate as a
	// temporary choice for a disaster-recovery re-bootstrap: it removes the
	// protection against minting new client credentials through a typo in a
	// name, slug or pk.
	// +kubebuilder:default=Create
	// +optional
	ManagementPolicy ManagementPolicy `json:"managementPolicy,omitempty"`

	// dryRun finds the existing object and reports, in the Ready condition
	// (reason AdoptionDryRun) and an event, which fields adoption would
	// change. Nothing is written to Authentik, and the kind's Secret (if it
	// has one) is not rendered. Set it back to false to adopt for real. Only with
	// managementPolicy Adopt or AdoptOrCreate. dryRun gates only the initial
	// takeover (and the decision to create under AdoptOrCreate): once the
	// object is adopted, spec edits are applied live whatever dryRun says.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// deletionPolicy is what happens to the object in Authentik when this
	// resource is deleted: Delete or Retain. When unset the per-kind default
	// applies: by provenance for providers, applications and groups (Delete
	// for an object this resource created, Retain for an adopted one or one of
	// undecided provenance); Retain for every AuthentikUser. An explicit
	// Delete wins, so it can remove an object this resource did not create; a
	// Warning event is emitted when it does so for undecided provenance.
	// ReadOnly never deletes (Delete is rejected). The kind's Secret, if it
	// renders one, is a Kubernetes object owned by this resource and is
	// garbage-collected with it under either policy.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ProviderMatch identifies an existing provider; exactly one field is set.
// It may differ from the resource's desired name: a provider created by
// another operator is typically named "<slug>-provider".
// +kubebuilder:validation:XValidation:rule="(has(self.name) ? 1 : 0) + (has(self.pk) ? 1 : 0) == 1",message="set exactly one of name or pk"
type ProviderMatch struct {
	// name is the provider's exact name in Authentik.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Name string `json:"name,omitempty"`

	// pk is the provider's primary key in Authentik.
	// +kubebuilder:validation:Minimum=1
	// +optional
	PK int32 `json:"pk,omitempty"`
}
