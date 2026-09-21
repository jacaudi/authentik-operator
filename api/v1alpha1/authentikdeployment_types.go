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

// AuthentikDeploymentSpec defines the desired state of AuthentikDeployment
type AuthentikDeploymentSpec struct {
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
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

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
