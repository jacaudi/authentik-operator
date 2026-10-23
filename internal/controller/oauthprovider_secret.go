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
	return orDefault(p.Spec.Secret.Name, p.Name+"-oauth")
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
func (r *AuthentikOAuthProviderReconciler) syncSecret(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider,
	resolved *connection.Resolved, got *api.OAuth2Provider) (requeue bool, err error) {
	name := credentialsSecretName(p)
	if name == "" {
		if prev := p.Status.SecretName; prev != "" {
			if err := r.deleteOwnedSecret(ctx, p, prev); err != nil {
				return false, err
			}
			p.Status.SecretName = ""
		}
		meta.RemoveStatusCondition(&p.Status.Conditions, ConditionCredentialsReady)
		return false, nil
	}

	tmpl, reason, msg, err := r.credentialsTemplate(ctx, p)
	if err != nil {
		return false, err
	}
	if reason != "" {
		// Access to the template is gone (or it is broken): what the
		// template granted -- its labels and annotations, which may opt the
		// Secret into replication or another controller -- is withdrawn.
		// The data, this provider's own credentials, is kept so workloads
		// keep working.
		stripped, err := r.stripTemplateMetadata(ctx, p)
		if err != nil {
			return false, err
		}
		if stripped {
			msg += fmt.Sprintf("; removed the template's labels and annotations from Secret %q (data kept)", p.Status.SecretName)
		}
		r.setCredentials(p, metav1.ConditionFalse, reason, msg, true)
		return true, nil
	}

	data, reason, msg, err := r.renderSecretData(ctx, resolved, got, tmpl)
	if err != nil {
		return false, err
	}
	if reason != "" {
		r.setCredentials(p, metav1.ConditionFalse, reason, msg, reason == reasonTemplateError)
		return true, nil
	}

	if reason, msg, err := r.applySecret(ctx, p, name, data, tmpl); err != nil || reason != "" {
		if err != nil {
			return false, err
		}
		r.setCredentials(p, metav1.ConditionFalse, reason, msg, true)
		return true, nil
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

// credentialsTemplate returns the template spec.secret.templateRef names,
// or an empty template (default keys, no labels or annotations) without
// one. A non-empty reason is a user-visible, non-transient problem; the
// previously rendered Secret is then left as it is.
func (r *AuthentikOAuthProviderReconciler) credentialsTemplate(ctx context.Context,
	p *authentikiov1alpha1.AuthentikOAuthProvider) (*authentikiov1alpha1.AuthentikCredentialsTemplateSpec, string, string, error) {
	ref := p.Spec.Secret.TemplateRef
	if ref == nil {
		return &authentikiov1alpha1.AuthentikCredentialsTemplateSpec{}, "", "", nil
	}
	var t authentikiov1alpha1.AuthentikCredentialsTemplate
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, &t); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, reasonTemplateNotFound, fmt.Sprintf("AuthentikCredentialsTemplate %q not found", ref.Name), nil
		}
		return nil, "", "", fmt.Errorf("get AuthentikCredentialsTemplate %s: %w", ref.Name, err)
	}
	sel, err := metav1.LabelSelectorAsSelector(&t.Spec.NamespaceSelector)
	if err != nil {
		return nil, reasonInvalidNamespaceSelector, fmt.Sprintf("AuthentikCredentialsTemplate %q has an invalid "+
			"namespaceSelector: %v", ref.Name, err), nil
	}
	if !sel.Empty() {
		var ns corev1.Namespace
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: p.Namespace}, &ns); err != nil {
			return nil, "", "", fmt.Errorf("get Namespace %s: %w", p.Namespace, err)
		}
		if !sel.Matches(labels.Set(ns.Labels)) {
			return nil, reasonTemplateNotAllowed, fmt.Sprintf("AuthentikCredentialsTemplate %q does not allow namespace %q "+
				"(its namespaceSelector does not match)", ref.Name, p.Namespace), nil
		}
	}
	return &t.Spec, "", "", nil
}

// renderSecretData computes the Secret data. A non-empty reason is a
// user-visible, non-transient problem.
func (r *AuthentikOAuthProviderReconciler) renderSecretData(ctx context.Context, resolved *connection.Resolved,
	got *api.OAuth2Provider, tmpl *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) (map[string][]byte, string, string, error) {
	slug := nullable(got.AssignedApplicationSlug.Get())
	if slug == "" {
		return nil, reasonWaitingForApplication,
			"no AuthentikApplication uses this provider yet; the credentials Secret needs the application's slug", nil
	}
	urls, err := resolved.Client.OAuth2ProviderSetupURLs(ctx, got.Pk)
	if err != nil {
		return nil, "", "", err
	}
	if urls.Issuer == "" || urls.Authorize == "" || urls.Token == "" {
		// The application was detached between the two reads.
		return nil, reasonWaitingForApplication, "Authentik returned no endpoint URLs for this provider yet", nil
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
		return nil, reasonExternalURLMismatch, fmt.Sprintf("cannot rewrite Authentik endpoint URLs to the external URL "+
			"(%v); they are built from the operator's API URL %s", rewriteErr, resolved.Client.BaseURL()), nil
	}
	data, err := credentials.Render(tmpl.Template, vars)
	if err != nil {
		if credentials.IsTemplateError(err) {
			return nil, reasonTemplateError, err.Error(), nil
		}
		return nil, "", "", err
	}
	return data, "", "", nil
}

