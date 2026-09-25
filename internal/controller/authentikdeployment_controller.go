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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

	// Field indexes used to map Secret / ConfigMap events to deployments.
	indexConnectionSecrets    = ".spec.connection.secrets"
	indexConnectionConfigMaps = ".spec.connection.configMaps"

	healthyRecheck   = 5 * time.Minute
	unhealthyRecheck = 30 * time.Second
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

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
		user, err := resolved.Client.Ping(ctx)
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
func (r *AuthentikDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(ctx, &authentikiov1alpha1.AuthentikDeployment{}, indexConnectionSecrets,
		func(o client.Object) []string {
			return connectionSecretNames(o.(*authentikiov1alpha1.AuthentikDeployment))
		}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &authentikiov1alpha1.AuthentikDeployment{}, indexConnectionConfigMaps,
		func(o client.Object) []string {
			return connectionConfigMapNames(o.(*authentikiov1alpha1.AuthentikDeployment))
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikDeployment{}).
		// Metadata-only watches: react to token / CA changes without caching
		// Secret contents cluster-wide.
		WatchesMetadata(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapBy(indexConnectionSecrets))).
		WatchesMetadata(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapBy(indexConnectionConfigMaps))).
		Named("authentikdeployment").
		Complete(r)
}

func (r *AuthentikDeploymentReconciler) mapBy(index string) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		var list authentikiov1alpha1.AuthentikDeploymentList
		if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()),
			client.MatchingFields{index: o.GetName()}); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to list AuthentikDeployments for watched object",
				"object", client.ObjectKeyFromObject(o))
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
			}})
		}
		return reqs
	}
}

func connectionSecretNames(d *authentikiov1alpha1.AuthentikDeployment) []string {
	c := d.Spec.Connection
	if c == nil {
		return nil
	}
	names := []string{c.TokenSecretRef.Name}
	if c.CABundle != nil && c.CABundle.SecretKeyRef != nil && c.CABundle.SecretKeyRef.Name != c.TokenSecretRef.Name {
		names = append(names, c.CABundle.SecretKeyRef.Name)
	}
	return names
}

func connectionConfigMapNames(d *authentikiov1alpha1.AuthentikDeployment) []string {
	c := d.Spec.Connection
	if c == nil || c.CABundle == nil || c.CABundle.ConfigMapKeyRef == nil {
		return nil
	}
	return []string{c.CABundle.ConfigMapKeyRef.Name}
}
