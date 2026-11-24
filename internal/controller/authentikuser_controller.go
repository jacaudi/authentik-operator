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
	"strconv"
	"strings"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/adopt"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// indexUserPK maps an AuthentikUser to the account pk it tracks (see claims.go).
const indexUserPK = ".status.userPk"

// AuthentikUserReconciler reconciles a AuthentikUser object.
type AuthentikUserReconciler struct {
	client.Client

	Scheme *runtime.Scheme
	// Connections resolves the Authentik client for spec.authentikRef.
	Connections *connection.Resolver
	// Recorder emits Kubernetes events; may be nil in tests.
	Recorder events.EventRecorder
	// APIReader reads Namespaces and the invitation Secret uncached (the
	// operator has get, not list/watch, on both), and lists AuthentikUsers for
	// the claim re-check; use the manager's API reader.
	APIReader client.Reader
	// AdoptionNamespaceSelector picks the namespaces whose resources may
	// adopt or observe existing objects on the default connection. nil denies
	// every namespace. See adoptionGate.
	AdoptionNamespaceSelector labels.Selector
	// IdentityNamespaceSelector picks the namespaces whose resources the
	// operator reconciles on the default connection, under every
	// managementPolicy. nil denies every namespace. See identityGate.
	IdentityNamespaceSelector labels.Selector
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile makes the person's account in Authentik match the spec.
//
// Identity: status.userPk (Authentik's integer pk). A person is never created
// through the API: under a policy that may create, the operator issues a
// single-use invitation that pins the username, and recognises the account the
// person enrols by the ownership proof the invitation wrote into its
// attributes. The policy flow is adopt.Decide; a Created account that vanishes
// is never re-invited.
func (r *AuthentikUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var u authentikiov1alpha1.AuthentikUser
	if err := r.Get(ctx, req.NamespacedName, &u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !u.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &u)
	}
	if err := addFinalizer(ctx, r.Client, &u, u.DeepCopy()); err != nil {
		return ctrl.Result{}, err
	}
	return syncThenPersist(r.statusPersister(ctx, &u), func(persist func() error) (ctrl.Result, error) {
		return r.sync(ctx, &u, persist)
	})
}

// statusPersister returns the function that writes the resource's status now, and from then on
// measures changes against what it wrote.
func (r *AuthentikUserReconciler) statusPersister(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) func() error {
	orig := u.Status.DeepCopy()
	return func() error {
		if err := r.patchStatus(ctx, u, orig); err != nil {
			return err
		}
		orig = u.Status.DeepCopy()
		return nil
	}
}

// sync does the work and records the outcome in u.Status. persist writes the
// status immediately; it is used to record the pending invitation before the
// request that creates it is sent, and a fresh claim before it is re-checked.
// Ready is set once: here on success, in failed otherwise.
func (r *AuthentikUserReconciler) sync(ctx context.Context, u *authentikiov1alpha1.AuthentikUser,
	persist func() error) (ctrl.Result, error) {
	got, authenticatorsUnknown, err := r.syncUser(ctx, u, persist)
	if err != nil {
		return r.failed(u, err)
	}
	u.Status.UserPK = got.Pk
	u.Status.ObservedGeneration = u.Generation
	if readOnly(u.Spec.ManagementPolicy) {
		setReady(&u.Status.Conditions, u.Generation, metav1.ConditionTrue, reasonReadOnly,
			fmt.Sprintf("User %q found; observed read only", got.Username))
	} else {
		meta.RemoveStatusCondition(&u.Status.Conditions, ConditionDrifted)
		setReady(&u.Status.Conditions, u.Generation, metav1.ConditionTrue, reasonSynced,
			fmt.Sprintf("User %q is in sync", got.Username))
	}
	if authenticatorsUnknown {
		return ctrl.Result{RequeueAfter: userErrorRecheck}, nil
	}
	return ctrl.Result{RequeueAfter: syncedRecheck}, nil
}

