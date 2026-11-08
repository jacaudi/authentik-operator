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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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

// AuthentikOAuthProviderReconciler reconciles a AuthentikOAuthProvider object.
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
// +kubebuilder:rbac:groups=authentik.io,resources=authentikcredentialstemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile makes the OAuth2 provider in Authentik match the spec.
//
// Identity: the provider is tracked by status.providerPk once this resource
// created it. A pre-existing provider with the same name is never taken
// over unless spec.adoption.enabled: that is reported as a Conflict. A
// provider found while status.pendingCreate shows this resource's own create
// was in flight, and carrying the client ID this resource sends, is its own
// (Created), not an adoption.
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
	persist := func() error {
		if err := r.patchStatus(ctx, &p, orig); err != nil {
			return err
		}
		orig = p.Status.DeepCopy()
		return nil
	}
	result, syncErr := r.sync(ctx, &p, persist)
	if err := persist(); err != nil {
		return ctrl.Result{}, errors.Join(syncErr, err)
	}
	return result, syncErr
}

// sync does the work and records the outcome in p.Status. It returns an
// error only for transient failures that should back off and retry. persist
// writes the status immediately; it records the pending-create marker
// before the create request is sent.
func (r *AuthentikOAuthProviderReconciler) sync(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, persist func() error) (ctrl.Result, error) {
	resolved, err := resolve(ctx, r.Connections, p.Namespace, p.Spec.AuthentikRef)
	if err != nil {
		return r.failed(p, err)
	}
	got, err := r.syncProvider(ctx, resolved.Client, p, persist)
	if err != nil {
		return r.failed(p, err)
	}

	p.Status.PendingCreate = ""
	p.Status.ClientID = got.GetClientId()
	p.Status.ObservedGeneration = p.Generation
	r.setReady(p, metav1.ConditionTrue, reasonSynced,
		fmt.Sprintf("OAuth2 provider %q (pk %d) is in sync", got.Name, got.Pk))

	// The credentials Secret has its own condition and never affects Ready
	// (see ConditionCredentialsReady).
	requeue, err := r.syncSecret(ctx, p, resolved, got)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	}
	return ctrl.Result{RequeueAfter: syncedRecheck}, nil
}

// syncProvider makes the provider in Authentik match the spec and returns
// it as Authentik now has it.
func (r *AuthentikOAuthProviderReconciler) syncProvider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, persist func() error) (*api.OAuth2Provider, error) {
	want, err := desiredOAuth2Provider(ctx, c, p)
	if err != nil {
		return nil, err
	}
	got, err := r.observeProvider(ctx, c, p, want, persist)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return r.createProvider(ctx, c, p, want, persist)
	}
	if p.Status.Provenance == authentikiov1alpha1.ProvenanceAdopted {
		adoptionView(p, want, got)
	}
	return r.updateProvider(ctx, c, p, want, got)
}

// observeProvider finds the provider this resource manages: by
// status.providerPk, then for a create whose outcome was lost by name, then
// (only with spec.adoption.enabled) the adoption target. nil means it has to
// be created; an existing provider that is none of these is a Conflict.
func (r *AuthentikOAuthProviderReconciler) observeProvider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest,
	persist func() error) (*api.OAuth2Provider, error) {
	log := logf.FromContext(ctx)
	name := want.Name
	if pk := p.Status.ProviderPK; pk != 0 {
		got, err := c.GetOAuth2Provider(ctx, pk)
		switch {
		case err == nil && !ownsProvider(p, got):
			// The pk now names a different provider: the connection was
			// pointed at another Authentik, a database was restored, or the
			// client ID was changed outside the operator. Never modify it.
			return nil, condErrf(reasonConflict,
				"OAuth2 provider pk %d in Authentik has client ID %q, not %q recorded by this resource; "+
					"refusing to modify it. If this resource should manage a new provider, clear status.providerPk",
				pk, got.GetClientId(), p.Status.ClientID)
		case err == nil:
			if p.Status.Provenance == "" {
				// Tracked since before provenance existed, when only a
				// create could produce a pk.
				p.Status.Provenance = authentikiov1alpha1.ProvenanceCreated
			}
			return got, nil
		case authentik.IsNotFound(err):
			log.Info("OAuth2 provider was deleted outside the operator; recreating", "pk", pk)
			r.event(p, corev1.EventTypeWarning, "ProviderMissing", "Recreate",
				"OAuth2 provider pk %d no longer exists in Authentik; recreating it (clients get a new secret)", pk)
			p.Status.ProviderPK = 0
			p.Status.ClientID = ""
			p.Status.Provenance = "" // decided afresh below
		default:
			return nil, err
		}
	}

	existing, err := c.FindOAuth2ProviderByName(ctx, name)
	if err != nil && !authentik.IsNotFound(err) && !errors.Is(err, authentik.ErrAmbiguous) {
		return nil, err
	}
	if err == nil && p.Status.PendingCreate == name && existing.GetClientId() == expectedClientID(p) {
		// Ours: a previous create succeeded but its outcome was lost. The
		// marker proves a create was sent; the client ID that it was this
		// one that landed (a pinned ID could also be someone else's). This
		// is checked before adoption so a recovered own create is never
		// classified as Adopted.
		log.Info("Recovered OAuth2 provider created by an earlier reconcile", "pk", existing.Pk)
		p.Status.ProviderPK = existing.Pk
		p.Status.ClientID = existing.GetClientId()
		p.Status.Provenance = authentikiov1alpha1.ProvenanceCreated
		return existing, nil
	}

	if !p.Spec.Adoption.Enabled {
		switch {
		case err == nil || errors.Is(err, authentik.ErrAmbiguous):
			msg := fmt.Sprintf("an OAuth2 provider named %q already exists in Authentik and was not created by "+
				"this resource; set spec.adoption.enabled to adopt it", name)
			if existing != nil {
				msg += fmt.Sprintf(" (existing pk %d)", existing.Pk)
			}
			return nil, condErr(reasonConflict, errors.New(msg))
		default:
			return nil, nil
		}
	}

	target, err := r.adoptionTarget(ctx, c, p, name, existing, err)
	if err != nil || target == nil {
		return nil, err
	}
	return r.adoptProvider(p, want, target, persist)
}

