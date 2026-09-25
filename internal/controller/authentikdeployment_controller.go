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
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	// ConditionConnected reports whether the Authentik API is reachable with
	// the configured token.
	ConditionConnected = "Connected"

	reasonConnected        = "Connected"
	reasonConnectionFailed = "ConnectionFailed"
	reasonNotImplemented   = "ManagedModeNotImplemented"

	healthyRecheck   = 5 * time.Minute
	unhealthyRecheck = 30 * time.Second
	// pingTimeout bounds one health check, retries included, so an
	// unreachable Authentik cannot monopolise a worker.
	pingTimeout = 15 * time.Second

	maxConcurrentReconciles = 4
)

// AuthentikDeploymentReconciler reconciles a AuthentikDeployment object
type AuthentikDeploymentReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Connections *connection.Resolver
}

// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get

// Reconcile verifies the connection an AuthentikDeployment describes and
// reports it in the Connected condition. Managing Authentik workloads
// (Managed mode) is not implemented yet.
func (r *AuthentikDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var d authentikiov1alpha1.AuthentikDeployment
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		if apierrors.IsNotFound(err) {
			r.Connections.Forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cond := metav1.Condition{Type: ConditionConnected, ObservedGeneration: d.Generation}
	status := d.Status.DeepCopy()
	var result ctrl.Result

	switch {
	case d.Spec.Connection == nil && d.Spec.Mode == authentikiov1alpha1.DeploymentModeManaged:
		cond.Status = metav1.ConditionUnknown
		cond.Reason = reasonNotImplemented
		cond.Message = "Managed mode does not deploy Authentik yet; set spec.connection to use an existing install"
		status.URL = ""
	default:
		resolved, err := r.Connections.ForDeployment(ctx, &d)
		if err != nil {
			reason := connection.Reason(err)
			if reason == "" {
				return ctrl.Result{}, err
			}
			cond.Status = metav1.ConditionFalse
			cond.Reason = reason
			cond.Message = err.Error()
			status.URL = ""
			result.RequeueAfter = unhealthyRecheck
			break
		}
		status.URL = resolved.Client.BaseURL()
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		user, err := resolved.Client.Ping(pingCtx)
		cancel()
		if err != nil {
			log.Info("Could not connect to Authentik", "url", status.URL, "error", err.Error())
			cond.Status = metav1.ConditionFalse
			cond.Reason = reasonConnectionFailed
			cond.Message = err.Error()
			result.RequeueAfter = unhealthyRecheck
			break
		}
		cond.Status = metav1.ConditionTrue
		cond.Reason = reasonConnected
		cond.Message = "Authenticated as " + user
		result.RequeueAfter = healthyRecheck
	}

	meta.SetStatusCondition(&status.Conditions, cond)
	if !equality.Semantic.DeepEqual(status, &d.Status) {
		patch := client.MergeFrom(d.DeepCopy())
		d.Status = *status
		if err := r.Status().Patch(ctx, &d, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Secrets and ConfigMaps are deliberately not watched: a watch needs
// cluster-wide list/watch on Secrets (RBAC has no metadata-only verb). Token
// and CA rotation still take effect immediately for API objects, which
// re-read the Secret on every reconcile; the Connected condition catches up
// on the next periodic recheck.
func (r *AuthentikDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikDeployment{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Named("authentikdeployment").
		Complete(r)
}
