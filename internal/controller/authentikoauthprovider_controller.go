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
	"strings"
	"time"

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

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	// FinalizerName guards Authentik objects the operator created.
	FinalizerName = "authentik.io/finalizer"

	// ConditionReady is True once the Authentik object matches the spec.
	ConditionReady = "Ready"

	reasonSynced         = "Synced"
	reasonConflict       = "Conflict"
	reasonInvalidSpec    = "InvalidSpec"
	reasonAuthentikError = "AuthentikError"

	// syncedRecheck is the drift-detection interval for a synced object.
	syncedRecheck = 5 * time.Minute
	// userErrorRecheck retries errors only the user (or another object) can
	// fix, without hot-looping.
	userErrorRecheck = time.Minute
)

// AuthentikOAuthProviderReconciler reconciles a AuthentikOAuthProvider object
type AuthentikOAuthProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Connections resolves the Authentik client for spec.authentikRef.
	Connections *connection.Resolver
	// Recorder emits Kubernetes events; may be nil in tests.
	Recorder events.EventRecorder
	// APIReader reads Secrets uncached (the operator has no list/watch on
	// Secrets); use the manager's API reader.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=authentik.io,resources=authentikoauthproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikoauthproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikoauthproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments,verbs=get
// +kubebuilder:rbac:groups=authentik.io,resources=authentikapplications,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile makes the OAuth2 provider in Authentik match the spec.
//
// Identity: the provider is tracked by status.providerPk once this resource
// created it. A pre-existing provider with the same name is never taken
// over -- that is reported as a Conflict (opt-in adoption is TASK-05).
func (r *AuthentikOAuthProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p authentikiov1alpha1.AuthentikOAuthProvider
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !p.DeletionTimestamp.IsZero() {
		orig := p.Status.DeepCopy()
		err := r.finalize(ctx, &p)
		if err != nil {
			// Deletion is blocked; say why on the object.
			return ctrl.Result{}, errors.Join(err, r.patchStatus(ctx, &p, orig))
		}
		return ctrl.Result{}, nil
	}
	// Patch only metadata.finalizers: a full Update would re-serialize the
	// spec and turn an explicit empty list (scopes: []) back into its default.
	base := p.DeepCopy()
	if controllerutil.AddFinalizer(&p, FinalizerName) {
		if err := r.Patch(ctx, &p, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := p.Status.DeepCopy()
	result, syncErr := r.sync(ctx, &p)
	if err := r.patchStatus(ctx, &p, orig); err != nil {
		return ctrl.Result{}, errors.Join(syncErr, err)
	}
	return result, syncErr
}

// sync does the work and records the outcome in p.Status. It returns an
// error only for transient failures that should back off and retry.
func (r *AuthentikOAuthProviderReconciler) sync(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}

	resolved, err := r.Connections.ForRef(ctx, p.Namespace, p.Spec.AuthentikRef)
	if err != nil {
		if reason := connection.Reason(err); reason != "" {
			r.setReady(p, metav1.ConditionFalse, reason, err.Error())
			return retryLater, nil
		}
		return ctrl.Result{}, err
	}
	c := resolved.Client

	want, err := desiredOAuth2Provider(ctx, c, p)
	if err != nil {
		if refErr, ok := errors.AsType[*referenceError](err); ok {
			r.setReady(p, metav1.ConditionFalse, refErr.reason, err.Error())
			return retryLater, nil
		}
		return r.authentikError(p, err)
	}

	var got *api.OAuth2Provider
	if pk := p.Status.ProviderPK; pk != 0 {
		got, err = c.GetOAuth2Provider(ctx, pk)
		switch {
		case err == nil && got.GetClientId() != p.Status.ClientID:
			// The pk now names a different provider: the connection was
			// pointed at another Authentik, a database was restored, or the
			// client ID was changed outside the operator. Never modify it.
			r.setReady(p, metav1.ConditionFalse, reasonConflict, fmt.Sprintf(
				"OAuth2 provider pk %d in Authentik has client ID %q, not %q recorded by this resource; "+
					"refusing to modify it. If this resource should manage a new provider, clear status.providerPk",
				pk, got.GetClientId(), p.Status.ClientID))
			return retryLater, nil
		case authentik.IsNotFound(err):
			log.Info("OAuth2 provider was deleted outside the operator; recreating", "pk", pk)
			r.event(p, corev1.EventTypeWarning, "ProviderMissing", "Recreate",
				"OAuth2 provider pk %d no longer exists in Authentik; recreating it (clients get a new secret)", pk)
			p.Status.ProviderPK = 0
			p.Status.ClientID = ""
			got = nil
		case err != nil:
			return r.authentikError(p, err)
		}
	}

	if got == nil {
		existing, err := c.FindOAuth2ProviderByName(ctx, want.Name)
		switch {
		case err == nil && p.Spec.ClientID == "" && existing.GetClientId() == derivedClientID(p):
			// Ours: a previous create succeeded but its outcome was lost.
			log.Info("Recovered OAuth2 provider created by an earlier reconcile", "pk", existing.Pk)
			p.Status.ProviderPK = existing.Pk
			p.Status.ClientID = existing.GetClientId()
			got = existing
		case err == nil || errors.Is(err, authentik.ErrAmbiguous):
			msg := fmt.Sprintf("an OAuth2 provider named %q already exists in Authentik and was not created by "+
				"this resource; adopting existing providers is not supported yet", want.Name)
			if existing != nil {
				msg += fmt.Sprintf(" (existing pk %d)", existing.Pk)
			}
			r.setReady(p, metav1.ConditionFalse, reasonConflict, msg)
			return retryLater, nil
		case !authentik.IsNotFound(err):
			return r.authentikError(p, err)
		}
	}

	if got == nil {
		created, err := c.CreateOAuth2Provider(ctx, *want)
		if err != nil {
			return r.writeError(p, err)
		}
		// Record ownership before anything else can fail, so the next
		// reconcile does not mistake our own provider for a conflict.
		p.Status.ProviderPK = created.Pk
		p.Status.ClientID = created.GetClientId()
		log.Info("Created OAuth2 provider", "name", created.Name, "pk", created.Pk)
		r.event(p, corev1.EventTypeNormal, "Created", "Create", "Created OAuth2 provider %q (pk %d)", created.Name, created.Pk)
		got = created
	} else if patch, changed := oauth2ProviderPatch(want, got); len(changed) > 0 {
		updated, err := c.PatchOAuth2Provider(ctx, got.Pk, patch)
		if err != nil {
			return r.writeError(p, err)
		}
		log.Info("Updated OAuth2 provider", "pk", got.Pk, "fields", changed)
		r.event(p, corev1.EventTypeNormal, "Updated", "Update", "Updated OAuth2 provider fields: %s", strings.Join(changed, ", "))
		got = updated
	}

	p.Status.ClientID = got.GetClientId()
	if res, done, err := r.syncSecret(ctx, p, resolved, got); !done {
		return res, err
	}
	p.Status.ObservedGeneration = p.Generation
	r.setReady(p, metav1.ConditionTrue, reasonSynced, fmt.Sprintf("OAuth2 provider %q (pk %d) is in sync", got.Name, got.Pk))
	return ctrl.Result{RequeueAfter: syncedRecheck}, nil
}

// finalize deletes the provider this resource created, then releases the
// finalizer.
//
// If the connection can no longer be resolved because its pieces were
// deleted (typical during namespace deletion: the token Secret goes first),
// the last client built for it is used. Only if there is none (e.g. after an
// operator restart) is the provider orphaned, with a Warning event, rather
// than blocking deletion forever. Fixable problems (missing default
// connection, Forbidden, Authentik unreachable) block deletion and are
// reported on the object.
func (r *AuthentikOAuthProviderReconciler) finalize(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider) error {
	if !controllerutil.ContainsFinalizer(p, FinalizerName) {
		return nil
	}
	log := logf.FromContext(ctx)

	if pk := p.Status.ProviderPK; pk != 0 {
		resolved, err := resolveForDeletion(ctx, r.Connections, p.Namespace, p.Spec.AuthentikRef)
		switch {
		case err == nil:
			if err := r.deleteOwnedProvider(ctx, resolved.Client, p, pk); err != nil {
				if r.setReady(p, metav1.ConditionFalse, "DeletionFailed", err.Error()) {
					r.event(p, corev1.EventTypeWarning, "DeletionFailed", "Delete",
						"Could not delete OAuth2 provider pk %d: %v", pk, err)
				}
				return err
			}
		case connectionGone(err):
			log.Info("Could not reach Authentik to delete OAuth2 provider; leaving it in place", "pk", pk, "reason", err.Error())
			r.event(p, corev1.EventTypeWarning, "Orphaned", "Delete",
				"OAuth2 provider pk %d left in Authentik: %v", pk, err)
		default:
			reason := connection.Reason(err)
			if reason == "" {
				reason = reasonAuthentikError
			}
			if r.setReady(p, metav1.ConditionFalse, reason, "Deletion blocked: "+err.Error()) {
				r.event(p, corev1.EventTypeWarning, "DeletionBlocked", "Delete",
					"Cannot delete OAuth2 provider pk %d yet: %v", pk, err)
			}
			return err
		}
	}

	base := p.DeepCopy()
	controllerutil.RemoveFinalizer(p, FinalizerName)
	return client.IgnoreNotFound(r.Patch(ctx, p, client.MergeFrom(base)))
}

// deleteOwnedProvider deletes pk only if it is still the provider this
// resource created (same client ID); a pk reused by another provider is left
// alone.
func (r *AuthentikOAuthProviderReconciler) deleteOwnedProvider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, pk int32) error {
	log := logf.FromContext(ctx)
	got, err := c.GetOAuth2Provider(ctx, pk)
	switch {
	case authentik.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case got.GetClientId() != p.Status.ClientID:
		log.Info("OAuth2 provider pk no longer belongs to this resource; not deleting it", "pk", pk)
		r.event(p, corev1.EventTypeWarning, "NotDeleted", "Delete",
			"OAuth2 provider pk %d has a different client ID than this resource created; left in place", pk)
		return nil
	}
	if err := c.DeleteOAuth2Provider(ctx, pk); err != nil {
		return err
	}
	log.Info("Deleted OAuth2 provider", "pk", pk)
	r.event(p, corev1.EventTypeNormal, "Deleted", "Delete", "Deleted OAuth2 provider pk %d", pk)
	return nil
}

