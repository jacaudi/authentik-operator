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

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AuthentikGroupSpec defines the desired state of AuthentikGroup.
// +kubebuilder:validation:XValidation:rule="has(self.authentikRef) == has(oldSelf.authentikRef) && (!has(self.authentikRef) || self.authentikRef.name == oldSelf.authentikRef.name)",message="authentikRef is immutable"
type AuthentikGroupSpec struct {
	// APIManagementSpec carries managementPolicy, dryRun and deletionPolicy.
	// An existing group is matched by name. Adoption patches the fields the
	// spec sets and leaves the others (isSuperuser when unset, every attribute
	// key the spec does not list, parents, roles, and members when absent) as
	// they are.
	APIManagementSpec `json:",inline"`

	// authentikRef names the AuthentikDeployment (same namespace) whose
	// connection this object is reconciled against. When omitted, the
	// operator's default connection is used.
	// +optional
	AuthentikRef *AuthentikRef `json:"authentikRef,omitempty"`

	// name is the group's name in Authentik; defaults to metadata.name. It
	// cannot change once the group is created or adopted (InvalidSpec).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[^\x00-\x1F\x7F]+$`
	// +optional
	Name string `json:"name,omitempty"`

	// isSuperuser makes the group's users superusers. Unset: held to false on a
	// group this resource created, the live value on an adopted group.
	// +optional
	IsSuperuser *bool `json:"isSuperuser,omitempty"`

	// attributes are the group's attributes, inherited by its members in
	// claims. The operator owns only the keys listed here: it sets them, removes
	// a key it set earlier that is no longer listed, and keeps every other key
	// (Authentik's own and any set in its UI). An empty map removes the keys the
	// operator set; omitting the field releases them without deleting them.
	// Values are strings, numbers, booleans or lists. Keys starting with
	// goauthentik.io/, and authentik_operator and settings, are reserved: the
	// resource reports InvalidSpec and writes nothing.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Attributes map[string]apiextensionsv1.JSON `json:"attributes,omitzero"`

	// members are Authentik usernames. Absent: membership is never read or
	// written. Present, including []: exactly these members (the operator
	// token's own user is never removed).
	// +listType=set
	// +kubebuilder:validation:MaxItems=1000
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=150
	// +optional
	Members *[]string `json:"members,omitempty"`
}

// GroupMemberStatus pins a member username to the Authentik user it first
// resolved to.
type GroupMemberStatus struct {
	// username is the member's Authentik username, as listed in spec.members.
	Username string `json:"username"`

	// pk is the Authentik user the username resolved to when it was first
	// added. The operator holds the username out if it later resolves to
	// another user.
	PK int32 `json:"pk"`
}

// AuthentikGroupStatus defines the observed state of AuthentikGroup.
type AuthentikGroupStatus struct {
	// conditions represent the current state of the resource. "Ready" is
	// True once the group in Authentik matches the spec.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// groupPk is the Authentik primary key (UUID) of the group this resource
	// manages, whether it created or adopted it.
	// +optional
	GroupPK string `json:"groupPk,omitempty"`

	// name is the group's name in Authentik when it was created or adopted.
	// spec.name cannot change from it.
	// +optional
	Name string `json:"name,omitempty"`

	// provenance is Created or Adopted once decided; empty means not yet
	// decided (including while an adoption dry run is reporting).
	// +optional
	Provenance Provenance `json:"provenance,omitempty"`

	// pendingCreate holds the group name while a create is in flight. It lets
	// the next reconcile recognise its own group, by the ownership proof it
	// carries, if the create's outcome was lost (timeout, failed status write).
	// +optional
	PendingCreate string `json:"pendingCreate,omitempty"`

	// managedAttributes lists the attribute keys the operator owns: the keys it
	// set from spec.attributes. It removes one from the group when the spec stops
	// listing it.
	// +optional
	ManagedAttributes []string `json:"managedAttributes,omitempty"`

	// members pins each username in spec.members to the Authentik user it
	// resolved to the first time it was added. A username that later resolves
	// to another user is held out (MemberIdentityChanged) until its entry is
	// removed from this list.
	// +listType=map
	// +listMapKey=username
	// +optional
	Members []GroupMemberStatus `json:"members,omitempty"`

	// observedGeneration is the spec generation last synced to Authentik.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=".status.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Provenance",type=string,JSONPath=".status.provenance"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikGroup is the Schema for the authentikgroups API.
type AuthentikGroup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikGroup
	// +required
	Spec AuthentikGroupSpec `json:"spec"`

	// status defines the observed state of AuthentikGroup
	// +optional
	Status AuthentikGroupStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikGroupList contains a list of AuthentikGroup.
type AuthentikGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []AuthentikGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikGroup{}, &AuthentikGroupList{})
		return nil
	})
}
