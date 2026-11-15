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
// pre-existing application with the same slug is a Conflict under
// managementPolicy Create, except when status.pendingCreate shows this
// resource's own create was in flight (then it is its own, Created, never
// Adopted): the marker is persisted before every create, so only a create
// whose outcome was lost can match it.
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
	got, err := r.syncApplication(ctx, a, persist)
	if err != nil {
		return r.failed(a, err)
	}
	a.Status.PendingCreate = ""
	a.Status.ApplicationUID = got.Pk
	a.Status.Slug = got.Slug
	a.Status.ProviderPK = int32OrZero(got.Provider.Get())
	a.Status.ObservedGeneration = a.Generation
	if readOnly(a.Spec.ManagementPolicy) {
		r.setReady(a, metav1.ConditionTrue, reasonReadOnly,
			fmt.Sprintf("Application %q found; observed read only", got.Slug))
	} else {
		meta.RemoveStatusCondition(&a.Status.Conditions, ConditionDrifted)
		r.setReady(a, metav1.ConditionTrue, reasonSynced, fmt.Sprintf("Application %q is in sync", got.Slug))
	}
	return ctrl.Result{RequeueAfter: syncedRecheck}, nil
}

// syncApplication makes the application in Authentik match the spec and
// returns it as Authentik now has it.
func (r *AuthentikApplicationReconciler) syncApplication(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, persist func() error) (*api.Application, error) {
	providerPK, err := r.providerPK(ctx, a)
	if err != nil {
		return nil, err
	}
	resolved, err := resolve(ctx, r.Connections, a.Namespace, a.Spec.AuthentikRef)
	if err != nil {
		return nil, err
	}
	c := resolved.Client
	want := desiredApplication(a, providerPK)
	got, err := r.observeApplication(ctx, c, a, want, persist)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return r.createApplication(ctx, c, a, want, persist)
	}
	// An adopted application, and any application a ReadOnly resource
	// observes, is compared on the fields the spec sets only.
	observing := readOnly(a.Spec.ManagementPolicy)
	if observing || a.Status.Provenance == authentikiov1alpha1.ProvenanceAdopted {
		adoptionViewApplication(a, want, got)
	}
	if observing {
		patch, changed := applicationPatch(want, got)
		recordDrift(&a.Status.Conditions, a.Generation, patch, got, changed)
		return got, nil
	}
	return r.updateApplication(ctx, c, a, want, got)
}

// observeTracked looks up the application recorded in status. It returns it
// when it is still this resource's own, and nil to carry on looking when it
// is gone. status.applicationUid is kept until a replacement exists: a 404
// can be transient (proxy during a rollout), and the uid is what lets the
// finalizer clean up.
func (r *AuthentikApplicationReconciler) observeTracked(ctx context.Context, c *authentik.Client,
	a *authentikiov1alpha1.AuthentikApplication) (*api.Application, error) {
	uid := a.Status.ApplicationUID
	got, err := c.GetApplication(ctx, a.Status.Slug)
	switch {
	case err == nil && got.Pk != uid:
		return nil, condErrf(reasonConflict,
			"application %q in Authentik has pk %s, not %s recorded by this resource; refusing to modify it. "+
				"If this resource should manage a new application, clear status.applicationUid",
			a.Status.Slug, got.Pk, uid)
	case err == nil && a.Status.Provenance == authentikiov1alpha1.ProvenanceAdopted:
		return got, r.yieldToLower(ctx, a)
	case err == nil:
		return got, nil
	case !authentik.IsNotFound(err):
		return nil, err
	}
	return nil, nil
}

