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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	// AnnotationCreatedBy marks an object this operator created itself, as
	// opposed to one it adopted. Teardown deletes created objects and releases
	// adopted ones.
	AnnotationCreatedBy = "authentik.io/created-by"

	// AnnotationAdoptedBy names the AuthentikDeployment that adopted a
	// pre-existing object.
	AnnotationAdoptedBy = "authentik.io/adopted-by"
)

// DeploymentMode selects what an AuthentikDeployment manages.
// +kubebuilder:validation:Enum=Managed;ConnectOnly
type DeploymentMode string

const (
	// DeploymentModeManaged: the operator manages the Authentik workloads
	// (Deployment, Service, Ingress, RBAC).
	DeploymentModeManaged DeploymentMode = "Managed"
	// DeploymentModeConnectOnly: the operator manages no workloads; the
	// resource only supplies a connection (URL, token, CA) to an Authentik
	// installed some other way, e.g. by Helm.
	DeploymentModeConnectOnly DeploymentMode = "ConnectOnly"
)

// AuthentikDeploymentSpec defines the desired state of AuthentikDeployment
// +kubebuilder:validation:XValidation:rule="self.mode != 'ConnectOnly' || has(self.connection)",message="connection is required when mode is ConnectOnly"
type AuthentikDeploymentSpec struct {
	// mode selects whether this resource manages the Authentik workloads
	// (Managed) or only describes how to reach an existing Authentik
	// (ConnectOnly). There is no default: managing workloads must be asked for.
	// Immutable: switching would orphan or adopt workloads implicitly.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode is immutable"
	// +required
	Mode DeploymentMode `json:"mode"`

	// connection describes how the operator reaches this Authentik's API.
	// Required in ConnectOnly mode.
	// +optional
	Connection *ConnectionSpec `json:"connection,omitempty"`

	// adoption controls whether this resource may take over pre-existing
	// objects that it did not create.
	// +optional
	Adoption AdoptionSpec `json:"adoption,omitzero"`
}

// AdoptionSpec governs takeover of pre-existing objects.
//
// Adoption is opt-in. With enabled false, a matching object the operator did
// not create is an error rather than something to overwrite, so pointing an
// AuthentikDeployment at a live install cannot silently reconfigure it.
type AdoptionSpec struct {
	// enabled permits this AuthentikDeployment to adopt pre-existing objects
	// matching the names it manages. An adopted object gains an owner
	// reference and the operator's labels; fields the spec does not set are
	// left as they are, and on teardown the object is released rather than
	// deleted.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// AuthentikDeploymentStatus defines the observed state of AuthentikDeployment.
type AuthentikDeploymentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the AuthentikDeployment resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	//
	// "Connected" reports whether the Authentik API is reachable with the
	// configured token.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// url is the Authentik base URL this resource resolved to.
	// +optional
	URL string `json:"url,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=".status.url"
// +kubebuilder:printcolumn:name="Connected",type=string,JSONPath=".status.conditions[?(@.type=='Connected')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AuthentikDeployment is the Schema for the authentikdeployments API
type AuthentikDeployment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AuthentikDeployment
	// +required
	Spec AuthentikDeploymentSpec `json:"spec"`

	// status defines the observed state of AuthentikDeployment
	// +optional
	Status AuthentikDeploymentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AuthentikDeploymentList contains a list of AuthentikDeployment
type AuthentikDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AuthentikDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &AuthentikDeployment{}, &AuthentikDeploymentList{})
		return nil
	})
}
