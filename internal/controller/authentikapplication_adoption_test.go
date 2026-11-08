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
		apps = &AuthentikApplicationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(),
			Connections: connections, Recorder: recorder}
		providers = &AuthentikOAuthProviderReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(),
			Connections: connections, APIReader: k8sClient}
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

	newApp := func(name, provider string, adopt, dryRun bool) *authentikiov1alpha1.AuthentikApplication {
		a := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{
				Name:     "App " + name,
				Adoption: authentikiov1alpha1.APIAdoptionSpec{Enabled: adopt, DryRun: dryRun},
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
	}

	It("stays a Conflict without adoption and says how to enable it", func() {
		seedLegacy("plain-app", nil)
		a := newApp("plain-app", "", false, false)

		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(ready(a).Message).To(ContainSubstring("spec.adoption.enabled"))
		Expect(get(a).Status.Provenance).To(BeEmpty(), "never infer Created for a fresh CR that found an object")
		Expect(get(a).Status.ApplicationUID).To(BeEmpty())
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("records Created provenance for an application it creates", func() {
		a := newApp("fresh-app", "", false, false)
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("adopts by slug and patches only the fields the spec sets", func() {
		seedLegacy("legacy-app", nil)
		a := newApp("legacy-app", "", true, false)

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
			"meta_icon", "slug", "provider"} {
			Expect(writes[0].Body).NotTo(HaveKey(untouched))
		}

		By("a second reconcile writes nothing")
		mustReconcile(a)
		Expect(fake.Requests(true)).To(HaveLen(1))
	})

	It("sets the provider link to providerRef when it differs, and leaves it alone when unset", func() {
		pk := readyProvider("link-prov")
		seedLegacy("link-app", nil)
		a := newApp("link-app", "link-prov", true, false)
		mustReconcile(a)
		Expect(fake.Application("link-app")["provider"]).To(BeEquivalentTo(pk))
		Expect(get(a).Status.ProviderPK).To(Equal(pk))

		other := readyProvider("keep-prov")
		seedLegacy("keep-app", other)
		b := newApp("keep-app", "", true, false)
		mustReconcile(b)
		Expect(fake.Application("keep-app")["provider"]).To(BeEquivalentTo(other))
	})

	It("makes no provider write when the link already matches providerRef", func() {
		pk := readyProvider("same-prov")
		seedLegacy("same-app", pk)
		a := newApp("same-app", "same-prov", true, false)
		before := len(fake.Requests(true))
		mustReconcile(a)
		writes := fake.Requests(true)[before:]
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Body).NotTo(HaveKey("provider"))
	})

	It("does not create when adoption is enabled and nothing matches, unless createIfMissing", func() {
		a := newApp("nothing-yet", "", true, false)
		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(fake.Requests(true)).To(BeEmpty())

		cur := get(a)
		cur.Spec.Adoption.CreateIfMissing = true
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		mustReconcile(a)
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("nothing was found to adopt")))
	})

	It("never recreates an adopted application that vanished", func() {
		seedLegacy("vanish-app", nil)
		a := newApp("vanish-app", "", true, false)
		mustReconcile(a)
		uid := get(a).Status.ApplicationUID
		fake.DeleteApplication("vanish-app")

		mustReconcile(a)
		Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(get(a).Status.ApplicationUID).To(Equal(uid))
	})

	It("refuses to adopt an application another resource already tracks", func() {
		seedLegacy("shared-app", nil)
		first := newApp("shared-app", "", true, false)
		mustReconcile(first)

		second := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "second-claim", Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: "Second", Slug: "shared-app",
				Adoption: authentikiov1alpha1.APIAdoptionSpec{Enabled: true}},
		}
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		cleanup(second)
		mustReconcile(second)
		Expect(ready(second).Reason).To(Equal(reasonConflict))
		Expect(ready(second).Message).To(ContainSubstring("shared-app"))
		Expect(get(second).Status.ApplicationUID).To(BeEmpty())
		Expect(get(second).Status.Provenance).To(BeEmpty())
	})

	Context("dry run with nothing to adopt", func() {
		It("creates nothing and reports a would-create when createIfMissing is set", func() {
			a := newApp("dry-miss", "", true, true)
			a.Spec.Adoption.CreateIfMissing = true
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(a).Reason).To(Equal(reasonAdoptionDryRun))
			Expect(ready(a).Message).To(ContainSubstring("a new one would be created"))
		})

		It("reports AdoptionTargetNotFound when createIfMissing is false", func() {
			a := newApp("dry-miss2", "", true, true)
			mustReconcile(a)
			Expect(fake.Requests(true)).To(BeEmpty())
			Expect(ready(a).Reason).To(Equal(reasonAdoptionTargetNotFound))
		})
	})

	Context("dry run", func() {
		It("writes nothing and reports the diff, then adopts when switched off", func() {
			seedLegacy("dry-app", nil)
			a := newApp("dry-app", "", true, true)

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
			cur.Spec.Adoption.DryRun = false
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
			a := newApp("keep-me", "", true, false)
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("keep-me")).NotTo(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Retained")))
		})

		It("never deletes an application whose provenance is undecided", func() {
			seedLegacy("unknown-app", nil)
			a := newApp("unknown-app", "", false, false)
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
			a := newApp("drop-me", "", false, false)
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("drop-me")).To(BeNil())
		})

		It("honours an explicit Retain on a created application", func() {
			a := newApp("explicit-retain", "", false, false)
			a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("explicit-retain")).NotTo(BeNil())
		})

		It("honours an explicit Delete on an adopted application", func() {
			seedLegacy("explicit-delete", nil)
			a := newApp("explicit-delete", "", true, false)
			a.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			mustReconcile(a)
			deleteAndFinalize(a)
			Expect(fake.Application("explicit-delete")).To(BeNil())
		})
	})

	Context("pendingCreate recovery", func() {
		It("classifies a recovered own create as Created even with adoption enabled", func() {
			a := newApp("lost-app", "", true, false)
			a.Spec.Adoption.CreateIfMissing = true
			Expect(k8sClient.Update(ctx, a)).To(Succeed())
			fake.LoseNextApplicationCreate()
			_, err := reconcileApp(a)
			Expect(err).To(HaveOccurred())
			Expect(fake.ApplicationCount()).To(Equal(1))

			mustReconcile(a)
			Expect(fake.ApplicationCount()).To(Equal(1))
			Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			Expect(get(a).Status.PendingCreate).To(BeEmpty())
		})
	})
})
