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

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/credentials"
)

const (
	reasonTemplateValid            = "Valid"
	reasonInvalidNamespaceSelector = "InvalidNamespaceSelector"
	reasonInvalidMetadata          = "InvalidMetadata"
)

// AuthentikCredentialsTemplateReconciler validates AuthentikCredentialsTemplates
// and reports the result in the Ready condition, so administrators see
// template mistakes on the template instead of on every provider using it.
// Rendering happens in the provider reconciler.
type AuthentikCredentialsTemplateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=authentik.io,resources=authentikcredentialstemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikcredentialstemplates/status,verbs=get;update;patch

// Reconcile validates the template and its namespaceSelector.
func (r *AuthentikCredentialsTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var t authentikiov1alpha1.AuthentikCredentialsTemplate
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cond := metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: reasonTemplateValid,
		Message: "template and namespaceSelector are valid", ObservedGeneration: t.Generation}
	if err := credentials.Check(t.Spec.Template); err != nil {
		if !credentials.IsTemplateError(err) {
			return ctrl.Result{}, err
		}
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reasonTemplateError, err.Error()
	} else if errs := validateTemplateMetadata(&t.Spec); len(errs) > 0 {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reasonInvalidMetadata, errs.ToAggregate().Error()
	} else if _, err := metav1.LabelSelectorAsSelector(&t.Spec.NamespaceSelector); err != nil {
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonInvalidNamespaceSelector
		cond.Message = fmt.Sprintf("invalid namespaceSelector: %v", err)
	}

	base := t.DeepCopy()
	if !meta.SetStatusCondition(&t.Status.Conditions, cond) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Patch(ctx, &t, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// validateTemplateMetadata applies the API server's own label and
// annotation rules, so a template the API server would reject on every
// Secret fails here once.
func validateTemplateMetadata(spec *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) field.ErrorList {
	errs := metav1validation.ValidateLabels(spec.Labels, field.NewPath("spec", "labels"))
	return append(errs, apivalidation.ValidateAnnotations(spec.Annotations, field.NewPath("spec", "annotations"))...)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthentikCredentialsTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikCredentialsTemplate{}).
		Named("authentikcredentialstemplate").
		Complete(r)
}
