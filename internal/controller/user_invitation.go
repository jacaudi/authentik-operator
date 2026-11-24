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
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// A new person gets an account by enrolling through a single-use invitation.
// The invitation's pk is the sign-up token: anyone holding it can create an
// account. It is therefore written only to the invitation Secret; it never
// reaches status, a condition, an event, a log line or an error message.

const (
	// reasonExternalURLRequired marks Ready=False on a user whose connection
	// has no browser-facing URL to build the sign-up link from.
	reasonExternalURLRequired = "ExternalURLRequired"
	// reasonInvitationPending marks Ready=False while the invitation waits for
	// the person to enrol; the link is in the Secret.
	reasonInvitationPending = "InvitationPending"
	// reasonInvitationConsumed marks Ready=False when the invitation is gone
	// before it expired and no account carrying the ownership proof exists
	// (yet: a normal enrolment passes through this state).
	reasonInvitationConsumed = "InvitationConsumed"
	// reasonInvitationExpired marks Ready=False when the invitation expired
	// before any account carrying the ownership proof appeared.
	reasonInvitationExpired = "InvitationExpired"
	// reasonInvitationAmbiguous marks Ready=False when several invitations in
	// Authentik carry the name this resource issued (names are not unique
	// there), so it cannot tell which one is its own.
	reasonInvitationAmbiguous = "InvitationAmbiguous"
	// reasonMultipleEnrolments marks Ready=False when more than one account
	// carries this resource's ownership proof.
	reasonMultipleEnrolments = "MultipleEnrolments"
	// reasonEnrolledUnderOtherName marks Ready=False when the person enrolled
	// through the invitation under a username other than spec.username.
	reasonEnrolledUnderOtherName = "EnrolledUnderOtherName"

	// ConditionAuthenticatorMissing is True on a Created account that has no
	// authenticator. It does not affect Ready.
	ConditionAuthenticatorMissing = "AuthenticatorMissing"
	// reasonNoAuthenticator is the reason of ConditionAuthenticatorMissing.
	reasonNoAuthenticator = "NoAuthenticator"

	// defaultInvitationExpiry is how long an invitation stays valid when
	// spec.invitation.expiresAfter is unset.
	defaultInvitationExpiry = 7 * 24 * time.Hour

	// Keys of the invitation Secret.
	invitationSecretURL     = "url"
	invitationSecretExpires = "expires"
)

// invitationName is the name of the invitation of the given attempt in
// Authentik. Names are not unique there, so the uid keeps one resource's
// invitations apart from every other's.
func invitationName(uid types.UID, attempt int32) string {
	return fmt.Sprintf("%s%d", invitationNamePrefix(uid), attempt)
}

// invitationNamePrefix starts the name of every invitation the resource with
// this uid issues, and so of every invitation it deletes.
func invitationNamePrefix(uid types.UID) string {
	return fmt.Sprintf("k8s-%s-", uid)
}

// invitationAttempt is spec.invitation.attempt, at least 1.
func invitationAttempt(u *authentikiov1alpha1.AuthentikUser) int32 {
	if u.Spec.Invitation == nil {
		return 1
	}
	return max(u.Spec.Invitation.Attempt, 1)
}

// invitationSecretName is the Secret that receives the sign-up link.
func invitationSecretName(u *authentikiov1alpha1.AuthentikUser) string {
	if u.Spec.Invitation == nil {
		return u.Name + "-invitation"
	}
	return cmp.Or(u.Spec.Invitation.SecretName, u.Name+"-invitation")
}

// invitationLink is the sign-up link of the invitation whose pk is pk, to the
// enrolment flow flowSlug, on the browser-facing base URL externalURL (which
// may carry a path prefix and a trailing slash).
func invitationLink(externalURL, flowSlug, pk string) string {
	return strings.TrimRight(externalURL, "/") + "/if/flow/" + flowSlug + "/?itoken=" + url.QueryEscape(pk)
}