// syncUser makes the account in Authentik match the spec and returns it as
// Authentik now has it, and whether the authenticators of a Created account
// could not be listed (see syncAuthenticators). The identity gate runs first,
// under every managementPolicy, so a denied namespace reads and writes
// nothing; a spec invalid on its face is refused before any call to Authentik.
// An invitation the policy no longer allows is withdrawn before the adoption
// gate: the live sign-up link must go whether or not the resource may adopt.
// The one exception to "nothing is read or written" for a denied namespace is
// that very withdrawal: an invitation the resource holds is revoked (see
// revokeGatedOutInvitation).
func (r *AuthentikUserReconciler) syncUser(ctx context.Context, u *authentikiov1alpha1.AuthentikUser,
	persist func() error) (*api.User, bool, error) {
	if err := (identityGate{r.APIReader, r.IdentityNamespaceSelector}).check(ctx, u.Namespace,
		u.Spec.AuthentikRef); err != nil {
		if conditionReason(err, "") == reasonIdentityNotAllowed {
			if rerr := r.revokeGatedOutInvitation(ctx, u); rerr != nil {
				return nil, false, rerr
			}
		}
		return nil, false, err
	}
	if err := r.withdrawInvitation(ctx, u); err != nil {
		return nil, false, err
	}
	if err := (adoptionGate{r.APIReader, r.AdoptionNamespaceSelector}).check(ctx, u.Namespace,
		u.Spec.AuthentikRef, u.Spec.ManagementPolicy, u.Status.Provenance); err != nil {
		return nil, false, err
	}
	if err := reservedAttributes(u.Spec.Attributes, attrGivenName, attrFamilyName); err != nil {
		return nil, false, condErr(reasonInvalidSpec, err)
	}
	want, err := specAttributes(u.Spec.Attributes)
	if err != nil {
		return nil, false, condErr(reasonInvalidSpec, err)
	}
	resolved, err := resolve(ctx, r.Connections, u.Namespace, u.Spec.AuthentikRef)
	if err != nil {
		return nil, false, err
	}
	work, err := r.observe(ctx, resolved.Client, u)
	if err != nil {
		return nil, false, err
	}
	work.want = want
	got, err := r.act(ctx, resolved, u, work, persist)
	if err != nil {
		return nil, false, err
	}
	return got, r.syncAuthenticators(ctx, resolved.Client, u, got), nil
}

// syncAuthenticators sets ConditionAuthenticatorMissing on a Created account
// from the devices Authentik lists for it: the person stopped after the account
// was written and has no way to sign in, and a new invitation cannot help (the
// username is taken). It never changes Ready: the account is in sync. When the
// list cannot be read the condition is left as it is and unknown is true, so
// the caller looks again soon.
func (r *AuthentikUserReconciler) syncAuthenticators(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser, got *api.User) (unknown bool) {
	if u.Status.Provenance != authentikiov1alpha1.ProvenanceCreated {
		meta.RemoveStatusCondition(&u.Status.Conditions, ConditionAuthenticatorMissing)
		return false
	}
	devices, err := c.UserAuthenticators(ctx, got.Pk)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list authenticators of User", "username", got.Username)
		return true
	}
	if len(devices) > 0 {
		meta.RemoveStatusCondition(&u.Status.Conditions, ConditionAuthenticatorMissing)
		return false
	}
	const msg = "the account has no authenticator; send the person a recovery link from the Authentik admin " +
		"interface (a new invitation cannot be used: the username is taken)"
	if meta.SetStatusCondition(&u.Status.Conditions, metav1.Condition{
		Type: ConditionAuthenticatorMissing, Status: metav1.ConditionTrue, Reason: reasonNoAuthenticator,
		Message: msg, ObservedGeneration: u.Generation,
	}) {
		emit(r.Recorder, u, corev1.EventTypeWarning, ConditionAuthenticatorMissing, "Sync",
			"User %q has no authenticator: %s",
			got.Username, msg)
	}
	return false
}

