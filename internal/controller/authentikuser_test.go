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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// Names the user specs repeat: Authentik's field names, the attributes
// firstName and lastName own, and the usernames several specs seed.
const (
	fieldIsActive = "is_active"
	fieldEmail    = "email"
	fieldType     = "type"
	bodyUsers     = "users"
	bodyRoles     = "roles"
	attrForeign   = "foreign"
	rawX          = `"x"`
	externalSSO   = "https://sso.example.com"
	usernameDave  = "dave"
	displayDave   = "Dave"
	displayAlice  = "Alice"
)

var _ = Describe("AuthentikUser", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake     *fakeauthentik.Server
		recorder *events.FakeRecorder
		users    *AuthentikUserReconciler
		logs     *bytes.Buffer
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		recorder = events.NewFakeRecorder(100)
		logs = &bytes.Buffer{}
		users = &AuthentikUserReconciler{
			Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: &connection.Resolver{Reader: k8sClient, DefaultExternalURL: externalSSO,
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
	invite := func() *authentikiov1alpha1.InvitationSpec {
		return &authentikiov1alpha1.InvitationSpec{Flow: fakeauthentik.FlowEnrollmentSlug}
	}
	// userSpec is a spec for username that satisfies the admission rule: an invitation exactly when the policy may create.
	userSpec := func(username string, policy authentikiov1alpha1.ManagementPolicy,
		mutate func(*authentikiov1alpha1.AuthentikUserSpec)) authentikiov1alpha1.AuthentikUserSpec {
		spec := authentikiov1alpha1.AuthentikUserSpec{
			APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy}, Username: username,
		}
		if policy == policyCreate || policy == policyAdoptOrCreate {
			spec.Invitation = invite()
		}
		if mutate != nil {
			mutate(&spec)
		}
		return spec
	}
	tryCreate := func(name string, spec authentikiov1alpha1.AuthentikUserSpec) error {
		u := &authentikiov1alpha1.AuthentikUser{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		err := k8sClient.Create(ctx, u)
		if err == nil {
			cleanup(u)
		}
		return err
	}
	newUser := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		mutate func(*authentikiov1alpha1.AuthentikUserSpec)) *authentikiov1alpha1.AuthentikUser {
		u := &authentikiov1alpha1.AuthentikUser{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: userSpec(name, policy, mutate),
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		cleanup(u)
		// There is no garbage collector in envtest: the invitation Secret outlives its owner, and a
		// later spec reusing the name would find a Secret it does not control.
		DeferCleanup(func() {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-invitation", Namespace: namespace}}
			_ = k8sClient.Delete(ctx, secret)
		})
		return u
	}
	// usernamed makes a spec's username differ from its resource's name, so several resources can name one person.
	usernamed := func(username string) func(*authentikiov1alpha1.AuthentikUserSpec) {
		return func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Username = username }
	}
	withAttrs := func(raw map[string]string) func(*authentikiov1alpha1.AuthentikUserSpec) {
		return func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Attributes = rawAttributes(raw) }
	}
	reconcileUser := func(u *authentikiov1alpha1.AuthentikUser) (reconcile.Result, error) {
		// The logger writes to logs, so a spec can assert what the reconciler logged.
		logCtx := logf.IntoContext(ctx, zap.New(zap.WriteTo(logs), zap.UseDevMode(true)))
		return users.Reconcile(logCtx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(u)})
	}
	mustReconcile := func(u *authentikiov1alpha1.AuthentikUser) {
		_, err := reconcileUser(u)
		Expect(err).NotTo(HaveOccurred())
	}
	get := func(u *authentikiov1alpha1.AuthentikUser) *authentikiov1alpha1.AuthentikUser {
		var cur authentikiov1alpha1.AuthentikUser
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), &cur)).To(Succeed())
		return &cur
	}
	ready := func(u *authentikiov1alpha1.AuthentikUser) *metav1.Condition {
		return meta.FindStatusCondition(get(u).Status.Conditions, ConditionReady)
	}
	drifted := func(u *authentikiov1alpha1.AuthentikUser) *metav1.Condition {
		return meta.FindStatusCondition(get(u).Status.Conditions, ConditionDrifted)
	}
	gone := func(u *authentikiov1alpha1.AuthentikUser) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), &authentikiov1alpha1.AuthentikUser{}))
	}
	writes := func() []fakeauthentik.Request { return fake.Requests(true) }
	setStatus := func(u *authentikiov1alpha1.AuthentikUser, mutate func(*authentikiov1alpha1.AuthentikUserStatus)) {
		cur := get(u)
		mutate(&cur.Status)
		Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
	}
	claimStatus := func(u *authentikiov1alpha1.AuthentikUser, pk int32, prov authentikiov1alpha1.Provenance) {
		setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) { s.UserPK, s.Provenance = pk, prov })
	}
	setSpec := func(u *authentikiov1alpha1.AuthentikUser, mutate func(*authentikiov1alpha1.AuthentikUserSpec)) {
		cur := get(u)
		mutate(&cur.Spec)
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
	}
	drain := func() []string { return drainEvents(recorder) }
	pkOf := func(u *authentikiov1alpha1.AuthentikUser) int32 { return get(u).Status.UserPK }
	secretOf := func(name string) (*corev1.Secret, error) {
		var s corev1.Secret
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s)
		return &s, err
	}
	// proof is the attributes an account enrolled through u's invitation carries.
	proof := func(u *authentikiov1alpha1.AuthentikUser) map[string]any {
		return authentik.OwnerProof(string(get(u).UID))
	}
	invitationOf := func(u *authentikiov1alpha1.AuthentikUser) string {
		return invitationName(get(u).UID, 1)
	}
	// enrol issues u's invitation and enrols the person through it, then reconciles: u tracks the new account as Created.
	enrol := func(u *authentikiov1alpha1.AuthentikUser) int32 {
		mustReconcile(u)
		Expect(fake.EnrolFromInvitation(invitationOf(u), "")).NotTo(BeZero())
		mustReconcile(u)
		Expect(get(u).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		return pkOf(u)
	}

	Context("admission", func() {
		It("rejects an invitation under Adopt and its absence under Create", func() {
			adopt := userSpec("adm-adopt", policyAdopt, nil)
			adopt.Invitation = invite()
			create := userSpec("adm-create", policyCreate, nil)
			create.Invitation = nil
			for name, spec := range map[string]authentikiov1alpha1.AuthentikUserSpec{"adm-adopt": adopt, "adm-create": create} {
				err := tryCreate(name, spec)
				Expect(err).To(HaveOccurred(), name)
				Expect(err.Error()).To(ContainSubstring("invitation is required when managementPolicy may create"), name)
			}
			Expect(tryCreate("adm-aoc", userSpec("adm-aoc", policyAdoptOrCreate, nil))).To(Succeed())
			Expect(tryCreate("adm-adopt-ok", userSpec("adm-adopt-ok", policyAdopt, nil))).To(Succeed())
		})

		It("rejects a zero or negative expiresAfter", func() { // Review Focus 5
			for name, d := range map[string]time.Duration{"adm-zero": 0, "adm-negative": -time.Hour} {
				spec := userSpec(name, policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
					s.Invitation.ExpiresAfter = &metav1.Duration{Duration: d}
				})
				err := tryCreate(name, spec)
				Expect(err).To(HaveOccurred(), name)
				Expect(err.Error()).To(ContainSubstring("expiresAfter must be positive"), name)
			}
			ok := userSpec("adm-week", policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.Invitation.ExpiresAfter = &metav1.Duration{Duration: 168 * time.Hour}
			})
			Expect(tryCreate("adm-week", ok)).To(Succeed())
		})

		It("rejects a changed username", func() {
			u := newUser("adm-rename", policyCreate, nil)
			cur := get(u)
			cur.Spec.Username = "someone-else"
			err := k8sClient.Update(ctx, cur)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("username is immutable"))
		})
	})

	It("reports reserved attribute keys (given_name, family_name, settings) as InvalidSpec and reads nothing", func() {
		for _, key := range []string{attrGivenName, attrFamilyName, authentik.ReservedSettingsKey} {
			u := newUser("reserved-"+strings.ReplaceAll(key, "_", "-"), policyAdopt,
				withAttrs(map[string]string{key: rawX, attrRole: rawAdmin}))
			mustReconcile(u)
			Expect(ready(u).Reason).To(Equal(reasonInvalidSpec), key)
			Expect(ready(u).Message).To(ContainSubstring(`"`+key+`"`), key)
			Expect(ready(u).Message).To(ContainSubstring("reserved"), key)
		}
		Expect(fake.Requests(false)).To(BeEmpty(), "nothing is read or written for a spec that is invalid on its face")
	})

	It("adopts an existing person by username and patches only what the spec sets", func() {
		pk := fake.SeedUser("alice", map[string]any{jsonName: displayAlice, fieldEmail: "a@x",
			fieldAttributes: map[string]any{attrForeign: "f"}})
		u := newUser("alice", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.FirstName = displayAlice
			withAttrs(map[string]string{labelTeam: `"ops"`})(s)
		})
		mustReconcile(u)

		Expect(writes()).To(HaveLen(1))
		patch := writes()[0]
		Expect(patch.Method).To(Equal(http.MethodPatch))
		Expect(patch.Body).To(HaveKeyWithValue(fieldAttributes,
			Equal(map[string]any{attrForeign: "f", attrGivenName: displayAlice, labelTeam: "ops"})))
		for _, untouched := range []string{jsonName, fieldEmail, "groups", bodyRoles} {
			Expect(patch.Body).NotTo(HaveKey(untouched))
		}
		cur := get(u)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(cur.Status.UserPK).To(Equal(pk))
		Expect(cur.Status.ManagedAttributes).To(Equal([]string{labelTeam}))
		Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
	})

	It("writes firstName to given_name even when spec.attributes is absent", func() {
		fake.SeedUser("carl", map[string]any{fieldAttributes: map[string]any{attrForeign: "f"}})
		u := newUser("carl", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.FirstName = "Carl" })
		mustReconcile(u)

		Expect(writes()).To(HaveLen(1))
		Expect(writes()[0].Body).To(HaveKeyWithValue(fieldAttributes,
			Equal(map[string]any{attrForeign: "f", attrGivenName: "Carl"})))
		Expect(get(u).Status.ManagedAttributes).To(BeEmpty())
	})

	It("writes lastName to family_name and leaves the live given_name when firstName is cleared", func() {
		fake.SeedUser("carla", map[string]any{fieldAttributes: map[string]any{attrForeign: "f"}})
		u := newUser("carla", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.FirstName, s.LastName = "Carla", "Smith"
		})
		mustReconcile(u)
		Expect(fake.UserByUsername("carla")[fieldAttributes]).To(Equal(
			map[string]any{attrForeign: "f", attrGivenName: "Carla", attrFamilyName: "Smith"}))
		before := len(writes())

		setSpec(u, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.FirstName = "" })
		mustReconcile(u)
		for _, w := range writes()[before:] {
			Expect(w.Body).NotTo(HaveKey(fieldAttributes))
		}
		Expect(fake.UserByUsername("carla")[fieldAttributes]).To(HaveKeyWithValue(attrGivenName, "Carla"))
	})

	It("removes an owned attribute key it no longer lists and keeps firstName and every other key", func() {
		fake.SeedUser("ola", map[string]any{fieldAttributes: map[string]any{attrForeign: "f", labelTeam: "ops"}})
		u := newUser("ola", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.FirstName = "Ola"
			withAttrs(map[string]string{labelTeam: `"ops"`})(s)
		})
		mustReconcile(u)
		Expect(get(u).Status.ManagedAttributes).To(Equal([]string{labelTeam}))

		setSpec(u, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Attributes = map[string]apiextensionsv1.JSON{} })
		mustReconcile(u)
		Expect(fake.UserByUsername("ola")[fieldAttributes]).To(Equal(
			map[string]any{attrForeign: "f", attrGivenName: "Ola"}))
		Expect(get(u).Status.ManagedAttributes).To(BeEmpty())
	})

	It("patches name, email, path, type and isActive only when the spec sets them", func() {
		fake.SeedUser("pat", map[string]any{jsonName: "Pat", fieldEmail: "p@x", "path": bodyUsers, fieldType: "internal"})
		u := newUser("pat", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.Name, s.Email, s.Path, s.Type = "Patricia", "pat@x", "staff", authentikiov1alpha1.UserTypeExternal
			s.IsActive = new(false)
		})
		mustReconcile(u)
		Expect(writes()).To(HaveLen(1))
		Expect(writes()[0].Body).To(Equal(map[string]any{jsonName: "Patricia", fieldEmail: "pat@x", "path": "staff",
			fieldType: "external", fieldIsActive: false}))
	})

	It("refuses to adopt a service account", func() {
		fake.SeedUser("svc", map[string]any{fieldType: typeServiceAccount})
		u := newUser("svc", policyAdopt, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonConflict))
		Expect(ready(u).Message).To(ContainSubstring("people only"))
		Expect(get(u).Status.UserPK).To(BeZero())
		Expect(writes()).To(BeEmpty())
	})

	It("refuses every policy outside the identity selector and reads nothing", func() {
		users.IdentityNamespaceSelector = labels.Nothing()
		for _, policy := range []authentikiov1alpha1.ManagementPolicy{
			policyCreate, policyAdopt, policyAdoptOrCreate, policyReadOnly} {
			u := newUser("gated-"+strings.ToLower(string(policy)), policy, nil)
			mustReconcile(u)
			Expect(ready(u).Reason).To(Equal(reasonIdentityNotAllowed), string(policy))
		}
		Expect(fake.Requests(false)).To(BeEmpty())
	})

	It("applies the adoption gate after the identity gate", func() {
		users.AdoptionNamespaceSelector = labels.Nothing()
		u := newUser("adoption-gated", policyAdopt, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonAdoptionNotAllowed))

		users.IdentityNamespaceSelector = labels.Nothing()
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonIdentityNotAllowed))
	})

	It("reports in a dry run what adoption would change and writes nothing", func() {
		fake.SeedUser("erin", map[string]any{jsonName: "Old"})
		u := newUser("erin", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.DryRun, s.Name = true, "New"
		})
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready(u).Message).To(ContainSubstring(`name: "Old" -> "New"`))
		Expect(writes()).To(BeEmpty())
		Expect(get(u).Status.Provenance).To(BeEmpty())
	})

	It("reports a would-invite in a dry run with nothing to adopt under AdoptOrCreate", func() {
		u := newUser("erin-new", policyAdoptOrCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.DryRun = true })
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonAdoptionDryRun))
		Expect(ready(u).Message).To(ContainSubstring("an invitation would be issued"))
		Expect(writes()).To(BeEmpty())
		Expect(fake.Invitations()).To(BeEmpty())
	})

	It("reports AdoptionTargetNotFound under Adopt when nothing matches", func() {
		u := newUser("nobody", policyAdopt, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(writes()).To(BeEmpty())
	})

	It("stays a Conflict under Create when an account with the username exists", func() {
		fake.SeedUser("taken", nil)
		u := newUser("taken", policyCreate, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonConflict))
		Expect(get(u).Status.Provenance).To(BeEmpty(), "never infer Created for a fresh CR that found an account")
		Expect(writes()).To(BeEmpty())
		Expect(fake.Invitations()).To(BeEmpty())
	})

	It("refuses to adopt a person another resource already tracks", func() {
		fake.SeedUser("frank", nil)
		first := newUser("frank-first", policyAdopt, usernamed("frank"))
		mustReconcile(first)
		second := newUser("frank-second", policyAdopt, usernamed("frank"))

		mustReconcile(second)
		Expect(ready(second).Reason).To(Equal(reasonConflict))
		Expect(ready(second).Message).To(ContainSubstring("already managed by default/frank-first"))
		Expect(get(second).Status.UserPK).To(BeZero())
		Expect(get(second).Status.Provenance).To(BeEmpty())
	})

	It("settles two holders: Created beats Adopted, then the lower UID", func() {
		pk := fake.SeedUser("hal", nil)
		a := newUser("hal-a", policyCreate, usernamed("hal"))
		claimStatus(a, pk, authentikiov1alpha1.ProvenanceCreated)
		b := newUser("hal-b", policyAdopt, usernamed("hal"))
		claimStatus(b, pk, authentikiov1alpha1.ProvenanceAdopted)
		setStatus(b, func(s *authentikiov1alpha1.AuthentikUserStatus) { s.ManagedAttributes = []string{earlierKey} })

		mustReconcile(b)
		Expect(ready(b).Reason).To(Equal(reasonConflict))
		Expect(ready(b).Message).To(ContainSubstring("default/hal-a"))
		Expect(ready(b).Message).To(ContainSubstring("takes precedence"))
		Expect(get(b).Status.UserPK).To(BeZero())
		Expect(get(b).Status.Provenance).To(BeEmpty())
		Expect(get(b).Status.ManagedAttributes).To(BeEmpty(), "a released claim forgets the keys it owned")
		Expect(pkOf(a)).To(Equal(pk))

		By("two Adopted holders: the higher UID releases, the lower keeps it")
		seeded := fake.SeedUser("two-adopters", nil)
		mk := func(name string) *authentikiov1alpha1.AuthentikUser {
			x := newUser(name, policyAdopt, usernamed("two-adopters"))
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
		Expect(get(high).Status.UserPK).To(BeZero())
		mustReconcile(low)
		Expect(ready(low).Status).To(Equal(metav1.ConditionTrue))
		Expect(pkOf(low)).To(Equal(seeded))
	})

	It("re-checks a fresh claim through the API reader when the cache missed the other holder", func() {
		pk := fake.SeedUser("raced", nil)
		holder := newUser("raced-holder", policyAdopt, usernamed("raced"))
		claimStatus(holder, pk, authentikiov1alpha1.ProvenanceCreated)
		adopter := newUser("raced-adopter", policyAdopt, usernamed("raced"))
		users.Client = staleListClient{k8sClient} // the cache has not seen the holder's claim yet

		mustReconcile(adopter)
		Expect(ready(adopter).Reason).To(Equal(reasonConflict))
		Expect(get(adopter).Status.UserPK).To(BeZero())
		Expect(writes()).To(BeEmpty(), "the loser must yield before it writes to Authentik")
	})

	It("refuses a tracked account that now has another username", func() {
		pk := fake.SeedUser("renamed", nil)
		u := newUser("renamed", policyAdopt, nil)
		claimStatus(u, pk, authentikiov1alpha1.ProvenanceAdopted)
		fake.SetUserField(pk, "username", "someone-else")

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonConflict))
		Expect(ready(u).Message).To(ContainSubstring(`now named "someone-else", not "renamed"`))
		Expect(writes()).To(BeEmpty())
	})

	It("offboards with isActive false and never deactivates the token user", func() {
		fake.SeedUser("bob", nil)
		bob := newUser("bob", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.IsActive = new(false) })
		mustReconcile(bob)
		Expect(fake.UserByUsername("bob")[fieldIsActive]).To(BeFalse())

		admin := newUser(fakeauthentik.TokenUsername, policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.IsActive = new(false)
		})
		mustReconcile(admin)
		Expect(ready(admin).Reason).To(Equal(reasonInvalidSpec))
		Expect(ready(admin).Message).To(ContainSubstring(`refusing to deactivate the operator's own user "akadmin"`))
		Expect(fake.User(fakeauthentik.TokenUserPK)[fieldIsActive]).To(BeTrue())
	})

	It("observes under ReadOnly and reports drift", func() {
		fake.SeedUser("gina", map[string]any{jsonName: "Gina", fieldIsActive: true})
		u := newUser("gina", policyReadOnly, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.Name, s.IsActive = "Regina", new(false)
		})
		mustReconcile(u)
		Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
		Expect(ready(u).Reason).To(Equal(reasonReadOnly))
		Expect(get(u).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(drifted(u).Status).To(Equal(metav1.ConditionTrue))
		Expect(drifted(u).Reason).To(Equal(reasonDriftDetected))
		Expect(drifted(u).Message).To(HavePrefix("the object in Authentik differs from the spec: "))
		Expect(drifted(u).Message).To(ContainSubstring("is_active: true -> false"))
		Expect(drifted(u).Message).To(ContainSubstring(`name: "Gina" -> "Regina"`))
		Expect(writes()).To(BeEmpty())

		setSpec(u, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Name, s.IsActive = "Gina", new(true) })
		mustReconcile(u)
		Expect(drifted(u).Status).To(Equal(metav1.ConditionFalse))
		Expect(drifted(u).Reason).To(Equal(reasonInSync))
	})

	It("reports ExternalURLRequired and creates no invitation without an explicit external URL", func() {
		users.Connections.DefaultExternalURL = ""
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonExternalURLRequired))
		Expect(ready(u).Message).To(ContainSubstring("--authentik-external-url"))
		Expect(fake.Invitations()).To(BeEmpty())
		Expect(get(u).Status.Invitation).To(BeNil())
	})

	It("reports a missing enrolment flow as ReferenceNotFound and creates no invitation", func() {
		u := newUser(usernameDave, policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.Invitation.Flow = "no-such-flow"
		})
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonReferenceNotFound))
		Expect(fake.Invitations()).To(BeEmpty())
		Expect(get(u).Status.Invitation).To(BeNil())
	})

	It("clears status.invitation when Authentik rejects the invitation outright", func() {
		fake.FailNextWith(http.MethodPost, http.StatusBadRequest, map[string]any{jsonName: []string{"bad name"}})
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonInvalidSpec))
		Expect(get(u).Status.Invitation).To(BeNil())
	})

	It("issues a single-use invitation pinning the username and writes the link only to the Secret", func() {
		u := newUser(usernameDave, policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Name = displayDave })
		mustReconcile(u)

		uid := string(get(u).UID)
		name := "k8s-" + uid + "-1"
		inv := fake.InvitationByName(name)
		Expect(inv).NotTo(BeNil())
		Expect(inv["single_use"]).To(BeTrue())
		Expect(inv["flow"]).To(Equal(fakeauthentik.FlowEnrollmentPK))
		Expect(inv["fixed_data"]).To(Equal(map[string]any{"username": usernameDave, jsonName: displayDave,
			fieldAttributes: map[string]any{authentik.OwnerAttribute: map[string]any{authentik.OwnerUIDKey: uid}}}))

		pk, ok := inv["pk"].(string)
		Expect(ok).To(BeTrue())
		secret, err := secretOf("dave-invitation")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(secret.Data["url"])).To(Equal(externalSSO + "/if/flow/default-enrollment-flow/?itoken=" + pk))
		expires, err := time.Parse(time.RFC3339, string(secret.Data["expires"]))
		Expect(err).NotTo(HaveOccurred())
		Expect(expires).To(BeTemporally("~", time.Now().Add(7*24*time.Hour), time.Minute))
		Expect(metav1.IsControlledBy(secret, get(u))).To(BeTrue())
		Expect(secret.Type).To(Equal(corev1.SecretTypeOpaque))
		Expect(secret.Labels).To(HaveKeyWithValue(labelManagedBy, labelManagedByValue))

		cur := get(u)
		Expect(cur.Status.Invitation).NotTo(BeNil())
		Expect(cur.Status.Invitation.Name).To(Equal(name))
		Expect(cur.Status.Invitation.Attempt).To(Equal(int32(1)))
		Expect(cur.Status.Invitation.Pending).To(BeFalse())
		Expect(cur.Status.Invitation.SecretName).To(Equal("dave-invitation"))
		Expect(cur.Status.Invitation.Expires.Time).To(BeTemporally("~", expires, time.Second))
		Expect(ready(u).Status).To(Equal(metav1.ConditionFalse))
		Expect(ready(u).Reason).To(Equal(reasonInvitationPending))

		raw, err := json.Marshal(cur.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring(pk))
		Expect(drain()).To(ContainElement(And(ContainSubstring("Normal InvitationIssued"),
			ContainSubstring("dave-invitation"), Not(ContainSubstring(pk)))))
	})

	It("recovers an invitation whose create response was lost without issuing a second one", func() {
		fake.LoseNextInvitationCreate()
		u := newUser("mia", policyCreate, nil)
		_, err := reconcileUser(u)
		Expect(err).To(HaveOccurred())
		Expect(get(u).Status.Invitation).NotTo(BeNil())
		Expect(get(u).Status.Invitation.Pending).To(BeTrue())

		mustReconcile(u)
		Expect(get(u).Status.Invitation.Pending).To(BeFalse())
		Expect(ready(u).Reason).To(Equal(reasonInvitationPending))
		Expect(fake.Invitations()).To(HaveLen(1))
		_, err = secretOf("mia-invitation")
		Expect(err).NotTo(HaveOccurred())
	})

	It("sends the create again when the lost request never reached Authentik", func() {
		u := newUser("noor", policyCreate, nil)
		name := invitationName(get(u).UID, 1)
		setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) {
			s.Invitation = &authentikiov1alpha1.InvitationStatus{Name: name, Attempt: 1, Pending: true,
				Expires: metav1.NewTime(time.Now().Add(time.Hour)), SecretName: "noor-invitation"}
		})

		mustReconcile(u)
		Expect(fake.Invitations()).To(HaveLen(1))
		Expect(fake.InvitationByName(name)).NotTo(BeNil())
		Expect(get(u).Status.Invitation.Pending).To(BeFalse())
		_, err := secretOf("noor-invitation")
		Expect(err).NotTo(HaveOccurred())
	})

	It("honours expiresAfter and secretName", func() {
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ellen-link", Namespace: namespace}})
		})
		u := newUser("ellen", policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.Invitation.ExpiresAfter = &metav1.Duration{Duration: time.Hour}
			s.Invitation.SecretName = "ellen-link"
		})
		mustReconcile(u)
		secret, err := secretOf("ellen-link")
		Expect(err).NotTo(HaveOccurred())
		expires, err := time.Parse(time.RFC3339, string(secret.Data["expires"]))
		Expect(err).NotTo(HaveOccurred())
		Expect(expires).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
		Expect(get(u).Status.Invitation.SecretName).To(Equal("ellen-link"))
	})

	It("keeps the invitation pk out of every condition, event, log line and error", func() { // spec 5.3 step 4
		u := newUser("fiona", policyCreate, nil)
		mustReconcile(u)
		pk := fake.IssuedInvitationPKs()[0]
		leaks := func() string {
			var seen []string
			raw, err := json.Marshal(get(u).Status)
			Expect(err).NotTo(HaveOccurred())
			seen = append(seen, string(raw))
			seen = append(seen, drain()...)
			seen = append(seen, logs.String())
			return strings.Join(seen, "\n")
		}
		Expect(leaks()).NotTo(ContainSubstring(pk), "after the invitation was issued")

		By("a failing delete of the expired invitation")
		setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) {
			s.Invitation.Expires = metav1.NewTime(time.Now().Add(-time.Hour))
		})
		fake.FailNextOn(http.MethodDelete, "/stages/invitation/invitations/", http.StatusServiceUnavailable)
		_, err := reconcileUser(u)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring(pk))
		Expect(leaks()).NotTo(ContainSubstring(pk), "after the delete failed")

		By("the delete succeeding and the invitation expiring")
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonInvitationExpired))
		Expect(leaks()).NotTo(ContainSubstring(pk), "after the invitation expired")
	})

	It("refuses to overwrite a Secret it does not own", func() {
		squatter := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "gus-invitation", Namespace: namespace},
			Data: map[string][]byte{"url": []byte("not-ours")}}
		Expect(k8sClient.Create(ctx, squatter)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, squatter) })
		u := newUser("gus", policyCreate, nil)
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonSecretConflict))
		secret, err := secretOf("gus-invitation")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(secret.Data["url"])).To(Equal("not-ours"))
	})

	It("rewrites a missing or changed Secret while the invitation is pending", func() {
		u := newUser("ivy", policyCreate, nil)
		mustReconcile(u)
		secret, err := secretOf("ivy-invitation")
		Expect(err).NotTo(HaveOccurred())
		want := string(secret.Data["url"])

		secret.Data["url"] = []byte("tampered")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		mustReconcile(u)
		secret, err = secretOf("ivy-invitation")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(secret.Data["url"])).To(Equal(want))

		Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
		mustReconcile(u)
		secret, err = secretOf("ivy-invitation")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(secret.Data["url"])).To(Equal(want))
		Expect(fake.Invitations()).To(HaveLen(1))
	})

	It("recognises the enrolled account as Created and deletes the Secret", func() {
		u := newUser(usernameDave, policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Name = displayDave })
		mustReconcile(u)
		pk := fake.EnrolFromInvitation(invitationOf(u), "")
		Expect(pk).NotTo(BeZero())
		drain()

		mustReconcile(u)
		cur := get(u)
		Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(cur.Status.UserPK).To(Equal(pk))
		Expect(cur.Status.Invitation).To(BeNil())
		_, err := secretOf("dave-invitation")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(drain()).To(ContainElement(ContainSubstring(`Normal Enrolled`)))
		Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
		Expect(ready(u).Reason).To(Equal(reasonSynced))
	})

	It("deletes a still-live invitation when it recognises the account", func() {
		u := newUser("jo", policyCreate, nil)
		mustReconcile(u)
		fake.SeedUser("jo", map[string]any{fieldAttributes: proof(u)})
		Expect(fake.Invitations()).To(HaveLen(1))

		mustReconcile(u)
		Expect(get(u).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(fake.Invitations()).To(BeEmpty())
	})

	It("passes through InvitationConsumed during a normal enrolment", func() {
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		fake.ConsumeInvitation(invitationOf(u))

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonInvitationConsumed))
		Expect(ready(u).Message).To(ContainSubstring("increase spec.invitation.attempt"))
		Expect(get(u).Status.Invitation).NotTo(BeNil(), "the search for the account keeps running")
		_, err := secretOf("dave-invitation")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		fake.SeedUser(usernameDave, map[string]any{fieldAttributes: proof(u)})
		mustReconcile(u)
		Expect(get(u).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		Expect(get(u).Status.Invitation).To(BeNil())
	})

	It("reports InvitationExpired from status once past expires", func() {
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		name := invitationOf(u)
		setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) {
			s.Invitation.Expires = metav1.NewTime(time.Now().Add(-time.Hour))
		})

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonInvitationExpired))
		Expect(fake.InvitationByName(name)).To(BeNil(), "an expired invitation is deleted in Authentik")
		_, err := secretOf("dave-invitation")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(get(u).Status.Invitation).NotTo(BeNil())
		Expect(fake.Invitations()).To(BeEmpty(), "no new link is issued automatically")
	})

	It("never re-invites a Created user that vanished", func() {
		u := newUser(usernameDave, policyCreate, nil)
		pk := enrol(u)
		fake.DeleteUser(pk)

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonAdoptionTargetNotFound))
		Expect(ready(u).Message).To(ContainSubstring("increase spec.invitation.attempt"))
		Expect(pkOf(u)).To(Equal(pk))
		Expect(fake.IssuedInvitationPKs()).To(HaveLen(1))
	})

	It("keeps the person's fields it was not given and sends no writes on a second reconcile", func() {
		fake.SeedUser("kim", map[string]any{jsonName: "Kim"})
		adopted := newUser("kim", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.Email = "kim@x"
			s.FirstName = "Kim"
		})
		mustReconcile(adopted)
		n := len(writes())
		mustReconcile(adopted)
		Expect(writes()).To(HaveLen(n))
		Expect(ready(adopted).Status).To(Equal(metav1.ConditionTrue))

		pending := newUser("lou", policyCreate, nil)
		mustReconcile(pending)
		n = len(writes())
		mustReconcile(pending)
		Expect(writes()).To(HaveLen(n), "a pending invitation is read, not rewritten")
		Expect(fake.Invitations()).To(HaveLen(1))
	})

	Context("deletion", func() {
		del := func(u *authentikiov1alpha1.AuthentikUser) {
			Expect(k8sClient.Delete(ctx, u)).To(Succeed())
		}

		It("retains a Created user on deletion by default and deletes it with an explicit Delete", func() {
			keep := newUser("keep", policyCreate, nil)
			keepPK := enrol(keep)
			drain()
			del(keep)
			mustReconcile(keep)
			Expect(fake.User(keepPK)).NotTo(BeNil())
			Expect(gone(keep)).To(BeTrue())
			Expect(drain()).To(ContainElement(And(ContainSubstring("Normal Retained"), ContainSubstring("isActive: false"))))

			remove := newUser("remove", policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			})
			removePK := enrol(remove)
			drain()
			del(remove)
			mustReconcile(remove)
			Expect(fake.User(removePK)).To(BeNil())
			Expect(gone(remove)).To(BeTrue())
			Expect(drain()).To(ContainElement(ContainSubstring("Normal Deleted")))
		})

		It("retains an adopted user by default", func() {
			pk := fake.SeedUser("adopted-keep", nil)
			u := newUser("adopted-keep", policyAdopt, nil)
			mustReconcile(u)
			del(u)
			mustReconcile(u)
			Expect(fake.User(pk)).NotTo(BeNil())
			Expect(gone(u)).To(BeTrue())
		})

		It("deletes only a Created user outside the identity selector", func() {
			users.IdentityNamespaceSelector = labels.Nothing()
			createdPK := fake.SeedUser("out-created", nil)
			created := newUser("out-created", policyCreate, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			})
			mustReconcile(created) // adds the finalizer; the identity gate then refuses the sync
			claimStatus(created, createdPK, authentikiov1alpha1.ProvenanceCreated)
			adoptedPK := fake.SeedUser("out-adopted", nil)
			adopted := newUser("out-adopted", policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			})
			mustReconcile(adopted)
			claimStatus(adopted, adoptedPK, authentikiov1alpha1.ProvenanceAdopted)

			del(created)
			mustReconcile(created)
			Expect(fake.User(createdPK)).To(BeNil())

			drain()
			del(adopted)
			mustReconcile(adopted)
			Expect(fake.User(adoptedPK)).NotTo(BeNil())
			Expect(gone(adopted)).To(BeTrue())
			Expect(drain()).To(ContainElement(And(ContainSubstring("Warning Retained"),
				ContainSubstring("--default-connection-identity-namespace-selector"))))
		})

		It("never deletes the operator's own user", func() {
			u := newUser(fakeauthentik.TokenUsername, policyAdopt, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			})
			mustReconcile(u) // adds the finalizer
			claimStatus(u, fakeauthentik.TokenUserPK, authentikiov1alpha1.ProvenanceAdopted)
			del(u)
			mustReconcile(u)
			Expect(fake.User(fakeauthentik.TokenUserPK)).NotTo(BeNil())
			Expect(gone(u)).To(BeTrue())
			Expect(drain()).To(ContainElement(ContainSubstring("Warning NotDeleted")))
		})
	})
})