// adoptionTarget picks the provider to adopt. byName and byNameErr are the
// lookup of the desired name. With no spec.adoption.match, that is the
// candidate, and nil (create) when absent. An explicit match must resolve:
// creating instead would mint new credentials, the thing adoption avoids.
func (r *AuthentikOAuthProviderReconciler) adoptionTarget(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, name string, byName *api.OAuth2Provider,
	byNameErr error) (*api.OAuth2Provider, error) {
	m := p.Spec.Adoption.Match
	var (
		got   *api.OAuth2Provider
		err   error
		which string
	)
	switch {
	case m == nil:
		got, err, which = byName, byNameErr, fmt.Sprintf("named %q", name)
	case m.PK != 0:
		got, err = c.GetOAuth2Provider(ctx, m.PK)
		which = fmt.Sprintf("with pk %d", m.PK)
	case m.Name == name:
		got, err, which = byName, byNameErr, fmt.Sprintf("named %q", m.Name)
	default:
		got, err = c.FindOAuth2ProviderByName(ctx, m.Name)
		which = fmt.Sprintf("named %q", m.Name)
	}
	switch {
	case err == nil:
		return got, nil
	case errors.Is(err, authentik.ErrAmbiguous):
		return nil, condErrf(reasonConflict, "several OAuth2 providers %s exist in Authentik; "+
			"select one with spec.adoption.match.pk", which)
	case authentik.IsNotFound(err) && m == nil:
		return nil, nil
	case authentik.IsNotFound(err):
		return nil, condErrf(reasonAdoptionTargetNotFound,
			"spec.adoption.match: no OAuth2 provider %s exists in Authentik", which)
	default:
		return nil, err
	}
}

// adoptProvider takes over target: in dry-run mode it only reports what
// would change; otherwise it records the decision in status (before any
// write, so a crash cannot lose it).
func (r *AuthentikOAuthProviderReconciler) adoptProvider(p *authentikiov1alpha1.AuthentikOAuthProvider,
	want *api.OAuth2ProviderRequest, target *api.OAuth2Provider, persist func() error) (*api.OAuth2Provider, error) {
	if p.Spec.Adoption.DryRun {
		view := *want
		adoptionView(p, &view, target)
		_, changed := oauth2ProviderPatch(&view, target)
		msg := fmt.Sprintf("dry run: would adopt OAuth2 provider %q (pk %d); ", target.Name, target.Pk)
		if len(changed) == 0 {
			msg += "would change no fields"
		} else {
			msg += "would change: " + strings.Join(changed, ", ")
		}
		msg += "; nothing was written. Set spec.adoption.dryRun to false to adopt"
		if cond := meta.FindStatusCondition(p.Status.Conditions, ConditionReady); cond == nil ||
			cond.Reason != reasonAdoptionDryRun || cond.Message != msg {
			r.event(p, corev1.EventTypeNormal, reasonAdoptionDryRun, "Adopt", "%s", msg)
		}
		return nil, condErr(reasonAdoptionDryRun, errors.New(msg))
	}
	p.Status.Provenance = authentikiov1alpha1.ProvenanceAdopted
	p.Status.ProviderPK = target.Pk
	p.Status.ClientID = target.GetClientId()
	p.Status.PendingCreate = ""
	if err := persist(); err != nil {
		return nil, err
	}
	r.event(p, corev1.EventTypeNormal, "Adopted", "Adopt", "Adopted existing OAuth2 provider %q (pk %d)",
		target.Name, target.Pk)
	return target, nil
}

