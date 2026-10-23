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
	"errors"
	"fmt"
	"net/http"
	"strings"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	reasonProviderNotFound           = "ProviderNotFound"
	reasonProviderNotReady           = "ProviderNotReady"
	reasonProviderConnectionMismatch = "ProviderConnectionMismatch"

	// indexProviderRef maps an AuthentikOAuthProvider to the applications
	// that reference it.
	indexProviderRef = ".spec.providerRef.name"
)

// AuthentikApplicationReconciler reconciles a AuthentikApplication object.
type AuthentikApplicationReconciler struct {
	client.Client

	Scheme *runtime.Scheme
	// Connections resolves the Authentik client for spec.authentikRef.
	Connections *connection.Resolver
	// Recorder emits Kubernetes events; may be nil in tests.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=authentik.io,resources=authentikapplications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikapplications/finalizers,verbs=update
// +kubebuilder:rbac:groups=authentik.io,resources=authentikoauthproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile makes the application in Authentik match the spec.
//
// Identity: status.applicationUid (Authentik's UUID pk) plus status.slug.
// The pk is re-checked before every change or delete and is kept until a
// replacement exists, so a transient 404 cannot lose track of it. A
// pre-existing application with the same slug is a Conflict, except when
// status.pendingCreate shows this resource's own create was in flight: the
// marker is persisted before every create, so only a create whose outcome
// was lost can match it.
func (r *AuthentikApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var a authentikiov1alpha1.AuthentikApplication
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !a.DeletionTimestamp.IsZero() {
		orig := a.Status.DeepCopy()
		if err := r.finalize(ctx, &a); err != nil {
			return ctrl.Result{}, errors.Join(err, r.patchStatus(ctx, &a, orig))
		}
		return ctrl.Result{}, nil
	}
	base := a.DeepCopy()
	if controllerutil.AddFinalizer(&a, FinalizerName) {
		if err := r.Patch(ctx, &a, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := a.Status.DeepCopy()
	persist := func() error {
		if err := r.patchStatus(ctx, &a, orig); err != nil {
			return err
		}
		orig = a.Status.DeepCopy()
		return nil
	}
	result, syncErr := r.sync(ctx, &a, persist)
	if err := persist(); err != nil {
		return ctrl.Result{}, errors.Join(syncErr, err)
	}
	return result, syncErr
}

// sync does the work and records the outcome in a.Status. persist writes the
// status immediately; it is used to record the pending-create marker before
// the create request is sent.
func (r *AuthentikApplicationReconciler) sync(ctx context.Context, a *authentikiov1alpha1.AuthentikApplication,
	persist func() error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}

	providerPK, ok, err := r.providerPK(ctx, a)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		// Waiting on the provider; its watch re-triggers this reconcile.
		return retryLater, nil
	}

	resolved, err := r.Connections.ForRef(ctx, a.Namespace, a.Spec.AuthentikRef)
	if err != nil {
		if reason := connection.Reason(err); reason != "" {
			r.setReady(a, metav1.ConditionFalse, reason, err.Error())
			return retryLater, nil
		}
		return ctrl.Result{}, err
	}
	c := resolved.Client
	want := desiredApplication(a, providerPK)

	var got *api.Application
	if uid := a.Status.ApplicationUID; uid != "" {
		got, err = c.GetApplication(ctx, a.Status.Slug)
		switch {
		case err == nil && got.Pk != uid:
			r.setReady(a, metav1.ConditionFalse, reasonConflict, fmt.Sprintf(
				"application %q in Authentik has pk %s, not %s recorded by this resource; refusing to modify it. "+
					"If this resource should manage a new application, clear status.applicationUid",
				a.Status.Slug, got.Pk, uid))
			return retryLater, nil
		case authentik.IsNotFound(err):
			// Keep status.applicationUid until a replacement exists: a 404
			// can be transient (proxy during a rollout), and the uid is what
			// lets the finalizer clean up.
			got = nil
		case err != nil:
			return r.authentikError(a, err)
		}
	}

	if got == nil {
		existing, err := c.GetApplication(ctx, want.Slug)
		switch {
		case err == nil && a.Status.ApplicationUID != "" && existing.Pk == a.Status.ApplicationUID:
			got = existing // the earlier 404 was transient
		case err == nil && a.Status.PendingCreate == want.Slug:
			log.Info("Recovered Application from a create whose outcome was lost", "slug", existing.Slug)
			r.event(a, corev1.EventTypeNormal, "Recovered", "Create",
				"Recovered application %q created by an earlier reconcile", existing.Slug)
			got = existing
		case err == nil:
			r.setReady(a, metav1.ConditionFalse, reasonConflict, fmt.Sprintf(
				"an application with slug %q already exists in Authentik and was not created by this resource; "+
					"adopting existing applications is not supported yet", want.Slug))
			return retryLater, nil
		case !authentik.IsNotFound(err):
			return r.authentikError(a, err)
		}
	}

	if got == nil {
		if a.Status.ApplicationUID != "" {
			log.Info("Application was deleted outside the operator; recreating", "slug", want.Slug)
			r.event(a, corev1.EventTypeWarning, "ApplicationMissing", "Recreate",
				"Application %q no longer exists in Authentik; recreating it", want.Slug)
		}
		a.Status.PendingCreate = want.Slug
		if err := persist(); err != nil {
			return ctrl.Result{}, err
		}
		created, err := c.CreateApplication(ctx, *want)
		if err != nil {
			if code := authentik.StatusCode(err); code >= 400 && code < 500 {
				a.Status.PendingCreate = "" // definitely not created
			}
			return r.writeError(a, err)
		}
		log.Info("Created Application", "slug", created.Slug, "pk", created.Pk)
		r.event(a, corev1.EventTypeNormal, "Created", "Create", "Created application %q", created.Slug)
		got = created
	} else if patch, changed := applicationPatch(want, got); len(changed) > 0 {
		updated, err := c.PatchApplication(ctx, got.Slug, patch)
		if err != nil {
			return r.writeError(a, err)
		}
		log.Info("Updated Application", "slug", got.Slug, "fields", changed)
		r.event(a, corev1.EventTypeNormal, "Updated", "Update", "Updated application fields: %s", strings.Join(changed, ", "))
		got = updated
	}

	a.Status.PendingCreate = ""
	a.Status.ApplicationUID = got.Pk
	a.Status.Slug = got.Slug
	a.Status.ProviderPK = int32OrZero(got.Provider.Get())
	a.Status.ObservedGeneration = a.Generation
	r.setReady(a, metav1.ConditionTrue, reasonSynced, fmt.Sprintf("Application %q is in sync", got.Slug))
	return ctrl.Result{RequeueAfter: syncedRecheck}, nil
}

// providerPK resolves spec.providerRef to the provider's Authentik pk. ok is
// false (with the Ready condition set) while the provider is missing, not
// Ready, or on a different connection.
func (r *AuthentikApplicationReconciler) providerPK(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication) (int32, bool, error) {
	ref := a.Spec.ProviderRef
	if ref == nil {
		return 0, true, nil
	}
	var p authentikiov1alpha1.AuthentikOAuthProvider
	if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: ref.Name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.setReady(a, metav1.ConditionFalse, reasonProviderNotFound,
				fmt.Sprintf("%s %s/%s not found", ref.Kind, a.Namespace, ref.Name))
			return 0, false, nil
		}
		return 0, false, err
	}
	if !sameAuthentikRef(p.Spec.AuthentikRef, a.Spec.AuthentikRef) {
		r.setReady(a, metav1.ConditionFalse, reasonProviderConnectionMismatch, fmt.Sprintf(
			"provider %s uses a different authentikRef than this application; both must point at the same Authentik",
			ref.Name))
		return 0, false, nil
	}
	cond := meta.FindStatusCondition(p.Status.Conditions, ConditionReady)
	if p.Status.ProviderPK == 0 || cond == nil || cond.Status != metav1.ConditionTrue ||
		cond.ObservedGeneration != p.Generation || !p.DeletionTimestamp.IsZero() {
		// Only the provider's reason, not its message, to avoid rewriting
		// this status every time the provider's message text changes.
		msg := fmt.Sprintf("waiting for provider %s to become Ready", ref.Name)
		if cond != nil && cond.Status != metav1.ConditionTrue {
			msg += fmt.Sprintf(" (provider reason: %s)", cond.Reason)
		}
		r.setReady(a, metav1.ConditionFalse, reasonProviderNotReady, msg)
		return 0, false, nil
	}
	return p.Status.ProviderPK, true, nil
}