// observeApplication finds the application this resource manages: by
// status.applicationUid, then by slug -- a create whose outcome was lost is
// recovered, and under a policy that may adopt an existing application is
// adopted. nil means it has to be created; an existing application that is
// none of these is a Conflict.
func (r *AuthentikApplicationReconciler) observeApplication(ctx context.Context, c *authentik.Client,
	a *authentikiov1alpha1.AuthentikApplication, want *api.ApplicationRequest,
	persist func() error) (*api.Application, error) {
	slug := want.Slug
	if a.Status.ApplicationUID != "" {
		got, err := r.observeTracked(ctx, c, a)
		if err != nil || got != nil {
			return got, err
		}
	}

	existing, err := c.GetApplication(ctx, slug)
	switch {
	case err == nil && a.Status.ApplicationUID != "" && existing.Pk == a.Status.ApplicationUID:
		return existing, nil // the earlier 404 was transient
	case err == nil && a.Status.PendingCreate == slug:
		// Checked before adoption so a recovered own create is never
		// classified as Adopted.
		logf.FromContext(ctx).Info("Recovered Application from a create whose outcome was lost", "slug", existing.Slug)
		r.event(a, corev1.EventTypeNormal, "Recovered", "Create",
			"Recovered application %q created by an earlier reconcile", existing.Slug)
		a.Status.Provenance = authentikiov1alpha1.ProvenanceCreated
		return existing, nil
	case err == nil && a.Spec.ManagementPolicy.MayAdopt():
		return r.adoptApplication(ctx, a, want, existing, persist)
	case err == nil:
		return nil, condErrf(reasonConflict, "an application with slug %q already exists in Authentik and was not "+
			"created by this resource; set spec.managementPolicy to Adopt, AdoptOrCreate or ReadOnly to take it "+
			"over or observe it", slug)
	case authentik.IsNotFound(err):
		return nil, r.missingApplication(a, slug)
	default:
		return nil, err
	}
}

// missingApplication decides what a miss means: nil to create (Create, an
// application this resource created that vanished, or AdoptOrCreate),
// otherwise an error that writes nothing.
func (r *AuthentikApplicationReconciler) missingApplication(a *authentikiov1alpha1.AuthentikApplication,
	slug string) error {
	policy := a.Spec.ManagementPolicy
	switch {
	case a.Status.Provenance == authentikiov1alpha1.ProvenanceAdopted && a.Status.ApplicationUID != "":
		// Whatever the policy now says: an adopted application that vanished
		// is not silently replaced by an empty one. The uid stays in status.
		return condErrf(reasonAdoptionTargetNotFound, "the adopted application %q no longer exists in Authentik; "+
			"not recreating it. Restore it, or clear status.applicationUid, status.slug and status.provenance "+
			"and set spec.managementPolicy to AdoptOrCreate (or Create) to create a new one", slug)
	case readOnly(policy):
		return condErrf(reasonAdoptionTargetNotFound, "no application with slug %q exists in Authentik to observe; "+
			"managementPolicy ReadOnly never writes to Authentik, so nothing is created", slug)
	case a.Status.Provenance == authentikiov1alpha1.ProvenanceCreated:
		return nil // an application this resource created vanished: recreate, whatever the policy says
	case !policy.MayAdopt():
		return nil
	case !policy.MayCreate():
		return condErrf(reasonAdoptionTargetNotFound, "no application with slug %q exists in Authentik to adopt; "+
			"nothing was written. Fix the slug, or set spec.managementPolicy to AdoptOrCreate to create one", slug)
	case a.Spec.DryRun:
		return condErrf(reasonAdoptionDryRun, "dry run: no existing application with slug %q; a new one would be "+
			"created; nothing was written", slug)
	}
	r.event(a, corev1.EventTypeNormal, "NothingToAdopt", "Create",
		"Creating a new application because nothing was found to adopt (slug %q)", slug)
	return nil
}

// yieldToLower clears this resource's claim and reports a Conflict when
// another resource tracks the same application and has the lower UID, so two
// resources that passed the pre-adoption check together are settled by UID.
func (r *AuthentikApplicationReconciler) yieldToLower(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication) error {
	other, otherUID, err := r.trackingBy(ctx, a, a.Status.ApplicationUID)
	if err != nil || other == "" || otherUID >= a.UID {
		return err
	}
	slug := a.Status.Slug
	a.Status.Provenance, a.Status.ApplicationUID, a.Status.Slug = "", "", ""
	return condErrf(reasonConflict, "application %q is also claimed by %s, which takes precedence; "+
		"this resource released it", slug, other)
}

// otherTracking names another application resource (namespace/name) that
// already tracks uid on the same connection, or "".
func (r *AuthentikApplicationReconciler) otherTracking(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, uid string) (string, error) {
	name, _, err := r.trackingBy(ctx, a, uid)
	return name, err
}

