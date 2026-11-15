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
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

var _ = Describe("AuthentikApplication Controller", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake        *fakeauthentik.Server
		recorder    *events.FakeRecorder
		connections *connection.Resolver
		apps        *AuthentikApplicationReconciler
		providers   *AuthentikOAuthProviderReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		recorder = events.NewFakeRecorder(100)
		connections = &connection.Resolver{
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

	// readyProvider creates an AuthentikOAuthProvider and reconciles it to
	// Ready, returning its Authentik pk.
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
		Expect(p.Status.ProviderPK).NotTo(BeZero())
		return p.Status.ProviderPK
	}

	newApp := func(name, provider string) *authentikiov1alpha1.AuthentikApplication {
		a := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       authentikiov1alpha1.AuthentikApplicationSpec{Name: "App " + name},
		}
		if provider != "" {
			a.Spec.ProviderRef = &authentikiov1alpha1.ProviderRef{Name: provider}
		}
		return a
	}

	create := func(a *authentikiov1alpha1.AuthentikApplication) {
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		cleanup(a)
	}

	reconcileApp := func(a *authentikiov1alpha1.AuthentikApplication) (reconcile.Result, error) {
		return apps.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
	}

	get := func(a *authentikiov1alpha1.AuthentikApplication) *authentikiov1alpha1.AuthentikApplication {
		var cur authentikiov1alpha1.AuthentikApplication
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), &cur)).To(Succeed())
		return &cur
	}

	ready := func(a *authentikiov1alpha1.AuthentikApplication) *metav1.Condition {
		return meta.FindStatusCondition(get(a).Status.Conditions, ConditionReady)
	}

	It("leaves the adoption-sensitive fields unset at admission and applies the defaults when it creates", func() {
		a := newApp("app-defaults", "")
		create(a)
		s := get(a).Spec
		Expect(s.PolicyEngineMode).To(BeEmpty())
		Expect(s.OpenInNewTab).To(BeNil())
		Expect(s.MetaHide).To(BeNil())

		_, err := apps.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		Expect(err).NotTo(HaveOccurred())
		got := fake.Application("app-defaults")
		Expect(got["policy_engine_mode"]).To(Equal("any"))
		Expect(got["open_in_new_tab"]).To(BeTrue())
		Expect(got["meta_hide"]).To(BeFalse())
	})

	It("waits for a missing provider, then a not-yet-Ready one, then creates the application", func() {
		a := newApp("app-wait", "prov-wait")
		create(a)

		res, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(a).Reason).To(Equal(reasonProviderNotFound))
		Expect(fake.ApplicationCount()).To(BeZero())

		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "prov-wait", Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://wait.example.com/cb"}},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		cleanup(p)
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonProviderNotReady))
		Expect(fake.ApplicationCount()).To(BeZero())

		_, err = providers.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())

		res, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(syncedRecheck))
		cur := get(a)
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(cur.Status.Slug).To(Equal("app-wait"))
		Expect(cur.Status.ApplicationUID).NotTo(BeEmpty())
		Expect(cur.Status.ProviderPK).To(Equal(p.Status.ProviderPK))
		got := fake.Application("app-wait")
		Expect(got["name"]).To(Equal("App app-wait"))
		Expect(got["provider"]).To(BeEquivalentTo(p.Status.ProviderPK))
		Expect(got["open_in_new_tab"]).To(BeTrue())
	})

	It("refuses a provider on a different connection", func() {
		readyProvider("prov-default-conn")
		a := newApp("app-mismatch", "prov-default-conn")
		a.Spec.AuthentikRef = &authentikiov1alpha1.AuthentikRef{Name: "some-other-authentik"}
		create(a)

		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonProviderConnectionMismatch))
		Expect(fake.ApplicationCount()).To(BeZero())
	})

	It("creates an application without a provider and is idempotent", func() {
		a := newApp("app-plain", "")
		a.Spec.MetaLaunchURL = "https://plain.example.com"
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-plain")["provider"]).To(BeNil())
		Expect(fake.Application("app-plain")["meta_launch_url"]).To(Equal("https://plain.example.com"))

		writes := len(fake.Requests(true))
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Requests(true)).To(HaveLen(writes))
	})

	It("repairs drift by patching only the differing field", func() {
		a := newApp("app-drift", "")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		fake.SetApplicationField("app-drift", "meta_hide", true)

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		writes := fake.Requests(true)
		last := writes[len(writes)-1]
		Expect(last.Method).To(Equal(http.MethodPatch))
		Expect(last.Body).To(HaveLen(1))
		Expect(last.Body).To(HaveKeyWithValue("meta_hide", false))
	})

	It("applies spec changes, including detaching the provider", func() {
		pk := readyProvider("prov-change")
		a := newApp("app-change", "prov-change")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-change")["provider"]).To(BeEquivalentTo(pk))

		cur := get(a)
		cur.Spec.Name = "Renamed"
		cur.Spec.Group = "Infra"
		cur.Spec.MetaDescription = "desc"
		cur.Spec.MetaPublisher = "pub"
		cur.Spec.PolicyEngineMode = "all"
		cur.Spec.OpenInNewTab = new(false)
		cur.Spec.ProviderRef = nil
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())

		got := fake.Application("app-change")
		Expect(got["name"]).To(Equal("Renamed"))
		Expect(got["group"]).To(Equal("Infra"))
		Expect(got["meta_description"]).To(Equal("desc"))
		Expect(got["meta_publisher"]).To(Equal("pub"))
		Expect(got["policy_engine_mode"]).To(Equal("all"))
		Expect(got["open_in_new_tab"]).To(BeFalse())
		Expect(got["provider"]).To(BeNil())
		Expect(get(a).Status.ProviderPK).To(BeZero())
	})

	It("never takes over a pre-existing application with the same slug", func() {
		fake.SeedApplication("app-taken", nil)
		a := newApp("app-taken", "")
		create(a)

		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(fake.Requests(true)).To(BeEmpty())
	})

	It("never lets a second resource with the same slug and provider take the application over", func() {
		readyProvider("prov-dup")
		owner := newApp("app-dup", "prov-dup")
		create(owner)
		_, err := reconcileApp(owner)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(owner).Status).To(Equal(metav1.ConditionTrue))

		intruder := newApp("other-dup", "prov-dup")
		intruder.Spec.Slug = "app-dup"
		intruder.Spec.Name = "Different"
		create(intruder)
		writes := len(fake.Requests(true))
		_, err = reconcileApp(intruder)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(intruder).Reason).To(Equal(reasonConflict))
		Expect(get(intruder).Status.ApplicationUID).To(BeEmpty())
		Expect(fake.Requests(true)).To(HaveLen(writes))
		Expect(fake.Application("app-dup")["name"]).To(Equal("App app-dup"))

		Expect(k8sClient.Delete(ctx, get(intruder))).To(Succeed())
		_, err = reconcileApp(intruder)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-dup")).NotTo(BeNil())
	})

	It("survives a transient 404 on retrieve without losing track of its application", func() {
		a := newApp("app-404", "")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		uid := get(a).Status.ApplicationUID

		fake.FailNext(http.MethodGet, http.StatusNotFound)
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(a).Status.ApplicationUID).To(Equal(uid))
		Expect(fake.ApplicationCount()).To(Equal(1))

		Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-404")).To(BeNil())
	})

	It("recovers a lost create when the application links to the provider this resource created", func() {
		readyProvider("prov-lost")
		a := newApp("app-lost-plain", "prov-lost")
		create(a)
		fake.LoseNextApplicationCreate()

		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		Expect(get(a).Status.PendingCreate).To(Equal("app-lost-plain"))

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(a).Status.PendingCreate).To(BeEmpty())
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(get(a).Status.ApplicationUID).To(Equal(fake.Application("app-lost-plain")["pk"]))
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Recovered")))
	})

	It("cannot prove a lost create of an application without a provider, so reports a Conflict", func() {
		a := newApp("app-lost-bare", "")
		create(a)
		fake.LoseNextApplicationCreate()

		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(get(a).Status.Provenance).To(BeEmpty())
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("outcome is unproven")))
	})

	It("clears the pending marker when a create is definitively rejected", func() {
		a := newApp("app-rejected", "")
		create(a)
		fake.FailNextWith(http.MethodPost, http.StatusBadRequest, map[string]any{"name": []string{"bad"}})
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(a).Status.PendingCreate).To(BeEmpty())
		Expect(ready(a).Reason).To(Equal(reasonInvalidSpec))
	})

	It("clears metaIcon when removed from spec", func() {
		a := newApp("app-icon", "")
		a.Spec.MetaIcon = "https://cdn.example.com/icon.png"
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-icon")["meta_icon"]).To(Equal("https://cdn.example.com/icon.png"))

		cur := get(a)
		cur.Spec.MetaIcon = ""
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-icon")["meta_icon"]).To(Equal(""))
	})

	It("re-points to a provider recreated with a new pk, waiting meanwhile", func() {
		oldPK := readyProvider("prov-recreated")
		a := newApp("app-repoint", "prov-recreated")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())

		fake.DeleteProvider(oldPK) // detaches it from the application
		_, err = reconcileApp(a)   // provider resource still reports the stale pk
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonProviderNotReady))
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring(reasonInvalidSpec)))

		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "prov-recreated", Namespace: namespace}}
		_, err = providers.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		Expect(p.Status.ProviderPK).NotTo(Equal(oldPK))

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(fake.Application("app-repoint")["provider"]).To(BeEquivalentTo(p.Status.ProviderPK))
	})

	It("waits without writing when the provider resource is deleted", func() {
		readyProvider("prov-gone")
		a := newApp("app-provgone", "prov-gone")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())

		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "prov-gone", Namespace: namespace}}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		p.Finalizers = nil
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Expect(k8sClient.Delete(ctx, p)).To(Succeed())
		writes := len(fake.Requests(true))

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonProviderNotFound))
		Expect(fake.Requests(true)).To(HaveLen(writes))
	})

	It("recovers its own application after a lost create when it holds this resource's provider", func() {
		readyProvider("prov-lost")
		a := newApp("app-lost", "prov-lost")
		create(a)
		fake.LoseNextApplicationCreate()

		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		Expect(fake.ApplicationCount()).To(Equal(1))

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
		Expect(get(a).Status.ApplicationUID).To(Equal(fake.Application("app-lost")["pk"]))
	})

	It("reports a provider already attached to another application as Conflict", func() {
		pk := readyProvider("prov-shared")
		fake.SeedApplication("someone-else", pk)
		a := newApp("app-shared", "prov-shared")
		create(a)

		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(ready(a).Message).To(ContainSubstring("already exists"))
	})

	It("never modifies or deletes an application replaced out of band", func() {
		a := newApp("app-replaced", "")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		fake.DeleteApplication("app-replaced")
		fake.SeedApplication("app-replaced", nil) // same slug, new pk
		fake.SetApplicationField("app-replaced", "name", "theirs")
		writes := len(fake.Requests(true))

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ready(a).Reason).To(Equal(reasonConflict))
		Expect(fake.Requests(true)).To(HaveLen(writes))

		Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-replaced")).NotTo(BeNil())
	})

	It("recreates an application deleted out of band", func() {
		a := newApp("app-recreate", "")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		fake.DeleteApplication("app-recreate")

		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-recreate")).NotTo(BeNil())
		Expect(ready(a).Status).To(Equal(metav1.ConditionTrue))
	})

	It("deletes the application it created when the resource is deleted", func() {
		a := newApp("app-delete", "")
		create(a)
		_, err := reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, get(a))).To(Succeed())
		_, err = reconcileApp(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.Application("app-delete")).To(BeNil())
		err = k8sClient.Get(ctx, client.ObjectKeyFromObject(a), &authentikiov1alpha1.AuthentikApplication{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("maps a provider to the applications that reference it", func() {
		scheme := k8sClient.Scheme()
		c := clientfake.NewClientBuilder().WithScheme(scheme).
			WithIndex(&authentikiov1alpha1.AuthentikApplication{}, indexProviderRef, providerRefIndex).
			WithObjects(newApp("uses-p", "p"), newApp("uses-q", "q"), newApp("no-provider", "")).Build()
		r := &AuthentikApplicationReconciler{Client: c}
		reqs := r.applicationsForProvider(ctx, &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: namespace}})
		Expect(reqs).To(HaveExactElements(
			reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "uses-p"}}))
	})

	Context("admission validation", func() {
		It("rejects changing the effective slug, but allows spelling out the default", func() {
			a := newApp("app-slug", "")
			create(a)
			cur := get(a)
			cur.Spec.Slug = "app-slug"
			Expect(k8sClient.Update(ctx, cur)).To(Succeed())
			cur = get(a)
			cur.Spec.Slug = "changed"
			Expect(k8sClient.Update(ctx, cur)).To(MatchError(ContainSubstring("slug is immutable")))
		})
		It("rejects changing authentikRef", func() {
			a := newApp("app-ref", "")
			a.Spec.AuthentikRef = &authentikiov1alpha1.AuthentikRef{Name: "one"}
			create(a)
			cur := get(a)
			cur.Spec.AuthentikRef.Name = "two"
			Expect(k8sClient.Update(ctx, cur)).To(MatchError(ContainSubstring("authentikRef is immutable")))
		})
		It("requires spec.slug when metadata.name is not a valid slug", func() {
			a := newApp("app.with.dots", "")
			Expect(k8sClient.Create(ctx, a)).To(MatchError(ContainSubstring("not a valid slug")))
			a.Spec.Slug = "app-with-dots"
			create(a)
		})
		It("rejects a non-http launch URL", func() {
			a := newApp("app-badurl", "")
			a.Spec.MetaLaunchURL = "javascript:alert(1)"
			Expect(k8sClient.Create(ctx, a)).NotTo(Succeed())
		})
		It("requires a display name", func() {
			a := newApp("app-noname", "")
			a.Spec.Name = ""
			Expect(k8sClient.Create(ctx, a)).NotTo(Succeed())
		})
	})
})
