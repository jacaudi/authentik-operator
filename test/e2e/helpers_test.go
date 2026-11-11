//go:build e2e

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

package e2e

import (
	"context"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/test/aktest"
)

const (
	condReady        = "Ready"
	flowAuthorize    = "default-provider-authorization-implicit-consent"
	flowInvalidation = "default-provider-invalidation-flow"
	pokeAnnotation   = "e2e.authentik.io/poke"
)

// newProvider returns a provider CR with the API server's defaults applied on
// create (scopes, grant types, validities, ...), as a user's manifest would.
func newProvider(name, redirect string) *authentikiov1alpha1.AuthentikOAuthProvider {
	return &authentikiov1alpha1.AuthentikOAuthProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appsNS},
		Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
			AuthorizationFlow: flowAuthorize,
			InvalidationFlow:  flowInvalidation,
			RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: redirect}},
		},
	}
}

// newApplication returns an application CR for the provider CR providerName.
func newApplication(name, display, slug, providerName string) *authentikiov1alpha1.AuthentikApplication {
	return &authentikiov1alpha1.AuthentikApplication{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appsNS},
		Spec: authentikiov1alpha1.AuthentikApplicationSpec{
			Name: display, Slug: slug,
			ProviderRef: &authentikiov1alpha1.ProviderRef{Kind: authentikiov1alpha1.ProviderKindOAuth, Name: providerName},
		},
	}
}

func key(name string) client.ObjectKey { return client.ObjectKey{Namespace: appsNS, Name: name} }

func getProvider(ctx context.Context, name string) *authentikiov1alpha1.AuthentikOAuthProvider {
	var p authentikiov1alpha1.AuthentikOAuthProvider
	ExpectWithOffset(1, k8s.Get(ctx, key(name), &p)).To(Succeed())
	return &p
}

func getApplication(ctx context.Context, name string) *authentikiov1alpha1.AuthentikApplication {
	var a authentikiov1alpha1.AuthentikApplication
	ExpectWithOffset(1, k8s.Get(ctx, key(name), &a)).To(Succeed())
	return &a
}

func condition(conds []metav1.Condition, t string) *metav1.Condition {
	return meta.FindStatusCondition(conds, t)
}

// providerReady polls until the provider's Ready condition has status/reason
// (an empty reason matches any).
func providerCondition(ctx context.Context, name string, status metav1.ConditionStatus, reason string) {
	EventuallyWithOffset(1, func(g Gomega) {
		var p authentikiov1alpha1.AuthentikOAuthProvider
		g.Expect(k8s.Get(ctx, key(name), &p)).To(Succeed())
		c := condition(p.Status.Conditions, condReady)
		g.Expect(c).NotTo(BeNil(), "no Ready condition yet")
		g.Expect(c.Status).To(Equal(status), "Ready=%s reason=%s: %s", c.Status, c.Reason, c.Message)
		if reason != "" {
			g.Expect(c.Reason).To(Equal(reason), c.Message)
		}
	}).WithContext(ctx).Should(Succeed())
}

func applicationCondition(ctx context.Context, name string, status metav1.ConditionStatus, reasons ...string) {
	EventuallyWithOffset(1, func(g Gomega) {
		var a authentikiov1alpha1.AuthentikApplication
		g.Expect(k8s.Get(ctx, key(name), &a)).To(Succeed())
		c := condition(a.Status.Conditions, condReady)
		g.Expect(c).NotTo(BeNil(), "no Ready condition yet")
		g.Expect(c.Status).To(Equal(status), "Ready=%s reason=%s: %s", c.Status, c.Reason, c.Message)
		if len(reasons) > 0 {
			g.Expect(reasons).To(ContainElement(c.Reason), c.Message)
		}
	}).WithContext(ctx).Should(Succeed())
}

// poke changes an annotation so the operator reconciles the object now
// instead of at its resync interval (five minutes when in sync).
func poke(ctx context.Context, obj client.Object) {
	base, ok := obj.DeepCopyObject().(client.Object)
	ExpectWithOffset(1, ok).To(BeTrue())
	a := obj.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[pokeAnnotation] = strconv.FormatInt(time.Now().UnixNano(), 10)
	obj.SetAnnotations(a)
	ExpectWithOffset(1, k8s.Patch(ctx, obj, client.MergeFrom(base))).To(Succeed())
}

func secretData(ctx context.Context, name string) map[string]string {
	var s corev1.Secret
	ExpectWithOffset(1, k8s.Get(ctx, key(name), &s)).To(Succeed())
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}

// eventuallySecret waits for the credentials Secret to exist and returns it.
func eventuallySecret(ctx context.Context, name string) map[string]string {
	var out map[string]string
	EventuallyWithOffset(1, func(g Gomega) {
		var s corev1.Secret
		g.Expect(k8s.Get(ctx, key(name), &s)).To(Succeed())
		out = map[string]string{}
		for k, v := range s.Data {
			out[k] = string(v)
		}
	}).WithContext(ctx).Should(Succeed())
	return out
}

func deleteAndWait(ctx context.Context, obj client.Object) {
	err := k8s.Delete(ctx, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}
	EventuallyWithOffset(1, func() bool {
		return apierrors.IsNotFound(k8s.Get(ctx, client.ObjectKeyFromObject(obj), obj))
	}).WithContext(ctx).Should(BeTrue(), "%s did not finish deleting", client.ObjectKeyFromObject(obj))
}

// creds are what a consuming application would read from the credentials Secret.
type creds struct{ ClientID, ClientSecret, Issuer, AuthURL, TokenURL, UserInfoURL string }

// login performs a full headless OIDC authorization-code login as the
// bootstrap user with the given credentials against the rendered endpoints.
func login(ctx context.Context, c creds, redirect string) (*aktest.OIDCResult, error) {
	return aktest.AuthorizationCodeLogin(ctx, aktest.OIDCParams{
		Transport: clusterTransport(),
		Issuer:    c.Issuer, AuthURL: c.AuthURL, TokenURL: c.TokenURL, UserInfoURL: c.UserInfoURL,
		ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		RedirectURI: redirect,
		Username:    bootstrapUser, Password: bootstrapPassword,
	})
}

// oauthWrites counts audit events for writes to providers and applications.
// Authentik records one per API save whether or not a field changed, so the
// count measures what the operator actually sent to the server.
func oauthWrites(ctx context.Context) int {
	ws, err := ak.ModelWrites(ctx, time.Time{}, "model_created", "model_updated", "model_deleted")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	n := 0
	for _, w := range ws {
		if strings.HasSuffix(w.Model, ".oauth2provider") || strings.HasSuffix(w.Model, ".application") {
			n++
		}
	}
	ExpectWithOffset(1, n).To(BeNumerically("<", 150), "audit event listing is paginated at 200 per action")
	return n
}
