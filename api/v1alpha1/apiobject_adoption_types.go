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
// objects (applications, providers): adoption of a pre-existing object,
// provenance, and the deletion policy. See docs/design/scope.md, section
// "API-object adoption".

// Provenance records how a resource came to manage its Authentik object.
// Authentik objects cannot carry annotations, so this is kept in status.
// An empty value means "not yet decided": a fresh resource that finds an
// existing object is never assumed to have created it.
// +kubebuilder:validation:Enum=Created;Adopted
type Provenance string

const (
	// ProvenanceCreated means this resource created the Authentik object.
	ProvenanceCreated Provenance = "Created"
	// ProvenanceAdopted means the Authentik object existed first and this
	// resource took it over under spec.adoption.
	ProvenanceAdopted Provenance = "Adopted"
)

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

// EffectiveDeletionPolicy resolves an optional spec.deletionPolicy: an
// explicit value wins; otherwise Delete for a Created object and Retain for
// an Adopted one (release, not delete -- the same contract as
// AuthentikDeployment adoption). An undecided provenance also resolves to
// Retain: if the operator cannot say it created the object, it must not
// delete it.
func EffectiveDeletionPolicy(spec DeletionPolicy, prov Provenance) DeletionPolicy {
	switch {
	case spec != "":
		return spec
	case prov == ProvenanceCreated:
		return DeletionPolicyDelete
	default:
		return DeletionPolicyRetain
	}
}

// APIAdoptionSpec governs takeover of a pre-existing Authentik API object.
//
// It is a separate type from AdoptionSpec (AuthentikDeployment), which
// governs Kubernetes objects and has no dry run or API-side match; the
// vocabulary (adoption.enabled, opt-in, release rather than delete) is the
// same.
//
// Adoption is opt-in. With enabled false, an existing object with the
// target identity is a Conflict, never overwritten.
//
// What adoption writes: the operator PATCHes the fields the spec sets and
// leaves the rest alone. Fields that carry a +kubebuilder:default are filled
// in by the API server on admission, so the operator cannot tell them from
// values you wrote and WILL patch them. Set every defaulted field to the
// value the existing object has, or accept the default, before enabling
// adoption (dryRun shows exactly what would change). The client ID and
// client secret of a provider are never changed unless spec.clientId is set.
type APIAdoptionSpec struct {
	// enabled permits this resource to take over an existing Authentik
	// object that it did not create.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// dryRun finds the existing object and reports, in the Ready condition
	// (reason AdoptionDryRun) and an event, which fields adoption would
	// change. Nothing is written to Authentik, and no credentials Secret is
	// rendered. Set it back to false to adopt for real. Only meaningful
	// with enabled true. dryRun gates only the initial takeover (and the
	// decision to create when createIfMissing is set): once the object is
	// adopted, spec edits are applied live whatever dryRun says.
	// +kubebuilder:default=false
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// createIfMissing lets a resource with adoption enabled create the
	// object when none is found to adopt. When false (the default) a miss is
	// reported as AdoptionTargetNotFound and nothing is written, so a
	// migrating user can never mint new client credentials by a typo in the
	// name, slug or pk. A disaster-recovery re-bootstrap (the Authentik
	// database was lost, nothing exists to adopt) sets it to true
	// deliberately. With adoption disabled the resource creates as usual,
	// and a resource that created its object itself recreates it if it
	// vanishes, whatever this says.
	// +kubebuilder:default=false
	// +optional
	CreateIfMissing bool `json:"createIfMissing,omitempty"`
}

// ProviderAdoptionSpec is APIAdoptionSpec plus the means to pick which
// provider to take over.
type ProviderAdoptionSpec struct {
	APIAdoptionSpec `json:",inline"`

	// match selects the existing provider by name or pk. When unset, the
	// provider named like this resource's desired name (spec.name, else
	// metadata.name) is the candidate and, if there is none, a new provider
	// is created only if createIfMissing is set. A missing match is otherwise
	// an error (reason AdoptionTargetNotFound), never a create, so a
	// migration cannot silently mint new credentials.
	// +optional
	Match *ProviderMatch `json:"match,omitempty"`
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
