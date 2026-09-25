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

// AuthentikRef names the AuthentikDeployment whose connection an Authentik
// API object (application, provider, user, group) is reconciled against.
//
// When authentikRef is omitted, the operator-level default connection
// (configured via flags / environment) is used; if none is configured, the
// object is not reconciled and reports why in its status.
type AuthentikRef struct {
	// name of an AuthentikDeployment in the same namespace. Cross-namespace
	// references are deliberately not supported: the referenced deployment
	// carries an API token, and referencing it grants its privileges.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
}

// ConnectionSpec describes how the operator reaches an Authentik instance's API.
// +kubebuilder:validation:XValidation:rule="!(has(self.insecureSkipVerify) && self.insecureSkipVerify && has(self.caBundle))",message="caBundle and insecureSkipVerify are mutually exclusive"
type ConnectionSpec struct {
	// url is the base URL of Authentik, e.g. https://authentik.example.com or
	// http://authentik-server.authentik.svc.cluster.local. Plain HTTP is used
	// only when the URL says http.
	// +kubebuilder:validation:Pattern=`^https?://[^/?#@]+(/[^?#@]*)?$`
	// +kubebuilder:validation:MaxLength=2048
	// +required
	URL string `json:"url"`

	// tokenSecretRef selects the Secret key holding an Authentik API token.
	// The Secret must be in the same namespace as the AuthentikDeployment.
	// The token is never logged or copied into any status.
	// +required
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`

	// caBundle is an optional PEM CA bundle trusted in addition to the
	// system roots when verifying Authentik's TLS certificate.
	// +optional
	CABundle *CABundleSource `json:"caBundle,omitempty"`

	// insecureSkipVerify disables TLS certificate verification. Intended for
	// testing only.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// SecretKeyRef selects a key of a Secret in the referring object's namespace.
type SecretKeyRef struct {
	// name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// key within the Secret.
	// +kubebuilder:default=token
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Key string `json:"key,omitempty"`
}

// ConfigMapKeyRef selects a key of a ConfigMap in the referring object's namespace.
type ConfigMapKeyRef struct {
	// name of the ConfigMap.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// key within the ConfigMap.
	// +kubebuilder:default=ca.crt
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Key string `json:"key,omitempty"`
}

// CABundleSource selects a PEM CA bundle from exactly one of a ConfigMap or a Secret.
// +kubebuilder:validation:XValidation:rule="has(self.configMapKeyRef) != has(self.secretKeyRef)",message="exactly one of configMapKeyRef or secretKeyRef must be set"
type CABundleSource struct {
	// configMapKeyRef selects a ConfigMap key holding the bundle
	// (e.g. one populated by trust-manager).
	// +optional
	ConfigMapKeyRef *ConfigMapKeyRef `json:"configMapKeyRef,omitempty"`

	// secretKeyRef selects a Secret key holding the bundle
	// (e.g. ca.crt of a cert-manager Certificate).
	// +optional
	SecretKeyRef *SecretKeyRef `json:"secretKeyRef,omitempty"`
}