// invite drives the invitation of a person who has no account yet, and so
// always returns an error: a condition error saying where the invitation
// stands (ExternalURLRequired, ReferenceNotFound, InvitationPending,
// InvitationConsumed, InvitationExpired), or the failure that stopped it.
// Nothing is created without an explicit browser-facing URL.
func (r *AuthentikUserReconciler) invite(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, persist func() error) error {
	if !resolved.ExternalURLSet {
		return condErrf(reasonExternalURLRequired, "invitations need a browser-facing URL: set "+
			"spec.connection.externalURL on the AuthentikDeployment, or --authentik-external-url (Helm value "+
			"authentik.externalUrl) for the default connection")
	}
	switch inv := u.Status.Invitation; {
	case inv == nil:
		return r.issueInvitation(ctx, resolved, u, persist)
	case invitationAttempt(u) > inv.Attempt:
		return r.reissueInvitation(ctx, resolved, u, persist)
	case inv.Pending:
		return r.confirmInvitation(ctx, resolved, u)
	}
	return r.checkInvitation(ctx, resolved, u)
}

// invitationFlow resolves the enrolment flow the invitation is bound to.
func invitationFlow(ctx context.Context, c *authentik.Client, u *authentikiov1alpha1.AuthentikUser) (*api.Flow, error) {
	slug := u.Spec.Invitation.Flow
	flow, err := c.FlowBySlug(ctx, slug)
	if authentik.IsNotFound(err) {
		return nil, condErrf(reasonReferenceNotFound, "the enrolment flow %q does not exist in Authentik; fix "+
			"spec.invitation.flow", slug)
	}
	return flow, err
}

// issueInvitation issues the first invitation of a resource that has none.
func (r *AuthentikUserReconciler) issueInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, persist func() error) error {
	flow, err := invitationFlow(ctx, resolved.Client, u)
	if err != nil {
		return err
	}
	return r.startInvitation(ctx, resolved, u, flow, persist)
}

// reissueInvitation replaces the invitation of an earlier attempt by one for
// the attempt the spec now names: the live invitation of the earlier attempt
// is deleted, and so is its Secret when the new attempt's link goes to a Secret
// of another name (the same Secret is rewritten). The flow is resolved first,
// so a mistyped flow does not cost the live link.
func (r *AuthentikUserReconciler) reissueInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, persist func() error) error {
	flow, err := invitationFlow(ctx, resolved.Client, u)
	if err != nil {
		return err
	}
	previous := u.Status.Invitation
	if err := deleteInvitation(ctx, resolved.Client, u.UID, previous.Name); err != nil {
		return err
	}
	if previous.SecretName != invitationSecretName(u) {
		if err := r.deleteInvitationSecret(ctx, u); err != nil {
			return err
		}
	}
	logf.FromContext(ctx).Info("Replaced Invitation of an earlier attempt", "name", previous.Name,
		"attempt", previous.Attempt)
	return r.startInvitation(ctx, resolved, u, flow, persist)
}

// startInvitation records the invitation of the current attempt in status
// (pending, so a response lost in flight is recovered by name), persists it,
// and only then asks Authentik to create it: Authentik chooses the pk, so the
// name is all that can find it again.
func (r *AuthentikUserReconciler) startInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, flow *api.Flow, persist func() error) error {
	lifetime := defaultInvitationExpiry
	if e := u.Spec.Invitation.ExpiresAfter; e != nil {
		lifetime = e.Duration
	}
	attempt := invitationAttempt(u)
	u.Status.Invitation = &authentikiov1alpha1.InvitationStatus{
		Name: invitationName(u.UID, attempt), Attempt: attempt, Pending: true,
		Expires:    metav1.NewTime(time.Now().Add(lifetime).UTC().Truncate(time.Second)),
		SecretName: invitationSecretName(u),
	}
	if err := persist(); err != nil {
		return err
	}
	return r.createInvitation(ctx, resolved, u, flow)
}

// createInvitation sends the create request for the invitation status records,
// then writes the Secret that holds its link.
func (r *AuthentikUserReconciler) createInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, flow *api.Flow) error {
	inv := u.Status.Invitation
	created, err := resolved.Client.CreateInvitation(ctx, *desiredInvitation(u, inv.Name, flow.Pk, inv.Expires.Time))
	if err != nil {
		if code := authentik.StatusCode(err); code >= 400 && code < 500 {
			u.Status.Invitation = nil // definitely not created
		}
		return err
	}
	inv.Pending = false
	logf.FromContext(ctx).Info("Issued Invitation", "name", inv.Name)
	if err := r.ensureInvitationSecret(ctx, resolved, u, created); err != nil {
		return err
	}
	r.event(u, corev1.EventTypeNormal, "InvitationIssued", "Invite", "Issued invitation %q; the link is in Secret %q",
		inv.Name, inv.SecretName)
	return invitationPending(inv)
}

