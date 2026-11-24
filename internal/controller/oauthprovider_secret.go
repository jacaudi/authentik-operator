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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
	"github.com/jacaudi/authentik-operator/internal/credentials"
)

const (
	// ConditionCredentialsReady reports whether the credentials Secret is
	// rendered and current. Kept separate from Ready, which only describes
	// the provider in Authentik: applications gate on Ready, and the Secret
	// needs an application -- folding the two together would deadlock.
	ConditionCredentialsReady = "CredentialsReady"

	reasonSecretSynced          = "SecretSynced"
	reasonWaitingForApplication = "WaitingForApplication"
	reasonTemplateError         = "TemplateError"
	reasonSecretConflict        = "SecretConflict"
	reasonSecretInvalid         = "SecretInvalid"
	reasonExternalURLMismatch   = "ExternalURLMismatch"
	reasonTemplateNotFound      = "TemplateNotFound"
	reasonTemplateNotAllowed    = "TemplateNotAllowed"

	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelManagedByValue = "authentik-operator"
	// annotationProvider names the owning provider (an annotation, not a
	// label: CR names may exceed the 63-character label value limit).
	annotationProvider = "authentik.io/provider"
	// annotationApplied records which template label/annotation keys the
	// operator applied, so keys removed from the template can be removed from
	// the Secret without touching keys added by other controllers.
	annotationApplied = "authentik.io/applied-metadata"
)

// credentialsSecretName is the Secret this resource should render, or "".
func credentialsSecretName(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	if p.Spec.Secret == nil {
		return ""
	}
	return cmp.Or(p.Spec.Secret.Name, p.Name+"-oauth")
}

// syncSecret renders the credentials Secret after the provider itself is in
// sync and records the outcome in the CredentialsReady condition. It never
// affects Ready. A non-nil error is a transient failure to retry with
// backoff; requeue asks for a slow retry of a user-fixable problem.
//
// Invariants: the Secret is only ever written in the resource's own
// namespace, and an existing Secret not controlled by this resource is never
// modified or deleted -- so spec.secret.name cannot be used to overwrite or
// remove another application's Secret.
func (r *AuthentikOAuthProviderReconciler) syncSecret(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, resolved *connection.Resolved,
	got *api.OAuth2Provider) (requeue bool, err error) {
	name := credentialsSecretName(p)
	if name == "" {
		return false, r.clearSecret(ctx, p)
	}

	tmpl, err := r.credentialsTemplate(ctx, p)
	if err != nil {
		return r.credentialsFailed(ctx, p, err, true)
	}
	data, err := r.renderSecretData(ctx, resolved, got, tmpl)
	if err != nil {
		return r.credentialsFailed(ctx, p, err, false)
	}
	if err := r.applySecret(ctx, p, name, data, tmpl); err != nil {
		return r.credentialsFailed(ctx, p, err, false)
	}

	// Only now that the new Secret exists is a previously rendered one
	// (rename) removed, so a failed rename never leaves workloads without
	// credentials.
	if prev := p.Status.SecretName; prev != "" && prev != name {
		if err := r.deleteOwnedSecret(ctx, p, prev); err != nil {
			return false, err
		}
	}
	p.Status.SecretName = name
	r.setCredentials(p, metav1.ConditionTrue, reasonSecretSynced, fmt.Sprintf("Secret %q is current", name), false)
	return false, nil
}

// credentialsFailed records why the Secret was not rendered. A
// conditionError is reported in CredentialsReady and retried on the slow
// interval; anything else is transient and backs off. withdraw strips the
// template's labels and annotations from the existing Secret: used when
// access to the template is gone (or it is broken), since what it granted
// -- opting the Secret into replication or another controller -- goes with
// it. The data, this provider's own credentials, is kept so workloads keep
// working.
func (r *AuthentikOAuthProviderReconciler) credentialsFailed(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, err error, withdraw bool) (bool, error) {
	ce, ok := errors.AsType[*conditionError](err)
	if !ok {
		return false, err
	}
	msg := err.Error()
	if withdraw {
		stripped, err := r.stripTemplateMetadata(ctx, p)
		if err != nil {
			return false, err
		}
		if stripped {
			msg += fmt.Sprintf("; removed the template's labels and annotations from Secret %q (data kept)", p.Status.SecretName)
		}
	}
	// A missing application is a normal transitional state and an external
	// URL mismatch is already on the condition; neither gets a Warning event.
	warn := ce.reason != reasonWaitingForApplication && ce.reason != reasonExternalURLMismatch
	r.setCredentials(p, metav1.ConditionFalse, ce.reason, msg, warn)
	return true, nil
}

