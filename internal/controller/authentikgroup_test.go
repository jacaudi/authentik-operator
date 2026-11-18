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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// Names the group specs repeat: Authentik's field names and the attribute they set.
const (
	fieldIsSuperuser = "is_superuser"
	fieldAttributes  = "attributes"
	fieldGroup       = "group"
	attrRole         = "role"
	attrAdmin        = "admin"
	rawAdmin         = `"admin"`
	earlierKey       = "earlier-key"
)

var _ = Describe("AuthentikGroup", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake     *fakeauthentik.Server
		recorder *events.FakeRecorder
		groups   *AuthentikGroupReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		recorder = events.NewFakeRecorder(100)
		groups = &AuthentikGroupReconciler{
			Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: &connection.Resolver{Reader: k8sClient,
				Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1}},
			Recorder: recorder, APIReader: k8sClient,
			AdoptionNamespaceSelector: labels.Everything(), IdentityNamespaceSelector: labels.Everything(),
		}
	})
	AfterEach(func() { fake.Close() })

	const (
		policyCreate        = authentikiov1alpha1.ManagementPolicyCreate
		policyAdopt         = authentikiov1alpha1.ManagementPolicyAdopt
		policyAdoptOrCreate = authentikiov1alpha1.ManagementPolicyAdoptOrCreate
		policyReadOnly      = authentikiov1alpha1.ManagementPolicyReadOnly
	)

	cleanup := func(obj client.Object) {
		DeferCleanup(func() {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err == nil {
				obj.SetFinalizers(nil)
				_ = k8sClient.Update(ctx, obj)
				_ = k8sClient.Delete(ctx, obj)
			}
		})
	}
	newGroup := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		mutate func(*authentikiov1alpha1.AuthentikGroupSpec)) *authentikiov1alpha1.AuthentikGroup {
		g := &authentikiov1alpha1.AuthentikGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikGroupSpec{
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy},
			},
		}
		if mutate != nil {
			mutate(&g.Spec)
		}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		cleanup(g)
		return g
	}
	named := func(name string) func(*authentikiov1alpha1.AuthentikGroupSpec) {
		return func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.Name = name }
	}
	withAttributes := func(raw map[string]string) func(*authentikiov1alpha1.AuthentikGroupSpec) {
		return func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.Attributes = rawAttributes(raw) }
	}
	reconcileGroup := func(g *authentikiov1alpha1.AuthentikGroup) (reconcile.Result, error) {
		return groups.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	}
	mustReconcile := func(g *authentikiov1alpha1.AuthentikGroup) {
		_, err := reconcileGroup(g)
		Expect(err).NotTo(HaveOccurred())
	}
	get := func(g *authentikiov1alpha1.AuthentikGroup) *authentikiov1alpha1.AuthentikGroup {
		var cur authentikiov1alpha1.AuthentikGroup
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(g), &cur)).To(Succeed())
		return &cur
	}
	ready := func(g *authentikiov1alpha1.AuthentikGroup) *metav1.Condition {
		return meta.FindStatusCondition(get(g).Status.Conditions, ConditionReady)
	}
	drifted := func(g *authentikiov1alpha1.AuthentikGroup) *metav1.Condition {
		return meta.FindStatusCondition(get(g).Status.Conditions, ConditionDrifted)
	}
	gone := func(g *authentikiov1alpha1.AuthentikGroup) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(g), &authentikiov1alpha1.AuthentikGroup{}))
	}
	writes := func() []fakeauthentik.Request { return fake.Requests(true) }
	setStatus := func(g *authentikiov1alpha1.AuthentikGroup, mutate func(*authentikiov1alpha1.AuthentikGroupStatus)) {
		cur := get(g)
		mutate(&cur.Status)
		Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
	}
	// claimStatus makes g hold pk with the given provenance, as a reconcile would have left it.
	claimStatus := func(g *authentikiov1alpha1.AuthentikGroup, pk string, prov authentikiov1alpha1.Provenance) {
		setStatus(g, func(s *authentikiov1alpha1.AuthentikGroupStatus) {
			s.GroupPK, s.Name, s.Provenance = pk, groupName(g), prov
		})
	}
	setSpec := func(g *authentikiov1alpha1.AuthentikGroup, mutate func(*authentikiov1alpha1.AuthentikGroupSpec)) {
		cur := get(g)
		mutate(&cur.Spec)
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
	}
	drain := func() []string { return drainEvents(recorder) }
	pkOf := func(g *authentikiov1alpha1.AuthentikGroup) string { return get(g).Status.GroupPK }
	usedByRequests := func() int {
		n := 0
		for _, r := range fake.Requests(false) {
			if strings.Contains(r.Path, "/used_by/") {
				n++
			}
		}
		return n
	}

	It("creates the group with the owner proof and records Created", func() {
		g := newGroup("team-a", policyCreate, nil)
		mustReconcile(g)

		live := fake.GroupByName("team-a")
		Expect(live).NotTo(BeNil())
		Expect(live[fieldAttributes]).To(Equal(map[string]any{
			authentik.OwnerAttribute: map[string]any{authentik.OwnerUIDKey: string(get(g).UID)}}))
		Expect(live[fieldIsSuperuser]).To(BeFalse())
		cur := get(g)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(cur.Status.GroupPK).To(Equal(live["pk"]))
		Expect(cur.Status.Name).To(Equal("team-a"))
		Expect(cur.Status.PendingCreate).To(BeEmpty())
		Expect(ready(g).Status).To(Equal(metav1.ConditionTrue))
		Expect(ready(g).Reason).To(Equal(reasonSynced))
		Expect(writes()).To(HaveLen(1))
		Expect(writes()[0].Body).To(HaveKey(jsonName))
		Expect(writes()[0].Body).NotTo(HaveKey("users"))
	})

	It("keeps the owner proof next to the spec attributes on create", func() {
		g := newGroup("with-attrs", policyCreate, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
			withAttributes(map[string]string{attrRole: rawAdmin})(s)
			s.IsSuperuser = new(true)
		})
		mustReconcile(g)
		live := fake.GroupByName("with-attrs")
		Expect(live[fieldAttributes]).To(HaveKeyWithValue(attrRole, attrAdmin))
		Expect(live[fieldAttributes]).To(HaveKey("authentik_operator"))
		Expect(live[fieldIsSuperuser]).To(BeTrue())
		Expect(get(g).Status.ManagedAttributes).To(Equal([]string{attrRole}))
		Expect(writes()[0].Body).NotTo(HaveKey("users"))
	})

	It("stays a Conflict under Create when a group with the name exists", func() {
		fake.SeedGroup("team-b", nil)
		g := newGroup("team-b", policyCreate, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonConflict))
		Expect(ready(g).Message).To(ContainSubstring("spec.managementPolicy"))
		Expect(get(g).Status.Provenance).To(BeEmpty(), "never infer Created for a fresh CR that found a group")
		Expect(writes()).To(BeEmpty())
	})

	It("reports several groups with the name as a Conflict", func() {
		fake.SeedGroup("twin", nil)
		fake.SeedGroup("twin", nil)
		g := newGroup("twin", policyAdopt, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonConflict))
		Expect(get(g).Status.GroupPK).To(BeEmpty())
		Expect(writes()).To(BeEmpty())
	})

	It("adopts by name and patches only what the spec sets", func() {
		fake.SeedGroup("legacy", map[string]any{fieldIsSuperuser: true, fieldAttributes: map[string]any{"legacy": "x"}})
		g := newGroup("legacy", policyAdopt, withAttributes(map[string]string{attrRole: rawAdmin}))
		mustReconcile(g)

		live := fake.GroupByName("legacy")
		Expect(live[fieldIsSuperuser]).To(BeTrue())
		Expect(live[fieldAttributes]).To(Equal(map[string]any{"legacy": "x", attrRole: attrAdmin}))
		cur := get(g)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(cur.Status.GroupPK).To(Equal(live["pk"]))
		Expect(cur.Status.ManagedAttributes).To(Equal([]string{attrRole}))
		Expect(writes()).To(HaveLen(1))
		Expect(writes()[0].Method).To(Equal(http.MethodPatch))
		for _, untouched := range []string{"users", fieldIsSuperuser, jsonName, "parents", "roles"} {
			Expect(writes()[0].Body).NotTo(HaveKey(untouched))
		}
	})

	It("holds isSuperuser to false on a group it created when the spec leaves it unset", func() {
		g := newGroup("held", policyCreate, nil)
		mustReconcile(g)
		fake.SetGroupField(pkOf(g), fieldIsSuperuser, true)

		mustReconcile(g)
		Expect(fake.Group(pkOf(g))[fieldIsSuperuser]).To(BeFalse())
		Expect(drain()).To(ContainElement(ContainSubstring("Normal Updated")))
		last := writes()[len(writes())-1]
		Expect(last.Method).To(Equal(http.MethodPatch))
		Expect(last.Body).NotTo(HaveKey("users"))
	})

	It("reports AdoptionTargetNotFound under Adopt when nothing matches and writes nothing", func() {
		g := newGroup("nobody", policyAdopt, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(ready(g).Message).To(ContainSubstring(`"nobody"`))
		Expect(ready(g).Message).To(ContainSubstring("AdoptOrCreate"))
		Expect(writes()).To(BeEmpty())
		Expect(get(g).Status.GroupPK).To(BeEmpty())
	})

	It("creates under AdoptOrCreate when nothing matches, with a NothingToAdopt event", func() {
		g := newGroup("fresh", policyAdoptOrCreate, nil)
		mustReconcile(g)
		Expect(drain()).To(ContainElement(ContainSubstring("Normal NothingToAdopt")))
		Expect(fake.GroupByName("fresh")[fieldAttributes]).To(HaveKey("authentik_operator"))
		Expect(get(g).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("reports in a dry run the fields and attribute keys adoption would change and writes nothing", func() {
		fake.SeedGroup("dry", map[string]any{fieldIsSuperuser: true})
		g := newGroup("dry", policyAdopt, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
			withAttributes(map[string]string{attrRole: rawAdmin})(s)
			s.DryRun = true
			s.IsSuperuser = new(false)
		})
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready(g).Message).To(ContainSubstring("is_superuser: true -> false"))
		Expect(ready(g).Message).To(ContainSubstring(attrRole))
		Expect(writes()).To(BeEmpty())
		Expect(get(g).Status.Provenance).To(BeEmpty())
	})

	It("reports a would-create in a dry run with nothing to adopt under AdoptOrCreate", func() {
		g := newGroup("dry-new", policyAdoptOrCreate, func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.DryRun = true })
		mustReconcile(g)
		Expect(ready(g).Status).To(Equal(metav1.ConditionFalse))
		Expect(ready(g).Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready(g).Message).To(ContainSubstring("would be created"))
		Expect(ready(g).Message).To(ContainSubstring("nothing was written"))
		Expect(writes()).To(BeEmpty())
		Expect(fake.GroupCount()).To(BeZero())
	})

	// longAttributes seeds a group whose live attributes run past the 100 characters a rendered value is cut at,
	// with a value that belongs to nobody the operator knows.
	longAttributes := func() map[string]any {
		live := map[string]any{}
		for _, k := range []string{"a00", "a01", "a02", "a03", "a04", "a05", "a06", "a07", "a08", "a09"} {
			live[k] = "foreign-value-of-" + k
		}
		return live
	}
	const lastKey = "zz-last"

	It("names the attribute keys a dry run would set, however long the live attributes are, and hides foreign values",
		func() {
			fake.SeedGroup("dry-long", map[string]any{fieldAttributes: longAttributes()})
			g := newGroup("dry-long", policyAdopt, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
				withAttributes(map[string]string{lastKey: rawAdmin})(s)
				s.DryRun = true
			})
			mustReconcile(g)
			Expect(ready(g).Reason).To(Equal(reasonAdoptionDryRun))
			Expect(ready(g).Message).To(ContainSubstring(lastKey))
			Expect(ready(g).Message).NotTo(ContainSubstring("foreign-value-of-"))
			Expect(writes()).To(BeEmpty())
		})

	It("does not report a key from an earlier claim as removed in a dry run", func() {
		fake.SeedGroup("dry-stale", map[string]any{fieldAttributes: map[string]any{"stale-key": "x"}})
		g := newGroup("dry-stale", policyAdopt, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
			withAttributes(map[string]string{attrRole: rawAdmin})(s)
			s.DryRun = true
		})
		setStatus(g, func(s *authentikiov1alpha1.AuthentikGroupStatus) { s.ManagedAttributes = []string{"stale-key"} })

		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready(g).Message).To(ContainSubstring(attrRole))
		Expect(ready(g).Message).NotTo(ContainSubstring("stale-key"), "adoption keeps every live key")
	})

	It("names the attribute keys that drift under ReadOnly, however long the live attributes are", func() {
		fake.SeedGroup("ro-long", map[string]any{fieldAttributes: longAttributes()})
		g := newGroup("ro-long", policyReadOnly, withAttributes(map[string]string{lastKey: rawAdmin}))
		mustReconcile(g)
		Expect(drifted(g).Status).To(Equal(metav1.ConditionTrue))
		Expect(drifted(g).Message).To(ContainSubstring(lastKey))
		Expect(drifted(g).Message).NotTo(ContainSubstring("foreign-value-of-"))
		Expect(writes()).To(BeEmpty())
	})

	It("keeps every live key on adoption although status still lists keys from an earlier claim", func() {
		fake.SeedGroup("stale-owned", map[string]any{fieldAttributes: map[string]any{earlierKey: "x", "other": "y"}})
		g := newGroup("stale-owned", policyAdopt, withAttributes(map[string]string{attrRole: rawAdmin}))
		setStatus(g, func(s *authentikiov1alpha1.AuthentikGroupStatus) { s.ManagedAttributes = []string{earlierKey} })

		mustReconcile(g)
		Expect(get(g).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(fake.GroupByName("stale-owned")[fieldAttributes]).To(Equal(
			map[string]any{earlierKey: "x", "other": "y", attrRole: attrAdmin}))
		Expect(get(g).Status.ManagedAttributes).To(Equal([]string{attrRole}))
	})

	It("observes under ReadOnly, reports drift in Drifted and never writes", func() {
		fake.SeedGroup("ro", map[string]any{fieldIsSuperuser: true})
		g := newGroup("ro", policyReadOnly, func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.IsSuperuser = new(false) })
		mustReconcile(g)
		Expect(ready(g).Status).To(Equal(metav1.ConditionTrue))
		Expect(ready(g).Reason).To(Equal(reasonReadOnly))
		Expect(drifted(g).Status).To(Equal(metav1.ConditionTrue))
		Expect(drifted(g).Message).To(ContainSubstring(fieldIsSuperuser))
		Expect(writes()).To(BeEmpty())
	})

	It("reports a ReadOnly resource with nothing to observe as AdoptionTargetNotFound", func() {
		g := newGroup("ro-none", policyReadOnly, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(writes()).To(BeEmpty())
	})

	It("recovers its own create whose response was lost as Created", func() {
		fake.LoseNextGroupCreate()
		g := newGroup("lost", policyCreate, nil)
		_, err := reconcileGroup(g)
		Expect(err).To(HaveOccurred())
		Expect(get(g).Status.PendingCreate).To(Equal("lost"))

		mustReconcile(g)
		cur := get(g)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(cur.Status.GroupPK).NotTo(BeEmpty())
		Expect(cur.Status.PendingCreate).To(BeEmpty())
		Expect(drain()).To(ContainElement(ContainSubstring("Normal Recovered")))
		Expect(fake.GroupCount()).To(Equal(1))
	})

	It("treats a group found during a pending create without the proof as a Conflict under every policy", func() {
		g := newGroup("squat", policyAdoptOrCreate, nil)
		setStatus(g, func(s *authentikiov1alpha1.AuthentikGroupStatus) { s.PendingCreate = "squat" })
		fake.SeedGroup("squat", nil)

		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonConflict))
		Expect(ready(g).Message).To(ContainSubstring("clear status.pendingCreate"))
		cur := get(g)
		Expect(cur.Status.Provenance).To(BeEmpty())
		Expect(cur.Status.PendingCreate).To(Equal("squat"))
		Expect(writes()).To(BeEmpty())
	})

	It("recreates a group it created that vanished", func() {
		g := newGroup("vanishing", policyCreate, nil)
		mustReconcile(g)
		old := pkOf(g)
		fake.DeleteGroup(old)

		mustReconcile(g)
		Expect(pkOf(g)).NotTo(Equal(old))
		Expect(pkOf(g)).NotTo(BeEmpty())
		Expect(drain()).To(ContainElement(ContainSubstring("Warning GroupMissing")))
		Expect(get(g).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("never recreates an adopted group that vanished", func() {
		fake.SeedGroup("adopted-gone", nil)
		g := newGroup("adopted-gone", policyAdopt, nil)
		mustReconcile(g)
		old := pkOf(g)
		fake.DeleteGroup(old)

		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(pkOf(g)).To(Equal(old))
		Expect(fake.GroupCount()).To(BeZero())
	})

	It("refuses a changed spec.name once the group is tracked and writes nothing", func() {
		g := newGroup("rename-me", policyCreate, nil)
		mustReconcile(g)
		before := len(writes())
		setSpec(g, named("renamed"))

		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonInvalidSpec))
		Expect(ready(g).Message).To(ContainSubstring(`"rename-me"`))
		Expect(writes()).To(HaveLen(before))
	})

	It("restores a name changed in the Authentik UI", func() {
		g := newGroup("ui-restore", policyCreate, nil)
		mustReconcile(g)
		fake.SetGroupField(pkOf(g), jsonName, "ui-name")

		mustReconcile(g)
		Expect(fake.Group(pkOf(g))[jsonName]).To(Equal("ui-restore"))
		last := writes()[len(writes())-1]
		Expect(last.Method).To(Equal(http.MethodPatch))
		Expect(last.Body).To(HaveKey(jsonName))
	})

	It("refuses to adopt a group another resource already tracks", func() {
		fake.SeedGroup("shared", nil)
		first := newGroup("shared-first", policyAdopt, named("shared"))
		mustReconcile(first)
		second := newGroup("shared-second", policyAdopt, named("shared"))

		mustReconcile(second)
		Expect(ready(second).Reason).To(Equal(reasonConflict))
		Expect(ready(second).Message).To(ContainSubstring("already managed by default/shared-first"))
		Expect(get(second).Status.GroupPK).To(BeEmpty())
		Expect(get(second).Status.Provenance).To(BeEmpty())
	})

	It("settles two holders: Created beats Adopted, then the lower UID", func() {
		a := newGroup("holder-a", policyCreate, nil)
		mustReconcile(a)
		pk := pkOf(a)
		b := newGroup("holder-b", policyAdopt, named("holder-a"))
		claimStatus(b, pk, authentikiov1alpha1.ProvenanceAdopted)
		setStatus(b, func(s *authentikiov1alpha1.AuthentikGroupStatus) { s.ManagedAttributes = []string{"stale"} })

		mustReconcile(b)
		Expect(ready(b).Reason).To(Equal(reasonConflict))
		Expect(ready(b).Message).To(ContainSubstring("default/holder-a"))
		Expect(ready(b).Message).To(ContainSubstring("takes precedence"))
		Expect(get(b).Status.GroupPK).To(BeEmpty())
		Expect(get(b).Status.Provenance).To(BeEmpty())
		Expect(get(b).Status.ManagedAttributes).To(BeEmpty(), "a released claim forgets the keys it owned")
		Expect(pkOf(a)).To(Equal(pk))
		Expect(get(a).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))

		By("two Adopted holders: the higher UID releases, the lower keeps it")
		seeded := fake.SeedGroup("two-adopters", nil)
		mk := func(name string) *authentikiov1alpha1.AuthentikGroup {
			x := newGroup(name, policyAdopt, named("two-adopters"))
			claimStatus(x, seeded, authentikiov1alpha1.ProvenanceAdopted)
			return x
		}
		x, y := mk("adopter-x"), mk("adopter-y")
		low, high := x, y
		if get(x).UID > get(y).UID {
			low, high = y, x
		}
		mustReconcile(high)
		Expect(ready(high).Reason).To(Equal(reasonConflict))
		Expect(get(high).Status.GroupPK).To(BeEmpty())
		mustReconcile(low)
		Expect(ready(low).Status).To(Equal(metav1.ConditionTrue))
		Expect(pkOf(low)).To(Equal(seeded))
	})

	It("re-checks a fresh claim through the API reader when the cache missed the other holder", func() {
		pk := fake.SeedGroup("raced", nil)
		holder := newGroup("raced-holder", policyAdopt, named("raced"))
		claimStatus(holder, pk, authentikiov1alpha1.ProvenanceCreated)
		adopter := newGroup("raced-adopter", policyAdopt, named("raced"))
		groups.Client = staleListClient{k8sClient} // the cache has not seen the holder's claim yet

		mustReconcile(adopter)
		Expect(ready(adopter).Reason).To(Equal(reasonConflict))
		Expect(get(adopter).Status.GroupPK).To(BeEmpty())
		Expect(writes()).To(BeEmpty(), "the loser must yield before it writes to Authentik")
	})

	It("reports a reserved attribute key as InvalidSpec naming the key and writes no attributes", func() {
		for _, key := range []string{reservedSettingsKey, reservedOwnerKey, "goauthentik.io/x"} {
			name := "reserved-" + strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(key)
			fake.SeedGroup(name, nil)
			g := newGroup(name, policyAdopt,
				withAttributes(map[string]string{key: `"x"`, attrRole: rawAdmin}))
			mustReconcile(g)
			Expect(ready(g).Reason).To(Equal(reasonInvalidSpec), key)
			Expect(ready(g).Message).To(ContainSubstring(`"`+key+`"`), key)
			Expect(ready(g).Message).To(ContainSubstring("reserved"), key)
			Expect(get(g).Status.GroupPK).To(BeEmpty(), key)
		}
		Expect(fake.Requests(false)).To(BeEmpty(), "nothing is read or written for a spec that is invalid on its face")
	})

	It("reports an attribute whose value is an object as InvalidSpec", func() {
		g := newGroup("nested-attr", policyCreate, withAttributes(map[string]string{"nested": `{"a": 1}`}))
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonInvalidSpec))
		Expect(ready(g).Message).To(ContainSubstring(`"nested"`))
		Expect(writes()).To(BeEmpty())
	})

	It("removes every owned key with attributes: {} and sends an explicit empty object", func() { // Review Focus 1
		fake.SeedGroup("own", map[string]any{fieldAttributes: map[string]any{"a": "1"}})
		g := newGroup("own", policyAdopt, withAttributes(map[string]string{"a": `"1"`}))
		mustReconcile(g)
		Expect(get(g).Status.ManagedAttributes).To(Equal([]string{"a"}))

		setSpec(g, func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.Attributes = map[string]apiextensionsv1.JSON{} })
		cur := get(g)
		Expect(cur.Spec.Attributes).NotTo(BeNil(), "the CRD must keep an explicit empty map distinct from an absent one")
		Expect(cur.Spec.Attributes).To(BeEmpty())

		mustReconcile(g)
		last := writes()[len(writes())-1]
		Expect(last.Method).To(Equal(http.MethodPatch))
		Expect(last.Body).To(HaveKeyWithValue(fieldAttributes, Equal(map[string]any{})))
		Expect(fake.GroupByName("own")[fieldAttributes]).To(BeEmpty())
		Expect(get(g).Status.ManagedAttributes).To(BeEmpty())
	})

	It("releases owned keys without deleting them when spec.attributes is removed", func() {
		fake.SeedGroup("release", map[string]any{fieldAttributes: map[string]any{"a": "1"}})
		g := newGroup("release", policyAdopt, withAttributes(map[string]string{"a": `"1"`}))
		mustReconcile(g)
		Expect(get(g).Status.ManagedAttributes).To(Equal([]string{"a"}))
		before := len(writes())

		setSpec(g, func(s *authentikiov1alpha1.AuthentikGroupSpec) { s.Attributes = nil })
		mustReconcile(g)
		for _, w := range writes()[before:] {
			Expect(w.Body).NotTo(HaveKey(fieldAttributes))
		}
		Expect(fake.GroupByName("release")[fieldAttributes]).To(HaveKeyWithValue("a", "1"))
		Expect(get(g).Status.ManagedAttributes).To(BeEmpty())
	})

	It("reports an Authentik attribute rejection as InvalidSpec with the 400 message", func() {
		fake.SeedGroup("rejected", nil)
		g := newGroup("rejected", policyAdopt, withAttributes(map[string]string{attrRole: rawAdmin}))
		fake.FailNextWith(http.MethodPatch, http.StatusBadRequest,
			map[string]any{fieldAttributes: []string{"value does not match schema"}})

		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonInvalidSpec))
		Expect(ready(g).Message).To(ContainSubstring("value does not match schema"))
		Expect(drain()).To(ContainElement(ContainSubstring("Warning InvalidSpec")))
	})

	It("sends no writes on a second reconcile", func() {
		g := newGroup("steady", policyCreate, withAttributes(map[string]string{attrRole: rawAdmin}))
		mustReconcile(g)
		n := len(writes())
		mustReconcile(g)
		Expect(writes()).To(HaveLen(n))
		Expect(ready(g).Status).To(Equal(metav1.ConditionTrue))
	})

	It("refuses every policy outside the identity selector and reads nothing", func() {
		groups.IdentityNamespaceSelector = labels.Nothing()
		for _, policy := range []authentikiov1alpha1.ManagementPolicy{
			policyCreate, policyAdopt, policyAdoptOrCreate, policyReadOnly} {
			g := newGroup("gated-"+strings.ToLower(string(policy)), policy, nil)
			mustReconcile(g)
			Expect(ready(g).Reason).To(Equal(reasonIdentityNotAllowed), string(policy))
		}
		Expect(fake.Requests(false)).To(BeEmpty())
	})

	It("checks the identity gate before the adoption gate", func() {
		groups.IdentityNamespaceSelector = labels.Nothing()
		groups.AdoptionNamespaceSelector = labels.Nothing()
		g := newGroup("both-gated", policyAdopt, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonIdentityNotAllowed))
	})

	It("applies the adoption gate when the identity gate passes", func() {
		groups.AdoptionNamespaceSelector = labels.Nothing()
		g := newGroup("adoption-gated", policyAdopt, nil)
		mustReconcile(g)
		Expect(ready(g).Reason).To(Equal(reasonAdoptionNotAllowed))
	})

	Context("deletion", func() {
		del := func(g *authentikiov1alpha1.AuthentikGroup) {
			Expect(k8sClient.Delete(ctx, g)).To(Succeed())
		}

		It("deletes a group it created when nothing binds it", func() {
			g := newGroup("del-created", policyCreate, nil)
			mustReconcile(g)
			pk := pkOf(g)
			del(g)

			mustReconcile(g)
			Expect(fake.Group(pk)).To(BeNil())
			Expect(gone(g)).To(BeTrue())
			Expect(drain()).To(ContainElement(ContainSubstring("Normal Deleted")))
		})

		It("retains an adopted group by default", func() {
			pk := fake.SeedGroup("del-adopted", nil)
			g := newGroup("del-adopted", policyAdopt, nil)
			mustReconcile(g)
			del(g)

			mustReconcile(g)
			Expect(fake.Group(pk)).NotTo(BeNil())
			Expect(gone(g)).To(BeTrue())
			Expect(drain()).To(ContainElement(ContainSubstring("Normal Retained")))
		})

		It("blocks deletion while a policy binding uses the group, quoting used_by", func() {
			g := newGroup("immich-users", policyCreate, nil)
			mustReconcile(g)
			pk := pkOf(g)
			fake.SeedApplication("immich", nil)
			appPK, ok := fake.Application("immich")["pk"].(string)
			Expect(ok).To(BeTrue())
			bindingPK := fake.SeedBinding(appPK, map[string]any{fieldGroup: pk, "order": float64(0)})
			del(g)

			res, err := reconcileGroup(g)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(controllerutil.ContainsFinalizer(get(g), FinalizerName)).To(BeTrue())
			Expect(ready(g).Status).To(Equal(metav1.ConditionFalse))
			Expect(ready(g).Reason).To(Equal(reasonDeletionBlocked))
			Expect(ready(g).Message).To(ContainSubstring(`"Binding from immich #0 to Group immich-users"`))
			Expect(ready(g).Message).To(ContainSubstring("deletionPolicy: Retain"))
			Expect(fake.Group(pk)).NotTo(BeNil())
			Expect(drain()).To(ContainElement(ContainSubstring("Warning DeletionBlocked")))

			fake.DeleteBinding(bindingPK)
			mustReconcile(g)
			Expect(fake.Group(pk)).To(BeNil())
			Expect(gone(g)).To(BeTrue())
		})

		It("retains without deleting when the token user is not a superuser", func() {
			fake.SetTokenUserSuperuser(false)
			g := newGroup("unverifiable", policyCreate, nil)
			mustReconcile(g)
			pk := pkOf(g)
			del(g)

			mustReconcile(g)
			Expect(fake.Group(pk)).NotTo(BeNil())
			Expect(gone(g)).To(BeTrue())
			Expect(drain()).To(ContainElement(And(ContainSubstring("Warning RetainedUnverifiable"),
				ContainSubstring("delete it in Authentik after checking its bindings"))))
			Expect(usedByRequests()).To(BeZero())
		})

		It("never blocks Retain", func() {
			g := newGroup("retain-bound", policyCreate, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			})
			mustReconcile(g)
			pk := pkOf(g)
			fake.SeedApplication("retain-app", nil)
			appPK, ok := fake.Application("retain-app")["pk"].(string)
			Expect(ok).To(BeTrue())
			fake.SeedBinding(appPK, map[string]any{fieldGroup: pk, "order": float64(0)})
			del(g)

			mustReconcile(g)
			Expect(fake.Group(pk)).NotTo(BeNil())
			Expect(gone(g)).To(BeTrue())
			Expect(usedByRequests()).To(BeZero())
		})

		It("keeps the resource terminating and deletes nothing when used_by fails", func() { // Review Focus 2
			g := newGroup("used-by-down", policyCreate, nil)
			mustReconcile(g)
			pk := pkOf(g)
			fake.FailNextOn(http.MethodGet, "/used_by/", http.StatusServiceUnavailable)
			del(g)

			_, err := reconcileGroup(g)
			Expect(err).To(HaveOccurred())
			Expect(controllerutil.ContainsFinalizer(get(g), FinalizerName)).To(BeTrue())
			Expect(fake.Group(pk)).NotTo(BeNil())
			Expect(ready(g).Reason).To(Equal(reasonAuthentikError))
		})

		It("deletes only a Created group outside the identity selector", func() {
			created := newGroup("outside-created", policyCreate, nil)
			mustReconcile(created)
			createdPK := pkOf(created)
			adoptedPK := fake.SeedGroup("outside-adopted", nil)
			adopted := newGroup("outside-adopted", policyAdopt, func(s *authentikiov1alpha1.AuthentikGroupSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			})
			mustReconcile(adopted)
			groups.IdentityNamespaceSelector = labels.Nothing()
			del(created)
			del(adopted)

			mustReconcile(created)
			Expect(fake.Group(createdPK)).To(BeNil())
			mustReconcile(adopted)
			Expect(fake.Group(adoptedPK)).NotTo(BeNil())
			Expect(gone(adopted)).To(BeTrue())
			Expect(drain()).To(ContainElement(And(ContainSubstring("Warning Retained"),
				ContainSubstring("--default-connection-identity-namespace-selector"))))
		})
	})
})