// Attribute keys that firstName and lastName own on an account.
const (
	attrGivenName  = "given_name"
	attrFamilyName = "family_name"
)

// isServiceAccount reports whether t is an Authentik user type that is not a person.
func isServiceAccount(t api.UserTypeEnum) bool {
	return t == api.USERTYPEENUM_SERVICE_ACCOUNT || t == api.USERTYPEENUM_INTERNAL_SERVICE_ACCOUNT
}

// userWork is what one sync works from: what Authentik says about the account
// (the tracked one, status.userPk, the one found by username, and the facts
// adopt.Decide needs) and what the spec wants of its attributes (nil: the field
// is absent, so spec.attributes owns no key).
type userWork struct {
	tracked, target *api.User
	facts           adopt.Facts
	want            map[string]any
}

// observe looks the account up: by status.userPk first (a 404 is Gone, and the
// pk is kept), then, unless the tracked account was found, the accounts that
// carry this resource's ownership proof while it is inviting (more than one, or
// one under another username, is reported before anything is decided), then the
// one named username. The account named username that carries the proof is the
// person's own enrolment.
func (r *AuthentikUserReconciler) observe(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser) (userWork, error) {
	username := u.Spec.Username
	obs := userWork{facts: adopt.Facts{
		Policy: u.Spec.ManagementPolicy, Provenance: u.Status.Provenance, DryRun: u.Spec.DryRun,
		OwnCreatePending: u.Status.Invitation != nil, RecreateVanished: false,
	}}
	if pk := u.Status.UserPK; pk != 0 {
		got, err := c.GetUser(ctx, pk)
		switch {
		case err == nil && got.Username != username:
			return obs, condErrf(reasonConflict, "the account pk %d is now named %q, not %q; refusing to manage it",
				pk, got.Username, username)
		case err == nil:
			obs.tracked, obs.facts.Tracked = got, adopt.TrackedFound
			return obs, nil
		case authentik.IsNotFound(err):
			obs.facts.Tracked = adopt.TrackedGone
		default:
			return obs, err
		}
	}
	owned, err := enrolled(ctx, c, u, obs.facts.OwnCreatePending)
	if err != nil {
		return obs, err
	}
	target, err := c.FindUserByUsername(ctx, username)
	switch {
	case err == nil:
		// While inviting, a service account of that name is a squatter like any other account without the proof:
		// Decide reports the Conflict, and the invitation is withdrawn.
		if isServiceAccount(target.GetType()) && !obs.facts.OwnCreatePending {
			return obs, condErrf(reasonConflict, "the account %q is a service account; AuthentikUser manages people only",
				username)
		}
		obs.target, obs.facts.Target = target, adopt.TargetFound
		// enrolmentProblem left at most one account holding the proof, under username: the one found, or not. A service
		// account is never recognised as a person, proof or not: it is a Conflict, and the invitation is withdrawn.
		obs.facts.OwnCreateProven = len(owned) == 1 && owned[0].Pk == target.Pk && !isServiceAccount(target.GetType())
	case errors.Is(err, authentik.ErrAmbiguous):
		obs.facts.Target = adopt.TargetAmbiguous
	case authentik.IsNotFound(err):
		obs.facts.Target = adopt.TargetMissing
	default:
		return obs, err
	}
	return obs, nil
}

