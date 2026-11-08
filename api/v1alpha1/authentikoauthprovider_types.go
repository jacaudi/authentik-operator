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

// Durations use Authentik's own format, e.g. "minutes=5", "hours=1;minutes=30"
// or "hours=1.5" (units weeks..microseconds, fractional values allowed).

// OAuthClientType is an OAuth2 client type.
// +kubebuilder:validation:Enum=confidential;public
type OAuthClientType string

// OAuthGrantType is an OAuth2 grant type Authentik can allow for a provider.
// +kubebuilder:validation:Enum=authorization_code;implicit;hybrid;refresh_token;client_credentials;password;"urn:ietf:params:oauth:grant-type:device_code";"urn:ietf:params:oauth:grant-type:token-exchange"
type OAuthGrantType string

// OAuthSubMode selects the value of the sub claim.
// +kubebuilder:validation:Enum=hashed_user_id;user_id;user_uuid;user_username;user_email;user_upn
type OAuthSubMode string

// OAuthIssuerMode selects how the iss claim is built.
// +kubebuilder:validation:Enum=global;per_provider
type OAuthIssuerMode string

// OAuthLogoutMethod selects how the provider notifies clients on logout.
// +kubebuilder:validation:Enum=backchannel;frontchannel
type OAuthLogoutMethod string

// RedirectURIMatchingMode selects how a redirect URI is compared.
// +kubebuilder:validation:Enum=strict;regex
type RedirectURIMatchingMode string

// RedirectURIType selects what a redirect URI is used for.
// +kubebuilder:validation:Enum=authorization;logout
type RedirectURIType string

// RedirectURI is an allowed redirect URI.
type RedirectURI struct {
	// url is the redirect URI, or a regular expression when matchingMode is
	// regex. Custom schemes (e.g. com.example.app:/callback) are allowed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +required
	URL string `json:"url"`

	// matchingMode is strict (exact match) or regex.
	// +kubebuilder:default=strict
	// +optional
	MatchingMode RedirectURIMatchingMode `json:"matchingMode,omitempty"`

	// type is authorization (default) or logout (post-logout redirect).
	// +kubebuilder:default=authorization
	// +optional
	Type RedirectURIType `json:"type,omitempty"`
}