// createProvider creates the provider, recording status.pendingCreate first
// so a lost response can be recovered, and ownership right after.
func (r *AuthentikOAuthProviderReconciler) createProvider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest,
	persist func() error) (*api.OAuth2Provider, error) {
	p.Status.PendingCreate = want.Name
	if err := persist(); err != nil {
		return nil, err
	}
	created, err := c.CreateOAuth2Provider(ctx, *want)
	if err != nil {
		if code := authentik.StatusCode(err); code >= 400 && code < 500 {
			p.Status.PendingCreate = "" // definitely not created
		}
		return nil, err
	}
	// Record ownership before anything else can fail, so the next
	// reconcile does not mistake our own provider for a conflict.
	p.Status.ProviderPK = created.Pk
	p.Status.ClientID = created.GetClientId()
	p.Status.Provenance = authentikiov1alpha1.ProvenanceCreated
	logf.FromContext(ctx).Info("Created OAuth2 provider", "name", created.Name, "pk", created.Pk)
	r.event(p, corev1.EventTypeNormal, "Created", "Create", "Created OAuth2 provider %q (pk %d)",
		created.Name, created.Pk)
	return created, nil
}

// updateProvider patches the fields that differ, if any.
func (r *AuthentikOAuthProviderReconciler) updateProvider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) (*api.OAuth2Provider, error) {
	patch, changed := oauth2ProviderPatch(want, got)
	if len(changed) == 0 {
		return got, nil
	}
	updated, err := c.PatchOAuth2Provider(ctx, got.Pk, patch)
	if err != nil {
		return nil, err
	}
	logf.FromContext(ctx).Info("Updated OAuth2 provider", "pk", got.Pk, "fields", changed)
	r.event(p, corev1.EventTypeNormal, "Updated", "Update", "Updated OAuth2 provider fields: %s",
		strings.Join(changed, ", "))
	return updated, nil
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
func (r *AuthentikOAuthProviderReconciler) finalize(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider) error {
	if !controllerutil.ContainsFinalizer(p, FinalizerName) {
		return nil
	}
	log := logf.FromContext(ctx)

	if pk := p.Status.ProviderPK; pk != 0 && authentikiov1alpha1.EffectiveDeletionPolicy(
		p.Spec.DeletionPolicy, p.Status.Provenance) == authentikiov1alpha1.DeletionPolicyRetain {
		log.Info("Retaining OAuth2 provider in Authentik", "pk", pk)
		r.event(p, corev1.EventTypeNormal, "Retained", "Delete",
			"OAuth2 provider pk %d left in Authentik (deletionPolicy Retain)", pk)
	} else if pk != 0 {
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
			reason := conditionReason(err, reasonAuthentikError)
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
	case !ownsProvider(p, got):
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

// failed records why sync stopped. Problems retrying cannot fix -- a
// conditionError, an Authentik conflict or validation rejection -- are
// reported and retried on the slow interval; anything else backs off.
func (r *AuthentikOAuthProviderReconciler) failed(p *authentikiov1alpha1.AuthentikOAuthProvider,
	err error) (ctrl.Result, error) {
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}
	if ce, ok := errors.AsType[*conditionError](err); ok {
		r.setReady(p, metav1.ConditionFalse, ce.reason, err.Error())
		return retryLater, nil
	}
	switch {
	case authentik.IsConflict(err):
		r.setReady(p, metav1.ConditionFalse, reasonConflict, err.Error())
		return retryLater, nil
	case authentik.IsValidation(err):
		if r.setReady(p, metav1.ConditionFalse, reasonInvalidSpec, err.Error()) {
			r.event(p, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the provider: %v", err)
		}
		return retryLater, nil
	default:
		r.setReady(p, metav1.ConditionFalse, reasonAuthentikError, err.Error())
		return ctrl.Result{}, err
	}
}

// setReady records the Ready condition and reports whether it changed, so
// Warning events can be emitted once per change instead of on every retry.
func (r *AuthentikOAuthProviderReconciler) setReady(p *authentikiov1alpha1.AuthentikOAuthProvider,
	status metav1.ConditionStatus, reason, msg string) bool {
	return meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

//nolint:revive // argument-limit: mirrors events.EventRecorder.Eventf, guarded for a nil Recorder.
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
		// A template change re-renders every Secret that uses it.
		// Status-only writes (the template reconciler's) are ignored.
		Watches(&authentikiov1alpha1.AuthentikCredentialsTemplate{},
			handler.EnqueueRequestsFromMapFunc(r.providersForTemplate),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Named("authentikoauthprovider").
		Complete(r)
}

// providerForApplication maps an application to the provider it references.
func providerForApplication(_ context.Context, o client.Object) []reconcile.Request {
	a, ok := o.(*authentikiov1alpha1.AuthentikApplication)
	if !ok || a.Spec.ProviderRef == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: a.Namespace, Name: a.Spec.ProviderRef.Name}}}
}

// providersForTemplate maps a credentials template to the providers that
// reference it. Templates change rarely, so a list beats a field index.
func (r *AuthentikOAuthProviderReconciler) providersForTemplate(ctx context.Context,
	o client.Object) []reconcile.Request {
	var list authentikiov1alpha1.AuthentikOAuthProviderList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list providers for credentials template", "template", o.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.Secret != nil && p.Spec.Secret.TemplateRef != nil && p.Spec.Secret.TemplateRef.Name == o.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		}
	}
	return reqs
}