// clearSecret removes a previously rendered Secret and the CredentialsReady
// condition once spec.secret is unset.
func (r *AuthentikOAuthProviderReconciler) clearSecret(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider) error {
	if prev := p.Status.SecretName; prev != "" {
		if err := r.deleteOwnedSecret(ctx, p, prev); err != nil {
			return err
		}
		p.Status.SecretName = ""
	}
	meta.RemoveStatusCondition(&p.Status.Conditions, ConditionCredentialsReady)
	return nil
}

// credentialsTemplate returns the template spec.secret.templateRef names,
// or an empty template (default keys, no labels or annotations) without
// one. A conditionError is a user-visible, non-transient problem; the
// previously rendered Secret is then left as it is.
func (r *AuthentikOAuthProviderReconciler) credentialsTemplate(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider) (*authentikiov1alpha1.AuthentikCredentialsTemplateSpec, error) {
	ref := p.Spec.Secret.TemplateRef
	if ref == nil {
		return &authentikiov1alpha1.AuthentikCredentialsTemplateSpec{}, nil
	}
	var t authentikiov1alpha1.AuthentikCredentialsTemplate
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, &t); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, condErrf(reasonTemplateNotFound, "AuthentikCredentialsTemplate %q not found", ref.Name)
		}
		return nil, fmt.Errorf("get AuthentikCredentialsTemplate %s: %w", ref.Name, err)
	}
	sel, err := metav1.LabelSelectorAsSelector(&t.Spec.NamespaceSelector)
	if err != nil {
		return nil, condErrf(reasonInvalidNamespaceSelector, "AuthentikCredentialsTemplate %q has an invalid "+
			"namespaceSelector: %w", ref.Name, err)
	}
	if !sel.Empty() {
		var ns corev1.Namespace
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: p.Namespace}, &ns); err != nil {
			return nil, fmt.Errorf("get Namespace %s: %w", p.Namespace, err)
		}
		if !sel.Matches(labels.Set(ns.Labels)) {
			return nil, condErrf(reasonTemplateNotAllowed, "AuthentikCredentialsTemplate %q does not allow namespace %q "+
				"(its namespaceSelector does not match)", ref.Name, p.Namespace)
		}
	}
	return &t.Spec, nil
}

// renderSecretData computes the Secret data. A conditionError is a
// user-visible, non-transient problem.
func (r *AuthentikOAuthProviderReconciler) renderSecretData(ctx context.Context, resolved *connection.Resolved,
	got *api.OAuth2Provider, tmpl *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) (map[string][]byte, error) {
	slug := nullable(got.AssignedApplicationSlug.Get())
	if slug == "" {
		return nil, condErrf(reasonWaitingForApplication,
			"no AuthentikApplication uses this provider yet; the credentials Secret needs the application's slug")
	}
	urls, err := resolved.Client.OAuth2ProviderSetupURLs(ctx, got.Pk)
	if err != nil {
		return nil, err
	}
	if urls.Issuer == "" || urls.Authorize == "" || urls.Token == "" {
		// The application was detached between the two reads.
		return nil, condErrf(reasonWaitingForApplication, "Authentik returned no endpoint URLs for this provider yet")
	}

	var rewriteErr error
	rw := func(u string) string {
		if u == "" {
			return ""
		}
		out, err := credentials.Rewrite(u, resolved.Client.BaseURL(), resolved.ExternalURL)
		if err != nil && rewriteErr == nil {
			rewriteErr = err
		}
		return out
	}
	vars := credentials.Vars{
		ClientID:        got.GetClientId(),
		ClientSecret:    got.GetClientSecret(),
		IssuerURL:       rw(urls.Issuer),
		AuthURL:         rw(urls.Authorize),
		TokenURL:        rw(urls.Token),
		UserInfoURL:     rw(urls.UserInfo),
		LogoutURL:       rw(urls.Logout),
		JWKSURL:         rw(urls.Jwks),
		ProviderInfoURL: rw(urls.ProviderInfo),
		Slug:            slug,
		Name:            nullable(got.AssignedApplicationName.Get()),
	}
	if rewriteErr != nil {
		return nil, condErrf(reasonExternalURLMismatch, "cannot rewrite Authentik endpoint URLs to the external URL "+
			"(%w); they are built from the operator's API URL %s", rewriteErr, resolved.Client.BaseURL())
	}
	data, err := credentials.Render(tmpl.Template, vars)
	if err != nil {
		if credentials.IsTemplateError(err) {
			return nil, condErr(reasonTemplateError, err)
		}
		return nil, err
	}
	return data, nil
}

