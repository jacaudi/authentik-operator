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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

var _ = Describe("AuthentikOAuthProvider credentials Secret", func() {
	const (
		namespace   = "default"
		externalURL = "https://sso.example.com"
	)
	ctx := context.Background()

	var (
		fake       *fakeauthentik.Server
		recorder   *events.FakeRecorder
		reconciler *AuthentikOAuthProviderReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		recorder = events.NewFakeRecorder(100)
		reconciler = &AuthentikOAuthProviderReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder, APIReader: k8sClient,
			Connections: &connection.Resolver{
				Reader:             k8sClient,
				Default:            &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1},
				DefaultExternalURL: externalURL,
			},
		}
	})
	AfterEach(func() { fake.Close() })

	newProvider := func(name string, secret *authentikiov1alpha1.CredentialsSecretSpec) *authentikiov1alpha1.AuthentikOAuthProvider {
		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://" + name + ".example.com/cb"}},
				Secret:            secret,
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() {
			var cur authentikiov1alpha1.AuthentikOAuthProvider
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &cur); err == nil {
				cur.Finalizers = nil
				_ = k8sClient.Update(ctx, &cur)
				_ = k8sClient.Delete(ctx, &cur)
			}
		})
		return p
	}

	reconcileP := func(p *authentikiov1alpha1.AuthentikOAuthProvider) {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		Expect(err).NotTo(HaveOccurred())
	}

	get := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *authentikiov1alpha1.AuthentikOAuthProvider {
		var cur authentikiov1alpha1.AuthentikOAuthProvider
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &cur)).To(Succeed())
		return &cur
	}

	ready := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *metav1.Condition {
		return meta.FindStatusCondition(get(p).Status.Conditions, ConditionReady)
	}

	secret := func(name string) (*corev1.Secret, error) {
		var s corev1.Secret
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &s)
		return &s, err
	}

	// attached creates the provider, syncs it, and attaches an application
	// with the given slug in Authentik.
	attached := func(name, slug string, spec *authentikiov1alpha1.CredentialsSecretSpec) *authentikiov1alpha1.AuthentikOAuthProvider {
		p := newProvider(name, spec)
		reconcileP(p)
		fake.SeedApplication(slug, get(p).Status.ProviderPK)
		reconcileP(p)
		return p
	}

	cleanupSecret := func(name string) {
		DeferCleanup(func() {
			if s, err := secret(name); err == nil {
				_ = k8sClient.Delete(ctx, s)
			}
		})
	}

	It("waits for an application before rendering", func() {
		p := newProvider("sec-wait", &authentikiov1alpha1.CredentialsSecretSpec{})
		reconcileP(p)
		Expect(ready(p).Reason).To(Equal(reasonWaitingForApplication))
		_, err := secret("sec-wait-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("renders the default keys with browser-facing URLs and owns the Secret", func() {
		cleanupSecret("sec-default-oauth")
		p := attached("sec-default", "grafana", &authentikiov1alpha1.CredentialsSecretSpec{
			Labels: map[string]string{"team": "obs"}, Annotations: map[string]string{"note": "x"},
		})
		cur := get(p)
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(cur.Status.SecretName).To(Equal("sec-default-oauth"))

		s, err := secret("sec-default-oauth")
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.IsControlledBy(s, cur)).To(BeTrue())
		Expect(s.Type).To(Equal(corev1.SecretTypeOpaque))
		Expect(s.Labels).To(HaveKeyWithValue("team", "obs"))
		Expect(s.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))
		Expect(s.Labels).To(HaveKeyWithValue(labelProvider, "sec-default"))
		Expect(s.Annotations).To(HaveKeyWithValue("note", "x"))
		Expect(s.Data).To(HaveLen(6))
		Expect(string(s.Data["client-id"])).To(Equal(cur.Status.ClientID))
		Expect(string(s.Data["client-secret"])).To(HavePrefix("generated-secret-"))
		Expect(string(s.Data["issuer-url"])).To(Equal(externalURL + "/application/o/grafana/"))
		Expect(string(s.Data["authorization-url"])).To(Equal(externalURL + "/application/o/authorize/"))
		Expect(string(s.Data["token-url"])).To(Equal(externalURL + "/application/o/token/"))
		Expect(string(s.Data["userinfo-url"])).To(Equal(externalURL + "/application/o/userinfo/"))

		By("not leaking the secret into status or events")
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring("generated-secret")))
	})

	It("renders a template with every variable", func() {
		cleanupSecret("grafana-oidc")
		p := attached("sec-tmpl", "grafana-t", &authentikiov1alpha1.CredentialsSecretSpec{
			Name: "grafana-oidc",
			Template: "ID: {{ .ClientID }}\nSECRET: {{ .ClientSecret }}\nISSUER: {{ .IssuerURL }}\n" +
				"AUTH: {{ .AuthURL }}\nTOKEN: {{ .TokenURL }}\nUSERINFO: {{ .UserInfoURL }}\n" +
				"LOGOUT: {{ .LogoutURL }}\nJWKS: {{ .JWKSURL }}\nINFO: {{ .ProviderInfoURL }}\n" +
				"SLUG: {{ .Slug }}\nNAME: {{ .Name }}\n",
		})
		s, err := secret("grafana-oidc")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(s.Data["LOGOUT"])).To(Equal(externalURL + "/application/o/grafana-t/end-session/"))
		Expect(string(s.Data["JWKS"])).To(Equal(externalURL + "/application/o/grafana-t/jwks/"))
		Expect(string(s.Data["INFO"])).To(Equal(externalURL + "/application/o/grafana-t/.well-known/openid-configuration"))
		Expect(string(s.Data["SLUG"])).To(Equal("grafana-t"))
		Expect(string(s.Data["NAME"])).To(Equal("grafana-t"))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports template errors once and writes nothing", func() {
		p := attached("sec-bad", "bad", &authentikiov1alpha1.CredentialsSecretSpec{Template: "A: {{ .NoSuchVar }}"})
		Expect(ready(p).Reason).To(Equal(reasonTemplateError))
		reconcileP(p)
		_, err := secret("sec-bad-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(drainEvents(recorder)).To(HaveExactElements(ContainSubstring("Created"), ContainSubstring(reasonTemplateError)))
	})

	It("never overwrites a Secret it does not own", func() {
		foreign := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: namespace},
			StringData: map[string]string{"password": "keep-me"},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		cleanupSecret("someone-elses")

		p := attached("sec-conflict", "conflict", &authentikiov1alpha1.CredentialsSecretSpec{Name: "someone-elses"})
		Expect(ready(p).Reason).To(Equal(reasonSecretConflict))
		s, err := secret("someone-elses")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Data).To(HaveLen(1))
		Expect(string(s.Data["password"])).To(Equal("keep-me"))
		Expect(s.Data).NotTo(HaveKey("client-id"))
	})

	It("does not rewrite an unchanged Secret, and repairs one edited out of band", func() {
		cleanupSecret("sec-stable-oauth")
		p := attached("sec-stable", "stable", &authentikiov1alpha1.CredentialsSecretSpec{})
		s, _ := secret("sec-stable-oauth")
		rv := s.ResourceVersion

		reconcileP(p)
		s, _ = secret("sec-stable-oauth")
		Expect(s.ResourceVersion).To(Equal(rv))

		s.Data["client-secret"] = []byte("tampered")
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
		reconcileP(p)
		s, _ = secret("sec-stable-oauth")
		Expect(string(s.Data["client-secret"])).To(HavePrefix("generated-secret-"))
	})

	It("deletes the old Secret on rename and the Secret when spec.secret is removed", func() {
		cleanupSecret("sec-rename-oauth")
		cleanupSecret("renamed")
		p := attached("sec-rename", "rename", &authentikiov1alpha1.CredentialsSecretSpec{})
		_, err := secret("sec-rename-oauth")
		Expect(err).NotTo(HaveOccurred())

		cur := get(p)
		cur.Spec.Secret.Name = "renamed"
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		reconcileP(p)
		_, err = secret("sec-rename-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = secret("renamed")
		Expect(err).NotTo(HaveOccurred())
		Expect(get(p).Status.SecretName).To(Equal("renamed"))

		cur = get(p)
		cur.Spec.Secret = nil
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		reconcileP(p)
		_, err = secret("renamed")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(get(p).Status.SecretName).To(BeEmpty())
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("uses Authentik's URLs unchanged when no external URL is configured", func() {
		cleanupSecret("sec-internal-oauth")
		reconciler.Connections.DefaultExternalURL = ""
		attached("sec-internal", "internal", &authentikiov1alpha1.CredentialsSecretSpec{})
		s, err := secret("sec-internal-oauth")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(s.Data["issuer-url"])).To(Equal(fake.URL + "/application/o/internal/"))
	})

	It("maps an application to the provider it references", func() {
		app := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: namespace},
			Spec:       authentikiov1alpha1.AuthentikApplicationSpec{ProviderRef: &authentikiov1alpha1.ProviderRef{Name: "p"}},
		}
		Expect(providerForApplication(ctx, app)).To(HaveExactElements(
			reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "p"}}))
		app.Spec.ProviderRef = nil
		Expect(providerForApplication(ctx, app)).To(BeEmpty())
	})
})
