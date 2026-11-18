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

// UserType is the kind of account an AuthentikUser may set. Service accounts
// are not people and are never managed here.
// +kubebuilder:validation:Enum=internal;external
type UserType string

const (
	// UserTypeInternal is an account of a member of the organisation.
	UserTypeInternal UserType = "internal"
	// UserTypeExternal is an account of someone outside it.
	UserTypeExternal UserType = "external"
)

// InvitationSpec is how a new person gets their account: a single-use
// Authentik invitation that pins the username, whose sign-up link the operator
// writes to a Secret.
type InvitationSpec struct {
	// flow is the slug of the enrolment flow the invitation is bound to. The
	// flow's username prompt must be read-only (text_read_only) so the account
	// is created under spec.username.
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	// +kubebuilder:validation:MaxLength=50
	// +required
	Flow string `json:"flow"`

	// expiresAfter is how long the invitation stays valid, from the moment it
	// is issued. Defaults to 168h (seven days).
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="expiresAfter must be positive"
	// +optional
	ExpiresAfter *metav1.Duration `json:"expiresAfter,omitempty"`

	// secretName is the Secret, in this resource's namespace, that receives
	// the sign-up link (keys url and expires). Defaults to
	// <metadata.name>-invitation.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// attempt numbers the invitation, from 1. Increase it to get a new link
	// after the last one was lost, opened and abandoned, or expired: the
	// operator never reissues on its own. Increasing it once the person has an
	// account does nothing.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
}

// AuthentikUserSpec defines the desired state of AuthentikUser: a person. A
// field left unset keeps the account's live value, whatever the provenance.
// +kubebuilder:validation:XValidation:rule="has(self.authentikRef) == has(oldSelf.authentikRef) && (!has(self.authentikRef) || self.authentikRef.name == oldSelf.authentikRef.name)",message="authentikRef is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.managementPolicy) && self.managementPolicy in ['Adopt', 'ReadOnly']) ? !has(self.invitation) : has(self.invitation)",message="invitation is required when managementPolicy may create (Create, AdoptOrCreate) and not allowed under Adopt and ReadOnly"
type AuthentikUserSpec struct {
	// APIManagementSpec carries managementPolicy, dryRun and deletionPolicy.
	// An existing account is matched by username; Create never touches one
	// that exists (Conflict). Unlike other kinds, an unset deletionPolicy is
	// Retain whatever the provenance: a person's passkeys are costly to
	// enrol again. Set isActive: false to offboard.
	APIManagementSpec `json:",inline"`

	// authentikRef names the AuthentikDeployment (same namespace) whose
	// connection this object is reconciled against. When omitted, the
	// operator's default connection is used.
	// +optional
	AuthentikRef *AuthentikRef `json:"authentikRef,omitempty"`

	// username is the account's username in Authentik. It cannot change.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=150
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="username is immutable"
	// +required
	Username string `json:"username"`

	// name is the person's display name. Unset keeps the live value.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Name string `json:"name,omitempty"`

	// email is the person's email address. Unset keeps the live value.
	// +kubebuilder:validation:MaxLength=254
	// +optional
	Email string `json:"email,omitempty"`

	// path is the account's path in Authentik. Unset keeps the live value.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Path string `json:"path,omitempty"`

	// type is internal or external. Unset keeps the live value.
	// +optional
	Type UserType `json:"type,omitempty"`

	// firstName is written to the account's given_name attribute, which the
	// OIDC profile scope hands to applications. Unset keeps the live value.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	FirstName string `json:"firstName,omitempty"`

	// lastName is written to the account's family_name attribute. Unset keeps
	// the live value.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	LastName string `json:"lastName,omitempty"`

	// isActive says whether the account may sign in; false is offboarding.
	// Unset keeps the live value. The operator never deactivates its own
	// token's user.
	// +optional
	IsActive *bool `json:"isActive,omitempty"`

	// attributes are the account's attributes. The operator owns only the
	// keys listed here: it sets them, removes a key it set earlier that is no
	// longer listed, and keeps every other key. An empty map removes the keys
	// the operator set; omitting the field releases them without deleting
	// them. Values are strings, numbers, booleans or lists. Keys starting
	// with goauthentik.io/, and authentik_operator, settings, given_name and
	// family_name (firstName and lastName own the last two), are reserved: the
	// resource reports InvalidSpec and writes nothing.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Attributes map[string]apiextensionsv1.JSON `json:"attributes,omitzero"`

	// invitation is how a new person is invited. Required when
	// managementPolicy may create (Create, AdoptOrCreate), and not allowed
	// under Adopt and ReadOnly.
	// +optional
	Invitation *InvitationSpec `json:"invitation,omitempty"`
}

// InvitationStatus tracks the invitation of a person who has no account yet.
// It never holds the invitation's pk: the pk is the sign-up token, and lives
// only in the Secret.
type InvitationStatus struct {
	// name is the invitation's name in Authentik: k8s-<metadata.uid>-<attempt>.
	Name string `json:"name"`

	// attempt is the spec.invitation.attempt this invitation answers.
	Attempt int32 `json:"attempt"`

	// expires is when the invitation stops being valid.
	Expires metav1.Time `json:"expires"`

	// pending is true from before the invitation is created until its
	// creation is confirmed, so a response lost in flight is recovered by name.
	// +optional
	Pending bool `json:"pending,omitempty"`

	// secretName is the Secret that holds the sign-up link.
	SecretName string `json:"secretName"`
}

// AuthentikUserStatus defines the observed state of AuthentikUser.
type AuthentikUserStatus struct {
	// conditions represent the current state of the resource. "Ready" is
	// True once the account in Authentik matches the spec.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// userPk is the Authentik primary key of the account this resource
	// manages, whether it adopted it or the person enrolled it.
	// +optional
	UserPK int32 `json:"userPk,omitempty"`

	// provenance is Created (the person enrolled through this resource's
	// invitation) or Adopted once decided; empty means not yet decided.
	// +optional
	Provenance Provenance `json:"provenance,omitempty"`

	// managedAttributes lists the attribute keys the operator owns: the keys it
	// set from spec.attributes. It removes one from the account when the spec
	// stops listing it. given_name and family_name are never listed here.
	// +optional
	ManagedAttributes []string `json:"managedAttributes,omitempty"`

	// invitation is set while the person has no account yet.
	// +optional
	Invitation *InvitationStatus `json:"invitation,omitempty"`

	// observedGeneration is the spec generation last synced to Authentik.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=".spec.username"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Provenance",type=string,JSONPath=".status.provenance"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikUser is the Schema for the authentikusers API.
type AuthentikUser struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikUser
	// +required
	Spec AuthentikUserSpec `json:"spec"`

	// status defines the observed state of AuthentikUser
	// +optional
	Status AuthentikUserStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikUserList contains a list of AuthentikUser.
type AuthentikUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []AuthentikUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikUser{}, &AuthentikUserList{})
		return nil
	})
}