// finalize deletes the application this resource created, then releases the
// finalizer. Same connection-loss handling as the provider reconciler.
func (r *AuthentikApplicationReconciler) finalize(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication) error {
	if !controllerutil.ContainsFinalizer(a, FinalizerName) {
		return nil
	}
	log := logf.FromContext(ctx)

	if uid := a.Status.ApplicationUID; uid != "" {
		resolved, err := resolveForDeletion(ctx, r.Connections, a.Namespace, a.Spec.AuthentikRef)
		switch {
		case err == nil:
			if err := r.deleteOwnedApplication(ctx, resolved.Client, a); err != nil {
				if r.setReady(a, metav1.ConditionFalse, "DeletionFailed", err.Error()) {
					r.event(a, corev1.EventTypeWarning, "DeletionFailed", "Delete",
						"Could not delete application %q: %v", a.Status.Slug, err)
				}
				return err
			}
		case connectionGone(err):
			log.Info("Could not reach Authentik to delete Application; leaving it in place",
				"slug", a.Status.Slug, "reason", err.Error())
			r.event(a, corev1.EventTypeWarning, "Orphaned", "Delete",
				"Application %q left in Authentik: %v", a.Status.Slug, err)
		default:
			reason := connection.Reason(err)
			if reason == "" {
				reason = reasonAuthentikError
			}
			if r.setReady(a, metav1.ConditionFalse, reason, "Deletion blocked: "+err.Error()) {
				r.event(a, corev1.EventTypeWarning, "DeletionBlocked", "Delete",
					"Cannot delete application %q yet: %v", a.Status.Slug, err)
			}
			return err
		}
	}

	base := a.DeepCopy()
	controllerutil.RemoveFinalizer(a, FinalizerName)
	return client.IgnoreNotFound(r.Patch(ctx, a, client.MergeFrom(base)))
}

