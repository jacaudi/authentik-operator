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
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	testAPIToken = "tok"
	jsonName     = "name"
	frontchannel = "frontchannel"
)

// drainEvents returns and clears every event recorded so far.
func drainEvents(r *events.FakeRecorder) []string {
	var out []string
	for len(r.Events) > 0 {
		out = append(out, <-r.Events)
	}
	return out
}

var _ = Describe("AuthentikOAuthProvider Controller", func() {
	const namespace = "default"
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
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: recorder,
			Connections: &connection.Resolver{
				Reader: k8sClient,
				// No retries: tests inject single failures and assert on them.
				Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1},
			},
		}
	})
	AfterEach(func() { fake.Close() })

	newProvider := func(name string) *authentikiov1alpha1.AuthentikOAuthProvider {
		return &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
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
		Expect(cur.Status.ClientID).To(Equal("k8s-" + string(cur.UID)))
		Expect(cur.Status.ObservedGeneration).To(Equal(cur.Generation))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))

		got := fake.Provider(cur.Status.ProviderPK)
		Expect(got[jsonName]).To(Equal("create"))
		// JeffResc #279: grant_types must never be sent empty.
		Expect(got["grant_types"]).To(ConsistOf("authorization_code", "refresh_token"))
		Expect(got["property_mappings"]).To(ConsistOf(fakeauthentik.ScopeOpenIDPK, fakeauthentik.ScopeProfilePK,
			fakeauthentik.ScopeEmailPK))
		Expect(got["signing_key"]).To(Equal(fakeauthentik.SigningKeyPK))
		Expect(got["authorization_flow"]).To(Equal(fakeauthentik.FlowAuthorizationPK))
		Expect(got["redirect_uris"]).To(ConsistOf(SatisfyAll(
			HaveKeyWithValue("url", "https://app.example.com/callback"),
			HaveKeyWithValue("matching_mode", "strict"))))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Created")))
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
		cur.Spec.AuthenticationFlow = fakeauthentik.FlowAuthenticationSlug
		cur.Spec.ClientType = "public"
		cur.Spec.GrantTypes = []authentikiov1alpha1.OAuthGrantType{"authorization_code"}
		cur.Spec.LogoutURI = "https://app.example.com/logout"
		cur.Spec.LogoutMethod = frontchannel
		cur.Spec.IncludeClaimsInIDToken = new(false)
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		got := fake.Provider(pk)
		Expect(got[jsonName]).To(Equal("renamed"))
		Expect(got["redirect_uris"]).To(HaveLen(2))
		Expect(got["property_mappings"]).To(ContainElement(fakeauthentik.MappingCustomPK))
		Expect(got["encryption_key"]).To(Equal(fakeauthentik.EncryptionKeyPK))
		Expect(got["authentication_flow"]).To(Equal(fakeauthentik.FlowAuthenticationPK))
		Expect(got["client_type"]).To(Equal("public"))
		Expect(got["grant_types"]).To(ConsistOf("authorization_code"))
		Expect(got["logout_method"]).To(Equal(frontchannel))
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

	// connectOnlyDeployment creates an opted-in token Secret and a
	// ConnectOnly AuthentikDeployment pointing at the fake.
	connectOnlyDeployment := func(name string) (*corev1.Secret, *authentikiov1alpha1.AuthentikDeployment) {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: namespace, Labels: map[string]string{
				authentikiov1alpha1.LabelConnection: authentikiov1alpha1.LabelConnectionValue}},
			StringData: map[string]string{"token": "tok"},
		}
		Expect(k8sClient.Create(ctx, sec)).To(Succeed())
		DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, sec)) })
		d := &authentikiov1alpha1.AuthentikDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikDeploymentSpec{
				Mode: authentikiov1alpha1.DeploymentModeConnectOnly,
				Connection: &authentikiov1alpha1.ConnectionSpec{
					URL:            fake.URL,
					TokenSecretRef: authentikiov1alpha1.SecretKeyRef{Name: sec.Name},
				},
			},
		}
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, d)) })
		return sec, d
	}

	It("still deletes the provider when the token Secret is gone (namespace teardown)", func() {
		sec, d := connectOnlyDeployment("teardown")
		p := newProvider("teardown")
		p.Spec.AuthentikRef = &authentikiov1alpha1.AuthentikRef{Name: d.Name}
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK
		Expect(pk).NotTo(BeZero())

		Expect(k8sClient.Delete(ctx, sec)).To(Succeed())
		Expect(k8sClient.Delete(ctx, d)).To(Succeed())
		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)).To(BeNil())
	})

	It("orphans the provider only when the connection is gone and nothing is cached", func() {
		_, d := connectOnlyDeployment("orphan")
		p := newProvider("orphan")
		p.Spec.AuthentikRef = &authentikiov1alpha1.AuthentikRef{Name: d.Name}
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		reconciler.Connections = &connection.Resolver{Reader: k8sClient} // e.g. after an operator restart
		Expect(k8sClient.Delete(ctx, d)).To(Succeed())
		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)).NotTo(BeNil())
		err = k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &authentikiov1alpha1.AuthentikOAuthProvider{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("blocks deletion and says why when the default connection is missing (fixable)", func() {
		p := newProvider("blocked")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK

		reconciler.Connections = &connection.Resolver{Reader: k8sClient}
		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).To(HaveOccurred())
		Expect(get(p).Finalizers).To(ContainElement(FinalizerName))
		Expect(ready(p).Reason).To(Equal(connection.ReasonNoConnection))
		Expect(fake.Provider(pk)).NotTo(BeNil())
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("DeletionBlocked")))
	})

	It("never modifies or deletes a provider whose pk now belongs to someone else", func() {
		p := newProvider("pk-reuse")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK
		// Simulate pointing at a different Authentik / restored DB where the
		// same pk is another application's provider.
		fake.SetProviderField(pk, "client_id", "someone-elses-client")
		fake.SetProviderField(pk, jsonName, "someone-elses-provider")
		writes := len(fake.Requests(true))

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(p).Reason).To(Equal(reasonConflict))
		Expect(fake.Requests(true)).To(HaveLen(writes))
		Expect(fake.Provider(pk)[jsonName]).To(Equal("someone-elses-provider"))

		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Provider(pk)).NotTo(BeNil())
	})

	It("recovers its own provider when a create's response was lost", func() {
		p := newProvider("lost-create")
		create(p)
		fake.LoseNextCreate()

		_, err := reconcileOnce(p)
		Expect(err).To(HaveOccurred())
		Expect(fake.ProviderCount()).To(Equal(1))

		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.ProviderCount()).To(Equal(1))
		Expect(get(p).Status.ProviderPK).NotTo(BeZero())
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("keeps an explicit empty scope list instead of re-defaulting it", func() {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "authentik.io/v1alpha1", "kind": "AuthentikOAuthProvider",
			"metadata": map[string]any{jsonName: "no-scopes", "namespace": namespace},
			"spec": map[string]any{
				"authorizationFlow": fakeauthentik.FlowAuthorizationSlug,
				"invalidationFlow":  fakeauthentik.FlowInvalidationSlug,
				"redirectUris":      []any{map[string]any{"url": "https://m2m.example.com/cb"}},
				"grantTypes":        []any{"client_credentials"},
				"scopes":            []any{},
			},
		}}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		p := &authentikiov1alpha1.AuthentikOAuthProvider{ObjectMeta: metav1.ObjectMeta{Name: "no-scopes", Namespace: namespace}}
		DeferCleanup(func() {
			cur := get(p)
			cur.Finalizers = nil
			_ = k8sClient.Update(ctx, cur)
			_ = k8sClient.Delete(ctx, cur)
		})

		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(p).Spec.Scopes).To(BeEmpty())
		Expect(fake.Provider(get(p).Status.ProviderPK)["property_mappings"]).To(BeEmpty())
	})

	It("resets refreshTokenThreshold and logoutMethod when removed from spec", func() {
		p := newProvider("reset")
		p.Spec.RefreshTokenThreshold = "days=1"
		p.Spec.LogoutURI = "https://app.example.com/logout"
		p.Spec.LogoutMethod = frontchannel
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		pk := get(p).Status.ProviderPK
		Expect(fake.Provider(pk)["logout_method"]).To(Equal(frontchannel))

		cur := get(p)
		cur.Spec.RefreshTokenThreshold = ""
		cur.Spec.LogoutURI = ""
		cur.Spec.LogoutMethod = ""
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		got := fake.Provider(pk)
		Expect(got["refresh_token_threshold"]).To(Equal("seconds=0"))
		Expect(got["logout_method"]).To(Equal("backchannel"))
		Expect(got["logout_uri"]).To(Equal(""))
	})

	It("uses a pinned client ID", func() {
		p := newProvider("pinned")
		p.Spec.ClientID = "grafana"
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(p).Status.ClientID).To(Equal("grafana"))
		Expect(fake.Provider(get(p).Status.ProviderPK)["client_id"]).To(Equal("grafana"))
	})

	It("reports a rename onto an existing provider name as Conflict", func() {
		fake.SeedProvider("occupied")
		p := newProvider("rename-collision")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())

		cur := get(p)
		cur.Spec.Name = "occupied"
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(p).Reason).To(Equal(reasonConflict))
	})

	It("reports a create race (name taken between lookup and create) as Conflict", func() {
		p := newProvider("race")
		create(p)
		fake.FailNextWith(http.MethodPost, http.StatusBadRequest,
			map[string]any{jsonName: []string{"provider with this name already exists."}})
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(p).Reason).To(Equal(reasonConflict))
	})

	It("emits InvalidSpec once, not on every retry", func() {
		p := newProvider("invalid-once")
		create(p)
		body := map[string]any{"redirect_uris": []string{"Enter a valid URL."}}
		fake.FailNextWith(http.MethodPost, http.StatusBadRequest, body)
		_, _ = reconcileOnce(p)
		fake.FailNextWith(http.MethodPost, http.StatusBadRequest, body)
		_, _ = reconcileOnce(p)
		Expect(drainEvents(recorder)).To(HaveExactElements(ContainSubstring(reasonInvalidSpec)))
	})

	It("never puts the client secret in events or status", func() {
		p := newProvider("no-secret-leak")
		create(p)
		_, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		fake.SetProviderField(get(p).Status.ProviderPK, "grant_types", []any{})
		_, err = reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring("generated-secret")))
		Expect(fmt.Sprintf("%+v", get(p).Status)).NotTo(ContainSubstring("generated-secret"))
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
		It("rejects frontchannel logout without logoutUri", func() {
			p := newProvider("invalid-logout")
			p.Spec.LogoutMethod = frontchannel
			Expect(k8sClient.Create(ctx, p)).To(MatchError(ContainSubstring("requires logoutUri")))
		})
		It("accepts Authentik duration forms: compound, fractional, sub-second", func() {
			p := newProvider("durations")
			p.Spec.AccessCodeValidity = "hours=1.5"
			p.Spec.AccessTokenValidity = "hours=1;minutes=30"
			p.Spec.RefreshTokenThreshold = "milliseconds=500"
			create(p)
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