// confirmInvitation settles an invitation whose creation outcome is unknown: a
// request was sent and its response lost. One found by name was created;
// otherwise the request never arrived, and is sent again.
func (r *AuthentikUserReconciler) confirmInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser) error {
	inv := u.Status.Invitation
	found, err := findOwnInvitation(ctx, resolved.Client, u)
	switch {
	case err == nil:
		inv.Pending = false
		if err := r.ensureInvitationSecret(ctx, resolved, u, found); err != nil {
			return err
		}
		return invitationPending(inv)
	case !authentik.IsNotFound(err):
		return err
	}
	flow, err := invitationFlow(ctx, resolved.Client, u)
	if err != nil {
		return err
	}
	return r.createInvitation(ctx, resolved, u, flow)
}

// checkInvitation reports where an invitation that Authentik confirmed stands:
// still valid (the Secret is made current), or gone because the person is
// enrolling or has abandoned it, or because it expired. An invitation found
// past its expiry is deleted: Authentik keeps expired ones until cleanup. The
// search for the enrolled account runs again on the next reconcile; this state
// is where the person passes through between the invitation's first stage and
// the account's creation.
func (r *AuthentikUserReconciler) checkInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser) error {
	inv := u.Status.Invitation
	expired := !time.Now().Before(inv.Expires.Time)
	found, err := findOwnInvitation(ctx, resolved.Client, u)
	switch {
	case err == nil && !expired:
		if err := r.ensureInvitationSecret(ctx, resolved, u, found); err != nil {
			return err
		}
		return invitationPending(inv)
	case err == nil:
		if err := resolved.Client.DeleteInvitation(ctx, found.Pk); err != nil {
			return err
		}
	case !authentik.IsNotFound(err):
		return err
	}
	if err := r.deleteInvitationSecret(ctx, u); err != nil {
		return err
	}
	const remedy = "no new link is issued automatically: increase spec.invitation.attempt"
	if expired {
		return condErrf(reasonInvitationExpired, "the invitation expired on %s before an account carrying this "+
			"resource's ownership proof appeared; %s", inv.Expires.UTC().Format(time.RFC3339), remedy)
	}
	return condErrf(reasonInvitationConsumed, "the invitation is gone before an account carrying this resource's "+
		"ownership proof appeared (opened, deleted or abandoned); %s", remedy)
}

// invitationPending is the state of an invitation waiting for the person.
func invitationPending(inv *authentikiov1alpha1.InvitationStatus) error {
	return condErrf(reasonInvitationPending, "invitation %q expires %s; the sign-up link is in Secret %q, key %s",
		inv.Name, inv.Expires.UTC().Format(time.RFC3339), inv.SecretName, invitationSecretURL)
}

// findInvitation is FindInvitationByName with the one answer every caller gives
// when several invitations carry the name (names are not unique in Authentik):
// this resource cannot tell which one it issued, so it must neither delete nor
// trust any of them.
func findInvitation(ctx context.Context, c *authentik.Client, name string) (*api.Invitation, error) {
	found, err := c.FindInvitationByName(ctx, name)
	if errors.Is(err, authentik.ErrAmbiguous) {
		return nil, condErrf(reasonInvitationAmbiguous, "several invitations in Authentik are named %q; this resource "+
			"cannot tell which one it issued, so it issues and deletes none. Delete the extra ones in Authentik", name)
	}
	return found, err
}

// findOwnInvitation is findInvitation for the invitation status records, and
// answers only with an invitation carrying what desiredInvitation sets: the
// username, the ownership proof and single use. Any other invitation of that
// name is not this resource's: its link is never written to the Secret.
func findOwnInvitation(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser) (*api.Invitation, error) {
	name := u.Status.Invitation.Name
	found, err := findInvitation(ctx, c, name)
	if err != nil {
		return nil, err
	}
	username, _ := found.FixedData[fixedDataUsername].(string)
	attributes, _ := found.FixedData[fixedDataAttributes].(map[string]any)
	if username != u.Spec.Username || !found.GetSingleUse() || !authentik.HasOwnerProof(attributes, string(u.UID)) {
		return nil, condErrf(reasonInvitationAmbiguous, "an invitation named %q exists in Authentik but does not carry "+
			"this resource's username and ownership proof, so this resource did not issue it; it neither links nor "+
			"deletes it. Delete it in Authentik", name)
	}
	return found, nil
}