// deleteOwnedApplication deletes the application only if its pk is still
// the one this resource created.
func (r *AuthentikApplicationReconciler) deleteOwnedApplication(ctx context.Context, c *authentik.Client,
	a *authentikiov1alpha1.AuthentikApplication) error {
	got, err := c.GetApplication(ctx, a.Status.Slug)
	switch {
	case authentik.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case got.Pk != a.Status.ApplicationUID:
		r.event(a, corev1.EventTypeWarning, "NotDeleted", "Delete",
			"Application %q now has a different pk than this resource created; left in place", a.Status.Slug)
		return nil
	}
	if err := c.DeleteApplication(ctx, a.Status.Slug); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Deleted Application", "slug", a.Status.Slug)
	r.event(a, corev1.EventTypeNormal, "Deleted", "Delete", "Deleted application %q", a.Status.Slug)
	return nil
}

func (r *AuthentikApplicationReconciler) writeError(a *authentikiov1alpha1.AuthentikApplication,
	err error) (ctrl.Result, error) {
	switch {
	case staleProviderPK(err):
		// The provider resource still reports a pk that Authentik no longer
		// has (it is being recreated); its watch re-triggers this reconcile.
		r.setReady(a, metav1.ConditionFalse, reasonProviderNotReady,
			"provider's Authentik object does not exist (yet); waiting for the provider to be recreated")
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	case authentik.IsConflict(err):
		// Includes "application with this provider already exists": the
		// provider is attached to another application.
		r.setReady(a, metav1.ConditionFalse, reasonConflict, err.Error())
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	case authentik.IsValidation(err):
		if r.setReady(a, metav1.ConditionFalse, reasonInvalidSpec, err.Error()) {
			r.event(a, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the application: %v", err)
		}
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	default:
		return r.authentikError(a, err)
	}
}

func (r *AuthentikApplicationReconciler) authentikError(a *authentikiov1alpha1.AuthentikApplication,
	err error) (ctrl.Result, error) {
	r.setReady(a, metav1.ConditionFalse, reasonAuthentikError, err.Error())
	return ctrl.Result{}, err
}

// staleProviderPK reports a 400 rejecting the provider field because the
// referenced provider does not exist.
func staleProviderPK(err error) bool {
	apiErr, ok := errors.AsType[*authentik.APIError](err)
	return ok && apiErr.StatusCode == http.StatusBadRequest &&
		strings.Contains(apiErr.Body, `"provider"`) && strings.Contains(apiErr.Body, "does not exist")
}

// setReady records the Ready condition and reports whether it changed.
func (r *AuthentikApplicationReconciler) setReady(a *authentikiov1alpha1.AuthentikApplication,
	status metav1.ConditionStatus, reason, msg string) bool {
	return meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: a.Generation,
	})
}

//nolint:revive // argument-limit: mirrors events.EventRecorder.Eventf, guarded for a nil Recorder.
func (r *AuthentikApplicationReconciler) event(a *authentikiov1alpha1.AuthentikApplication,
	eventType, reason, action, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(a, nil, eventType, reason, action, note, args...)
	}
}

func (r *AuthentikApplicationReconciler) patchStatus(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, orig *authentikiov1alpha1.AuthentikApplicationStatus) error {
	if equality.Semantic.DeepEqual(orig, &a.Status) {
		return nil
	}
	base := a.DeepCopy()
	base.Status = *orig
	err := r.Status().Patch(ctx, a, client.MergeFrom(base))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthentikApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &authentikiov1alpha1.AuthentikApplication{},
		indexProviderRef, providerRefIndex); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikApplication{}).
		Watches(&authentikiov1alpha1.AuthentikOAuthProvider{},
			handler.EnqueueRequestsFromMapFunc(r.applicationsForProvider)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Named("authentikapplication").
		Complete(r)
}

// providerRefIndex indexes applications by the provider they reference.
func providerRefIndex(o client.Object) []string {
	a, ok := o.(*authentikiov1alpha1.AuthentikApplication)
	if !ok || a.Spec.ProviderRef == nil {
		return nil
	}
	return []string{a.Spec.ProviderRef.Name}
}

// applicationsForProvider maps a provider to the applications referencing it.
func (r *AuthentikApplicationReconciler) applicationsForProvider(ctx context.Context,
	o client.Object) []reconcile.Request {
	var list authentikiov1alpha1.AuthentikApplicationList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()),
		client.MatchingFields{indexProviderRef: o.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list AuthentikApplications for provider",
			"provider", client.ObjectKeyFromObject(o))
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