// appliedMetadata is the content of annotationApplied.
type appliedMetadata struct {
	Labels      []string `json:"labels,omitempty"`
	Annotations []string `json:"annotations,omitempty"`
}

// appliedTo reads annotationApplied from s. A missing or malformed
// annotation (someone edited it) counts as nothing applied.
func appliedTo(s *corev1.Secret) appliedMetadata {
	var m appliedMetadata
	if err := json.Unmarshal([]byte(s.Annotations[annotationApplied]), &m); err != nil {
		return appliedMetadata{}
	}
	return m
}

// applySecret creates or updates the owned Secret. A conditionError is a
// user-visible problem (Secret owned by someone else, rejected by the API).
func (r *AuthentikOAuthProviderReconciler) applySecret(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, name string, data map[string][]byte,
	tmpl *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) error {
	tmplLabels, tmplAnnotations := tmpl.Labels, tmpl.Annotations
	applied := appliedMetadata{Labels: sortedKeys(tmplLabels), Annotations: sortedKeys(tmplAnnotations)}
	appliedJSON, err := json.Marshal(applied)
	if err != nil {
		return err
	}

	var s corev1.Secret
	err = r.APIReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		s = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}
		s.Labels = mergeMetadata(nil, tmplLabels, nil, map[string]string{labelManagedBy: labelManagedByValue})
		s.Annotations = mergeMetadata(nil, tmplAnnotations, nil,
			map[string]string{annotationProvider: p.Name, annotationApplied: string(appliedJSON)})
		if err := controllerutil.SetControllerReference(p, &s, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &s); err != nil {
			// AlreadyExists (created concurrently by someone else) and
			// other errors retry; the next attempt sees the Secret and
			// applies the ownership check.
			return secretWriteError("create", p.Namespace, name, err)
		}
		logf.FromContext(ctx).Info("Created credentials Secret", "secret", name)
		emit(r.Recorder, p, corev1.EventTypeNormal, "SecretCreated", "Create", "Created credentials Secret %q", name)
		return nil
	case err != nil:
		return fmt.Errorf("get Secret %s/%s: %w", p.Namespace, name, err)
	}

	if !metav1.IsControlledBy(&s, p) {
		return condErrf(reasonSecretConflict, "Secret %q already exists and is not managed by this resource; "+
			"refusing to overwrite it. Choose another spec.secret.name or delete that Secret", name)
	}

	previous := appliedTo(&s)
	wantLabels := mergeMetadata(s.Labels, tmplLabels, previous.Labels,
		map[string]string{labelManagedBy: labelManagedByValue})
	wantAnnotations := mergeMetadata(s.Annotations, tmplAnnotations, previous.Annotations,
		map[string]string{annotationProvider: p.Name, annotationApplied: string(appliedJSON)})
	if equality.Semantic.DeepEqual(s.Data, data) && equality.Semantic.DeepEqual(s.Labels, wantLabels) &&
		equality.Semantic.DeepEqual(s.Annotations, wantAnnotations) {
		return nil // unchanged: not rewritten
	}
	s.Data, s.Labels, s.Annotations = data, wantLabels, wantAnnotations
	if err := r.Update(ctx, &s); err != nil {
		return secretWriteError("update", p.Namespace, name, err)
	}
	logf.FromContext(ctx).Info("Updated credentials Secret", "secret", name)
	emit(r.Recorder, p, corev1.EventTypeNormal, "SecretUpdated", "Update", "Updated credentials Secret %q", name)
	return nil
}

