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
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

var _ = Describe("AuthentikOAuthProvider Controller", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake       *fakeauthentik.Server
		recorder   *events.FakeRecorder
		reconciler *AuthentikOAuthProviderReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New("tok")
		recorder = events.NewFakeRecorder(100)
		reconciler = &AuthentikOAuthProviderReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: recorder,
			Connections: &connection.Resolver{
				Reader: k8sClient,
				// No retries: tests inject single failures and assert on them.
				Default: &authentik.Config{URL: fake.URL, Token: "tok", RPS: -1, MaxRetries: -1},
			},
		}
	})
	AfterEach(func() { fake.Close() })

	newProvider := func(name string) *authentikiov1alpha1.AuthentikOAuthProvider {
		return &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: "default-provider-authorization-implicit-consent",
				InvalidationFlow:  "default-provider-invalidation-flow",
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://app.example.com/callback"}},
			},
		}
	}

	create := func(p *authentikiov1alpha1.AuthentikOAuthProvider) {
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() {
			// Strip the finalizer so cleanup never depends on the fake.
			var cur authentikiov1alpha1.AuthentikOAuthProvider
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &cur); err == nil {
				cur.Finalizers = nil
				_ = k8sClient.Update(ctx, &cur)
				_ = k8sClient.Delete(ctx, &cur)
			}
		})
	}

	reconcileOnce := func(p *authentikiov1alpha1.AuthentikOAuthProvider) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	}

	get := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *authentikiov1alpha1.AuthentikOAuthProvider {
		var cur authentikiov1alpha1.AuthentikOAuthProvider
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &cur)).To(Succeed())
		return &cur
	}

	ready := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *metav1.Condition {
		return meta.FindStatusCondition(get(p).Status.Conditions, ConditionReady)
	}

	It("applies spec defaults at admission", func() {
		p := newProvider("defaults")
		create(p)
		s := get(p).Spec
		Expect(s.GrantTypes).To(ConsistOf(authentikiov1alpha1.OAuthGrantType("authorization_code"),
			authentikiov1alpha1.OAuthGrantType("refresh_token")))
		Expect(s.Scopes).To(ConsistOf("openid", "profile", "email"))
		Expect(s.ClientType).To(BeEquivalentTo("confidential"))
		Expect(s.SigningKey).To(Equal(fakeauthentik.SigningKeyName))
		Expect(s.RedirectURIs[0].MatchingMode).To(BeEquivalentTo("strict"))
		Expect(*s.IncludeClaimsInIDToken).To(BeTrue())
	})

	It("creates the provider with explicit grant types and records ownership", func() {
		p := newProvider("create")
		create(p)

		res, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(syncedRecheck))

		cur := get(p)
		Expect(cur.Finalizers).To(ContainElement(FinalizerName))
		Expect(cur.Status.ProviderPK).NotTo(BeZero())
		Expect(cur.Status.ClientID).To(HavePrefix("generated-client-"))
		Expect(cur.Status.ObservedGeneration).To(Equal(cur.Generation))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))

		got := fake.Provider(cur.Status.ProviderPK)
		Expect(got["name"]).To(Equal("create"))
		// JeffResc #279: grant_types must never be sent empty.
		Expect(got["grant_types"]).To(ConsistOf("authorization_code", "refresh_token"))
		Expect(got["property_mappings"]).To(ConsistOf(fakeauthentik.ScopeOpenIDPK, fakeauthentik.ScopeProfilePK,
			fakeauthentik.ScopeEmailPK))
		Expect(got["signing_key"]).To(Equal(fakeauthentik.SigningKeyPK))
		Expect(got["authorization_flow"]).To(Equal(fakeauthentik.FlowAuthorizationPK))
		Expect(got["redirect_uris"]).To(ConsistOf(SatisfyAll(
			HaveKeyWithValue("url", "https://app.example.com/callback"),
			HaveKeyWithValue("matching_mode", "strict"))))
		Expect(recorder.Events).To(Receive(ContainSubstring("Created")))
		Expect(cur.Status.ClientID).NotTo(ContainSubstring("secret"))
	})

	It("is idempotent: a second reconcile writes nothing", func() {
		p := newProvider("idempotent")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		writes := len(fake.Requests(true))

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Requests(true)).To(HaveLen(writes))
	})

	It("repairs drift by patching only the differing fields", func() {
		p := newProvider("drift")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		fake.SetProviderField(pk, "grant_types", []any{})
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())

		writes := fake.Requests(true)
		last := writes[len(writes)-1]
		Expect(last.Method).To(Equal(http.MethodPatch))
		Expect(last.Body).To(HaveLen(1))
		Expect(last.Body).To(HaveKey("grant_types"))
		Expect(fake.Provider(pk)["grant_types"]).To(ConsistOf("authorization_code", "refresh_token"))
	})

	It("applies spec changes, resolving names, UUIDs and optional fields", func() {
		p := newProvider("update")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		cur := get(p)
		cur.Spec.Name = "renamed"
		cur.Spec.RedirectURIs = append(cur.Spec.RedirectURIs,
			authentikiov1alpha1.RedirectURI{URL: "^https://.*\\.example\\.com/cb$", MatchingMode: "regex"})
		cur.Spec.PropertyMappings = []string{fakeauthentik.MappingCustomName}
		cur.Spec.EncryptionKey = fakeauthentik.EncryptionKeyName
		cur.Spec.AuthenticationFlow = "default-authentication-flow"
		cur.Spec.ClientType = "public"
		cur.Spec.GrantTypes = []authentikiov1alpha1.OAuthGrantType{"authorization_code"}
		cur.Spec.LogoutURI = "https://app.example.com/logout"
		cur.Spec.LogoutMethod = "frontchannel"
		cur.Spec.IncludeClaimsInIDToken = new(false)
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		got := fake.Provider(pk)
		Expect(got["name"]).To(Equal("renamed"))
		Expect(got["redirect_uris"]).To(HaveLen(2))
		Expect(got["property_mappings"]).To(ContainElement(fakeauthentik.MappingCustomPK))
		Expect(got["encryption_key"]).To(Equal(fakeauthentik.EncryptionKeyPK))
		Expect(got["authentication_flow"]).To(Equal(fakeauthentik.FlowAuthenticationPK))
		Expect(got["client_type"]).To(Equal("public"))
		Expect(got["grant_types"]).To(ConsistOf("authorization_code"))
		Expect(got["logout_method"]).To(Equal("frontchannel"))
		Expect(got["include_claims_in_id_token"]).To(BeFalse())
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))

		By("resolving a property mapping by UUID to the same object")
		cur = get(p)
		cur.Spec.PropertyMappings = []string{fakeauthentik.MappingCustomPK}
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		writes := len(fake.Requests(true))
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Requests(true)).To(HaveLen(writes))

		By("clearing the encryption key when removed from spec")
		cur = get(p)
		cur.Spec.EncryptionKey = ""
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)["encryption_key"]).To(BeNil())
	})

	It("never takes over a pre-existing provider with the same name", func() {
		foreign := fake.SeedProvider("taken")
		p := newProvider("taken")
		create(p)

		res, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(p).Reason).To(Equal(reasonConflict))
		Expect(get(p).Status.ProviderPK).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())
		Expect(fake.Provider(foreign)).NotTo(BeNil())
	})

	It("reports unresolvable references without creating anything", func() {
		p := newProvider("badref")
		p.Spec.AuthorizationFlow = "no-such-flow"
		create(p)

		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(p).Reason).To(Equal(reasonReferenceNotFound))
		Expect(ready(p).Message).To(ContainSubstring("no-such-flow"))
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("reports a rejected create as InvalidSpec without hot-looping", func() {
		p := newProvider("rejected")
		create(p)
		fake.FailNext(http.MethodPost, http.StatusBadRequest)

		res, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(p).Reason).To(Equal(reasonInvalidSpec))
	})

	It("backs off on Authentik server errors", func() {
		p := newProvider("servererr")
		create(p)
		fake.FailNext(http.MethodPost, http.StatusInternalServerError)

		_, err := reconcileOnce(p)
		Expect(err).To(HaveOccurred())
		Expect(ready(p).Reason).To(Equal(reasonAuthentikError))
	})

	It("recreates a provider deleted outside the operator", func() {
		p := newProvider("recreate")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		oldPK := get(p).Status.ProviderPK
		fake.DeleteProvider(oldPK)

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		newPK := get(p).Status.ProviderPK
		Expect(newPK).NotTo(Equal(oldPK))
		Expect(fake.Provider(newPK)).NotTo(BeNil())
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("deletes the provider it created when the resource is deleted", func() {
		p := newProvider("delete")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)).To(BeNil())
		err = k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &authentikiov1alpha1.AuthentikOAuthProvider{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("does not delete a provider it did not create", func() {
		foreign := fake.SeedProvider("not-mine")
		p := newProvider("not-mine")
		create(p)
		_, err := reconcileOnce(p) // Conflict; adds the finalizer only.
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(foreign)).NotTo(BeNil())
	})

	It("releases the finalizer and orphans the provider when the connection is gone", func() {
		p := newProvider("orphan")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		reconciler.Connections = &connection.Resolver{Reader: k8sClient} // no default connection
		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)).NotTo(BeNil())
		err = k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &authentikiov1alpha1.AuthentikOAuthProvider{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("keeps the finalizer when deletion in Authentik fails transiently", func() {
		p := newProvider("delete-retry")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		fake.FailNext(http.MethodDelete, http.StatusBadGateway)

		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).To(HaveOccurred())
		Expect(get(p).Finalizers).To(ContainElement(FinalizerName))
	})

	Context("admission validation", func() {
		It("rejects logoutMethod without logoutUri", func() {
			p := newProvider("invalid-logout")
			p.Spec.LogoutMethod = "backchannel"
			Expect(k8sClient.Create(ctx, p)).To(MatchError(ContainSubstring("logoutMethod requires logoutUri")))
		})
		It("rejects an unknown grant type", func() {
			p := newProvider("invalid-grant")
			p.Spec.GrantTypes = []authentikiov1alpha1.OAuthGrantType{"magic"}
			Expect(k8sClient.Create(ctx, p)).NotTo(Succeed())
		})
		It("rejects a malformed duration", func() {
			p := newProvider("invalid-duration")
			p.Spec.AccessTokenValidity = "5m"
			Expect(k8sClient.Create(ctx, p)).NotTo(Succeed())
		})
		It("rejects an empty redirect URI list", func() {
			p := newProvider("invalid-redirects")
			p.Spec.RedirectURIs = nil
			Expect(k8sClient.Create(ctx, p)).NotTo(Succeed())
		})
	})
})
