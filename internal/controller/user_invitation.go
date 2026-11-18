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
	"fmt"
	"net/url"
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
	return fmt.Sprintf("k8s-%s-%d", uid, attempt)
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

// issueInvitation records the invitation in status (pending, so a response
// lost in flight is recovered by name), persists it, and only then asks
// Authentik to create it: Authentik chooses the pk, so the name is all that
// can find it again.
func (r *AuthentikUserReconciler) issueInvitation(ctx context.Context, resolved *connection.Resolved,
	u *authentikiov1alpha1.AuthentikUser, persist func() error) error {
	flow, err := invitationFlow(ctx, resolved.Client, u)
	if err != nil {
		return err
	}
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
	found, err := resolved.Client.FindInvitationByName(ctx, inv.Name)
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
	found, err := resolved.Client.FindInvitationByName(ctx, inv.Name)
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

// deleteInvitation deletes the live invitation named name, if there is one.
// Errors carry no pk (see authentik.Client.DeleteInvitation).
func deleteInvitation(ctx context.Context, c *authentik.Client, name string) error {
	found, err := c.FindInvitationByName(ctx, name)
	switch {
	case authentik.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	return c.DeleteInvitation(ctx, found.Pk)
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
