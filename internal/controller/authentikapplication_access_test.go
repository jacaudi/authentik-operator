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
	"maps"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

const (
	bindingsPath = "/policies/bindings/"

	// Names and keys that recur in the specs below.
	immichGroup = "immich-users"
	fieldOrder  = "order"
	fieldUser   = "user"
	modeAll     = "all"
	fieldNegate = "negate"
	// groupResource is the metadata.name of a group whose spec.name differs.
	groupResource = "g-res"
)

var _ = Describe("AuthentikApplication spec.access", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake     *fakeauthentik.Server
		recorder *events.FakeRecorder
		apps     *AuthentikApplicationReconciler
		groups   *AuthentikGroupReconciler
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
		groups = &AuthentikGroupReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: connections, Recorder: recorder, APIReader: k8sClient,
			AdoptionNamespaceSelector: labels.Everything(), IdentityNamespaceSelector: labels.Everything()}
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

	const (
		policyCreate   = authentikiov1alpha1.ManagementPolicyCreate
		policyAdopt    = authentikiov1alpha1.ManagementPolicyAdopt
		policyAdoptNew = authentikiov1alpha1.ManagementPolicyAdoptOrCreate
		policyReadOnly = authentikiov1alpha1.ManagementPolicyReadOnly
	)

	groupsAccess := func(names ...string) *authentikiov1alpha1.ApplicationAccess {
		return &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeGroups, Groups: names}
	}
	unmanaged := &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged}

	build := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		access *authentikiov1alpha1.ApplicationAccess) *authentikiov1alpha1.AuthentikApplication {
		return &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{
				Name:              "App " + name,
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy},
				Access:            access,
			},
		}
	}
	newApp := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		access *authentikiov1alpha1.ApplicationAccess, opts ...func(*authentikiov1alpha1.AuthentikApplication),
	) *authentikiov1alpha1.AuthentikApplication {
		a := build(name, policy, access)
		for _, o := range opts {
			o(a)
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		cleanup(a)
		return a
	}
	dryRun := func(a *authentikiov1alpha1.AuthentikApplication) { a.Spec.DryRun = true }

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
	// cond is the named condition, or the zero Condition when absent.
	cond := func(a *authentikiov1alpha1.AuthentikApplication, t string) metav1.Condition {
		if c := meta.FindStatusCondition(get(a).Status.Conditions, t); c != nil {
			return *c
		}
		return metav1.Condition{}
	}
	writes := func() []fakeauthentik.Request { return fake.Requests(true) }
	bindingWrites := func() []fakeauthentik.Request {
		var out []fakeauthentik.Request
		for _, r := range writes() {
			if strings.Contains(r.Path, bindingsPath) {
				out = append(out, r)
			}
		}
		return out
	}
	appPK := func(slug string) string {
		pk, ok := fake.Application(slug)["pk"].(string)
		Expect(ok).To(BeTrue(), "application %s has no pk", slug)
		return pk
	}
	// bindGroup seeds a binding of the group (by pk) to the application.
	bindGroup := func(slug, groupPK string, order int, extra map[string]any) string {
		fields := map[string]any{fieldGroup: groupPK, fieldOrder: float64(order)}
		maps.Copy(fields, extra)
		return fake.SeedBinding(appPK(slug), fields)
	}
	boundGroups := func(slug string) []any {
		var out []any
		for _, b := range fake.Bindings(appPK(slug)) {
			out = append(out, b[fieldGroup])
		}
		return out
	}

	It("rejects an application without spec.access unless ReadOnly", func() {
		a := build("no-access", policyCreate, nil)
		err := k8sClient.Create(ctx, a)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.access is required"))

		ro := build("ro-no-access", policyReadOnly, nil)
		Expect(k8sClient.Create(ctx, ro)).To(Succeed())
		cleanup(ro)
	})

	It("rejects type Groups without groups and Unmanaged with groups", func() {
		const msg = "groups is required when type is Groups and not allowed when type is Unmanaged"
		for _, access := range []*authentikiov1alpha1.ApplicationAccess{
			{Type: authentikiov1alpha1.AccessTypeGroups},
			{Type: authentikiov1alpha1.AccessTypeUnmanaged, Groups: []string{"a"}},
		} {
			err := k8sClient.Create(ctx, build("bad-access", policyCreate, access))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(msg))
		}
	})

	It("rejects policyEngineMode all with more than one group", func() {
		two := build("all-two", policyCreate, groupsAccess("a", "b"))
		two.Spec.PolicyEngineMode = modeAll
		err := k8sClient.Create(ctx, two)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("policyEngineMode all with more than one access group"))

		one := build("all-one", policyCreate, groupsAccess("a"))
		one.Spec.PolicyEngineMode = modeAll
		Expect(k8sClient.Create(ctx, one)).To(Succeed())
		cleanup(one)
	})

	It("binds the listed groups right after creating the application", func() {
		groupPK := fake.SeedGroup(immichGroup, nil)
		a := newApp("bind-app", policyCreate, groupsAccess(immichGroup))

		mustReconcile(a)
		bindings := fake.Bindings(appPK("bind-app"))
		Expect(bindings).To(HaveLen(1))
		Expect(bindings[0]).To(HaveKeyWithValue(fieldGroup, groupPK))
		Expect(bindings[0]).To(HaveKeyWithValue("enabled", true))
		Expect(bindings[0]).To(HaveKeyWithValue("negate", false))
		Expect(bindings[0][fieldOrder]).To(BeEquivalentTo(0))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		Expect(cond(a, ConditionAccessInSync).Status).To(Equal(metav1.ConditionTrue))
		open := cond(a, ConditionAccessOpen)
		Expect(open.Status).To(Equal(metav1.ConditionFalse))
		Expect(open.Reason).To(Equal(reasonBound))
	})

	It("does not create the application until a listed group exists", func() {
		a := newApp("wait-app", policyCreate, groupsAccess("later-group"))
		mustReconcile(a)
		ready := cond(a, ConditionReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonWaitingForGroups))
		Expect(ready.Message).To(ContainSubstring(`"later-group"`))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(writes()).To(BeEmpty())

		By("the same under AdoptOrCreate, without the NothingToAdopt event")
		drainEvents(recorder)
		b := newApp("wait-adopt-or-create", policyAdoptNew, groupsAccess("later-group"))
		mustReconcile(b)
		Expect(cond(b, ConditionReady).Reason).To(Equal(reasonWaitingForGroups))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring("NothingToAdopt")))

		By("the group appears: created and bound")
		groupPK := fake.SeedGroup("later-group", nil)
		mustReconcile(a)
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(boundGroups("wait-app")).To(ConsistOf(groupPK))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not adopt-write an existing application until a listed group exists", func() {
		fake.SeedApplication("legacy", nil)
		a := newApp("legacy", policyAdopt, groupsAccess("missing"))

		mustReconcile(a)
		Expect(cond(a, ConditionReady).Reason).To(Equal(reasonWaitingForGroups))
		Expect(get(a).Status.Provenance).To(BeEmpty())
		Expect(writes()).To(BeEmpty())
	})

	It("does not recreate a vanished Created application while no listed group exists", func() {
		groupPK := fake.SeedGroup(immichGroup, nil)
		a := newApp("vanish-app", policyCreate, groupsAccess(immichGroup))
		mustReconcile(a)
		Expect(boundGroups("vanish-app")).To(ConsistOf(groupPK))

		fake.DeleteApplication("vanish-app") // cascades its bindings
		fake.DeleteGroup(groupPK)
		drainEvents(recorder)
		mustReconcile(a)
		Expect(cond(a, ConditionReady).Reason).To(Equal(reasonWaitingForGroups))
		Expect(fake.ApplicationCount()).To(BeZero())
		Expect(get(a).Status.PendingCreate).To(BeEmpty())
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring("ApplicationMissing")))

		By("the group is back: recreated and bound in the same reconcile")
		newPK := fake.SeedGroup(immichGroup, nil)
		mustReconcile(a)
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(boundGroups("vanish-app")).To(ConsistOf(newPK))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("adds missing bindings before removing unlisted and duplicate ones, one past the highest order", func() {
		fake.SeedApplication("order-app", nil)
		oldPK := fake.SeedGroup("old", nil)
		listedPK := fake.SeedGroup(immichGroup, nil)
		newPK := fake.SeedGroup("new", nil)
		bindGroup("order-app", oldPK, 3, nil)
		keep := bindGroup("order-app", listedPK, 1, nil)
		bindGroup("order-app", listedPK, 2, nil)
		a := newApp("order-app", policyAdopt, groupsAccess(immichGroup, "new"))

		mustReconcile(a)
		ops := bindingWrites()
		Expect(ops).To(HaveLen(3))
		Expect(ops[0].Method).To(Equal(http.MethodPost))
		Expect(ops[0].Body).To(HaveKeyWithValue(fieldGroup, newPK))
		Expect(ops[0].Body[fieldOrder]).To(BeEquivalentTo(4))
		Expect(ops[0].Body).To(HaveKeyWithValue("enabled", true))
		Expect(ops[0].Body).To(HaveKeyWithValue("negate", false))
		Expect(ops[1].Method).To(Equal(http.MethodDelete))
		Expect(ops[2].Method).To(Equal(http.MethodDelete))

		final := fake.Bindings(appPK("order-app"))
		Expect(final).To(HaveLen(2))
		Expect(final[0]).To(HaveKeyWithValue("pk", keep))
		Expect(boundGroups("order-app")).To(ConsistOf(listedPK, newPK))
	})

	It("removes nothing while a listed group does not resolve", func() {
		fake.SeedApplication("ghost-app", nil)
		oldPK := fake.SeedGroup("old", nil)
		listedPK := fake.SeedGroup(immichGroup, nil)
		bindGroup("ghost-app", oldPK, 0, nil)
		a := newApp("ghost-app", policyAdopt, groupsAccess(immichGroup, "ghost"))

		mustReconcile(a)
		Expect(boundGroups("ghost-app")).To(ConsistOf(oldPK, listedPK), "old survives, the resolved group is added")
		for _, w := range bindingWrites() {
			Expect(w.Method).NotTo(Equal(http.MethodDelete))
		}
		inSync := cond(a, ConditionAccessInSync)
		Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
		Expect(inSync.Reason).To(Equal(reasonGroupNotFound))
		Expect(inSync.Message).To(ContainSubstring(`"ghost"`))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("re-enables a disabled binding of a listed group", func() {
		fake.SeedApplication("enable-app", nil)
		listedPK := fake.SeedGroup(immichGroup, nil)
		bindGroup("enable-app", listedPK, 0, map[string]any{"enabled": false})
		a := newApp("enable-app", policyAdopt, groupsAccess(immichGroup))

		mustReconcile(a)
		ops := bindingWrites()
		Expect(ops).To(HaveLen(1))
		Expect(ops[0].Method).To(Equal(http.MethodPatch))
		Expect(ops[0].Body).To(HaveKeyWithValue("enabled", true))
		Expect(fake.Bindings(appPK("enable-app"))[0]).To(HaveKeyWithValue("enabled", true))
	})

	It("removes negated group bindings under policyEngineMode any", func() {
		fake.SeedApplication("negate-any", nil)
		fake.SetApplicationField("negate-any", "policy_engine_mode", "any")
		listedPK := fake.SeedGroup(immichGroup, nil)
		otherPK := fake.SeedGroup("x", nil)
		bindGroup("negate-any", listedPK, 0, nil)
		bindGroup("negate-any", otherPK, 1, map[string]any{fieldNegate: true})
		a := newApp("negate-any", policyAdopt, groupsAccess(immichGroup))

		mustReconcile(a)
		Expect(boundGroups("negate-any")).To(ConsistOf(listedPK))
		Expect(get(a).Status.Access).NotTo(BeNil())
		Expect(get(a).Status.Access.Bindings).To(Equal([]string{"group immich-users"}))
	})

	It("leaves negated bindings under mode all, and expiring, user and policy bindings always, and lists them", func() {
		fake.SeedApplication("leave-all", nil)
		fake.SetApplicationField("leave-all", "policy_engine_mode", "all")
		listedPK := fake.SeedGroup(immichGroup, nil)
		xPK := fake.SeedGroup("x", nil)
		yPK := fake.SeedGroup("y", nil)
		bobPK := fake.SeedUser("bob", nil)
		bindGroup("leave-all", listedPK, 0, nil)
		bindGroup("leave-all", xPK, 1, map[string]any{fieldNegate: true})
		bindGroup("leave-all", yPK, 2, map[string]any{"expiring": true})
		fake.SeedBinding(appPK("leave-all"), map[string]any{fieldUser: float64(bobPK), fieldOrder: float64(3)})
		policyPK := "00000000-0000-0000-0005-000000000001"
		fake.SeedBinding(appPK("leave-all"), map[string]any{
			"policy": policyPK, fieldOrder: float64(9),
			"policy_obj": map[string]any{
				"pk": policyPK, jsonName: "p", "component": "ak-policy-expression-form",
				"verbose_name": "Expression Policy", "verbose_name_plural": "Expression Policies",
				"meta_model_name": "authentik_policies_expression.expressionpolicy", "bound_to": float64(1),
			},
		})
		a := newApp("leave-all", policyAdopt, groupsAccess(immichGroup))

		mustReconcile(a)
		Expect(bindingWrites()).To(BeEmpty(), "no write touches any of them")
		Expect(get(a).Status.Access.Bindings).To(ConsistOf("group immich-users", "group x (negated)",
			"group y (expiring)", "user bob", "policy p"))
		Expect(cond(a, ConditionAccessInSync).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports a negated binding of a listed group under mode all as AccessNarrowed and leaves it", func() {
		fake.SeedApplication("negate-listed", nil)
		fake.SetApplicationField("negate-listed", "policy_engine_mode", modeAll)
		listedPK := fake.SeedGroup(immichGroup, nil)
		bindGroup("negate-listed", listedPK, 0, nil)
		bindGroup("negate-listed", listedPK, 1, map[string]any{fieldNegate: true})
		a := newApp("negate-listed", policyAdopt, groupsAccess(immichGroup))

		mustReconcile(a)
		Expect(bindingWrites()).To(BeEmpty())
		inSync := cond(a, ConditionAccessInSync)
		Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
		Expect(inSync.Reason).To(Equal(reasonAccessNarrowed))
		Expect(inSync.Message).To(ContainSubstring("group immich-users (negated)"))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports a live policyEngineMode all with several groups as AccessNarrowed and leaves it", func() {
		fake.SeedApplication("narrow-app", nil)
		fake.SetApplicationField("narrow-app", "policy_engine_mode", "all")
		fake.SeedGroup("a", nil)
		fake.SeedGroup("b", nil)
		a := newApp("narrow-app", policyAdopt, groupsAccess("a", "b"))

		mustReconcile(a)
		inSync := cond(a, ConditionAccessInSync)
		Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
		Expect(inSync.Reason).To(Equal(reasonAccessNarrowed))
		for _, w := range writes() {
			if strings.Contains(w.Path, "/core/applications/") {
				Expect(w.Body).NotTo(HaveKey("policy_engine_mode"))
			}
		}
		Expect(fake.Application("narrow-app")["policy_engine_mode"]).To(Equal("all"))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("never writes bindings under Unmanaged and reports AccessOpen when there are none", func() {
		a := newApp("unmanaged-app", policyCreate, unmanaged)

		mustReconcile(a)
		Expect(bindingWrites()).To(BeEmpty())
		open := cond(a, ConditionAccessOpen)
		Expect(open.Status).To(Equal(metav1.ConditionTrue))
		Expect(open.Reason).To(Equal(reasonNoBindings))
		Expect(open.Message).To(ContainSubstring("unless the instance setting"))
		Expect(meta.FindStatusCondition(get(a).Status.Conditions, ConditionAccessInSync)).To(BeNil())
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("lists bindings in the adoption dry run", func() {
		fake.SeedApplication("dry-access", nil)
		oldPK := fake.SeedGroup("old", nil)
		fake.SeedGroup("new", nil)
		bobPK := fake.SeedUser("bob", nil)
		bindGroup("dry-access", oldPK, 0, nil)
		fake.SeedBinding(appPK("dry-access"), map[string]any{fieldUser: float64(bobPK), fieldOrder: float64(1)})
		a := newApp("dry-access", policyAdopt, groupsAccess("new"), dryRun)

		mustReconcile(a)
		ready := cond(a, ConditionReady)
		Expect(ready.Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready.Message).To(SatisfyAll(
			ContainSubstring(`would bind groups ["new"]`),
			ContainSubstring(`would remove group bindings ["old"]`),
			ContainSubstring("also grants access: user bob"),
			ContainSubstring("nothing was written"),
		))
		Expect(writes()).To(BeEmpty())
		Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("AdoptionDryRun")))

		By("a second reconcile emits no new AdoptionDryRun event")
		mustReconcile(a)
		Expect(drainEvents(recorder)).NotTo(ContainElement(ContainSubstring("AdoptionDryRun")))

		By("an application with only group bindings grants nothing else")
		fake.SeedApplication("dry-groups-only", nil)
		bindGroup("dry-groups-only", oldPK, 0, nil)
		b := newApp("dry-groups-only", policyAdopt, groupsAccess("new"), dryRun)
		mustReconcile(b)
		Expect(cond(b, ConditionReady).Message).To(ContainSubstring("also grants access: none"))
	})

	It("lists the groups that were not found in the adoption dry run and claims no removal", func() {
		fake.SeedApplication("dry-ghost", nil)
		oldPK := fake.SeedGroup("old", nil)
		bindGroup("dry-ghost", oldPK, 0, nil)
		a := newApp("dry-ghost", policyAdopt, groupsAccess("ghost"), dryRun)

		mustReconcile(a)
		ready := cond(a, ConditionReady)
		Expect(ready.Reason).To(Equal(reasonAdoptionDryRun), "the dry run is not gated by WaitingForGroups")
		Expect(ready.Message).To(SatisfyAll(
			ContainSubstring(`; access: groups not found ["ghost"], nothing would be removed; also grants access: none`),
			Not(ContainSubstring("would remove"))))
		Expect(writes()).To(BeEmpty())
	})

	It("pins the Unmanaged adoption dry-run text", func() {
		fake.SeedApplication("legacy-u", nil)
		bobPK := fake.SeedUser("bob", nil)
		fake.SeedBinding(appPK("legacy-u"), map[string]any{fieldUser: float64(bobPK), fieldOrder: float64(0)})
		a := newApp("legacy-u", policyAdopt, unmanaged, dryRun)
		mustReconcile(a)
		Expect(cond(a, ConditionReady).Message).To(
			ContainSubstring("; access: unmanaged; also grants access: user bob; nothing was written"))

		fake.SeedApplication("legacy-u2", nil)
		b := newApp("legacy-u2", policyAdopt, unmanaged, dryRun)
		mustReconcile(b)
		Expect(cond(b, ConditionReady).Message).To(ContainSubstring("; access: unmanaged; also grants access: none"))
	})

	It("lists bindings by the application pk, never by pbm_uuid", func() {
		fake.SeedGroup(immichGroup, nil)
		a := newApp("pk-app", policyCreate, groupsAccess(immichGroup))
		mustReconcile(a)
		pk := appPK("pk-app")

		fake.SetApplicationField("pk-app", "pbm_uuid", "") // what the 2026.2.3 compat shim yields
		mustReconcile(a)
		var listed int
		for _, r := range fake.Requests(false) {
			if r.Method == http.MethodGet && strings.Contains(r.Path, bindingsPath) {
				listed++
				Expect(r.Query).To(ContainSubstring("target=" + pk))
			}
		}
		Expect(listed).To(BeNumerically(">=", 2))
		Expect(cond(a, ConditionAccessInSync).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports AccessPending and backs off when the first binding write fails on a new application", func() {
		groupPK := fake.SeedGroup(immichGroup, nil)
		a := newApp("pending-write", policyCreate, groupsAccess(immichGroup))
		fake.FailNextOn(http.MethodPost, bindingsPath, http.StatusInternalServerError)
		drainEvents(recorder)

		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		ready := cond(a, ConditionReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonAccessPending))
		Expect(drainEvents(recorder)).To(ContainElement(SatisfyAll(
			ContainSubstring("Warning"), ContainSubstring("AccessBindingFailed"))))

		mustReconcile(a)
		Expect(boundGroups("pending-write")).To(ConsistOf(groupPK))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports AccessPending when listing bindings fails right after the create", func() {
		groupPK := fake.SeedGroup(immichGroup, nil)
		a := newApp("pending-list", policyCreate, groupsAccess(immichGroup))
		fake.FailNextOn(http.MethodGet, bindingsPath, http.StatusInternalServerError)

		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		Expect(fake.ApplicationCount()).To(Equal(1))
		Expect(cond(a, ConditionReady).Reason).To(Equal(reasonAccessPending))

		mustReconcile(a)
		Expect(boundGroups("pending-list")).To(ConsistOf(groupPK))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue))
		Expect(cond(a, ConditionAccessInSync).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports AccessInSync AuthentikError when listing fails on an existing application", func() {
		fake.SeedGroup(immichGroup, nil)
		a := newApp("list-fails", policyCreate, groupsAccess(immichGroup))
		mustReconcile(a)

		fake.FailNextOn(http.MethodGet, bindingsPath, http.StatusInternalServerError)
		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		inSync := cond(a, ConditionAccessInSync)
		Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
		Expect(inSync.Reason).To(Equal(reasonAuthentikError))
		Expect(cond(a, ConditionReady).Status).To(Equal(metav1.ConditionTrue), "an access problem never flips Ready")
	})

	It("deletes nothing when the binding list cannot be read", func() {
		fake.SeedApplication("incomplete", nil)
		fake.SeedGroup(immichGroup, nil)
		a := newApp("incomplete", policyAdopt, groupsAccess(immichGroup))
		mustReconcile(a)

		strayPK := fake.SeedGroup("stray", nil)
		bindGroup("incomplete", strayPK, 5, nil)
		fake.FailNextOn(http.MethodGet, bindingsPath, http.StatusInternalServerError)
		before := len(bindingWrites())
		_, err := reconcileApp(a)
		Expect(err).To(HaveOccurred())
		Expect(bindingWrites()).To(HaveLen(before), "a failed read never turns into a delete")
		Expect(boundGroups("incomplete")).To(ContainElement(strayPK))

		mustReconcile(a)
		Expect(boundGroups("incomplete")).NotTo(ContainElement(strayPK), "the next complete picture removes it")
	})

	It("computes AccessOpen and status.access.bindings from the bindings after the writes", func() {
		fake.SeedGroup(immichGroup, nil)
		a := newApp("after-writes", policyCreate, groupsAccess(immichGroup))

		mustReconcile(a)
		open := cond(a, ConditionAccessOpen)
		Expect(open.Status).To(Equal(metav1.ConditionFalse), "the pre-write list was empty")
		Expect(open.Reason).To(Equal(reasonBound))
		Expect(get(a).Status.Access.Bindings).To(Equal([]string{"group immich-users"}))
	})

	It("re-enqueues applications listing a group, and groups listed by an application", func() {
		g := &authentikiov1alpha1.AuthentikGroup{
			ObjectMeta: metav1.ObjectMeta{Name: groupResource, Namespace: namespace},
			Spec:       authentikiov1alpha1.AuthentikGroupSpec{Name: immichGroup},
		}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		cleanup(g)
		a := newApp("watch-app", policyCreate, groupsAccess(immichGroup))
		other := newApp("watch-other", policyCreate, groupsAccess(groupResource))

		Expect(apps.applicationsForGroup(ctx, g)).To(ConsistOf(
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)}),
			"matched by the Authentik name, not metadata.name (watch-other lists g-res)")
		Expect(groups.groupsForApplication(ctx, a)).To(ConsistOf(
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)}))
		Expect(groups.groupsForApplication(ctx, other)).To(BeEmpty())
		Expect(groups.groupsForApplication(ctx, build("unmanaged-watch", policyCreate, unmanaged))).To(BeEmpty())
	})

	Context("ReadOnly with access type Groups", func() {
		It("reports drift and writes nothing", func() {
			fake.SeedApplication("ro-app", nil)
			oldPK := fake.SeedGroup("old", nil)
			fake.SeedGroup(immichGroup, nil)
			bindGroup("ro-app", oldPK, 0, nil)
			a := newApp("ro-app", policyReadOnly, groupsAccess(immichGroup))

			mustReconcile(a)
			inSync := cond(a, ConditionAccessInSync)
			Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
			Expect(inSync.Reason).To(Equal(reasonDriftDetected))
			Expect(inSync.Message).To(SatisfyAll(
				ContainSubstring(`would bind or re-enable groups ["immich-users"]`),
				ContainSubstring(`would remove group bindings ["old"]`)))
			ready := cond(a, ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(reasonReadOnly))
			Expect(writes()).To(BeEmpty())
		})

		It("reports InSync when the bindings match", func() {
			fake.SeedApplication("ro-sync", nil)
			listedPK := fake.SeedGroup(immichGroup, nil)
			bindGroup("ro-sync", listedPK, 0, nil)
			a := newApp("ro-sync", policyReadOnly, groupsAccess(immichGroup))

			mustReconcile(a)
			inSync := cond(a, ConditionAccessInSync)
			Expect(inSync.Status).To(Equal(metav1.ConditionTrue))
			Expect(inSync.Reason).To(Equal(reasonInSync))
			Expect(writes()).To(BeEmpty())
		})

		It("reports GroupNotFound and claims no drift for an unresolved name", func() {
			fake.SeedApplication("ro-partial", nil)
			oldPK := fake.SeedGroup("old", nil)
			fake.SeedGroup(immichGroup, nil)
			bindGroup("ro-partial", oldPK, 0, nil)
			a := newApp("ro-partial", policyReadOnly, groupsAccess(immichGroup, "ghost"))

			mustReconcile(a)
			inSync := cond(a, ConditionAccessInSync)
			Expect(inSync.Status).To(Equal(metav1.ConditionFalse))
			Expect(inSync.Reason).To(Equal(reasonGroupNotFound))
			Expect(inSync.Message).To(SatisfyAll(
				ContainSubstring(`"ghost"`),
				ContainSubstring(`would bind or re-enable groups ["immich-users"]`),
				Not(ContainSubstring("would remove"))))
			Expect(writes()).To(BeEmpty())
		})

		It("is never gated by WaitingForGroups", func() {
			fake.SeedApplication("ro-ghost", nil)
			a := newApp("ro-ghost", policyReadOnly, groupsAccess("ghost"))

			mustReconcile(a)
			ready := cond(a, ConditionReady)
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			Expect(ready.Reason).To(Equal(reasonReadOnly))
			Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(cond(a, ConditionAccessInSync).Reason).To(Equal(reasonGroupNotFound))
			Expect(writes()).To(BeEmpty())
		})
	})

	It("sends no writes on a second reconcile", func() {
		fake.SeedGroup(immichGroup, nil)
		a := newApp("idempotent", policyCreate, groupsAccess(immichGroup))
		mustReconcile(a)
		n := len(writes())

		mustReconcile(a)
		Expect(writes()).To(HaveLen(n))
	})
})