// deleteInvitation deletes the live invitation named name, if there is one and
// it is a name the resource with this uid issues (see invitationNamePrefix).
// Errors carry no pk (see authentik.Client.DeleteInvitation).
func deleteInvitation(ctx context.Context, c *authentik.Client, uid types.UID, name string) error {
	if !strings.HasPrefix(name, invitationNamePrefix(uid)) {
		return nil
	}
	found, err := findInvitation(ctx, c, name)
	switch {
	case authentik.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	return c.DeleteInvitation(ctx, found.Pk)
}

// revokeInvitation deletes the invitation that status records, if Authentik still
// has it, and the Secret that holds its link. The invitation goes first: a
// failed delete retries with the evidence still in status.
func (r *AuthentikUserReconciler) revokeInvitation(ctx context.Context, c *authentik.Client,
	u *authentikiov1alpha1.AuthentikUser) error {
	if err := deleteInvitation(ctx, c, u.UID, u.Status.Invitation.Name); err != nil {
		return err
	}
	return r.deleteInvitationSecret(ctx, u)
}

// withdrawInvitation revokes the invitation of a resource whose
// managementPolicy no longer creates (it was switched to Adopt or ReadOnly) and
// forgets it, so the resource is then decided like any other of that policy.
// The live link must not outlive the policy that issued it.
func (r *AuthentikUserReconciler) withdrawInvitation(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) error {
	inv := u.Status.Invitation
	if inv == nil || u.Spec.ManagementPolicy.MayCreate() {
		return nil
	}
	resolved, err := resolve(ctx, r.Connections, u.Namespace, u.Spec.AuthentikRef)
	if err != nil {
		return err
	}
	if err := r.revokeInvitation(ctx, resolved.Client, u); err != nil {
		return err
	}
	u.Status.Invitation = nil
	logf.FromContext(ctx).Info("Withdrew Invitation after the managementPolicy stopped creating", "name", inv.Name,
		"policy", u.Spec.ManagementPolicy.Resolve())
	r.event(u, corev1.EventTypeNormal, "InvitationWithdrawn", "Withdraw",
		"Withdrew invitation %q: managementPolicy %s does not create accounts", inv.Name, u.Spec.ManagementPolicy.Resolve())
	return nil
}

// revokeGatedOutInvitation revokes the invitation of a resource whose namespace
// the identity gate no longer allows. The link is the operator's own credential
// and must not outlive the authority that issued it, as it must not outlive the
// resource (see deleteLiveInvitation). status.invitation is kept, as for a
// Conflict: it is the evidence that stops AdoptOrCreate adopting an account of
// that name, and it makes the resource report InvitationConsumed, not issue a
// link again, once the namespace is allowed.
//
// ponytail: this repeats, at the cost of one list and one Secret read, on every
// recheck while the namespace stays gated out; record that it was done in
// status if that cost matters.
func (r *AuthentikUserReconciler) revokeGatedOutInvitation(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) error {
	if u.Status.Invitation == nil {
		return nil
	}
	resolved, err := resolve(ctx, r.Connections, u.Namespace, u.Spec.AuthentikRef)
	if err != nil {
		return err
	}
	return r.revokeInvitation(ctx, resolved.Client, u)
}

// deleteLiveInvitation deletes the invitation of a resource being deleted, so
// no usable sign-up link outlives it (the Secret goes with its owner
// reference). A connection whose objects are already gone cannot be reached:
// the invitation is then left to expire.
func (r *AuthentikUserReconciler) deleteLiveInvitation(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) error {
	name := u.Status.Invitation.Name
	resolved, err := resolveForDeletion(ctx, r.Connections, u.Namespace, u.Spec.AuthentikRef)
	switch {
	case connectionGone(err):
		logf.FromContext(ctx).Info("Could not reach Authentik to delete Invitation; leaving it in place", "name", name,
			"reason", err.Error())
		r.event(u, corev1.EventTypeWarning, "Orphaned", "Delete", "Invitation %q left in Authentik: %v", name, err)
		return nil
	case err != nil:
		return err
	}
	return deleteInvitation(ctx, resolved.Client, u.UID, name)
}

// enrolmentProblem is the failure when the accounts that carry this resource's
// ownership proof are not exactly the one the invitation pinned to username:
// nil for none yet, or for exactly one named username.
func enrolmentProblem(username string, owned []api.User) error {
	switch {
	case len(owned) == 0, len(owned) == 1 && owned[0].Username == username:
		return nil
	case len(owned) == 1:
		return condErrf(reasonEnrolledUnderOtherName, "the invitation was used to enrol %q, not %q (the enrolment "+
			"flow's username prompt is not read-only); fix the blueprint, then delete that account or use a new "+
			"resource with that username", owned[0].Username, username)
	}
	names := make([]string, len(owned))
	for i := range owned {
		names[i] = owned[i].Username
	}
	slices.Sort(names)
	return condErrf(reasonMultipleEnrolments, "several accounts carry this resource's ownership proof: %s (earlier "+
		"invitations were used under other names); none is adopted. Delete the accounts that are not wanted in "+
		"Authentik", quoted(names))
}

// ensureInvitationSecret makes the Secret status names hold the sign-up link of
// inv, creating it or rewriting it when missing or different. A Secret this
// resource does not control is never touched.
func (r *AuthentikUserReconciler) ensureInvitationSecret(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, inv *api.Invitation) error {
	status := u.Status.Invitation
	data := map[string][]byte{
		invitationSecretURL:     []byte(invitationLink(resolved.ExternalURL, inv.FlowObj.Slug, inv.Pk)),
		invitationSecretExpires: []byte(status.Expires.UTC().Format(time.RFC3339)),
	}
	var s corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: u.Namespace, Name: status.SecretName}, &s)
	switch {
	case apierrors.IsNotFound(err):
		s = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: status.SecretName, Namespace: u.Namespace,
				Labels: map[string]string{labelManagedBy: labelManagedByValue}},
			Type: corev1.SecretTypeOpaque, Data: data,
		}
		if err := controllerutil.SetControllerReference(u, &s, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &s); err != nil {
			return secretWriteError("create", u.Namespace, status.SecretName, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get Secret %s/%s: %w", u.Namespace, status.SecretName, err)
	case !metav1.IsControlledBy(&s, u):
		return condErrf(reasonSecretConflict, "Secret %q already exists and is not managed by this resource; "+
			"refusing to overwrite it. Choose another spec.invitation.secretName or delete that Secret", status.SecretName)
	}
	if equality.Semantic.DeepEqual(s.Data, data) && s.Labels[labelManagedBy] == labelManagedByValue {
		return nil
	}
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	s.Labels[labelManagedBy] = labelManagedByValue
	s.Data = data
	if err := r.Update(ctx, &s); err != nil {
		return secretWriteError("update", u.Namespace, status.SecretName, err)
	}
	return nil
}

