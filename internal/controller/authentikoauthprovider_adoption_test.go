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
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	jsonSigningKey = "signing_key"
	otherName      = "elsewhere"

	msgDryRunPolicy = "dryRun requires managementPolicy"
	roCreds         = "ro-creds"
)

var _ = Describe("AuthentikOAuthProvider adoption", func() {
	const (
		namespace    = "default"
		callbackURL  = "https://x.example.com/cb"
		adoptedCreds = "adopted-creds"
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
			jsonName: name, "client_id": legacyID, "client_secret": legacySecret,
			"logout_uri": legacyLogout, "authentication_flow": fakeauthentik.FlowAuthenticationPK,
			"encryption_key": fakeauthentik.EncryptionKeyPK,
			// Differ from the defaulted spec (hashed_user_id, the three
			// scope mappings) so adoption visibly rewrites them; the
			// signing key is an unrelated cert that must survive.
			"sub_mode": "user_email", "property_mappings": []any{fakeauthentik.MappingCustomPK},
			jsonSigningKey: fakeauthentik.EncryptionKeyPK,
		})
	}

	type specMutator = func(*authentikiov1alpha1.AuthentikOAuthProviderSpec)

	newProvider := func(name string, mutate specMutator) *authentikiov1alpha1.AuthentikOAuthProvider {
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

	managing := func(policy authentikiov1alpha1.ManagementPolicy, match *authentikiov1alpha1.ProviderMatch,
		dryRun bool) specMutator {
		return func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.ManagementPolicy, s.Match, s.DryRun = policy, match, dryRun
		}
	}
	adopting := func(match *authentikiov1alpha1.ProviderMatch, dryRun bool) specMutator {
		return managing(authentikiov1alpha1.ManagementPolicyAdopt, match, dryRun)
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

	It("stays a Conflict under the Create policy and says how to adopt", func() {
		foreign := seedLegacy("plain")
		p := newProvider("plain", nil)

		res, err := reconcileOnce(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(p).Reason).To(Equal(reasonConflict))
		Expect(ready(p).Message).To(ContainSubstring("spec.managementPolicy"))
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
		Expect(got[jsonSigningKey]).To(Equal(fakeauthentik.EncryptionKeyPK), "an unset signingKey keeps the existing one")
		// The spec's redirect URI and flows are applied.
		Expect(got["redirect_uris"]).To(ConsistOf(HaveKeyWithValue("url", "https://grafana.example.com/cb")))

		writes := fake.Requests(true)
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Method).To(Equal(http.MethodPatch))
		for _, untouched := range []string{"client_id", "client_secret", "name", "logout_uri",
			"authentication_flow", "encryption_key", jsonSigningKey} {
			Expect(writes[0].Body).NotTo(HaveKey(untouched))
		}
		// Defaulted fields differ from the existing values, so they ARE written.
		Expect(writes[0].Body).To(HaveKeyWithValue("sub_mode", "hashed_user_id"))
		Expect(writes[0].Body).To(HaveKey("property_mappings"))
		Expect(got["sub_mode"]).To(Equal("hashed_user_id"))
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

	It("rejects an invalid combination of management fields at admission", func() {
		cases := []struct {
			name    string
			mutate  specMutator
			wantMsg string
		}{
			{"match with both name and pk", adopting(&authentikiov1alpha1.ProviderMatch{Name: "x", PK: 3}, false),
				"exactly one of name or pk"},
			{"match with neither name nor pk", adopting(&authentikiov1alpha1.ProviderMatch{}, false),
				"exactly one of name or pk"},
			{"match under the default Create policy", managing("", &authentikiov1alpha1.ProviderMatch{PK: 3}, false),
				"match requires managementPolicy"},
			{"match under an explicit Create policy",
				managing(authentikiov1alpha1.ManagementPolicyCreate, &authentikiov1alpha1.ProviderMatch{PK: 3}, false),
				"match requires managementPolicy"},
			{"dryRun under Create", managing(authentikiov1alpha1.ManagementPolicyCreate, nil, true),
				msgDryRunPolicy},
			{"dryRun under ReadOnly", managing(authentikiov1alpha1.ManagementPolicyReadOnly, nil, true),
				msgDryRunPolicy},
			{"deletionPolicy Delete under ReadOnly", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				managing(authentikiov1alpha1.ManagementPolicyReadOnly, nil, false)(s)
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			}, "ReadOnly never deletes"},
			{"unknown policy", managing("Observe", nil, false), "Unsupported value"},
		}
		for _, c := range cases {
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-management", Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
					AuthorizationFlow: "a", InvalidationFlow: "b",
					RedirectURIs: []authentikiov1alpha1.RedirectURI{{URL: callbackURL}},
				},
			}
			c.mutate(&p.Spec)
			err := k8sClient.Create(ctx, p)
			Expect(err).To(HaveOccurred(), c.name)
			Expect(err.Error()).To(ContainSubstring(c.wantMsg), c.name)
		}
	})

	It("accepts every valid management combination at admission", func() {
		valid := []specMutator{
			managing(authentikiov1alpha1.ManagementPolicyAdopt, nil, true),
			managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate, &authentikiov1alpha1.ProviderMatch{PK: 3}, true),
			managing(authentikiov1alpha1.ManagementPolicyReadOnly, &authentikiov1alpha1.ProviderMatch{Name: "x"}, false),
			func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				managing(authentikiov1alpha1.ManagementPolicyReadOnly, nil, false)(s)
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			},
		}
		for i, mutate := range valid {
			newProvider(fmt.Sprintf("valid-management-%d", i), mutate)
		}
	})

	It("defaults managementPolicy to Create at admission", func() {
		p := newProvider("default-policy", nil)
		Expect(get(p).Spec.ManagementPolicy).To(Equal(authentikiov1alpha1.ManagementPolicyCreate))
	})

	It("reports a missing explicit match instead of creating a provider", func() {
		p := newProvider("missing-target", adopting(&authentikiov1alpha1.ProviderMatch{Name: "nope"}, false))
		mustReconcile(p)
		Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ProviderCount()).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("does not create under Adopt when nothing matches, but does once switched to AdoptOrCreate", func() {
		p := newProvider("nothing-yet", adopting(nil, false))
		mustReconcile(p)
		Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ProviderCount()).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())

		cur := get(p)
		cur.Spec.ManagementPolicy = authentikiov1alpha1.ManagementPolicyAdoptOrCreate
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ProviderCount()).To(Equal(1))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("nothing was found to adopt")))
	})

	It("adopts when AdoptOrCreate finds the provider, without creating a second one", func() {
		pk := seedLegacy("either-way")
		p := newProvider("either-way", managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate, nil, false))
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(get(p).Status.ProviderPK).To(Equal(pk))
		Expect(fake.ProviderCount()).To(Equal(1))
	})

	It("creates under AdoptOrCreate when an explicit match finds nothing", func() {
		p := newProvider("typo", managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate,
			&authentikiov1alpha1.ProviderMatch{Name: "typo-provider"}, false))
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ProviderCount()).To(Equal(1))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("nothing was found to adopt")))
	})

	It("creates with the default signing key when signingKey is unset", func() {
		p := newProvider("default-key", nil)
		mustReconcile(p)
		Expect(fake.Provider(get(p).Status.ProviderPK)[jsonSigningKey]).To(Equal(fakeauthentik.SigningKeyPK))
	})

	It("refuses to adopt a provider another resource already tracks", func() {
		pk := seedLegacy("shared")
		first := newProvider("first", adopting(&authentikiov1alpha1.ProviderMatch{PK: pk}, false))
		mustReconcile(first)
		Expect(get(first).Status.ProviderPK).To(Equal(pk))

		second := newProvider("second", adopting(&authentikiov1alpha1.ProviderMatch{PK: pk}, false))
		mustReconcile(second)
		Expect(ready(second).Reason).To(Equal(reasonConflict))
		Expect(ready(second).Message).To(ContainSubstring("first"))
		Expect(get(second).Status.ProviderPK).To(BeZero())
		Expect(get(second).Status.Provenance).To(BeEmpty())
	})

	It("never recreates an adopted provider that vanished, and keeps its pk", func() {
		pk := seedLegacy("vanish")
		p := newProvider("vanish", adopting(nil, false))
		mustReconcile(p)
		fake.DeleteProvider(pk)
		before := len(fake.Requests(true))

		mustReconcile(p)
		Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ProviderCount()).To(BeZero())
		Expect(fake.Requests(true)).To(HaveLen(before))
		Expect(get(p).Status.ProviderPK).To(Equal(pk))
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
	})

	It("keeps a null signing key and then settles", func() {
		pk := fake.SeedProviderWith(map[string]any{jsonName: "no-key", jsonSigningKey: nil})
		p := newProvider("no-key", adopting(nil, false))
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		writes := fake.Requests(true)
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Body).NotTo(HaveKey(jsonSigningKey))
		Expect(fake.Provider(pk)[jsonSigningKey]).To(BeNil())

		mustReconcile(p)
		Expect(fake.Requests(true)).To(HaveLen(1))
	})

	It("adopts without resolving the default signing key, which it will not use", func() {
		fake.DeleteCertificate(fakeauthentik.SigningKeyName)
		seedLegacy("no-default-cert")
		p := newProvider("no-default-cert", adopting(nil, false))
		mustReconcile(p)
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
	})

	It("recreates a provider this resource created when it vanishes, whatever match and policy say", func() {
		p := newProvider("was-created", managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate, nil, false))
		mustReconcile(p)
		pk := get(p).Status.ProviderPK
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		fake.DeleteProvider(pk)

		cur := get(p)
		cur.Spec.ManagementPolicy = authentikiov1alpha1.ManagementPolicyAdopt
		cur.Spec.Match = &authentikiov1alpha1.ProviderMatch{Name: otherName}
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		mustReconcile(p)
		Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(get(p).Status.ProviderPK).NotTo(Equal(pk))
		Expect(fake.ProviderCount()).To(Equal(1))
	})

	It("does not recreate over a provider that took the vanished one's name; that is a Conflict", func() {
		p := newProvider("name-taken", nil)
		mustReconcile(p)
		fake.DeleteProvider(get(p).Status.ProviderPK)
		seedLegacy("name-taken")

		mustReconcile(p)
		Expect(ready(p).Reason).To(Equal(reasonConflict))
		Expect(fake.ProviderCount()).To(Equal(1))
	})

	It("yields to the lower-UID resource when two track one provider", func() {
		pk := seedLegacy("raced")
		a := newProvider("race-a", adopting(&authentikiov1alpha1.ProviderMatch{PK: pk}, false))
		b := newProvider("race-b", adopting(&authentikiov1alpha1.ProviderMatch{PK: pk}, false))
		for _, p := range []*authentikiov1alpha1.AuthentikOAuthProvider{a, b} {
			cur := get(p)
			cur.Status.Provenance = authentikiov1alpha1.ProvenanceAdopted
			cur.Status.ProviderPK = pk
			cur.Status.ClientID = legacyID
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
		}
		low, high := a, b
		if get(a).UID > get(b).UID {
			low, high = b, a
		}
		mustReconcile(high)
		Expect(ready(high).Reason).To(Equal(reasonConflict))
		Expect(get(high).Status.ProviderPK).To(BeZero())
		Expect(get(high).Status.Provenance).To(BeEmpty())

		mustReconcile(low)
		Expect(ready(low).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(low).Status.ProviderPK).To(Equal(pk))
	})

	It("never deletes a provider whose provenance is undecided", func() {
		pk := seedLegacy("unknown")
		p := newProvider("unknown", nil)
		mustReconcile(p) // Conflict: adds the finalizer
		cur := get(p)
		cur.Status.ProviderPK = pk
		cur.Status.ClientID = legacyID
		Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
		drainEvents(recorder)

		Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
		mustReconcile(p)
		Expect(gone(p)).To(BeTrue())
		Expect(fake.Provider(pk)).NotTo(BeNil())
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("provenance unknown")))
	})

	It("renders the credentials Secret from the existing client ID and secret, byte for byte", func() {
		pk := seedLegacy("secret-provider")
		fake.SeedApplication("secret-app", pk)
		p := newProvider("secret", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
			s.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: adoptedCreds}
			adopting(&authentikiov1alpha1.ProviderMatch{Name: "secret-provider"}, false)(s)
		})
		DeferCleanup(func() {
			_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: adoptedCreds, Namespace: namespace}}))
		})

		mustReconcile(p)
		var s corev1.Secret
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: adoptedCreds}, &s)).To(Succeed())
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
			Expect(cond.Message).To(ContainSubstring("redirect_uris"))
			Expect(cond.Message).To(ContainSubstring("sub_mode: "))
			Expect(cond.Message).To(ContainSubstring("user_email"))
			Expect(cond.Message).To(ContainSubstring("hashed_user_id"))
			Expect(cond.Message).NotTo(ContainSubstring("client_secret"))
			Expect(cond.Message).NotTo(ContainSubstring(jsonSigningKey))
			Expect(cond.Message).NotTo(ContainSubstring(legacySecret))
			Expect(get(p).Status.Provenance).To(BeEmpty())
			Expect(get(p).Status.ProviderPK).To(BeZero())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("AdoptionDryRun")))

			By("turning dryRun off adopts for real")
			cur := get(p)
			cur.Spec.DryRun = false
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			mustReconcile(p)
			Expect(get(p).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
			Expect(fake.Provider(pk)["redirect_uris"]).To(ConsistOf(HaveKeyWithValue("url", "https://dry.example.com/cb")))
		})
	})

	Context("dry run with nothing to adopt", func() {
		It("creates nothing and reports a would-create under AdoptOrCreate", func() {
			p := newProvider("dry-miss", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate, nil, true)(s)
				s.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: "dry-miss-creds"}
			})
			mustReconcile(p)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(p).Reason).To(Equal(reasonAdoptionDryRun))
			Expect(ready(p).Message).To(ContainSubstring("a new one would be created"))
			var s corev1.Secret
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "dry-miss-creds"}, &s)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("reports AdoptionTargetNotFound under Adopt", func() {
			p := newProvider("dry-miss2", adopting(nil, true))
			mustReconcile(p)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
		})
	})

	Context("read only", func() {
		readOnly := func(match *authentikiov1alpha1.ProviderMatch) specMutator {
			return managing(authentikiov1alpha1.ManagementPolicyReadOnly, match, false)
		}
		drifted := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *metav1.Condition {
			return meta.FindStatusCondition(get(p).Status.Conditions, ConditionDrifted)
		}

		It("finds the provider, claims it and renders the Secret, but never writes to Authentik", func() {
			pk := seedLegacy("ro-provider")
			fake.SeedApplication("ro-app", pk)
			p := newProvider("ro", func(s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
				s.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: roCreds}
				readOnly(&authentikiov1alpha1.ProviderMatch{Name: "ro-provider"})(s)
			})
			DeferCleanup(func() {
				_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: roCreds, Namespace: namespace}}))
			})

			mustReconcile(p)
			Expect(fake.Requests(true)).To(BeEmpty(), "read only must never write to Authentik")
			cur := get(p)
			Expect(cur.Status.ProviderPK).To(Equal(pk))
			Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(cur.Status.ClientID).To(Equal(legacyID))
			Expect(ready(p).Status).To(Equal(metav1.ConditionTrue))
			Expect(ready(p).Reason).To(Equal(reasonReadOnly))
			var s corev1.Secret
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: roCreds}, &s)).To(Succeed())
			Expect(s.Data["client-id"]).To(Equal([]byte(legacyID)))
			Expect(s.Data["client-secret"]).To(Equal([]byte(legacySecret)))
		})

		It("reports drift in the Drifted condition, naming the fields and hiding secrets", func() {
			seedLegacy("drift-provider")
			p := newProvider("drift", readOnly(&authentikiov1alpha1.ProviderMatch{Name: "drift-provider"}))
			mustReconcile(p)

			cond := drifted(p)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Message).To(ContainSubstring("sub_mode: "))
			Expect(cond.Message).To(ContainSubstring("user_email"))
			Expect(cond.Message).To(ContainSubstring("redirect_uris"))
			Expect(cond.Message).NotTo(ContainSubstring(legacySecret))
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("reports no drift once the live provider matches the spec", func() {
			pk := seedLegacy("match-provider")
			p := newProvider("match", readOnly(&authentikiov1alpha1.ProviderMatch{Name: "match-provider"}))
			mustReconcile(p)
			Expect(drifted(p).Status).To(Equal(metav1.ConditionTrue))

			By("the live provider is brought in line out of band")
			fake.SetProviderField(pk, "sub_mode", "hashed_user_id")
			fake.SetProviderField(pk, "grant_types", []any{"authorization_code", "refresh_token"})
			fake.SetProviderField(pk, "property_mappings", []any{fakeauthentik.ScopeOpenIDPK,
				fakeauthentik.ScopeProfilePK, fakeauthentik.ScopeEmailPK})
			fake.SetProviderField(pk, "redirect_uris", []any{map[string]any{
				"matching_mode": "strict", "url": "https://match.example.com/cb", "redirect_uri_type": "authorization"}})
			mustReconcile(p)
			Expect(drifted(p).Status).To(Equal(metav1.ConditionFalse))
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("reports a miss as AdoptionTargetNotFound and never creates", func() {
			p := newProvider("ro-miss", readOnly(nil))
			mustReconcile(p)
			Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
			Expect(fake.ProviderCount()).To(BeZero())
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("never recreates the provider when it vanishes", func() {
			pk := seedLegacy("ro-vanish")
			p := newProvider("ro-vanish", readOnly(nil))
			mustReconcile(p)
			fake.DeleteProvider(pk)

			mustReconcile(p)
			Expect(ready(p).Reason).To(Equal(reasonAdoptionTargetNotFound))
			Expect(fake.ProviderCount()).To(BeZero())
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("never deletes the provider when the resource is deleted", func() {
			pk := seedLegacy("ro-keep")
			p := newProvider("ro-keep", readOnly(nil))
			mustReconcile(p)
			Expect(k8sClient.Delete(ctx, get(p))).To(Succeed())
			mustReconcile(p)
			Expect(gone(p)).To(BeTrue())
			Expect(fake.Provider(pk)).NotTo(BeNil())
		})

		It("removes the Drifted condition when the policy is no longer ReadOnly", func() {
			seedLegacy("leave-ro")
			p := newProvider("leave-ro", readOnly(nil))
			mustReconcile(p)
			Expect(drifted(p)).NotTo(BeNil())

			cur := get(p)
			cur.Spec.ManagementPolicy = authentikiov1alpha1.ManagementPolicyAdopt
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			mustReconcile(p)
			Expect(drifted(p)).To(BeNil())
			Expect(fake.Requests(true)).NotTo(BeEmpty(), "Adopt writes the spec once ReadOnly is lifted")
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
		It("classifies a recovered own create as Created even under AdoptOrCreate", func() {
			p := newProvider("lost", managing(authentikiov1alpha1.ManagementPolicyAdoptOrCreate, nil, false))
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