// act carries out the decision adopt.Decide takes from the observation and
// returns the account as Authentik now has it.
func (r *AuthentikUserReconciler) act(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, work userWork, persist func() error) (*api.User, error) {
	c := resolved.Client
	username := u.Spec.Username
	action := adopt.Decide(work.facts)
	switch action {
	case adopt.ActionUseTracked:
		if err := r.yieldIfOutranked(ctx, r.Client, true, u, nil); err != nil {
			return nil, err
		}
		return r.syncFields(ctx, c, u, work.tracked, work.want)
	case adopt.ActionCreate:
		return nil, r.invite(ctx, resolved, u, persist)
	case adopt.ActionRecoverOwnCreate:
		return r.recognise(ctx, c, u, work)
	case adopt.ActionAdopt, adopt.ActionObserve:
		if err := r.adoptUser(ctx, u, work.target, persist); err != nil {
			return nil, err
		}
		return r.syncFields(ctx, c, u, work.target, work.want)
	case adopt.ActionDryRunAdopt:
		return nil, r.dryRunAdopt(ctx, u, work.target, work.want)
	case adopt.ActionDryRunCreate:
		return nil, condErrf(reasonAdoptionDryRun, "dry run: no user named %q exists; an invitation would be issued; "+
			"nothing was written", username)
	case adopt.ActionConflict:
		return nil, r.conflict(ctx, c, u)
	case adopt.ActionTargetNotFound, adopt.ActionTrackedGone:
		return nil, r.missing(u)
	case adopt.ActionAmbiguous:
		return nil, condErrf(reasonConflict, "several users named %q exist in Authentik; refusing to pick one", username)
	case adopt.ActionRecreate:
		// Never decided: RecreateVanished is false, so a vanished account is reported, not invited again.
	}
	return nil, fmt.Errorf("unexpected adoption action %q", action)
}

// recordUser records which account u manages and how it came to.
func recordUser(u *authentikiov1alpha1.AuthentikUser, prov authentikiov1alpha1.Provenance, user *api.User) {
	u.Status.Provenance = prov
	u.Status.UserPK = user.Pk
	// A new claim owns no attribute key yet: adoption keeps every live key, and an invited account starts clean.
	u.Status.ManagedAttributes = nil
}

// recognise takes the account the person enrolled through this resource's
// invitation as this resource's own, Created. The invitation (if Authentik
// still has it) and the Secret with its link are deleted first: the link is
// spent, and a failed delete retries with the evidence (the ownership proof)
// still in place.
func (r *AuthentikUserReconciler) recognise(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser, work userWork) (*api.User, error) {
	if err := r.revokeInvitation(ctx, c, u); err != nil {
		return nil, err
	}
	recordUser(u, authentikiov1alpha1.ProvenanceCreated, work.target)
	u.Status.Invitation = nil
	logf.FromContext(ctx).Info("Recognised enrolled User", "username", work.target.Username, "pk", work.target.Pk)
	emit(r.Recorder, u, corev1.EventTypeNormal, "Enrolled", "Enrol", "Recognised account %q enrolled from the invitation",
		work.target.Username)
	return r.syncFields(ctx, c, u, work.target, work.want)
}

// conflict is the Conflict for an account found by username that this resource
// may not take. While inviting, the account is someone else's squatting on the
// username: the invitation and its Secret are deleted, so the link can no
// longer create a second account, and status.invitation is kept so AdoptOrCreate
// never adopts it.
func (r *AuthentikUserReconciler) conflict(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser) error {
	username := u.Spec.Username
	if u.Status.Invitation != nil {
		if err := r.revokeInvitation(ctx, c, u); err != nil {
			return err
		}
		return condErrf(reasonConflict, "an account named %q exists, but it does not carry this resource's ownership "+
			"proof, so it is not treated as the account of this resource's invitation under any managementPolicy; the "+
			"invitation and its Secret were deleted. Remove that account from Authentik, or use another "+
			"spec.username", username)
	}
	return condErrf(reasonConflict, "an account named %q already exists in Authentik and was not created by this "+
		"resource; set spec.managementPolicy to Adopt (and remove spec.invitation), AdoptOrCreate or ReadOnly to take "+
		"it over or observe it", username)
}