// deleteInvitationSecret deletes the invitation Secret if this resource still
// controls it. Preconditions make the delete fail if the Secret was replaced
// or changed after the ownership check.
func (r *AuthentikUserReconciler) deleteInvitationSecret(ctx context.Context,
	u *authentikiov1alpha1.AuthentikUser) error {
	if u.Status.Invitation == nil {
		return nil
	}
	name := u.Status.Invitation.SecretName
	var s corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: u.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("get Secret %s/%s: %w", u.Namespace, name, err)
	case !metav1.IsControlledBy(&s, u):
		return nil
	}
	uid, rv := s.UID, s.ResourceVersion
	err = r.Delete(ctx, &s, client.Preconditions{UID: &uid, ResourceVersion: &rv})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Secret %s/%s: %w", u.Namespace, name, err)
	}
	return nil
}

// enrolled lists the accounts that carry this resource's ownership proof while
// it is inviting, and fails when they are not exactly the one account the
// invitation pinned (see enrolmentProblem). Outside the inviting phase there is
// nothing to look for.
func enrolled(ctx context.Context, c *authentik.Client, u *authentikiov1alpha1.AuthentikUser,
	inviting bool) ([]api.User, error) {
	if !inviting {
		return nil, nil
	}
	owned, err := c.FindUsersByOwnerUID(ctx, string(u.UID))
	if err != nil {
		return nil, err
	}
	return owned, enrolmentProblem(u.Spec.Username, owned)
}