// appliedMetadata is the content of annotationApplied.
type appliedMetadata struct {
	Labels      []string `json:"labels,omitempty"`
	Annotations []string `json:"annotations,omitempty"`
}

// applySecret creates or updates the owned Secret. A non-empty reason is a
// user-visible problem (Secret owned by someone else, rejected by the API).
func (r *AuthentikOAuthProviderReconciler) applySecret(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider,
	name string, data map[string][]byte, tmpl *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) (string, string, error) {
	tmplLabels, tmplAnnotations := tmpl.Labels, tmpl.Annotations
	applied := appliedMetadata{Labels: sortedKeys(tmplLabels), Annotations: sortedKeys(tmplAnnotations)}
	appliedJSON, err := json.Marshal(applied)
	if err != nil {
		return "", "", err
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
			return "", "", err
		}
		if err := r.Create(ctx, &s); err != nil {
			if reason, msg, ok := userWriteError(name, err); ok {
				return reason, msg, nil
			}
			// AlreadyExists (created concurrently by someone else) and
			// other errors retry; the next attempt sees the Secret and
			// applies the ownership check.
			return "", "", fmt.Errorf("create Secret %s/%s: %w", p.Namespace, name, err)
		}
		logf.FromContext(ctx).Info("Created credentials Secret", "secret", name)
		r.event(p, corev1.EventTypeNormal, "SecretCreated", "Create", "Created credentials Secret %q", name)
		return "", "", nil
	case err != nil:
		return "", "", fmt.Errorf("get Secret %s/%s: %w", p.Namespace, name, err)
	}

	if !metav1.IsControlledBy(&s, p) {
		return reasonSecretConflict, fmt.Sprintf("Secret %q already exists and is not managed by this resource; "+
			"refusing to overwrite it. Choose another spec.secret.name or delete that Secret", name), nil
	}

	var previous appliedMetadata
	_ = json.Unmarshal([]byte(s.Annotations[annotationApplied]), &previous)
	wantLabels := mergeMetadata(s.Labels, tmplLabels, previous.Labels, map[string]string{labelManagedBy: labelManagedByValue})
	wantAnnotations := mergeMetadata(s.Annotations, tmplAnnotations, previous.Annotations,
		map[string]string{annotationProvider: p.Name, annotationApplied: string(appliedJSON)})
	if equality.Semantic.DeepEqual(s.Data, data) && equality.Semantic.DeepEqual(s.Labels, wantLabels) &&
		equality.Semantic.DeepEqual(s.Annotations, wantAnnotations) {
		return "", "", nil // unchanged: not rewritten
	}
	s.Data, s.Labels, s.Annotations = data, wantLabels, wantAnnotations
	if err := r.Update(ctx, &s); err != nil {
		if reason, msg, ok := userWriteError(name, err); ok {
			return reason, msg, nil
		}
		return "", "", fmt.Errorf("update Secret %s/%s: %w", p.Namespace, name, err)
	}
	logf.FromContext(ctx).Info("Updated credentials Secret", "secret", name)
	r.event(p, corev1.EventTypeNormal, "SecretUpdated", "Update", "Updated credentials Secret %q", name)
	return "", "", nil
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
	var previous appliedMetadata
	_ = json.Unmarshal([]byte(s.Annotations[annotationApplied]), &previous)
	if len(previous.Labels) == 0 && len(previous.Annotations) == 0 {
		return false, nil
	}
	s.Labels = mergeMetadata(s.Labels, nil, previous.Labels, map[string]string{labelManagedBy: labelManagedByValue})
	s.Annotations = mergeMetadata(s.Annotations, nil, previous.Annotations,
		map[string]string{annotationProvider: p.Name, annotationApplied: "{}"})
	if err := r.Update(ctx, &s); err != nil {
		return false, fmt.Errorf("update Secret %s/%s: %w", p.Namespace, name, err)
	}
	r.event(p, corev1.EventTypeNormal, "SecretUpdated", "Update",
		"Removed template labels and annotations from credentials Secret %q", name)
	return true, nil
}

// userWriteError maps API rejections that retrying cannot fix (invalid
// labels, oversized data, ...) to a user-visible reason.
func userWriteError(name string, err error) (string, string, bool) {
	if apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err) {
		return reasonSecretInvalid, fmt.Sprintf("the API server rejected Secret %q: %s", name, apiErrorMessage(err)), true
	}
	return "", "", false
}

// apiErrorMessage returns the API server's message, which describes field
// problems; Secret data values are never echoed by the API server.
func apiErrorMessage(err error) string {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return st.Status().Message
	}
	return err.Error()
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
	if err := r.Delete(ctx, &s, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Secret %s/%s: %w", p.Namespace, name, err)
	}
	r.event(p, corev1.EventTypeNormal, "SecretDeleted", "Delete", "Deleted previous credentials Secret %q", name)
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
		r.event(p, corev1.EventTypeWarning, reason, "Render", "%s", msg)
	}
}