// missing is the report for an account that is not there and will not be
// invited: an adoption or ReadOnly target that does not exist, or an account
// this resource tracked that vanished. The pk stays in status.
func (r *AuthentikUserReconciler) missing(u *authentikiov1alpha1.AuthentikUser) error {
	username := u.Spec.Username
	switch {
	case u.Status.UserPK != 0 && u.Status.Provenance == authentikiov1alpha1.ProvenanceCreated:
		return condErrf(reasonAdoptionTargetNotFound, "the account %q (pk %d) was deleted in Authentik; it is not "+
			"re-invited. To invite again, clear status.userPk and status.provenance and increase "+
			"spec.invitation.attempt", username, u.Status.UserPK)
	case u.Status.UserPK != 0:
		return condErrf(reasonAdoptionTargetNotFound, "the account %q (pk %d) no longer exists in Authentik; not "+
			"recreating it. Restore it, or clear status.userPk and status.provenance to adopt another account of "+
			"that name", username, u.Status.UserPK)
	case readOnly(u.Spec.ManagementPolicy):
		return condErrf(reasonAdoptionTargetNotFound, "no user named %q exists in Authentik to observe; "+
			"managementPolicy ReadOnly never writes to Authentik, so nothing is invited", username)
	}
	return condErrf(reasonAdoptionTargetNotFound, "no user named %q exists in Authentik to adopt; nothing was "+
		"written. Fix spec.username, or set spec.managementPolicy to AdoptOrCreate (with spec.invitation) to invite "+
		"a new person", username)
}

// userClaim is u's own hold on its account.
func userClaim(u *authentikiov1alpha1.AuthentikUser) claim {
	return claim{namespace: u.Namespace, name: u.Name, uid: u.UID, provenance: u.Status.Provenance,
		ref: u.Spec.AuthentikRef}
}

// claimants lists the other user resources, terminating ones included, that
// track pk on the same Authentik as u. rd is the cached client for the routine
// check, which lists through the field index (cached true), and the API reader
// where a stale answer is not good enough, which cannot filter on a custom
// field and lists everything.
func (r *AuthentikUserReconciler) claimants(ctx context.Context, rd client.Reader, cached bool,
	u *authentikiov1alpha1.AuthentikUser, pk int32) ([]claim, error) {
	var opts []client.ListOption
	if cached {
		opts = append(opts, client.MatchingFields{indexUserPK: strconv.Itoa(int(pk))})
	}
	var list authentikiov1alpha1.AuthentikUserList
	if err := rd.List(ctx, &list, opts...); err != nil {
		return nil, fmt.Errorf("list AuthentikUsers: %w", err)
	}
	var candidates []claim
	for i := range list.Items {
		o := &list.Items[i]
		if o.UID == u.UID || o.Status.UserPK != pk {
			continue
		}
		candidates = append(candidates, userClaim(o))
	}
	return holders(ctx, r.Connections, userClaim(u), candidates)
}

// yieldIfOutranked persists the claim (persist, nil when it is already
// recorded), then releases it and reports a Conflict when another resource
// holding the same account outranks it (see recheckClaim). rd is the reader the
// holders are listed with.
func (r *AuthentikUserReconciler) yieldIfOutranked(ctx context.Context, rd client.Reader, cached bool,
	u *authentikiov1alpha1.AuthentikUser, persist func() error) error {
	return recheckClaim(persist, userClaim(u), func() ([]claim, error) {
		return r.claimants(ctx, rd, cached, u, u.Status.UserPK)
	}, fmt.Sprintf("user %q", u.Spec.Username), func() {
		u.Status.Provenance, u.Status.UserPK, u.Status.ManagedAttributes = "", 0, nil
	})
}

// adoptUser takes over target (or, under ReadOnly, starts observing it): any
// other holder refuses it, then the claim is recorded and persisted before the
// authoritative re-check, and before anything is written to Authentik.
func (r *AuthentikUserReconciler) adoptUser(ctx context.Context, u *authentikiov1alpha1.AuthentikUser,
	target *api.User, persist func() error) error {
	if err := r.refuseHeld(ctx, u, target); err != nil {
		return err
	}
	recordUser(u, authentikiov1alpha1.ProvenanceAdopted, target)
	// Re-check after the claim is persisted, reading the API rather than the
	// informer cache, which need not hold another resource's just-written
	// claim; no Authentik write has happened yet.
	if err := r.yieldIfOutranked(ctx, r.APIReader, false, u, persist); err != nil {
		return err
	}
	if readOnly(u.Spec.ManagementPolicy) {
		emit(r.Recorder, u, corev1.EventTypeNormal, "Observing", "Adopt", "Observing existing user %q read only",
			target.Username)
	} else {
		emit(r.Recorder, u, corev1.EventTypeNormal, "Adopted", "Adopt", "Adopted existing user %q", target.Username)
	}
	return nil
}