// stripTemplateMetadata removes the labels and annotations a template
// applied (per annotationApplied) from the currently rendered Secret, if this
// resource still controls it. It reports whether anything was removed.
func (r *AuthentikOAuthProviderReconciler) stripTemplateMetadata(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider) (bool, error) {
	name := p.Status.SecretName
	if name == "" {
		return false, nil
	}
	var s corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("get Secret %s/%s: %w", p.Namespace, name, err)
	case !metav1.IsControlledBy(&s, p):
		return false, nil
	}
	previous := appliedTo(&s)
	if len(previous.Labels) == 0 && len(previous.Annotations) == 0 {
		return false, nil
	}
	s.Labels = mergeMetadata(s.Labels, nil, previous.Labels, map[string]string{labelManagedBy: labelManagedByValue})
	s.Annotations = mergeMetadata(s.Annotations, nil, previous.Annotations,
		map[string]string{annotationProvider: p.Name, annotationApplied: "{}"})
	if err := r.Update(ctx, &s); err != nil {
		return false, fmt.Errorf("update Secret %s/%s: %w", p.Namespace, name, err)
	}
	emit(r.Recorder, p, corev1.EventTypeNormal, "SecretUpdated", "Update",
		"Removed template labels and annotations from credentials Secret %q", name)
	return true, nil
}

// secretWriteError classifies a failed Secret write. Rejections retrying
// cannot fix (invalid labels, oversized data, ...) are the user's; the API
// server's message describes the field problem and never echoes Secret
// data. Anything else is wrapped and retried.
func secretWriteError(verb, namespace, name string, err error) error {
	if apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err) {
		return condErrf(reasonSecretInvalid, "the API server rejected Secret %q: %w", name, err)
	}
	return fmt.Errorf("%s Secret %s/%s: %w", verb, namespace, name, err)
}

// mergeMetadata computes labels or annotations for an owned Secret: keys
// added by others are kept, keys this operator applied before but no longer
// wants are removed, desired template keys are set, and operator keys win.
// A key both a template and another controller set is removed with the
// template's key: the operator cannot tell the two writers apart.
func mergeMetadata(existing, desired map[string]string, previouslyApplied []string,
	operator map[string]string) map[string]string {
	out := maps.Clone(existing)
	if out == nil {
		out = map[string]string{}
	}
	for _, k := range previouslyApplied {
		if _, still := desired[k]; !still {
			delete(out, k)
		}
	}
	maps.Copy(out, desired)
	maps.Copy(out, operator)
	return out
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(m))
}

// deleteOwnedSecret deletes a previously rendered Secret if (and only if) it
// is still controlled by this resource. Preconditions make the delete fail
// if the Secret was replaced or changed after the ownership check.
func (r *AuthentikOAuthProviderReconciler) deleteOwnedSecret(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider, name string) error {
	var s corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("get Secret %s/%s: %w", p.Namespace, name, err)
	case !metav1.IsControlledBy(&s, p):
		return nil
	}
	uid, rv := s.UID, s.ResourceVersion
	err = r.Delete(ctx, &s, client.Preconditions{UID: &uid, ResourceVersion: &rv})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Secret %s/%s: %w", p.Namespace, name, err)
	}
	emit(r.Recorder, p, corev1.EventTypeNormal, "SecretDeleted", "Delete", "Deleted previous credentials Secret %q", name)
	return nil
}

// setCredentials records the CredentialsReady condition; with warn, a
// Warning event is emitted once per change.
func (r *AuthentikOAuthProviderReconciler) setCredentials(p *authentikiov1alpha1.AuthentikOAuthProvider,
	status metav1.ConditionStatus, reason, msg string, warn bool) {
	changed := meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: ConditionCredentialsReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
	if changed && warn {
		emit(r.Recorder, p, corev1.EventTypeWarning, reason, "Render", "%s", msg)
	}
}