// trackingBy is otherTracking that also returns the other resource's UID.
func (r *AuthentikApplicationReconciler) trackingBy(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, uid string) (string, types.UID, error) {
	var list authentikiov1alpha1.AuthentikApplicationList
	if err := r.List(ctx, &list); err != nil {
		return "", "", fmt.Errorf("list AuthentikApplications: %w", err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.UID != a.UID && o.Status.ApplicationUID == uid && o.DeletionTimestamp.IsZero() &&
			sameConnection(o.Namespace, o.Spec.AuthentikRef, a.Namespace, a.Spec.AuthentikRef) {
			return o.Namespace + "/" + o.Name, o.UID, nil
		}
	}
	return "", "", nil
}

// adoptApplication takes over target: in dry-run mode it only reports what
// would change; otherwise it records the decision in status first.
func (r *AuthentikApplicationReconciler) adoptApplication(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, want *api.ApplicationRequest, target *api.Application,
	persist func() error) (*api.Application, error) {
	if other, err := r.otherTracking(ctx, a, target.Pk); err != nil {
		return nil, err
	} else if other != "" {
		return nil, condErrf(reasonConflict, "application %q is already managed by %s; "+
			"two resources cannot manage one application", target.Slug, other)
	}
	if a.Spec.DryRun {
		view := *want
		adoptionViewApplication(a, &view, target)
		patch, changed := applicationPatch(&view, target)
		msg := fmt.Sprintf("dry run: would adopt application %q; ", target.Slug)
		if len(changed) == 0 {
			msg += "would change no fields"
		} else {
			msg += "would change " + describeChanges(patch, target)
		}
		msg += "; nothing was written. Set spec.dryRun to false to adopt"
		if cond := meta.FindStatusCondition(a.Status.Conditions, ConditionReady); cond == nil ||
			cond.Reason != reasonAdoptionDryRun || cond.Message != msg {
			r.event(a, corev1.EventTypeNormal, reasonAdoptionDryRun, "Adopt", "%s", msg)
		}
		return nil, condErr(reasonAdoptionDryRun, errors.New(msg))
	}
	a.Status.Provenance = authentikiov1alpha1.ProvenanceAdopted
	a.Status.ApplicationUID = target.Pk
	a.Status.Slug = target.Slug
	a.Status.PendingCreate = ""
	if err := persist(); err != nil {
		return nil, err
	}
	// Re-check after the claim is visible; no Authentik write has happened.
	if err := r.yieldToLower(ctx, a); err != nil {
		return nil, err
	}
	if readOnly(a.Spec.ManagementPolicy) {
		r.event(a, corev1.EventTypeNormal, "Observing", "Adopt", "Observing existing application %q read only", target.Slug)
	} else {
		r.event(a, corev1.EventTypeNormal, "Adopted", "Adopt", "Adopted existing application %q", target.Slug)
	}
	return target, nil
}

// createApplication creates the application, recording status.pendingCreate
// first so a lost response can be recovered.
func (r *AuthentikApplicationReconciler) createApplication(ctx context.Context, c *authentik.Client,
	a *authentikiov1alpha1.AuthentikApplication, want *api.ApplicationRequest,
	persist func() error) (*api.Application, error) {
	log := logf.FromContext(ctx)
	if a.Status.ApplicationUID != "" {
		log.Info("Application was deleted outside the operator; recreating", "slug", want.Slug)
		r.event(a, corev1.EventTypeWarning, "ApplicationMissing", "Recreate",
			"Application %q no longer exists in Authentik; recreating it", want.Slug)
	}
	a.Status.PendingCreate = want.Slug
	if err := persist(); err != nil {
		return nil, err
	}
	created, err := c.CreateApplication(ctx, *want)
	if err != nil {
		if code := authentik.StatusCode(err); code >= 400 && code < 500 {
			a.Status.PendingCreate = "" // definitely not created
		}
		return nil, err
	}
	a.Status.Provenance = authentikiov1alpha1.ProvenanceCreated
	log.Info("Created Application", "slug", created.Slug, "pk", created.Pk)
	r.event(a, corev1.EventTypeNormal, "Created", "Create", "Created application %q", created.Slug)
	return created, nil
}

// updateApplication patches the fields that differ, if any.
func (r *AuthentikApplicationReconciler) updateApplication(ctx context.Context, c *authentik.Client,
	a *authentikiov1alpha1.AuthentikApplication, want *api.ApplicationRequest,
	got *api.Application) (*api.Application, error) {
	patch, changed := applicationPatch(want, got)
	if len(changed) == 0 {
		return got, nil
	}
	updated, err := c.PatchApplication(ctx, got.Slug, patch)
	if err != nil {
		return nil, err
	}
	logf.FromContext(ctx).Info("Updated Application", "slug", got.Slug, "fields", changed)
	r.event(a, corev1.EventTypeNormal, "Updated", "Update", "Updated application fields: %s", strings.Join(changed, ", "))
	return updated, nil
}

// providerPK resolves spec.providerRef to the provider's Authentik pk. While
// the provider is missing, not Ready, or on a different connection, the
// returned conditionError says so; the provider's watch re-triggers this
// reconcile.
func (r *AuthentikApplicationReconciler) providerPK(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication) (int32, error) {
	ref := a.Spec.ProviderRef
	if ref == nil {
		return 0, nil
	}
	var p authentikiov1alpha1.AuthentikOAuthProvider
	if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: ref.Name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, condErrf(reasonProviderNotFound, "%s %s/%s not found", ref.Kind, a.Namespace, ref.Name)
		}
		return 0, err
	}
	if !sameAuthentikRef(p.Spec.AuthentikRef, a.Spec.AuthentikRef) {
		return 0, condErrf(reasonProviderConnectionMismatch,
			"provider %s uses a different authentikRef than this application; both must point at the same Authentik",
			ref.Name)
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
		return 0, condErr(reasonProviderNotReady, errors.New(msg))
	}
	return p.Status.ProviderPK, nil
}

