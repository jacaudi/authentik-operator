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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ProviderKind is the kind of provider an application references.
// +kubebuilder:validation:Enum=AuthentikOAuthProvider
type ProviderKind string

// ProviderKindOAuth references an AuthentikOAuthProvider.
const ProviderKindOAuth ProviderKind = "AuthentikOAuthProvider"

// ProviderRef names a provider resource in the same namespace.
type ProviderRef struct {
	// kind of the provider resource. Only AuthentikOAuthProvider is
	// supported today.
	// +kubebuilder:default=AuthentikOAuthProvider
	// +optional
	Kind ProviderKind `json:"kind,omitempty"`

	// name of the provider resource in this namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
}

// PolicyEngineMode selects how an application's policies combine.
// +kubebuilder:validation:Enum=all;any
type PolicyEngineMode string

// AuthentikApplicationSpec defines the desired state of AuthentikApplication.
// +kubebuilder:validation:XValidation:rule="has(self.authentikRef) == has(oldSelf.authentikRef) && (!has(self.authentikRef) || self.authentikRef.name == oldSelf.authentikRef.name)",message="authentikRef is immutable"
type AuthentikApplicationSpec struct {
	// authentikRef names the AuthentikDeployment (same namespace) whose
	// connection this object is reconciled against. When omitted, the
	// operator's default connection is used. Must match the referenced
	// provider's authentikRef.
	// +optional
	AuthentikRef *AuthentikRef `json:"authentikRef,omitempty"`

	// name is the application's display name. Control characters (e.g.
	// newlines) are not allowed: the name is available to credential
	// Secret templates.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[^\x00-\x1F\x7F]+$`
	// +required
	Name string `json:"name"`

	// slug is the application's identifier in Authentik URLs
	// (/application/o/<slug>/). Defaults to metadata.name. The effective
	// slug is immutable (setting it to metadata.name later is allowed).
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Slug string `json:"slug,omitempty"`

	// providerRef names the provider resource (same namespace) this
	// application uses. The application waits until the provider is Ready.
	// Optional: an application without a provider is a plain launcher link.
	// +optional
	ProviderRef *ProviderRef `json:"providerRef,omitempty"`

	// group groups applications on the user's library page.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Group string `json:"group,omitempty"`

	// policyEngineMode: any (one passing policy grants access) or all.
	// +kubebuilder:default=any
	// +optional
	PolicyEngineMode PolicyEngineMode `json:"policyEngineMode,omitempty"`

	// metaLaunchUrl is the URL opened from the library page. Must be
	// http(s). Defaults (in Authentik) to the provider's launch URL.
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	MetaLaunchURL string `json:"metaLaunchUrl,omitempty"`

	// metaDescription is shown on the library page.
	// +optional
	MetaDescription string `json:"metaDescription,omitempty"`

	// metaPublisher is shown on the library page.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	MetaPublisher string `json:"metaPublisher,omitempty"`

	// metaIcon is an icon URL (http(s)) or an Authentik-relative path.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	MetaIcon string `json:"metaIcon,omitempty"`

	// openInNewTab opens the launch URL in a new tab.
	// +kubebuilder:default=true
	// +optional
	OpenInNewTab *bool `json:"openInNewTab,omitempty"`

	// metaHide hides the application from the user's library page.
	// +kubebuilder:default=false
	// +optional
	MetaHide *bool `json:"metaHide,omitempty"`

	// adoption controls whether this resource may take over an application
	// that already exists in Authentik with this slug instead of reporting
	// a Conflict. The application is matched by slug. Adoption patches the
	// fields the spec sets and leaves the others (group, metaLaunchUrl,
	// metaDescription, metaPublisher, metaIcon, and the provider link when
	// providerRef is unset) as they are. The defaulted fields it will write
	// are: policyEngineMode, openInNewTab and metaHide. When providerRef is
	// set, the application's provider link is set to that provider (normally
	// the adopted provider, so a no-op). See APIAdoptionSpec.
	// +optional
	Adoption APIAdoptionSpec `json:"adoption,omitzero"`

	// deletionPolicy is what happens to the application in Authentik when
	// this resource is deleted: Delete or Retain. When unset: Delete for an
	// application this resource created, Retain for an adopted one.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// AuthentikApplicationStatus defines the observed state of AuthentikApplication.
type AuthentikApplicationStatus struct {
	// conditions represent the current state of the resource. "Ready" is
	// True once the application in Authentik matches the spec.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// applicationUid is the Authentik primary key (UUID) of the application
	// this resource manages, whether it created or adopted it.
	// +optional
	ApplicationUID string `json:"applicationUid,omitempty"`

	// provenance is Created or Adopted once decided; empty means not yet
	// decided (including while an adoption dry run is reporting).
	// +optional
	Provenance Provenance `json:"provenance,omitempty"`

	// slug is the application's slug in Authentik.
	// +optional
	Slug string `json:"slug,omitempty"`

	// pendingCreate holds the slug while a create is in flight. It lets the
	// next reconcile recognise its own application if the create's outcome
	// was lost (timeout, failed status write) without trusting anything
	// another resource could also claim.
	// +optional
	PendingCreate string `json:"pendingCreate,omitempty"`

	// providerPk is the primary key of the provider currently assigned.
	// +optional
	ProviderPK int32 `json:"providerPk,omitempty"`

	// observedGeneration is the spec generation last synced to Authentik.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="has(self.spec.slug) || self.metadata.name.matches('^[-a-zA-Z0-9_]{1,50}$')",message="metadata.name is not a valid slug (letters, digits, - and _; max 50); set spec.slug"
// +kubebuilder:validation:XValidation:rule="(has(self.spec.slug) ? self.spec.slug : self.metadata.name) == (has(oldSelf.spec.slug) ? oldSelf.spec.slug : oldSelf.metadata.name)",message="slug is immutable (it is the application's identity in Authentik URLs)"
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=".status.slug"
// +kubebuilder:printcolumn:name="Provenance",type=string,JSONPath=".status.provenance"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikApplication is the Schema for the authentikapplications API.
type AuthentikApplication struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikApplication
	// +required
	Spec AuthentikApplicationSpec `json:"spec"`

	// status defines the observed state of AuthentikApplication
	// +optional
	Status AuthentikApplicationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikApplicationList contains a list of AuthentikApplication.
type AuthentikApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []AuthentikApplication `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikApplication{}, &AuthentikApplicationList{})
		return nil
	})
}