// writeError classifies a failed create/update. Validation errors are the
// user's to fix and are not retried hot; conflicts are surfaced; anything
// else backs off.
func (r *AuthentikOAuthProviderReconciler) writeError(p *authentikiov1alpha1.AuthentikOAuthProvider, err error) (ctrl.Result, error) {
	switch {
	case authentik.IsConflict(err):
		r.setReady(p, metav1.ConditionFalse, reasonConflict, err.Error())
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	case authentik.IsValidation(err):
		if r.setReady(p, metav1.ConditionFalse, reasonInvalidSpec, err.Error()) {
			r.event(p, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the provider: %v", err)
		}
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	default:
		return r.authentikError(p, err)
	}
}

func (r *AuthentikOAuthProviderReconciler) authentikError(p *authentikiov1alpha1.AuthentikOAuthProvider, err error) (ctrl.Result, error) {
	r.setReady(p, metav1.ConditionFalse, reasonAuthentikError, err.Error())
	return ctrl.Result{}, err
}

// setReady records the Ready condition and reports whether it changed, so
// Warning events can be emitted once per change instead of on every retry.
func (r *AuthentikOAuthProviderReconciler) setReady(p *authentikiov1alpha1.AuthentikOAuthProvider,
	status metav1.ConditionStatus, reason, msg string) bool {
	return meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

func (r *AuthentikOAuthProviderReconciler) event(p *authentikiov1alpha1.AuthentikOAuthProvider,
	eventType, reason, action, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(p, nil, eventType, reason, action, note, args...)
	}
}

func (r *AuthentikOAuthProviderReconciler) patchStatus(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, orig *authentikiov1alpha1.AuthentikOAuthProviderStatus) error {
	if equality.Semantic.DeepEqual(orig, &p.Status) {
		return nil
	}
	base := p.DeepCopy()
	base.Status = *orig
	err := r.Status().Patch(ctx, p, client.MergeFrom(base))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthentikOAuthProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikOAuthProvider{}).
		// An application being attached (or renamed) changes the
		// provider's assigned slug/name, which the credentials Secret uses.
		Watches(&authentikiov1alpha1.AuthentikApplication{}, handler.EnqueueRequestsFromMapFunc(providerForApplication)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Named("authentikoauthprovider").
		Complete(r)
}

// providerForApplication maps an application to the provider it references.
func providerForApplication(_ context.Context, o client.Object) []reconcile.Request {
	ref := o.(*authentikiov1alpha1.AuthentikApplication).Spec.ProviderRef
	if ref == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: ref.Name}}}
}
