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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

var _ = Describe("AuthentikApplication adoption", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake      *fakeauthentik.Server
		recorder  *events.FakeRecorder
		apps      *AuthentikApplicationReconciler
		providers *AuthentikOAuthProviderReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		recorder = events.NewFakeRecorder(100)
		connections := &connection.Resolver{
			Reader:  k8sClient,
			Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1},
		}
		apps = &AuthentikApplicationReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: connections, Recorder: recorder, APIReader: k8sClient,
			AdoptionNamespaceSelector: labels.Everything()}
		providers = &AuthentikOAuthProviderReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: connections, APIReader: k8sClient, AdoptionNamespaceSelector: labels.Everything()}
	})
	AfterEach(func() { fake.Close() })

	cleanup := func(obj client.Object) {
		DeferCleanup(func() {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err == nil {
				obj.SetFinalizers(nil)
				_ = k8sClient.Update(ctx, obj)
				_ = k8sClient.Delete(ctx, obj)
			}
		})
	}

	// readyProvider creates a provider resource and reconciles it to Ready.
	readyProvider := func(name string) int32 {
		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://" + name + ".example.com/cb"}},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		cleanup(p)
		_, err := providers.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		return p.Status.ProviderPK
	}

	const (
		policyCreate        = authentikiov1alpha1.ManagementPolicyCreate
		policyAdopt         = authentikiov1alpha1.ManagementPolicyAdopt
		policyAdoptOrCreate = authentikiov1alpha1.ManagementPolicyAdoptOrCreate
		policyReadOnly      = authentikiov1alpha1.ManagementPolicyReadOnly
	)

	newApp := func(name, provider string, policy authentikiov1alpha1.ManagementPolicy,
		dryRun bool) *authentikiov1alpha1.AuthentikApplication {
		a := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{
				Access:            &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged},
				Name:              "App " + name,
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy, DryRun: dryRun},
			},
		}
		if provider != "" {
			a.Spec.ProviderRef = &authentikiov1alpha1.ProviderRef{Name: provider}
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		cleanup(a)
		return a
	}

	reconcileApp := func(a *authentikiov1alpha1.AuthentikApplication) (reconcile.Result, error) {
		return apps.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	}
	mustReconcile := func(a *authentikiov1alpha1.AuthentikApplication) {
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
	}
	get := func(a *authentikiov1alpha1.AuthentikApplication) *authentikiov1alpha1.AuthentikApplication {
		var cur authentikiov1alpha1.AuthentikApplication
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), &cur)).To(Succeed())
		return &cur
	}
	ready := func(a *authentikiov1alpha1.AuthentikApplication) *metav1.Condition {
		return meta.FindStatusCondition(get(a).Status.Conditions, ConditionReady)
	}
	gone := func(a *authentikiov1alpha1.AuthentikApplication) bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(a), &authentikiov1alpha1.AuthentikApplication{})
		return apierrors.IsNotFound(err)
	}

	// seedLegacy inserts an application with metadata the spec never sets.
	seedLegacy := func(slug string, provider any) {
		fake.SeedApplication(slug, provider)
		fake.SetApplicationField(slug, "group", "Legacy Group")
		fake.SetApplicationField(slug, "meta_launch_url", "https://legacy.example.com/")
		fake.SetApplicationField(slug, "meta_description", "legacy description")
		fake.SetApplicationField(slug, "meta_publisher", "legacy publisher")
		fake.SetApplicationField(slug, "meta_icon", "https://legacy.example.com/icon.png")
		fake.SetApplicationField(slug, "policy_engine_mode", "all")
		fake.SetApplicationField(slug, "open_in_new_tab", true)
		fake.SetApplicationField(slug, "meta_hide", true)
	}

	It("stays a Conflict under the Create policy and says how to adopt", func() {
		seedLegacy("plain-app", nil)
		a := newApp("plain-app", "", policyCreate, false)

		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(ready(a).Message).To(ContainSubstring("spec.managementPolicy"))
		Expect(get(a).Status.Provenance).To(BeEmpty(), "never infer Created for a fresh CR that found an object")
		Expect(get(a).Status.ApplicationUID).To(BeEmpty())
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("records Created provenance for an application it creates", func() {
		a := newApp("fresh-app", "", policyCreate, false)
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("adopts by slug and patches only the fields the spec sets", func() {
		seedLegacy("legacy-app", nil)
		a := newApp("legacy-app", "", policyAdopt, false)

		mustReconcile(a)
		cur := get(a)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(cur.Status.ApplicationUID).NotTo(BeEmpty())
		Expect(cur.Status.Slug).To(Equal("legacy-app"))
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(fake.ApplicationCount()).To(Equal(1))

		got := fake.Application("legacy-app")
		Expect(got[jsonName]).To(Equal("App legacy-app"))
		Expect(got["group"]).To(Equal("Legacy Group"))
		Expect(got["meta_launch_url"]).To(Equal("https://legacy.example.com/"))
		Expect(got["meta_description"]).To(Equal("legacy description"))
		Expect(got["meta_publisher"]).To(Equal("legacy publisher"))
		Expect(got["meta_icon"]).To(Equal("https://legacy.example.com/icon.png"))

		writes := fake.Requests(true)
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Method).To(Equal(http.MethodPatch))
		for _, untouched := range []string{"group", "meta_launch_url", "meta_description", "meta_publisher",
			"meta_icon", "slug", "provider", "policy_engine_mode", "open_in_new_tab", "meta_hide"} {
			Expect(writes[0].Body).NotTo(HaveKey(untouched))
		}
		By("unset policyEngineMode, openInNewTab and metaHide keep the live values, never the create defaults")
		Expect(got["policy_engine_mode"]).To(Equal("all"))
		Expect(got["open_in_new_tab"]).To(BeTrue())
		Expect(got["meta_hide"]).To(BeTrue())

		By("a second reconcile writes nothing")
		mustReconcile(a)
		Expect(fake.Requests(true)).To(HaveLen(1))
	})

	It("writes policyEngineMode, openInNewTab and metaHide on an adopted application when the spec sets them", func() {
		seedLegacy("explicit-app", nil)
		a := newApp("explicit-app", "", policyAdopt, false)
		a.Spec.PolicyEngineMode = "any"
		a.Spec.OpenInNewTab = new(false)
		a.Spec.MetaHide = new(false)
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
		mustReconcile(a)
		got := fake.Application("explicit-app")
		Expect(got["policy_engine_mode"]).To(Equal("any"))
		Expect(got["open_in_new_tab"]).To(BeFalse())
		Expect(got["meta_hide"]).To(BeFalse())
	})

	It("sets the provider link to providerRef when it differs, and leaves it alone when unset", func() {
		pk := readyProvider("link-prov")
		seedLegacy("link-app", nil)
		a := newApp("link-app", "link-prov", policyAdopt, false)
		mustReconcile(a)
		Expect(fake.Application("link-app")["provider"]).To(BeEquivalentTo(pk))
		Expect(get(a).Status.ProviderPK).To(Equal(pk))

		other := readyProvider("keep-prov")
		seedLegacy("keep-app", other)
		b := newApp("keep-app", "", policyAdopt, false)
		mustReconcile(b)
		Expect(fake.Application("keep-app")["provider"]).To(BeEquivalentTo(other))
	})

	It("makes no provider write when the link already matches providerRef", func() {
		pk := readyProvider("same-prov")
		seedLegacy("same-app", pk)
		a := newApp("same-app", "same-prov", policyAdopt, false)
		before := len(fake.Requests(true))
		mustReconcile(a)
		writes := fake.Requests(true)[before:]
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Body).NotTo(HaveKey("provider"))
	})

	It("rejects an invalid combination of management fields at admission", func() {
		cases := []struct {
			name    string
			policy  authentikiov1alpha1.ManagementPolicy
			dryRun  bool
			delete  bool
			wantMsg string
		}{
			{"dryRun under Create", policyCreate, true, false, msgDryRunPolicy},
			{"dryRun under ReadOnly", policyReadOnly, true, false, msgDryRunPolicy},
			{"deletionPolicy Delete under ReadOnly", policyReadOnly, false, true, "ReadOnly never deletes"},
			{"unknown policy", "Observe", false, false, "Unsupported value"},
		}
		for _, c := range cases {
			a := &authentikiov1alpha1.AuthentikApplication{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-management", Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: "Bad",
					Access:            &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged},
					APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: c.policy, DryRun: c.dryRun}},
			}
			if c.delete {
				a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			}
			err := k8sClient.Create(ctx, a)
			Expect(err).To(HaveOccurred(), c.name)
			Expect(err.Error()).To(ContainSubstring(c.wantMsg), c.name)
		}
	})

	It("defaults managementPolicy to Create at admission", func() {
		a := newApp("default-policy", "", "", false)
		Expect(get(a).Spec.ManagementPolicy).To(Equal(policyCreate))
	})

	It("does not create under Adopt when nothing matches, but does once switched to AdoptOrCreate", func() {
		a := newApp("nothing-yet", "", policyAdopt, false)
		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())

		cur := get(a)
		cur.Spec.ManagementPolicy = policyAdoptOrCreate
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("nothing was found to adopt")))
	})

	It("looks a vanished tracked application up once, not twice, before recreating it", func() {
		a := newApp("lookup-once", "", policyCreate, false)
		mustReconcile(a)
		fake.DeleteApplication("lookup-once")
		before := len(fake.Requests(false))

		mustReconcile(a)
		gets := 0
		for _, req := range fake.Requests(false)[before:] {
			if req.Method == http.MethodGet && req.Path == "/api/v3/core/applications/lookup-once/" {
				gets++
			}
		}
		Expect(gets).To(Equal(1), "the slug is immutable: the second GET repeats the first")
		Expect(fake.Application("lookup-once")).NotTo(BeNil(), "the application is recreated")
	})

	It("never recreates an adopted application that vanished", func() {
		seedLegacy("vanish-app", nil)
		a := newApp("vanish-app", "", policyAdopt, false)
		mustReconcile(a)
		uid := get(a).Status.ApplicationUID
		fake.DeleteApplication("vanish-app")

		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(get(a).Status.ApplicationUID).To(Equal(uid))
	})

	It("never recreates an adopted application that vanished, even after adoption is disabled", func() {
		seedLegacy("vanish-off", nil)
		a := newApp("vanish-off", "", policyAdopt, false)
		mustReconcile(a)
		uid := get(a).Status.ApplicationUID
		cur := get(a)
		cur.Spec.ManagementPolicy = policyCreate
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		fake.DeleteApplication("vanish-off")

		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(get(a).Status.ApplicationUID).To(Equal(uid))
	})

	It("yields to the lower-UID resource when two track one application", func() {
		seedLegacy("raced-app", nil)
		mk := func(name string) *authentikiov1alpha1.AuthentikApplication {
			a := &authentikiov1alpha1.AuthentikApplication{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: name, Slug: "raced-app",
					Access:            &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged},
					APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policyAdopt}},
			}
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			cleanup(a)
			return a
		}
		a, b := mk("race-a"), mk("race-b")
		uid, ok := fake.Application("raced-app")["pk"].(string)
		Expect(ok).To(BeTrue())
		for _, x := range []*authentikiov1alpha1.AuthentikApplication{a, b} {
			cur := get(x)
			cur.Status.Provenance = authentikiov1alpha1.ProvenanceAdopted
			cur.Status.ApplicationUID = uid
			cur.Status.Slug = "raced-app"
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
		}
		low, high := a, b
		if get(a).UID > get(b).UID {
			low, high = b, a
		}
		mustReconcile(high)
		Expect(ready(high).Reason).To(Equal(reasonConflict))
		Expect(get(high).Status.ApplicationUID).To(BeEmpty())
		Expect(get(high).Status.Provenance).To(BeEmpty())

		mustReconcile(low)
		Expect(ready(low).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(low).Status.ApplicationUID).To(Equal(uid))
	})

	It("refuses to adopt an application another resource already tracks", func() {
		seedLegacy("shared-app", nil)
		first := newApp("shared-app", "", policyAdopt, false)
		mustReconcile(first)

		second := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "second-claim", Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: "Second", Slug: "shared-app",
				Access:            &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged},
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policyAdopt}},
		}
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		cleanup(second)
		mustReconcile(second)
		Expect(ready(second).Reason).To(Equal(reasonConflict))
		Expect(ready(second).Message).To(ContainSubstring("shared-app"))
		Expect(get(second).Status.ApplicationUID).To(BeEmpty())
		Expect(get(second).Status.Provenance).To(BeEmpty())
	})

	It("adopts when AdoptOrCreate finds the application, without creating a second one", func() {
		seedLegacy("either-way", nil)
		a := newApp("either-way", "", policyAdoptOrCreate, false)
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(fake.ApplicationCount()).To(Equal(1))
	})

	It("recreates an application this resource created when it vanishes, even under Adopt", func() {
		a := newApp("was-created", "", policyAdoptOrCreate, false)
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		fake.DeleteApplication("was-created")

		cur := get(a)
		cur.Spec.ManagementPolicy = policyAdopt
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		mustReconcile(a)
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ApplicationCount()).To(Equal(1))
	})

	Context("read only", func() {
		drifted := func(a *authentikiov1alpha1.AuthentikApplication) *metav1.Condition {
			return meta.FindStatusCondition(get(a).Status.Conditions, ConditionDrifted)
		}

		It("finds the application and claims it, but never writes to Authentik", func() {
			seedLegacy("ro-app", nil)
			a := newApp("ro-app", "", policyReadOnly, false)

			mustReconcile(a)
			Expect(fake.Requests(true)).To(BeEmpty(), "read only must never write to Authentik")
			cur := get(a)
			Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(cur.Status.ApplicationUID).NotTo(BeEmpty())
			Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
			Expect(ready(a).Reason).To(Equal(reasonReadOnly))
		})

		It("reports drift in the Drifted condition and clears it once the live object matches", func() {
			seedLegacy("drift-app", nil)
			a := newApp("drift-app", "", policyReadOnly, false)
			mustReconcile(a)
			Expect(drifted(a).Status).To(Equal(metav1.ConditionTrue))
			Expect(drifted(a).Message).To(ContainSubstring(`name: "drift-app" -> "App drift-app"`))

			fake.SetApplicationField("drift-app", jsonName, "App drift-app")
			mustReconcile(a)
			Expect(drifted(a).Status).To(Equal(metav1.ConditionFalse))
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("reports a miss as AdoptionTargetNotFound and never creates", func() {
			a := newApp("ro-miss", "", policyReadOnly, false)
			mustReconcile(a)
			Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
			Expect(fake.ApplicationCount()).To(BeZero())
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("never recreates the application when it vanishes", func() {
			seedLegacy("ro-vanish", nil)
			a := newApp("ro-vanish", "", policyReadOnly, false)
			mustReconcile(a)
			fake.DeleteApplication("ro-vanish")

			mustReconcile(a)
			Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
			Expect(fake.ApplicationCount()).To(BeZero())
			Expect(fake.Requests(true)).To(BeEmpty())
		})

		It("never deletes the application when the resource is deleted", func() {
			seedLegacy("ro-keep", nil)
			a := newApp("ro-keep", "", policyReadOnly, false)
			mustReconcile(a)
			Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
			mustReconcile(a)
			Expect(gone(a)).To(BeTrue())
			Expect(fake.Application("ro-keep")).NotTo(BeNil())
		})
	})

	Context("dry run with nothing to adopt", func() {
		It("creates nothing and reports a would-create under AdoptOrCreate", func() {
			a := newApp("dry-miss", "", policyAdoptOrCreate, true)
			mustReconcile(a)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(a).Reason).To(Equal(reasonAdoptionDryRun))
			Expect(ready(a).Message).To(ContainSubstring("a new one would be created"))
		})

		It("reports AdoptionTargetNotFound under Adopt", func() {
			a := newApp("dry-miss2", "", policyAdopt, true)
			mustReconcile(a)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		})
	})

	Context("dry run", func() {
		It("writes nothing and reports the diff, then adopts when switched off", func() {
			seedLegacy("dry-app", nil)
			a := newApp("dry-app", "", policyAdopt, true)

			res, err := reconcileApp(a)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
			Expect(fake.Requests(true)).To(BeEmpty(), "dry run must not mutate Authentik")
			cond := ready(a)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonAdoptionDryRun))
			Expect(cond.Message).To(ContainSubstring(`name: "dry-app" -> "App dry-app"`))
			Expect(get(a).Status.Provenance).To(BeEmpty())
			Expect(get(a).Status.ApplicationUID).To(BeEmpty())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("AdoptionDryRun")))

			cur := get(a)
			cur.Spec.DryRun = false
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			mustReconcile(a)
			Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(fake.Application("dry-app")[jsonName]).To(Equal("App dry-app"))
		})
	})

	Context("deletion policy", func() {
		deleteAndFinalize := func(a *authentikiov1alpha1.AuthentikApplication) {
			Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
			mustReconcile(a)
			Expect(gone(a)).To(BeTrue(), "the finalizer is released either way")
		}

		It("retains an adopted application by default", func() {
			seedLegacy("keep-me", nil)
			a := newApp("keep-me", "", policyAdopt, false)
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("keep-me")).NotTo(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Retained")))
		})

		It("never deletes an application whose provenance is undecided", func() {
			seedLegacy("unknown-app", nil)
			a := newApp("unknown-app", "", policyCreate, false)
			mustReconcile(a) // Conflict: adds the finalizer
			cur := get(a)
			uid, ok := fake.Application("unknown-app")["pk"].(string)
			Expect(ok).To(BeTrue())
			cur.Status.ApplicationUID = uid
			cur.Status.Slug = "unknown-app"
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
			drainEvents(recorder)

			deleteAndFinalize(a)
			Expect(fake.Application("unknown-app")).NotTo(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("provenance unknown")))
		})

		It("deletes a created application by default", func() {
			a := newApp("drop-me", "", policyCreate, false)
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("drop-me")).To(BeNil())
		})

		It("honours an explicit Retain on a created application", func() {
			a := newApp("explicit-retain", "", policyCreate, false)
			a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("explicit-retain")).NotTo(BeNil())
		})

		It("honours an explicit Delete on an adopted application", func() {
			seedLegacy("explicit-delete", nil)
			a := newApp("explicit-delete", "", policyAdopt, false)
			a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("explicit-delete")).To(BeNil())
		})
	})

	Context("pendingCreate recovery", func() {
		// adoptedProvider makes a provider resource that adopted a live provider,
		// returning its pk.
		adoptedProvider := func(name string) int32 {
			pk := fake.SeedProviderWith(map[string]any{jsonName: name})
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
					AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
					InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
					RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://" + name + ".example.com/cb"}},
					APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policyAdopt},
				},
			}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			cleanup(p)
			_, err := providers.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
			Expect(p.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			return pk
		}
		markPending := func(a *authentikiov1alpha1.AuthentikApplication) {
			cur := get(a)
			cur.Status.PendingCreate = a.Name
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
		}

		It("classifies a recovered own create as Created when it links to a provider this resource created", func() {
			readyProvider("lost-prov")
			a := newApp("lost-app", "lost-prov", policyAdoptOrCreate, false)
			fake.LoseNextApplicationCreate()
			_, err := reconcileApp(a)
			Expect(err).To(HaveOccurred())
			Expect(fake.ApplicationCount()).To(Equal(1))

			mustReconcile(a)
			Expect(fake.ApplicationCount()).To(Equal(1))
			Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			Expect(get(a).Status.PendingCreate).To(BeEmpty())
		})

		It("does not trust a stale marker: a foreign application is a Conflict with a Warning event", func() {
			readyProvider("stale-prov")
			a := newApp("stale-app", "stale-prov", policyCreate, false)
			markPending(a)
			seedLegacy("stale-app", nil) // another operator made it; it links to no provider of ours
			writes := len(fake.Requests(true))

			mustReconcile(a)
			Expect(ready(a).Reason).To(Equal(reasonConflict))
			Expect(get(a).Status.Provenance).To(BeEmpty(), "never Created on an unproven marker")
			Expect(get(a).Status.PendingCreate).To(BeEmpty(), "the unproven marker is dropped")
			Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
				ContainSubstring("Warning"), ContainSubstring("outcome is unproven"))))
			Expect(fake.Requests(true)).To(HaveLen(writes), "nothing was written to the application")

			By("deleting the resource does not delete the foreign application")
			Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
			mustReconcile(a)
			Expect(fake.Application("stale-app")).NotTo(BeNil())
		})

		It("falls through to adoption, not Created, when the policy allows it", func() {
			readyProvider("fall-prov")
			a := newApp("fall-app", "fall-prov", policyAdopt, false)
			markPending(a)
			seedLegacy("fall-app", nil)

			mustReconcile(a)
			Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("outcome is unproven")))
		})

		It("does not recover an application that links to a provider this resource did not create", func() {
			pk := adoptedProvider("adopted-prov")
			a := newApp("adopted-link-app", "adopted-prov", policyCreate, false)
			markPending(a)
			fake.SeedApplication("adopted-link-app", pk)

			mustReconcile(a)
			Expect(ready(a).Reason).To(Equal(reasonConflict))
			Expect(get(a).Status.Provenance).To(BeEmpty())
		})

		It("treats a marker as unproven when the application has no providerRef", func() {
			a := newApp("no-ref-app", "", policyCreate, false)
			markPending(a)
			seedLegacy("no-ref-app", nil)

			mustReconcile(a)
			Expect(ready(a).Reason).To(Equal(reasonConflict))
			Expect(get(a).Status.Provenance).To(BeEmpty())
		})
	})

	Context("two resources tracking one application", func() {
		claimApp := func(a *authentikiov1alpha1.AuthentikApplication, slug, uid string,
			prov authentikiov1alpha1.Provenance) {
			cur := get(a)
			cur.Status.Provenance, cur.Status.ApplicationUID, cur.Status.Slug = prov, uid, slug
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
		}
		mk := func(name, slug string) *authentikiov1alpha1.AuthentikApplication {
			a := &authentikiov1alpha1.AuthentikApplication{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: name, Slug: slug,
					Access:            &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged},
					APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policyAdopt}},
			}
			Expect(k8sClient.Create(ctx, a)).To(Succeed())
			cleanup(a)
			return a
		}

		It("lets a Created resource keep it and makes the Adopted one yield, whatever their UIDs", func() {
			seedLegacy("tie-app", nil)
			uid, ok := fake.Application("tie-app")["pk"].(string)
			Expect(ok).To(BeTrue())
			a, b := mk("tie-a", "tie-app"), mk("tie-b", "tie-app")
			owner, adopter := a, b
			if get(a).UID < get(b).UID { // the Created one must have the HIGHER uid
				owner, adopter = b, a
			}
			claimApp(owner, "tie-app", uid, authentikiov1alpha1.ProvenanceCreated)
			claimApp(adopter, "tie-app", uid, authentikiov1alpha1.ProvenanceAdopted)

			mustReconcile(adopter)
			Expect(ready(adopter).Reason).To(Equal(reasonConflict))
			Expect(get(adopter).Status.ApplicationUID).To(BeEmpty())

			mustReconcile(owner)
			Expect(ready(owner).Status).To(Equal(metav1.ConditionTrue))
			Expect(get(owner).Status.ApplicationUID).To(Equal(uid))
		})

		It("counts a terminating owner: a new adopter is refused while the owner's finalizer is pending", func() {
			owner := newApp("dying-app-owner", "", policyCreate, false)
			mustReconcile(owner)
			Expect(get(owner).Status.ApplicationUID).NotTo(BeEmpty())
			Expect(k8sClient.Delete(ctx, get(owner))).To(Succeed())

			adopter := mk("late-app-adopter", "dying-app-owner")
			mustReconcile(adopter)
			Expect(ready(adopter).Reason).To(Equal(reasonConflict))
			Expect(ready(adopter).Message).To(ContainSubstring("dying-app-owner"))
			Expect(get(adopter).Status.ApplicationUID).To(BeEmpty())
		})

		It("settles a race the cache missed: the claim re-check reads the API, not the informer cache", func() {
			seedLegacy("raced-cache-app", nil)
			uid, ok := fake.Application("raced-cache-app")["pk"].(string)
			Expect(ok).To(BeTrue())
			owner := mk("raced-app-owner", "raced-cache-app")
			claimApp(owner, "raced-cache-app", uid, authentikiov1alpha1.ProvenanceCreated)

			loser := mk("raced-app-loser", "raced-cache-app")
			apps.Client = staleListClient{k8sClient}
			mustReconcile(loser)
			Expect(ready(loser).Reason).To(Equal(reasonConflict))
			Expect(get(loser).Status.ApplicationUID).To(BeEmpty())
			Expect(fake.Requests(true)).To(BeEmpty())
		})
	})

	Context("explicit Delete on undecided provenance", func() {
		It("deletes the application and emits a Warning event", func() {
			seedLegacy("undecided-del", nil)
			a := newApp("undecided-del", "", policyCreate, false)
			a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a) // Conflict: adds the finalizer
			cur := get(a)
			uid, ok := fake.Application("undecided-del")["pk"].(string)
			Expect(ok).To(BeTrue())
			cur.Status.ApplicationUID, cur.Status.Slug = uid, "undecided-del"
			Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
			drainEvents(recorder)

			Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
			mustReconcile(a)
			Expect(fake.Application("undecided-del")).To(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
				ContainSubstring("Warning"), ContainSubstring("provenance is undecided"))))
		})
	})
})
