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

// AuthentikCredentialsTemplateSpec defines how credentials Secrets that use
// this template are rendered.
//
// Template variables for OAuth providers (Go text/template, missing keys are
// errors): .ClientID .ClientSecret .IssuerURL .AuthURL .TokenURL
// .UserInfoURL .LogoutURL .JWKSURL .ProviderInfoURL .Slug .Name (Slug and
// Name are the assigned application's). The rendered output must be a YAML
// map of string keys to string values; each entry becomes a Secret key.
// Without a template the keys are client-id, client-secret, issuer-url,
// authorization-url, token-url and userinfo-url.
//
// Templates are restricted to text, {{ .Var }}, if/else, comparisons (eq,
// ne, lt, le, gt, ge, and, or, not), print and printf with %s/%q/%v.
// .ClientSecret may only be printed. Values are inserted as-is, so quote
// values that may contain ':' or newlines: {{ printf "%q" .Name }}.
type AuthentikCredentialsTemplateSpec struct {
	// template renders the Secret data; see the type description.
	// +kubebuilder:validation:MaxLength=65536
	// +optional
	Template string `json:"template,omitempty"`

	// labels added to every Secret rendered from this template. Keys under
	// authentik.io/ (and its subdomains) are reserved; keys the operator
	// sets itself (app.kubernetes.io/managed-by) are overridden.
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.matches('^([^/]*[.])?authentik[.]io/'))",message="label keys under authentik.io/ are reserved"
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations added to every Secret rendered from this template. Keys
	// under authentik.io/ (and its subdomains) are reserved.
	// +kubebuilder:validation:XValidation:rule="self.all(k, !k.matches('^([^/]*[.])?authentik[.]io/'))",message="annotation keys under authentik.io/ are reserved"
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// namespaceSelector selects the namespaces whose providers may use this
	// template; {} allows every namespace. Required, so that allowing every
	// namespace is an explicit choice. The selector is only as strong as
	// control over namespace labels: if tenants can create namespaces or
	// label them, prefer kubernetes.io/metadata.name, which the API server
	// sets to the namespace's name.
	// +required
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

// AuthentikCredentialsTemplateStatus defines the observed state of AuthentikCredentialsTemplate.
type AuthentikCredentialsTemplateStatus struct {
	// conditions: Ready reports whether the template, labels, annotations
	// and namespaceSelector are valid.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikCredentialsTemplate is an administrator-owned, cluster-scoped
// description of a credentials Secret: its data template, labels and
// annotations. Providers reference it by name and choose only the Secret's
// name, so granting a team AuthentikOAuthProvider access does not let it
// choose what operator-written Secrets contain. Grant write access to this
// kind to platform administrators only.
type AuthentikCredentialsTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikCredentialsTemplate
	// +required
	Spec AuthentikCredentialsTemplateSpec `json:"spec"`

	// status defines the observed state of AuthentikCredentialsTemplate
	// +optional
	Status AuthentikCredentialsTemplateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikCredentialsTemplateList contains a list of AuthentikCredentialsTemplate.
type AuthentikCredentialsTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []AuthentikCredentialsTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikCredentialsTemplate{}, &AuthentikCredentialsTemplateList{})
		return nil
	})
}
