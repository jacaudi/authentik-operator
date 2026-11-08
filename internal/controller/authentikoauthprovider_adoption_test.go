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

var _ = Describe("AuthentikOAuthProvider adoption", func() {
	const (
		namespace    = "default"
		legacyID     = "legacy-client-id"
		legacySecret = "legacy-client-secret"
		legacyLogout = "https://legacy.example.com/logout"
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
				Reader:  k8sClient,
				Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1},
			},
		}
	})
	AfterEach(func() { fake.Close() })

	// seedLegacy inserts a provider as another operator would have made it,
	// with credentials and optional fields the spec below never sets.
	seedLegacy := func(name string) int32 {
		return fake.SeedProviderWith(map[string]any{
			"name": name, "client_id": legacyID, "client_secret": legacySecret,
			"logout_uri": legacyLogout, "authentication_flow": fakeauthentik.FlowAuthenticationPK,
			"encryption_key": fakeauthentik.EncryptionKeyPK,
		})
	}

	newProvider := func(name string, mutate func(*authentikiov1alpha1.AuthentikOAuthProviderSpec)) *authentikiov1alpha1.AuthentikOAuthProvider {
		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://" + name + ".example.com/cb"}},
			},
		}
		if mutate != nil {
			mutate(&p.Spec)
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

	adopting := func(match *authentikiov1alpha1.ProviderMatch, dryRun bool) func(*authentikiov1alpha1.AuthentikOAuthProviderSpec) {
		return func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.Adoption = authentikiov1alpha1.ProviderAdoptionSpec{
				APIAdoptionSpec: authentikiov1alpha1.APIAdoptionSpec{Enabled: true, DryRun: dryRun},
				Match:           match,
			}
		}
	}

	reconcileOnce := func(p *authentikiov1alpha1.AuthentikOAuthProvider) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	}
	mustReconcile := func(p *authentikiov1alpha1.AuthentikOAuthProvider) {
		_, err := reconcileOnce(p)
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
	gone := func(p *authentikiov1alpha1.AuthentikOAuthProvider) bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &authentikiov1alpha1.AuthentikOAuthProvider{})
		return apierrors.IsNotFound(err)
	}

	It("stays a Conflict without adoption and says how to enable it", func() {
		foreign := seedLegacy("plain")
		p := newProvider("plain", nil)

		res, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(p).Reason).To(Equal(reasonConflict))
		Expect(ready(p).Message).To(ContainSubstring("spec.adoption.enabled"))
		Expect(get(p).Status.Provenance).To(BeEmpty(), "never infer Created for a fresh CR that found an object")
		Expect(get(p).Status.ProviderPK).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())
		Expect(fake.Provider(foreign)).NotTo(BeNil())
	})

	It("records Created provenance for a provider it creates", func() {
		p := newProvider("fresh", nil)
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("adopts by the default name and keeps its client ID and secret", func() {
		pk := seedLegacy("by-default")
		p := newProvider("by-default", adopting(nil, false))

		mustReconcile(p)
		cur := get(p)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(cur.Status.ProviderPK).To(Equal(pk))
		Expect(cur.Status.ClientID).To(Equal(legacyID))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(fake.ProviderCount()).To(Equal(1))
		Expect(fake.Provider(pk)["client_id"]).To(Equal(legacyID))
		Expect(fake.Provider(pk)["client_secret"]).To(Equal(legacySecret))
	})

	It("matches by name, differing from the desired name, and leaves unset fields alone", func() {
		pk := seedLegacy("grafana-provider")
		p := newProvider("grafana", adopting(&authentikiov1alpha1.ProviderMatch{Name: "grafana-provider"}, false))

		mustReconcile(p)
		cur := get(p)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(cur.Status.ProviderPK).To(Equal(pk))
		Expect(fake.ProviderCount()).To(Equal(1))

		got := fake.Provider(pk)
		Expect(got["name"]).To(Equal("grafana-provider"), "spec.name is unset, so the name is not managed")
		Expect(got["logout_uri"]).To(Equal(legacyLogout))
		Expect(got["authentication_flow"]).To(Equal(fakeauthentik.FlowAuthenticationPK))
		Expect(got["encryption_key"]).To(Equal(fakeauthentik.EncryptionKeyPK))
		Expect(got["client_id"]).To(Equal(legacyID))
		Expect(got["client_secret"]).To(Equal(legacySecret))
		// The spec's redirect URI and flows are applied.
		Expect(got["redirect_uris"]).To(ConsistOf(HaveKeyWithValue("url", "https://grafana.example.com/cb")))

		writes := fake.Requests(true)
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Method).To(Equal(http.MethodPatch))
		for _, untouched := range []string{"client_id", "client_secret", "name", "logout_uri",
			"authentication_flow", "encryption_key"} {
			Expect(writes[0].Body).NotTo(HaveKey(untouched))
		}
	})

	It("renames an adopted provider only when spec.name is set", func() {
		pk := seedLegacy("old-name")
		p := newProvider("renamer", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.Name = "new-name"
			adopting(&authentikiov1alpha1.ProviderMatch{Name: "old-name"}, false)(s)
		})
		mustReconcile(p)
		Expect(fake.Provider(pk)["name"]).To(Equal("new-name"))
		Expect(fake.Provider(pk)["client_id"]).To(Equal(legacyID))
	})

	It("matches by pk", func() {
		seedLegacy("decoy")
		pk := seedLegacy("pk-target")
		p := newProvider("by-pk", adopting(&authentikiov1alpha1.ProviderMatch{PK: pk}, false))

		mustReconcile(p)
		Expect(get(p).Status.ProviderPK).To(Equal(pk))
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(fake.ProviderCount()).To(Equal(2))
	})

	It("rejects a match with both or neither of name and pk at admission", func() {
		for _, m := range []*authentikiov1alpha1.ProviderMatch{{Name: "x", PK: 3}, {}} {
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-match", Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
					AuthorizationFlow: "a", InvalidationFlow: "b",
					RedirectURIs: []authentikiov1alpha1.RedirectURI{{URL: "https://x.example.com/cb"}},
				},
			}
			adopting(m, false)(&p.Spec)
			err := k8sClient.Create(ctx, p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one of name or pk"))
		}
	})

	It("reports a missing explicit match instead of creating a provider", func() {
		p := newProvider("missing-target", adopting(&authentikiov1alpha1.ProviderMatch{Name: "nope"}, false))
		mustReconcile(p)
		Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ProviderCount()).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("creates normally when adoption is enabled without a match and nothing exists", func() {
		p := newProvider("nothing-yet", adopting(nil, false))
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ProviderCount()).To(Equal(1))
	})

	It("renders the credentials Secret from the existing client ID and secret, byte for byte", func() {
		pk := seedLegacy("secret-provider")
		fake.SeedApplication("secret-app", pk)
		p := newProvider("secret", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: "adopted-creds"}
			adopting(&authentikiov1alpha1.ProviderMatch{Name: "secret-provider"}, false)(s)
		})
		DeferCleanup(func() {
			_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "adopted-creds", Namespace: namespace}}))
		})

		mustReconcile(p)
		var s corev1.Secret
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "adopted-creds"}, &s)).To(Succeed())
		Expect(s.Data["client-id"]).To(Equal([]byte(legacyID)))
		Expect(s.Data["client-secret"]).To(Equal([]byte(legacySecret)))
		Expect(fake.Provider(pk)["client_secret"]).To(Equal(legacySecret))
	})

	It("is idempotent once adopted: a second reconcile writes nothing", func() {
		seedLegacy("steady")
		p := newProvider("steady", adopting(nil, false))
		mustReconcile(p)
		writes := len(fake.Requests(true))
		mustReconcile(p)
		Expect(fake.Requests(true)).To(HaveLen(writes))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("only sets the client ID on an adopted provider when spec.clientId is explicit", func() {
		pk := seedLegacy("explicit-id")
		p := newProvider("explicit-id", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.ClientID = "chosen-id"
			adopting(nil, false)(s)
		})
		mustReconcile(p)
		Expect(fake.Provider(pk)["client_id"]).To(Equal("chosen-id"))
		Expect(get(p).Status.ClientID).To(Equal("chosen-id"))
		Expect(fake.Provider(pk)["client_secret"]).To(Equal(legacySecret))
	})

	Context("dry run", func() {
		It("writes nothing to Authentik or the Secret and reports the diff", func() {
			pk := seedLegacy("dry-provider")
			fake.SeedApplication("dry-app", pk)
			p := newProvider("dry", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				s.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: "dry-creds"}
				adopting(&authentikiov1alpha1.ProviderMatch{Name: "dry-provider"}, true)(s)
			})

			res, err := reconcileOnce(p)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
			Expect(fake.Requests(true)).To(BeEmpty(), "dry run must not mutate Authentik")
			var s corev1.Secret
			err = k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "dry-creds"}, &s)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "dry run must not render the Secret")

			cond := ready(p)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonAdoptionDryRun))
			Expect(cond.Message).To(ContainSubstring("redirectUris"))
			Expect(cond.Message).NotTo(ContainSubstring(legacySecret))
			Expect(get(p).Status.Provenance).To(BeEmpty())
			Expect(get(p).Status.ProviderPK).To(BeZero())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("AdoptionDryRun")))

			By("turning dryRun off adopts for real")
			cur := get(p)
			cur.Spec.Adoption.DryRun = false
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			mustReconcile(p)
			Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
			Expect(fake.Provider(pk)["redirect_uris"]).To(ConsistOf(HaveKeyWithValue("url", "https://dry.example.com/cb")))
		})
	})

	Context("deletion policy", func() {
		deleteAndFinalize := func(p *authentikiov1alpha1.AuthentikOAuthProvider) {
			Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
			mustReconcile(p)
			Expect(gone(p)).To(BeTrue(), "the finalizer is released either way")
		}

		It("retains an adopted provider by default", func() {
			pk := seedLegacy("keep-me")
			p := newProvider("keep-me", adopting(nil, false))
			mustReconcile(p)
			deleteAndFinalize(p)
			Expect(fake.Provider(pk)).NotTo(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Retained")))
		})

		It("deletes a created provider by default", func() {
			p := newProvider("drop-me", nil)
			mustReconcile(p)
			pk := get(p).Status.ProviderPK
			deleteAndFinalize(p)
			Expect(fake.Provider(pk)).To(BeNil())
		})

		It("honours an explicit Retain on a created provider", func() {
			p := newProvider("explicit-retain", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			})
			mustReconcile(p)
			pk := get(p).Status.ProviderPK
			deleteAndFinalize(p)
			Expect(fake.Provider(pk)).NotTo(BeNil())
		})

		It("honours an explicit Delete on an adopted provider", func() {
			pk := seedLegacy("explicit-delete")
			p := newProvider("explicit-delete", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
				adopting(nil, false)(s)
			})
			mustReconcile(p)
			deleteAndFinalize(p)
			Expect(fake.Provider(pk)).To(BeNil())
		})
	})

	Context("pendingCreate recovery", func() {
		It("classifies a recovered own create as Created even with adoption enabled", func() {
			p := newProvider("lost", adopting(nil, false))
			fake.LoseNextCreate()
			_, err := reconcileOnce(p)
			Expect(err).To(HaveOccurred())
			Expect(fake.ProviderCount()).To(Equal(1))

			mustReconcile(p)
			Expect(fake.ProviderCount()).To(Equal(1))
			Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			Expect(get(p).Status.PendingCreate).To(BeEmpty())
		})

		It("classifies an existing foreign provider as Adopted, not Created, even with a stale marker", func() {
			seedLegacy("stale")
			p := newProvider("stale", adopting(nil, false))
			cur := get(p)
			cur.Status.PendingCreate = "stale" // marker, but the client ID is not ours
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())

			mustReconcile(p)
			Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(get(p).Status.ClientID).To(Equal(legacyID))
		})
	})
})
