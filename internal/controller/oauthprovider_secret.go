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
	"fmt"
	"maps"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
	"github.com/jacaudi/authentik-operator/internal/credentials"
)

const (
	reasonWaitingForApplication = "WaitingForApplication"
	reasonTemplateError         = "TemplateError"
	reasonSecretConflict        = "SecretConflict"

	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelManagedByValue = "authentik-operator"
	labelProvider       = "authentik.io/provider"
)

// credentialsSecretName is the Secret this resource should render, or "".
func credentialsSecretName(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	if p.Spec.Secret == nil {
		return ""
	}
	return orDefault(p.Spec.Secret.Name, p.Name+"-oauth")
}

// syncSecret renders the credentials Secret after the provider itself is in
// sync. done is false when sync must stop here (the Ready condition has been
// set); err is non-nil only for transient failures.
//
// Invariants: the Secret is only ever written in the resource's own
// namespace, and an existing Secret not controlled by this resource is never
// modified or deleted (SecretConflict) -- so spec.secret.name cannot be used
// to overwrite another application's Secret.
func (r *AuthentikOAuthProviderReconciler) syncSecret(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider,
	resolved *connection.Resolved, got *api.OAuth2Provider) (ctrl.Result, bool, error) {
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}
	name := credentialsSecretName(p)

	if prev := p.Status.SecretName; prev != "" && prev != name {
		if err := r.deleteOwnedSecret(ctx, p, prev); err != nil {
			return ctrl.Result{}, false, err
		}
		p.Status.SecretName = ""
	}
	if name == "" {
		return ctrl.Result{}, true, nil
	}

	slug := nullable(got.AssignedApplicationSlug.Get())
	if slug == "" {
		r.setReady(p, metav1.ConditionFalse, reasonWaitingForApplication,
			"no AuthentikApplication uses this provider yet; the credentials Secret needs the application's slug")
		return retryLater, false, nil
	}

	urls, err := resolved.Client.OAuth2ProviderSetupURLs(ctx, got.Pk)
	if err != nil {
		res, err := r.authentikError(p, err)
		return res, false, err
	}
	rw := func(u string) string { return credentials.Rewrite(u, resolved.Client.BaseURL(), resolved.ExternalURL) }
	data, err := credentials.Render(p.Spec.Secret.Template, credentials.Vars{
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
	})
	if err != nil {
		if credentials.IsTemplateError(err) {
			if r.setReady(p, metav1.ConditionFalse, reasonTemplateError, err.Error()) {
				r.event(p, corev1.EventTypeWarning, reasonTemplateError, "Render", "%v", err)
			}
			return retryLater, false, nil
		}
		return ctrl.Result{}, false, err
	}

	if done, err := r.applySecret(ctx, p, name, data); !done || err != nil {
		return retryLater, false, err
	}
	p.Status.SecretName = name
	return ctrl.Result{}, true, nil
}

// applySecret creates or updates the owned Secret. done is false (with the
// Ready condition set) when an existing Secret belongs to someone else.
func (r *AuthentikOAuthProviderReconciler) applySecret(ctx context.Context, p *authentikiov1alpha1.AuthentikOAuthProvider,
	name string, data map[string][]byte) (bool, error) {
	labels := maps.Clone(p.Spec.Secret.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelManagedBy] = labelManagedByValue
	labels[labelProvider] = p.Name
	annotations := maps.Clone(p.Spec.Secret.Annotations)

	var s corev1.Secret
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		s = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: labels, Annotations: annotations},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}
		if err := controllerutil.SetControllerReference(p, &s, r.Scheme); err != nil {
			return false, err
		}
		if err := r.Create(ctx, &s); err != nil {
			return false, fmt.Errorf("create Secret %s/%s: %w", p.Namespace, name, err)
		}
		logf.FromContext(ctx).Info("Created credentials Secret", "secret", name)
		r.event(p, corev1.EventTypeNormal, "SecretCreated", "Create", "Created credentials Secret %q", name)
		return true, nil
	case err != nil:
		return false, fmt.Errorf("get Secret %s/%s: %w", p.Namespace, name, err)
	}

	if !metav1.IsControlledBy(&s, p) {
		r.setReady(p, metav1.ConditionFalse, reasonSecretConflict, fmt.Sprintf(
			"Secret %q already exists and is not managed by this resource; refusing to overwrite it. "+
				"Choose another spec.secret.name or delete that Secret", name))
		return false, nil
	}
	if equality.Semantic.DeepEqual(s.Data, data) && equality.Semantic.DeepEqual(s.Labels, labels) &&
		equality.Semantic.DeepEqual(nilIfEmpty(s.Annotations), nilIfEmpty(annotations)) {
		return true, nil // unchanged data is not rewritten
	}
	s.Data, s.Labels, s.Annotations = data, labels, annotations
	if err := r.Update(ctx, &s); err != nil {
		return false, fmt.Errorf("update Secret %s/%s: %w", p.Namespace, name, err)
	}
	logf.FromContext(ctx).Info("Updated credentials Secret", "secret", name)
	r.event(p, corev1.EventTypeNormal, "SecretUpdated", "Update", "Updated credentials Secret %q", name)
	return true, nil
}

// deleteOwnedSecret deletes a previously rendered Secret if (and only if) it
// is still controlled by this resource.
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
	if err := r.Delete(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Secret %s/%s: %w", p.Namespace, name, err)
	}
	r.event(p, corev1.EventTypeNormal, "SecretDeleted", "Delete", "Deleted previous credentials Secret %q", name)
	return nil
}

func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
