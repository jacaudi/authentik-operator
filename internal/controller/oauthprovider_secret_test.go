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
	"strings"

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

const (
	teamObs   = "obs"
	labelTeam = "team"
	tmplAX    = "A: x"
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

	newProvider := func(name string,
		secret *authentikiov1alpha1.CredentialsSecretSpec) *authentikiov1alpha1.AuthentikOAuthProvider {
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

	creds := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *metav1.Condition {
		return meta.FindStatusCondition(get(p).Status.Conditions, ConditionCredentialsReady)
	}

	secret := func(name string) (*corev1.Secret, error) {
		var s corev1.Secret
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &s)
		return &s, err
	}

	// attached creates the provider, syncs it, and attaches an application
	// with the given slug in Authentik.
	attached := func(name, slug string,
		spec *authentikiov1alpha1.CredentialsSecretSpec) *authentikiov1alpha1.AuthentikOAuthProvider {
		p := newProvider(name, spec)
		reconcileP(p)
		fake.SeedApplication(slug, get(p).Status.ProviderPK)
		reconcileP(p)
		return p
	}

	// newTemplate creates a cluster-scoped credentials template and returns
	// a Secret spec referencing it.
	newTemplate := func(name string,
		spec authentikiov1alpha1.AuthentikCredentialsTemplateSpec) *authentikiov1alpha1.CredentialsSecretSpec {
		t := &authentikiov1alpha1.AuthentikCredentialsTemplate{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
		Expect(k8sClient.Create(ctx, t)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, t) })
		return &authentikiov1alpha1.CredentialsSecretSpec{
			TemplateRef: &authentikiov1alpha1.CredentialsTemplateRef{Name: name}}
	}

	updateTemplate := func(name string, mutate func(*authentikiov1alpha1.AuthentikCredentialsTemplateSpec)) {
		var t authentikiov1alpha1.AuthentikCredentialsTemplate
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, &t)).To(Succeed())
		mutate(&t.Spec)
		Expect(k8sClient.Update(ctx, &t)).To(Succeed())
	}

	cleanupSecret := func(name string) {
		DeferCleanup(func() {
			if s, err := secret(name); err == nil {
				_ = k8sClient.Delete(ctx, s)
			}
		})
	}

	It("waits for an application before rendering, while the provider itself is Ready", func() {
		p := newProvider("sec-wait", &authentikiov1alpha1.CredentialsSecretSpec{})
		reconcileP(p)
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(creds(p).Reason).To(Equal(reasonWaitingForApplication))
		_, err := secret("sec-wait-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("renders the default keys with browser-facing URLs and owns the Secret", func() {
		cleanupSecret("sec-default-oauth")
		p := attached("sec-default", "grafana", newTemplate("tmpl-meta", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			Labels: map[string]string{labelTeam: teamObs}, Annotations: map[string]string{"note": "x"},
		}))
		cur := get(p)
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(cur.Status.SecretName).To(Equal("sec-default-oauth"))

		s, err := secret("sec-default-oauth")
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.IsControlledBy(s, cur)).To(BeTrue())
		Expect(s.Type).To(Equal(corev1.SecretTypeOpaque))
		Expect(s.Labels).To(HaveKeyWithValue(labelTeam, teamObs))
		Expect(s.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))
		Expect(s.Annotations).To(HaveKeyWithValue(annotationProvider, "sec-default"))
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
		spec := newTemplate("tmpl-all", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			Template: "ID: {{ .ClientID }}\nSECRET: {{ .ClientSecret }}\nISSUER: {{ .IssuerURL }}\n" +
				"AUTH: {{ .AuthURL }}\nTOKEN: {{ .TokenURL }}\nUSERINFO: {{ .UserInfoURL }}\n" +
				"LOGOUT: {{ .LogoutURL }}\nJWKS: {{ .JWKSURL }}\nINFO: {{ .ProviderInfoURL }}\n" +
				"SLUG: {{ .Slug }}\nNAME: {{ .Name }}\n",
		})
		spec.Name = "grafana-oidc"
		p := attached("sec-tmpl", "grafana-t", spec)
		s, err := secret("grafana-oidc")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(s.Data["LOGOUT"])).To(Equal(externalURL + "/application/o/grafana-t/end-session/"))
		Expect(string(s.Data["JWKS"])).To(Equal(externalURL + "/application/o/grafana-t/jwks/"))
		Expect(string(s.Data["INFO"])).To(Equal(externalURL + "/application/o/grafana-t/.well-known/openid-configuration"))
		Expect(string(s.Data["SLUG"])).To(Equal("grafana-t"))
		Expect(string(s.Data["NAME"])).To(Equal("grafana-t"))
		Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports template errors once and writes nothing", func() {
		p := attached("sec-bad", "bad", newTemplate("tmpl-bad",
			authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Template: "A: {{ .NoSuchVar }}"}))
		Expect(creds(p).Reason).To(Equal(reasonTemplateError))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		reconcileP(p)
		_, err := secret("sec-bad-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(drainEvents(recorder)).To(HaveExactElements(ContainSubstring("Created"),
			ContainSubstring(reasonTemplateError)))
	})

	It("never overwrites a Secret it does not own", func() {
		foreign := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: namespace},
			StringData: map[string]string{"password": "keep-me"},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		cleanupSecret("someone-elses")

		p := attached("sec-conflict", "conflict", &authentikiov1alpha1.CredentialsSecretSpec{Name: "someone-elses"})
		Expect(creds(p).Reason).To(Equal(reasonSecretConflict))
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
		Expect(creds(p)).To(BeNil())
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

	It("does not deadlock with an AuthentikApplication resource (both reconcilers, real CRs)", func() {
		cleanupSecret("sec-crs-oauth")
		p := newProvider("sec-crs", &authentikiov1alpha1.CredentialsSecretSpec{})
		app := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "sec-crs-app", Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: "CRs",
				ProviderRef: &authentikiov1alpha1.ProviderRef{Name: "sec-crs"}},
		}
		Expect(k8sClient.Create(ctx, app)).To(Succeed())
		DeferCleanup(func() {
			var cur authentikiov1alpha1.AuthentikApplication
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(app), &cur); err == nil {
				cur.Finalizers = nil
				_ = k8sClient.Update(ctx, &cur)
				_ = k8sClient.Delete(ctx, &cur)
			}
		})
		apps := &AuthentikApplicationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(),
			Connections: reconciler.Connections}
		appReq := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(app)}

		// Order as the controllers would run: provider, app, provider again
		// (triggered by the application watch).
		reconcileP(p)
		_, err := apps.Reconcile(ctx, appReq)
		Expect(err).NotTo(HaveOccurred())
		reconcileP(p)

		var cur authentikiov1alpha1.AuthentikApplication
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(app), &cur)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(cur.Status.Conditions, ConditionReady)).To(BeTrue())
		Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))
		s, err := secret("sec-crs-oauth")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(s.Data["issuer-url"])).To(Equal(externalURL + "/application/o/sec-crs-app/"))
	})

	It("keeps the old Secret when a rename fails", func() {
		cleanupSecret("sec-keep-oauth")
		foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "taken-name", Namespace: namespace},
			StringData: map[string]string{"x": "y"}}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		cleanupSecret("taken-name")
		p := attached("sec-keep", "keep", &authentikiov1alpha1.CredentialsSecretSpec{})

		cur := get(p)
		cur.Spec.Secret.Name = "taken-name"
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		reconcileP(p)
		Expect(creds(p).Reason).To(Equal(reasonSecretConflict))
		_, err := secret("sec-keep-oauth")
		Expect(err).NotTo(HaveOccurred(), "old Secret must survive a failed rename")
		Expect(get(p).Status.SecretName).To(Equal("sec-keep-oauth"))
	})

	It("preserves labels added by others and removes labels dropped from the spec, without churn", func() {
		cleanupSecret("sec-labels-oauth")
		p := attached("sec-labels", "labels", newTemplate("tmpl-labels", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			Labels: map[string]string{labelTeam: teamObs, "tier": "1"}}))
		s, _ := secret("sec-labels-oauth")
		s.Labels["policy.example.com/cost-center"] = "42" // added by a mutating admission policy
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
		reconcileP(p)
		s, _ = secret("sec-labels-oauth")
		rv := s.ResourceVersion
		Expect(s.Labels).To(HaveKeyWithValue("policy.example.com/cost-center", "42"))
		reconcileP(p)
		s, _ = secret("sec-labels-oauth")
		Expect(s.ResourceVersion).To(Equal(rv), "no update loop with a foreign label")

		updateTemplate("tmpl-labels", func(t *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) {
			t.Labels = map[string]string{labelTeam: teamObs}
		})
		reconcileP(p)
		s, _ = secret("sec-labels-oauth")
		Expect(s.Labels).NotTo(HaveKey("tier"))
		Expect(s.Labels).To(HaveKeyWithValue(labelTeam, teamObs))
		Expect(s.Labels).To(HaveKeyWithValue("policy.example.com/cost-center", "42"))
	})

	It("reports a Secret the API server rejects as SecretInvalid instead of retrying forever", func() {
		p := attached("sec-invalid", "invalid", newTemplate("tmpl-invalid",
			authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Labels: map[string]string{"bad": "not a valid label value"}}))
		Expect(creds(p).Reason).To(Equal(reasonSecretInvalid))
	})

	It("renders for provider names longer than a label value allows", func() {
		long := "p-" + strings.Repeat("x", 80)
		cleanupSecret(long + "-oauth")
		p := attached(long, "long", &authentikiov1alpha1.CredentialsSecretSpec{})
		Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports endpoint URLs that cannot be rewritten instead of writing internal URLs", func() {
		fake.SetupURLHost = "authentik.internal:9000"
		p := attached("sec-mismatch", "mismatch", &authentikiov1alpha1.CredentialsSecretSpec{})
		Expect(creds(p).Reason).To(Equal(reasonExternalURLMismatch))
		Expect(creds(p).Message).To(ContainSubstring("authentik.internal:9000"))
		_, err := secret("sec-mismatch-oauth")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("uses the global issuer when issuerMode is global", func() {
		cleanupSecret("sec-global-oauth")
		p := newProvider("sec-global", &authentikiov1alpha1.CredentialsSecretSpec{})
		cur := get(p)
		cur.Spec.IssuerMode = "global"
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		reconcileP(p)
		fake.SeedApplication("global", get(p).Status.ProviderPK)
		reconcileP(p)
		s, err := secret("sec-global-oauth")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(s.Data["issuer-url"])).To(Equal(externalURL + "/"))
	})

	Context("admission validation", func() {
		It("rejects authentik.io label and annotation keys on templates, and allows others", func() {
			for _, spec := range []authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				{Labels: map[string]string{"authentik.io/connection": "true"}},
				{Annotations: map[string]string{"x.authentik.io/y": "x"}},
			} {
				t := &authentikiov1alpha1.AuthentikCredentialsTemplate{
					ObjectMeta: metav1.ObjectMeta{GenerateName: "reserved-"}, Spec: spec}
				Expect(k8sClient.Create(ctx, t)).To(MatchError(ContainSubstring("reserved")))
			}
			newTemplate("allowed-keys", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				Labels: map[string]string{"app.kubernetes.io/part-of": "x"}})
		})
		It("rejects a client ID that could inject Secret keys", func() {
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-client-id", Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
					AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
					InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
					RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://x.example.com/cb"}},
					ClientID:          "x\nEXTRA: y",
				},
			}
			Expect(k8sClient.Create(ctx, p)).To(MatchError(ContainSubstring("clientId")))
		})
		It("rejects a malformed Secret name", func() {
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-secret-name", Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
					AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
					InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
					RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://x.example.com/cb"}},
					Secret:            &authentikiov1alpha1.CredentialsSecretSpec{Name: "a..b"},
				},
			}
			Expect(k8sClient.Create(ctx, p)).NotTo(Succeed())
		})
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

	Context("templates", func() {
		It("reports a missing template and renders once it exists", func() {
			cleanupSecret("sec-late-oauth")
			spec := &authentikiov1alpha1.CredentialsSecretSpec{
				TemplateRef: &authentikiov1alpha1.CredentialsTemplateRef{Name: "tmpl-late"}}
			p := attached("sec-late", "late", spec)
			Expect(creds(p).Reason).To(Equal(reasonTemplateNotFound))
			Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
			_, err := secret("sec-late-oauth")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			newTemplate("tmpl-late", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Template: "KEY: {{ .Slug }}"})
			Expect(reconciler.providersForTemplate(ctx, &authentikiov1alpha1.AuthentikCredentialsTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "tmpl-late"}})).To(ContainElement(
				reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)}))
			reconcileP(p)
			s, err := secret("sec-late-oauth")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Data).To(HaveKeyWithValue("KEY", []byte("late")))
		})

		It("re-renders when the template changes", func() {
			cleanupSecret("sec-edit-oauth")
			p := attached("sec-edit", "edit", newTemplate("tmpl-edit",
				authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Template: "A: one"}))
			s, _ := secret("sec-edit-oauth")
			Expect(s.Data).To(HaveKeyWithValue("A", []byte("one")))
			updateTemplate("tmpl-edit", func(t *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) { t.Template = "B: two" })
			reconcileP(p)
			s, _ = secret("sec-edit-oauth")
			Expect(s.Data).To(Equal(map[string][]byte{"B": []byte("two")}))
		})

		It("enforces the template's namespaceSelector", func() {
			cleanupSecret("sec-ns-oauth")
			spec := newTemplate("tmpl-ns", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				Template:          tmplAX,
				Labels:            map[string]string{labelTeam: teamObs},
				NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: otherName}},
			})
			p := attached("sec-ns", "ns", spec)
			Expect(creds(p).Reason).To(Equal(reasonTemplateNotAllowed))
			_, err := secret("sec-ns-oauth")
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			updateTemplate("tmpl-ns", func(t *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) {
				t.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] = namespace
			})
			reconcileP(p)
			Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))

			s, _ := secret("sec-ns-oauth")
			Expect(s.Labels).To(HaveKeyWithValue(labelTeam, teamObs))

			By("withdrawing the template's labels, but keeping the data, when access is revoked")
			updateTemplate("tmpl-ns", func(t *authentikiov1alpha1.AuthentikCredentialsTemplateSpec) {
				t.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] = otherName
			})
			reconcileP(p)
			Expect(creds(p).Reason).To(Equal(reasonTemplateNotAllowed))
			Expect(creds(p).Message).To(ContainSubstring("removed the template's labels"))
			s, err = secret("sec-ns-oauth")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Labels).NotTo(HaveKey(labelTeam))
			Expect(s.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))
			Expect(s.Data).To(HaveKeyWithValue("A", []byte("x")))
			rv := s.ResourceVersion
			reconcileP(p)
			s, _ = secret("sec-ns-oauth")
			Expect(s.ResourceVersion).To(Equal(rv), "stripping is not repeated")
		})

		It("falls back to the default keys and drops template metadata when templateRef is removed", func() {
			cleanupSecret("sec-unref-oauth")
			p := attached("sec-unref", "unref", newTemplate("tmpl-unref", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				Template: tmplAX, Labels: map[string]string{labelTeam: teamObs}, Annotations: map[string]string{"note": "n"}}))
			cur := get(p)
			cur.Spec.Secret.TemplateRef = nil
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			reconcileP(p)
			s, _ := secret("sec-unref-oauth")
			Expect(s.Data).To(HaveKey("client-id"))
			Expect(s.Data).NotTo(HaveKey("A"))
			Expect(s.Labels).NotTo(HaveKey(labelTeam))
			Expect(s.Annotations).NotTo(HaveKey("note"))
			Expect(creds(p).Status).To(Equal(metav1.ConditionTrue))
		})

		It("switches templates, replacing their labels", func() {
			cleanupSecret("sec-switch-oauth")
			p := attached("sec-switch", "switch", newTemplate("tmpl-switch-a",
				authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Labels: map[string]string{"a": "1", "both": "a"}}))
			newTemplate("tmpl-switch-b",
				authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Labels: map[string]string{"b": "1", "both": "b"}})
			cur := get(p)
			cur.Spec.Secret.TemplateRef.Name = "tmpl-switch-b"
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			reconcileP(p)
			s, _ := secret("sec-switch-oauth")
			Expect(s.Labels).NotTo(HaveKey("a"))
			Expect(s.Labels).To(HaveKeyWithValue("b", "1"))
			Expect(s.Labels).To(HaveKeyWithValue("both", "b"))
		})

		It("keeps the data but drops template metadata when the template is deleted", func() {
			cleanupSecret("sec-gone-oauth")
			p := attached("sec-gone", "gone", newTemplate("tmpl-gone", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				Template: tmplAX, Labels: map[string]string{labelTeam: teamObs}}))
			Expect(k8sClient.Delete(ctx, &authentikiov1alpha1.AuthentikCredentialsTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "tmpl-gone"}})).To(Succeed())
			reconcileP(p)
			Expect(creds(p).Reason).To(Equal(reasonTemplateNotFound))
			s, err := secret("sec-gone-oauth")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Data).To(HaveKeyWithValue("A", []byte("x")))
			Expect(s.Labels).NotTo(HaveKey(labelTeam))
		})

		It("reports an invalid namespaceSelector on the provider", func() {
			p := attached("sec-badsel", "badsel", newTemplate("tmpl-badsel",
				authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
					NamespaceSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: labelTeam, Operator: metav1.LabelSelectorOpIn}}}}))
			Expect(creds(p).Reason).To(Equal(reasonInvalidNamespaceSelector))
		})

		It("only maps templates to the providers that reference them", func() {
			cleanupSecret("sec-map-a-oauth")
			cleanupSecret("sec-map-b-oauth")
			attached("sec-map-a", "map-a", newTemplate("tmpl-map", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{}))
			attached("sec-map-b", "map-b", &authentikiov1alpha1.CredentialsSecretSpec{})
			reqs := reconciler.providersForTemplate(ctx, &authentikiov1alpha1.AuthentikCredentialsTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "tmpl-map"}})
			Expect(reqs).To(HaveExactElements(
				reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "sec-map-a"}}))
		})
	})
})