// refuseHeld is a Conflict when any other resource, terminating ones included,
// already tracks target. This read may be served by a lagging cache; the
// re-check after the claim is persisted is the authoritative one.
func (r *AuthentikUserReconciler) refuseHeld(ctx context.Context, u *authentikiov1alpha1.AuthentikUser,
	target *api.User) error {
	others, err := r.claimants(ctx, r.Client, true, u, target.Pk)
	if err != nil {
		return err
	}
	if len(others) > 0 {
		return condErrf(reasonConflict, "user %q is already managed by %s; two resources cannot manage one account",
			target.Username, others[0])
	}
	return nil
}

// dryRunAdopt reports what adopting target would change and writes nothing.
func (r *AuthentikUserReconciler) dryRunAdopt(ctx context.Context, u *authentikiov1alpha1.AuthentikUser,
	target *api.User, want map[string]any) error {
	if err := r.refuseHeld(ctx, u, target); err != nil {
		return err
	}
	// Adoption keeps every live key, so nothing is owned yet, whatever an earlier claim left in status.
	plan := planUser(u, want, target, nil)
	patch, live := plan.reportView(target)
	msg, announce := dryRunReport(u.Status.Conditions, fmt.Sprintf("user %q", target.Username), patch, live, plan.changed)
	if announce {
		emit(r.Recorder, u, corev1.EventTypeNormal, reasonAdoptionDryRun, "Adopt", "%s", msg)
	}
	return condErr(reasonAdoptionDryRun, errors.New(msg))
}

// syncFields makes live match the spec: a PATCH of the fields that differ, or
// under ReadOnly a Drifted condition and no write. It returns the account as
// Authentik now has it.
func (r *AuthentikUserReconciler) syncFields(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser, live *api.User, want map[string]any) (*api.User, error) {
	plan := planUser(u, want, live, u.Status.ManagedAttributes)
	if readOnly(u.Spec.ManagementPolicy) {
		var diffs []string
		if len(plan.changed) > 0 {
			patch, current := plan.reportView(live)
			diffs = append(diffs, describeChanges(patch, current))
		}
		setDrift(&u.Status.Conditions, u.Generation, diffs...)
		return live, nil
	}
	if err := refuseSelfDeactivation(ctx, c, u, live); err != nil {
		return nil, err
	}
	got := live
	if len(plan.changed) > 0 {
		updated, err := c.PatchUser(ctx, live.Pk, plan.patch)
		if err != nil {
			return nil, err
		}
		got = updated
		logf.FromContext(ctx).Info("Updated User", "username", live.Username, "pk", live.Pk, "fields", plan.changed)
		emit(r.Recorder, u, corev1.EventTypeNormal, "Updated", "Update", "Updated user fields: %s",
			strings.Join(plan.changed, ", "))
	}
	u.Status.ManagedAttributes = nil
	if u.Spec.Attributes != nil {
		u.Status.ManagedAttributes = plan.owned
	}
	return got, nil
}

// refuseSelfDeactivation is an InvalidSpec when the spec deactivates the
// account the operator's own token belongs to: the operator would lock itself
// out of Authentik.
func refuseSelfDeactivation(ctx context.Context, c *authentik.Client, u *authentikiov1alpha1.AuthentikUser,
	live *api.User) error {
	if u.Spec.IsActive == nil || *u.Spec.IsActive {
		return nil
	}
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	if me.Pk == live.Pk {
		return condErrf(reasonInvalidSpec, "refusing to deactivate the operator's own user %q", me.Username)
	}
	return nil
}