// finalize deletes the application this resource created, then releases the
// finalizer. Same connection-loss handling as the provider reconciler.
func (r *AuthentikApplicationReconciler) finalize(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication) error {
	if !controllerutil.ContainsFinalizer(a, FinalizerName) {
		return nil
	}
	log := logf.FromContext(ctx)

	if a.Status.ApplicationUID != "" && authentikiov1alpha1.EffectiveDeletionPolicy(
		a.Spec.ManagementPolicy, a.Spec.DeletionPolicy, a.Status.Provenance) == authentikiov1alpha1.DeletionPolicyRetain {
		log.Info("Retaining Application in Authentik", "slug", a.Status.Slug)
		if a.Status.Provenance == "" && a.Spec.DeletionPolicy == "" {
			r.event(a, corev1.EventTypeWarning, "Retained", "Delete",
				"Application %q left in Authentik: provenance unknown; retaining", a.Status.Slug)
		} else {
			r.event(a, corev1.EventTypeNormal, "Retained", "Delete",
				"Application %q left in Authentik (deletionPolicy Retain)", a.Status.Slug)
		}
	} else if a.Status.ApplicationUID != "" {
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
			reason := conditionReason(err, reasonAuthentikError)
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

// failed records why sync stopped. Problems retrying cannot fix -- a
// conditionError, a stale provider pk, an Authentik conflict or validation
// rejection -- are reported and retried on the slow interval; anything else
// backs off.
func (r *AuthentikApplicationReconciler) failed(a *authentikiov1alpha1.AuthentikApplication,
	err error) (ctrl.Result, error) {
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}
	if ce, ok := errors.AsType[*conditionError](err); ok {
		r.setReady(a, metav1.ConditionFalse, ce.reason, err.Error())
		return retryLater, nil
	}
	switch {
	case staleProviderPK(err):
		// The provider resource still reports a pk that Authentik no longer
		// has (it is being recreated); its watch re-triggers this reconcile.
		r.setReady(a, metav1.ConditionFalse, reasonProviderNotReady,
			"provider's Authentik object does not exist (yet); waiting for the provider to be recreated")
		return retryLater, nil
	case authentik.IsConflict(err):
		// Includes "application with this provider already exists": the
		// provider is attached to another application.
		r.setReady(a, metav1.ConditionFalse, reasonConflict, err.Error())
		return retryLater, nil
	case authentik.IsValidation(err):
		if r.setReady(a, metav1.ConditionFalse, reasonInvalidSpec, err.Error()) {
			r.event(a, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the application: %v", err)
		}
		return retryLater, nil
	default:
		r.setReady(a, metav1.ConditionFalse, reasonAuthentikError, err.Error())
		return ctrl.Result{}, err
	}
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