// AuthentikOAuthProviderSpec defines the desired state of AuthentikOAuthProvider.
//
// References to other Authentik objects use stable identifiers -- flows by
// slug, scopes by scope_name, certificates and property mappings by name (or
// property mappings by UUID) -- never by display name alone.
// +kubebuilder:validation:XValidation:rule="has(self.authentikRef) == has(oldSelf.authentikRef) && (!has(self.authentikRef) || self.authentikRef.name == oldSelf.authentikRef.name)",message="authentikRef is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.logoutMethod) || self.logoutMethod != 'frontchannel' || has(self.logoutUri)",message="logoutMethod frontchannel requires logoutUri"
type AuthentikOAuthProviderSpec struct {
	// authentikRef names the AuthentikDeployment (same namespace) whose
	// connection this object is reconciled against. When omitted, the
	// operator's default connection is used.
	// +optional
	AuthentikRef *AuthentikRef `json:"authentikRef,omitempty"`

	// name of the provider in Authentik. Defaults to metadata.name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +optional
	Name string `json:"name,omitempty"`

	// authorizationFlow is the slug of the flow used when authorizing this provider.
	// +kubebuilder:validation:MinLength=1
	// +required
	AuthorizationFlow string `json:"authorizationFlow"`

	// invalidationFlow is the slug of the flow used when ending the session.
	// +kubebuilder:validation:MinLength=1
	// +required
	InvalidationFlow string `json:"invalidationFlow"`

	// authenticationFlow is the slug of the flow used when an
	// unauthenticated user accesses the application. Optional.
	// +optional
	AuthenticationFlow string `json:"authenticationFlow,omitempty"`

	// redirectUris lists the allowed redirect URIs.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +required
	RedirectURIs []RedirectURI `json:"redirectUris"`

	// scopes lists OAuth scope mappings by scope_name (e.g. openid, profile,
	// email). When several mappings share a scope name, the managed
	// (built-in) one is used.
	// +kubebuilder:default={openid,profile,email}
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// propertyMappings lists additional property mappings by UUID or name.
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	// +optional
	PropertyMappings []string `json:"propertyMappings,omitempty"`

	// clientType is confidential (has a client secret) or public.
	// +kubebuilder:default=confidential
	// +optional
	ClientType OAuthClientType `json:"clientType,omitempty"`

	// grantTypes lists the grant types the provider accepts. Always sent
	// explicitly: Authentik 2026.8+ rejects logins for providers created
	// with an empty list ("Invalid grant_type for provider").
	// +kubebuilder:default={authorization_code,refresh_token}
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +optional
	GrantTypes []OAuthGrantType `json:"grantTypes,omitempty"`

	// clientId pins the client ID. When omitted, the operator uses a client
	// ID derived from this resource's UID ("k8s-<uid>"), reported in
	// status.clientId and never written back to spec. Client IDs are not
	// secret; the derived one lets the operator recognise its own provider
	// if a create's outcome was lost. Printable ASCII without spaces, so it
	// cannot inject keys into a rendered credentials Secret.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[\x21-\x7E]+$`
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// accessCodeValidity, e.g. "minutes=1".
	// +kubebuilder:default="minutes=1"
	// +kubebuilder:validation:Pattern=`^((weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?;)*(weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?$`
	// +optional
	AccessCodeValidity string `json:"accessCodeValidity,omitempty"`

	// accessTokenValidity, e.g. "minutes=5".
	// +kubebuilder:default="minutes=5"
	// +kubebuilder:validation:Pattern=`^((weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?;)*(weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?$`
	// +optional
	AccessTokenValidity string `json:"accessTokenValidity,omitempty"`

	// refreshTokenValidity, e.g. "days=30".
	// +kubebuilder:default="days=30"
	// +kubebuilder:validation:Pattern=`^((weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?;)*(weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?$`
	// +optional
	RefreshTokenValidity string `json:"refreshTokenValidity,omitempty"`

	// refreshTokenThreshold: a refresh token valid for less than this is
	// renewed on refresh; "seconds=0" renews on every refresh.
	// +kubebuilder:default="seconds=0"
	// +kubebuilder:validation:Pattern=`^((weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?;)*(weeks|days|hours|minutes|seconds|milliseconds|microseconds)=[0-9]+(\.[0-9]+)?$`
	// +optional
	RefreshTokenThreshold string `json:"refreshTokenThreshold,omitempty"`

	// subMode selects the value of the sub claim.
	// +kubebuilder:default=hashed_user_id
	// +optional
	SubMode OAuthSubMode `json:"subMode,omitempty"`

	// includeClaimsInIdToken includes user claims from scopes in the id_token.
	// +kubebuilder:default=true
	// +optional
	IncludeClaimsInIDToken *bool `json:"includeClaimsInIdToken,omitempty"`

	// issuerMode selects how the iss claim is built.
	// +kubebuilder:default=per_provider
	// +optional
	IssuerMode OAuthIssuerMode `json:"issuerMode,omitempty"`

	// signingKey is the name of the certificate-key pair used to sign tokens.
	// +kubebuilder:default="authentik Self-signed Certificate"
	// +kubebuilder:validation:MinLength=1
	// +optional
	SigningKey string `json:"signingKey,omitempty"`

	// encryptionKey is the name of a certificate-key pair used to encrypt
	// tokens (JWE). Optional.
	// +optional
	EncryptionKey string `json:"encryptionKey,omitempty"`

	// logoutUri is notified on logout. Optional.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	LogoutURI string `json:"logoutUri,omitempty"`

	// secret, when set, renders the provider's client credentials and
	// endpoint URLs into a Secret in this namespace (owned by this
	// resource, deleted with it).
	// +optional
	Secret *CredentialsSecretSpec `json:"secret,omitempty"`

	// logoutMethod is backchannel (server-to-server) or frontchannel
	// (browser iframes; requires logoutUri).
	// +kubebuilder:default=backchannel
	// +optional
	LogoutMethod OAuthLogoutMethod `json:"logoutMethod,omitempty"`

	// adoption controls whether this resource may take over a provider that
	// already exists in Authentik (for example one created by another
	// operator) instead of reporting a Conflict. See APIAdoptionSpec for
	// what adoption writes and which defaulted fields are included. An
	// adopted provider's client ID and secret are kept as they are (the
	// credentials Secret is rendered from the existing values) unless
	// spec.clientId is set. Unset optional fields (authenticationFlow,
	// encryptionKey, logoutUri, name) are left as the existing provider has
	// them. The defaulted fields adoption will write are: scopes, clientType,
	// grantTypes, accessCodeValidity, accessTokenValidity,
	// refreshTokenValidity, refreshTokenThreshold, subMode,
	// includeClaimsInIdToken, issuerMode, signingKey, logoutMethod, and the
	// matchingMode and type of each redirect URI.
	// +optional
	Adoption ProviderAdoptionSpec `json:"adoption,omitzero"`

	// deletionPolicy is what happens to the provider in Authentik when this
	// resource is deleted: Delete or Retain. When unset: Delete for a
	// provider this resource created, Retain for an adopted one. The
	// rendered credentials Secret is a Kubernetes object owned by this
	// resource and is garbage-collected with it under either policy.
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// CredentialsSecretSpec describes the Secret holding a provider's client
// credentials. The Secret's content, labels and annotations come from the
// referenced AuthentikCredentialsTemplate, which administrators own; without
// templateRef the keys are client-id, client-secret, issuer-url,
// authorization-url, token-url and userinfo-url.
//
// Security note: whoever can create or edit an AuthentikOAuthProvider can
// have the operator create a Secret, with any name not already taken, in
// that namespace, holding the provider's client secret.
type CredentialsSecretSpec struct {
	// name of the Secret. Defaults to "<metadata.name>-oauth". Renaming
	// deletes the previously rendered Secret once the new one exists.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	Name string `json:"name,omitempty"`

	// templateRef names the AuthentikCredentialsTemplate that renders the
	// Secret. The template's namespaceSelector must match this namespace.
	// +optional
	TemplateRef *CredentialsTemplateRef `json:"templateRef,omitempty"`
}

// CredentialsTemplateRef names a cluster-scoped AuthentikCredentialsTemplate.
type CredentialsTemplateRef struct {
	// name of the AuthentikCredentialsTemplate.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	Name string `json:"name"`
}

// AuthentikOAuthProviderStatus defines the observed state of AuthentikOAuthProvider.
type AuthentikOAuthProviderStatus struct {
	// conditions represent the current state of the resource. "Ready" is
	// True once the provider in Authentik matches the spec.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// providerPk is the primary key of the provider this resource manages in
	// Authentik, whether it created or adopted it.
	// +optional
	ProviderPK int32 `json:"providerPk,omitempty"`

	// provenance is Created or Adopted once decided; empty means not yet
	// decided (including while an adoption dry run is reporting).
	// +optional
	Provenance Provenance `json:"provenance,omitempty"`

	// clientId is the provider's client ID (generated by Authentik unless
	// spec.clientId is set). The client secret is never written to status.
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// pendingCreate holds the provider name while a create is in flight. It
	// lets the next reconcile recognise its own provider if the create's
	// outcome was lost (timeout, failed status write), which a pinned
	// spec.clientId alone cannot prove: another provider could carry it.
	// +optional
	PendingCreate string `json:"pendingCreate,omitempty"`

	// observedGeneration is the spec generation last synced to Authentik.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// secretName is the Secret this resource last rendered credentials into.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Client ID",type=string,JSONPath=".status.clientId"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Provenance",type=string,JSONPath=".status.provenance"
// +kubebuilder:printcolumn:name="Credentials",type=string,JSONPath=".status.conditions[?(@.type=='CredentialsReady')].status"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].reason",priority=1
// +kubebuilder:validation:XValidation:rule="!has(self.spec.secret) || has(self.spec.secret.name) || size(self.metadata.name) <= 247",message="metadata.name is too long for the default Secret name <name>-oauth; set spec.secret.name"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikOAuthProvider is the Schema for the authentikoauthproviders API.
type AuthentikOAuthProvider struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikOAuthProvider
	// +required
	Spec AuthentikOAuthProviderSpec `json:"spec"`

	// status defines the observed state of AuthentikOAuthProvider
	// +optional
	Status AuthentikOAuthProviderStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikOAuthProviderList contains a list of AuthentikOAuthProvider.
type AuthentikOAuthProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []AuthentikOAuthProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikOAuthProvider{}, &AuthentikOAuthProviderList{})
		return nil
	})
}