// finalize deletes the account this resource is entitled to delete, then
// releases the finalizer. An account is left alone unless an explicit
// deletionPolicy Delete and the identity selector allow deleting it.
func (r *AuthentikUserReconciler) finalize(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(u, FinalizerName) {
		return ctrl.Result{}, nil
	}
	orig := u.Status.DeepCopy()
	result, release, err := r.decideDeletion(ctx, u)
	if perr := r.patchStatus(ctx, u, orig); perr != nil {
		err = errors.Join(err, perr)
	}
	if err != nil || !release {
		return result, err
	}
	base := u.DeepCopy()
	controllerutil.RemoveFinalizer(u, FinalizerName)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, u, client.MergeFrom(base)))
}

// retainNote is what a retained account's event says about offboarding.
const retainNote = "AuthentikUser retains accounts unless deletionPolicy is Delete; " +
	"set isActive: false first to offboard"

// decideDeletion runs the deletion flow. release says the finalizer may go. An
// unset deletionPolicy is Retain whatever the provenance: a person's passkeys
// are costly to enrol again.
func (r *AuthentikUserReconciler) decideDeletion(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) (result ctrl.Result, release bool, err error) {
	// The invitation is this resource's own, whatever the identity gate says of the namespace: no usable sign-up link
	// may outlive the resource.
	if u.Status.Invitation != nil {
		if err := r.deleteLiveInvitation(ctx, u); err != nil {
			return r.deletionFailed(u, err)
		}
	}
	if u.Status.UserPK == 0 {
		return ctrl.Result{}, true, nil
	}
	allowed, err := (identityGate{r.APIReader, r.IdentityNamespaceSelector}).allowed(ctx, u.Namespace,
		u.Spec.AuthentikRef)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	fin := adopt.Finalize(adopt.FinalizeFacts{
		Policy: u.Spec.ManagementPolicy, DeletionPolicy: u.Spec.DeletionPolicy, Provenance: u.Status.Provenance,
		UnsetDefault: authentikiov1alpha1.DeletionPolicyRetain, IdentityAllowed: allowed,
	})
	name := u.Spec.Username
	switch fin {
	case adopt.FinalizeRetain:
		logf.FromContext(ctx).Info("Retaining User in Authentik", "username", name)
		emit(r.Recorder, u, corev1.EventTypeNormal, "Retained", "Delete", "User %q left in Authentik: %s", name, retainNote)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeRetainUndecided:
		emit(r.Recorder, u, corev1.EventTypeWarning, "Retained", "Delete",
			"User %q left in Authentik: provenance unknown; retaining", name)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeRetainNotAllowed:
		emit(r.Recorder, u, corev1.EventTypeWarning, "Retained", "Delete",
			"User %q left in Authentik: it was not created by this resource, and this namespace is outside "+
				"--default-connection-identity-namespace-selector, which confines deletion to accounts the resource "+
				"created", name)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeDeleteUndecided:
		emit(r.Recorder, u, corev1.EventTypeWarning, "DeletingUndecided", "Delete",
			"Deleting user %q on an explicit deletionPolicy Delete although this resource's provenance is "+
				"undecided: it may not have created it", name)
		return r.deleteUser(ctx, u)
	case adopt.FinalizeDelete:
		return r.deleteUser(ctx, u)
	}
	return ctrl.Result{}, false, fmt.Errorf("unexpected finalization %q", fin)
}

// deleteUser deletes the account in Authentik, except the one the operator's
// own token belongs to.
func (r *AuthentikUserReconciler) deleteUser(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)
	name, pk := u.Spec.Username, u.Status.UserPK
	resolved, err := resolveForDeletion(ctx, r.Connections, u.Namespace, u.Spec.AuthentikRef)
	switch {
	case connectionGone(err):
		log.Info("Could not reach Authentik to delete User; leaving it in place", "username", name,
			"reason", err.Error())
		emit(r.Recorder, u, corev1.EventTypeWarning, "Orphaned", "Delete", "User %q left in Authentik: %v", name, err)
		return ctrl.Result{}, true, nil
	case err != nil:
		return r.deletionFailed(u, err)
	}
	c := resolved.Client
	// An account this resource created carries its ownership proof, and is deleted only while it does.
	if u.Status.Provenance == authentikiov1alpha1.ProvenanceCreated {
		live, err := c.GetUser(ctx, pk)
		switch {
		case authentik.IsNotFound(err):
			return ctrl.Result{}, true, nil
		case err != nil:
			return r.deletionFailed(u, err)
		case !authentik.HasOwnerProof(live.Attributes, string(u.UID)):
			emit(r.Recorder, u, corev1.EventTypeWarning, "Retained", "Delete", "User %q left in Authentik: it does not carry "+
				"this resource's ownership proof, so this resource did not create it", name)
			return ctrl.Result{}, true, nil
		}
	}
	me, err := c.Me(ctx)
	if err != nil {
		return r.deletionFailed(u, err)
	}
	if me.Pk == pk {
		emit(r.Recorder, u, corev1.EventTypeWarning, "NotDeleted", "Delete",
			"User %q left in Authentik: it is the operator's own token user, which is never deleted", name)
		return ctrl.Result{}, true, nil
	}
	if err := c.DeleteUser(ctx, pk); err != nil {
		return r.deletionFailed(u, err)
	}
	log.Info("Deleted User", "username", name, "pk", pk)
	emit(r.Recorder, u, corev1.EventTypeNormal, "Deleted", "Delete", "Deleted user %q", name)
	return ctrl.Result{}, true, nil
}

// deletionFailed keeps the resource terminating and reports why: nothing was
// deleted, and the error returned makes the controller retry with backoff.
func (r *AuthentikUserReconciler) deletionFailed(u *authentikiov1alpha1.AuthentikUser,
	err error) (ctrl.Result, bool, error) {
	if setReady(&u.Status.Conditions, u.Generation, metav1.ConditionFalse, conditionReason(err, reasonAuthentikError),
		"Deletion blocked: "+err.Error()) {
		emit(r.Recorder, u, corev1.EventTypeWarning, reasonDeletionBlocked, "Delete",
			"Cannot delete user %q yet: %v", u.Spec.Username, err)
	}
	return ctrl.Result{}, false, err
}

// failed records why sync stopped; see classifyFailure.
func (r *AuthentikUserReconciler) failed(u *authentikiov1alpha1.AuthentikUser, err error) (ctrl.Result, error) {
	reason, result, retErr, warn := classifyFailure(err)
	if setReady(&u.Status.Conditions, u.Generation, metav1.ConditionFalse, reason, err.Error()) {
		switch {
		case warn:
			emit(r.Recorder, u, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the user: %v", err)
		case reason == reasonInvitationAmbiguous:
			emit(r.Recorder, u, corev1.EventTypeWarning, reasonInvitationAmbiguous, "Sync", "%v", err)
		}
	}
	return result, retErr
}

func (r *AuthentikUserReconciler) patchStatus(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser, orig *authentikiov1alpha1.AuthentikUserStatus) error {
	base := u.DeepCopy()
	base.Status = *orig
	return patchStatus(ctx, r.Status(), u, base)
}

// userPKIndex indexes users by the pk they track.
func userPKIndex(o client.Object) []string {
	u, ok := o.(*authentikiov1alpha1.AuthentikUser)
	if !ok || u.Status.UserPK == 0 {
		return nil
	}
	return []string{strconv.Itoa(int(u.Status.UserPK))}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthentikUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &authentikiov1alpha1.AuthentikUser{},
		indexUserPK, userPKIndex); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikUser{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Named("authentikuser").
		Complete(r)
}
