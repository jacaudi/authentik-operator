//go:build e2e

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

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/test/aktest"
	"github.com/jacaudi/authentik-operator/test/utils"
)

const (
	// idFlow is the test-only enrolment flow EnsureEnrolmentFlow creates.
	idFlow     = "e2e-enrolment"
	idPassword = "e2e-identity-password-not-a-secret"
	idMember   = "e2e-member"
	idOutsider = "e2e-outsider"
	idPerson   = "e2e-person"
	idGroup    = "e2e-access"
	idApp      = "e2e-groups"
	idRedirect = "https://e2e-groups.invalid/callback"

	// What the JeffResc group-migration spec pre-creates, the way JeffResc v0.2
	// would: a group, and an application with a provider and a binding to it.
	jrgGroup    = "e2e-jr-users"
	jrgApp      = "e2e-jr-bound"
	jrgProvider = jrgApp + "-provider" // the name the converter matches the provider by
	jrgRedirect = "https://e2e-jr-bound.invalid/callback"

	condMembersInSync = "MembersInSync"
	condAccessInSync  = "AccessInSync"
	condAccessOpen    = "AccessOpen"
	reasonDelBlocked  = "DeletionBlocked"

	groupsPath = "/api/v3/core/groups/"
)

// jeffrescGroupsYAML is what JeffResc v0.2 holds for the group and the
// application bound to it: the converter's input.
var jeffrescGroupsYAML = fmt.Sprintf(`apiVersion: goauthentik.io/v1alpha1
kind: AuthentikGroup
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  name: %[1]s
---
apiVersion: goauthentik.io/v1alpha1
kind: AuthentikOAuth2Application
metadata: {name: %[3]s, namespace: %[2]s}
spec:
  name: E2E JR Bound
  groupBindings: [%[1]s]
  provider:
    authorizationFlow: %[4]s
    invalidationFlow: %[5]s
    redirectUris:
      - %[6]s
    grantTypes: [authorization_code, refresh_token]
`, jrgGroup, appsNS, jrgApp, flowAuthorize, flowInvalidation, jrgRedirect)

// akGroupUsers returns the pks of the users of the Authentik group pk, and
// whether the group exists.
func akGroupUsers(ctx context.Context, g Gomega, pk string) ([]int, bool) {
	var grp aktest.Obj
	err := ak.Do(ctx, "GET", groupsPath+pk+"/", nil, &grp)
	if aktest.IsNotFound(err) {
		return nil, false
	}
	g.Expect(err).NotTo(HaveOccurred())
	var pks []int
	users, _ := grp["users"].([]any)
	for _, u := range users {
		if n, ok := u.(float64); ok {
			pks = append(pks, int(n))
		}
	}
	return pks, true
}

// wantCondition polls until the condition of the given type on obj has the
// status (and the reason, unless empty). get reads the conditions afresh.
func wantCondition(ctx context.Context, get func(Gomega) []metav1.Condition, t string,
	status metav1.ConditionStatus, reason string) {
	EventuallyWithOffset(1, func(g Gomega) {
		c := condition(get(g), t)
		g.Expect(c).NotTo(BeNil(), "no %s condition yet", t)
		g.Expect(c.Status).To(Equal(status), "%s=%s reason=%s: %s", t, c.Status, c.Reason, c.Message)
		if reason != "" {
			g.Expect(c.Reason).To(Equal(reason), c.Message)
		}
	}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
}

// groupConditions reads the conditions of the AuthentikGroup name in appsNS.
func groupConditions(ctx context.Context, name string) func(Gomega) []metav1.Condition {
	return func(g Gomega) []metav1.Condition {
		var grp authentikiov1alpha1.AuthentikGroup
		g.Expect(k8s.Get(ctx, key(name), &grp)).To(Succeed())
		return grp.Status.Conditions
	}
}

var _ = Describe("groups, users and access against a real Authentik", Ordered, func() {
	var (
		memberPK, personPK int
		groupPK            string
	)
	BeforeAll(func(ctx SpecContext) {
		// BeforeSuite creates only the operator namespace and appsNS; the
		// migration scenario creates and deletes e2e-migrated inside its own specs.
		Expect(ensureNamespace(ctx, migratedNS, nil)).To(Succeed())
		Expect(ak.EnsureEnrolmentFlow(ctx, idFlow)).To(Succeed())
		var err error
		memberPK, err = ak.CreateUserWithPassword(ctx, idMember, idPassword)
		Expect(err).NotTo(HaveOccurred())
		_, err = ak.CreateUserWithPassword(ctx, idOutsider, idPassword)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func(ctx SpecContext) {
		// The AuthentikUser keeps its account (deletionPolicy is Retain unless set)
		// and the migrated objects are Retain: the cluster is throwaway, so nothing
		// else is cleaned.
		for _, o := range []client.Object{
			&authentikiov1alpha1.AuthentikApplication{ObjectMeta: metav1.ObjectMeta{Name: idApp, Namespace: appsNS}},
			&authentikiov1alpha1.AuthentikOAuthProvider{ObjectMeta: metav1.ObjectMeta{Name: idApp, Namespace: appsNS}},
			&authentikiov1alpha1.AuthentikGroup{ObjectMeta: metav1.ObjectMeta{Name: idGroup, Namespace: appsNS}},
			&authentikiov1alpha1.AuthentikUser{ObjectMeta: metav1.ObjectMeta{Name: idPerson, Namespace: appsNS}},
			&authentikiov1alpha1.AuthentikApplication{ObjectMeta: metav1.ObjectMeta{Name: jrgApp, Namespace: migratedNS}},
			&authentikiov1alpha1.AuthentikOAuthProvider{ObjectMeta: metav1.ObjectMeta{Name: jrgApp, Namespace: migratedNS}},
			&authentikiov1alpha1.AuthentikGroup{ObjectMeta: metav1.ObjectMeta{Name: jrgGroup, Namespace: migratedNS}},
		} {
			deleteAndWait(ctx, o)
		}
	})

	It("enrols a person from the operator's invitation and recognises the account as Created", func(ctx SpecContext) {
		user := &authentikiov1alpha1.AuthentikUser{
			ObjectMeta: metav1.ObjectMeta{Name: idPerson, Namespace: appsNS},
			Spec: authentikiov1alpha1.AuthentikUserSpec{
				Username:   idPerson,
				Invitation: &authentikiov1alpha1.InvitationSpec{Flow: idFlow},
			},
		}
		Expect(k8s.Create(ctx, user)).To(Succeed())

		By("reading the sign-up link from the invitation Secret")
		data := eventuallySecret(ctx, idPerson+"-invitation")
		Expect(data).To(HaveKey("url"))

		By("enrolling through the link, as the person's browser would")
		Expect(aktest.Enrol(ctx, clusterTransport(), data["url"])).To(Succeed())

		By("the operator recognising the account it invited")
		var got authentikiov1alpha1.AuthentikUser
		Eventually(func(g Gomega) {
			g.Expect(k8s.Get(ctx, key(idPerson), &got)).To(Succeed())
			if got.Status.Provenance != authentikiov1alpha1.ProvenanceCreated {
				poke(ctx, g, &got) // a pending invitation is rechecked every minute: do not wait for it
			}
			g.Expect(got.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			g.Expect(got.Status.UserPK).NotTo(BeZero())
			g.Expect(got.Status.Invitation).To(BeNil())
			var s corev1.Secret
			g.Expect(apierrors.IsNotFound(k8s.Get(ctx, key(idPerson+"-invitation"), &s))).To(BeTrue(),
				"the invitation Secret is still there")
		}).WithContext(ctx).WithTimeout(waitTimeout).WithPolling(5 * time.Second).Should(Succeed())
		personPK = int(got.Status.UserPK)

		By("the account carrying the ownership proof the invitation's fixed data wrote")
		var acct aktest.Obj
		Expect(ak.Do(ctx, "GET", fmt.Sprintf("/api/v3/core/users/%d/", personPK), nil, &acct)).To(Succeed())
		Expect(aktest.Str(acct, "username")).To(Equal(idPerson))
		attrs, _ := acct["attributes"].(map[string]any)
		owner, _ := attrs["authentik_operator"].(map[string]any)
		Expect(owner).To(HaveKeyWithValue("owner_uid", string(got.UID)))
	})

	It("adds group members, including the enrolled person", func(ctx SpecContext) {
		members := []string{idMember, idPerson}
		grp := &authentikiov1alpha1.AuthentikGroup{
			ObjectMeta: metav1.ObjectMeta{Name: idGroup, Namespace: appsNS},
			Spec:       authentikiov1alpha1.AuthentikGroupSpec{Members: &members},
		}
		Expect(k8s.Create(ctx, grp)).To(Succeed())

		wantCondition(ctx, groupConditions(ctx, idGroup), condMembersInSync, metav1.ConditionTrue, "")
		var got authentikiov1alpha1.AuthentikGroup
		Expect(k8s.Get(ctx, key(idGroup), &got)).To(Succeed())
		groupPK = got.Status.GroupPK
		Expect(groupPK).NotTo(BeEmpty())

		users, exists := akGroupUsers(ctx, Default, groupPK)
		Expect(exists).To(BeTrue())
		Expect(users).To(ContainElements(memberPK, personPK))
	})

	It("lets a member open an application bound to the group and refuses a non-member at authorize",
		func(ctx SpecContext) {
			p := newProvider(idApp, idRedirect)
			p.Spec.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: idApp + "-creds"}
			Expect(k8s.Create(ctx, p)).To(Succeed())
			a := newApplication(idApp, "E2E Groups", idApp, idApp)
			a.Spec.Access = &authentikiov1alpha1.ApplicationAccess{
				Type: authentikiov1alpha1.AccessTypeGroups, Groups: []string{idGroup}}
			Expect(k8s.Create(ctx, a)).To(Succeed())

			providerCondition(ctx, idApp, metav1.ConditionTrue, "Synced")
			applicationCondition(ctx, idApp, metav1.ConditionTrue)
			appConditions := func(g Gomega) []metav1.Condition {
				return getApplication(ctx, g, idApp).Status.Conditions
			}
			wantCondition(ctx, appConditions, condAccessInSync, metav1.ConditionTrue, "")
			wantCondition(ctx, appConditions, condAccessOpen, metav1.ConditionFalse, "")

			c := defaultCreds(eventuallySecret(ctx, idApp+"-creds"))
			By("a member of the group signs in")
			res, err := loginAs(ctx, c, idRedirect, idMember, idPassword)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.UserInfo["preferred_username"]).To(Equal(idMember))

			By("a user outside the group is refused at authorize")
			_, err = loginAs(ctx, c, idRedirect, idOutsider, idPassword)
			Expect(err).To(MatchError(aktest.ErrPermissionDenied))
		})

	It("blocks deleting a group an application still binds", func(ctx SpecContext) {
		Expect(k8s.Delete(ctx, &authentikiov1alpha1.AuthentikGroup{
			ObjectMeta: metav1.ObjectMeta{Name: idGroup, Namespace: appsNS}})).To(Succeed())
		wantCondition(ctx, groupConditions(ctx, idGroup), condReady, metav1.ConditionFalse, reasonDelBlocked)
		_, exists := akGroupUsers(ctx, Default, groupPK)
		Expect(exists).To(BeTrue(), "the group was deleted in Authentik while an application binds it")

		By("deleting the application, which is Created: its application and bindings go")
		deleteAndWait(ctx, &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: idApp, Namespace: appsNS}})
		Eventually(func(g Gomega) {
			var got authentikiov1alpha1.AuthentikGroup
			g.Expect(apierrors.IsNotFound(k8s.Get(ctx, key(idGroup), &got))).To(BeTrue(),
				"the group is still terminating")
		}).WithContext(ctx).WithTimeout(waitTimeout + time.Minute).Should(Succeed())
		_, exists = akGroupUsers(ctx, Default, groupPK)
		Expect(exists).To(BeFalse(), "the group is still in Authentik")
	})

	It("migrates a JeffResc group and its groupBindings without touching the bindings", func(ctx SpecContext) {
		By("creating, the way JeffResc would, the group, the application with its provider, and the binding")
		jrGroupPK, err := ak.CreateGroup(ctx, jrgGroup)
		Expect(err).NotTo(HaveOccurred())
		prov, err := ak.CreateOAuthProvider(ctx, jrgProvider, []string{jrgRedirect})
		Expect(err).NotTo(HaveOccurred())
		var provPK int
		Expect(fmt.Sscan(aktest.Str(prov, "pk"), &provPK)).To(Equal(1))
		app, err := ak.CreateApplication(ctx, "E2E JR Bound", jrgApp, provPK)
		Expect(err).NotTo(HaveOccurred())
		appPK := aktest.Str(app, "pk")
		_, err = ak.CreateGroupBinding(ctx, jrGroupPK, appPK, 0)
		Expect(err).NotTo(HaveOccurred())
		before, err := ak.BindingPKs(ctx, appPK)
		Expect(err).NotTo(HaveOccurred())
		Expect(before).To(HaveLen(1))
		writes := func(g Gomega) int {
			n, err := ak.ModelWriteCount(ctx, "authentik_policies", "policybinding",
				"model_created", "model_updated", "model_deleted")
			g.Expect(err).NotTo(HaveOccurred())
			return n
		}
		writes0 := writes(Default)

		By("converting the JeffResc objects with the repository's converter and applying the result")
		dir := GinkgoT().TempDir()
		in, out := filepath.Join(dir, "jeffresc.yaml"), filepath.Join(dir, "out")
		Expect(os.WriteFile(in, []byte(jeffrescGroupsYAML), 0o600)).To(Succeed())
		_, err = utils.Run(exec.CommandContext(ctx, "go", "run", "./hack/convert-jeffresc", "--out", out,
			"--namespace", migratedNS, "--dry-run-adoption=false", in))
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectl(ctx, "apply", "-f", out)
		Expect(err).NotTo(HaveOccurred())

		By("the group and the application adopting what is there")
		migrated := func(name string) client.ObjectKey { return client.ObjectKey{Namespace: migratedNS, Name: name} }
		Eventually(func(g Gomega) {
			var grp authentikiov1alpha1.AuthentikGroup
			g.Expect(k8s.Get(ctx, migrated(jrgGroup), &grp)).To(Succeed())
			g.Expect(grp.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			c := condition(grp.Status.Conditions, condReady)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(metav1.ConditionTrue), "%s: %s", c.Reason, c.Message)
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
		var migratedApp authentikiov1alpha1.AuthentikApplication
		Eventually(func(g Gomega) {
			g.Expect(k8s.Get(ctx, migrated(jrgApp), &migratedApp)).To(Succeed())
			g.Expect(migratedApp.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			c := condition(migratedApp.Status.Conditions, condAccessInSync)
			g.Expect(c).NotTo(BeNil(), "no %s condition yet", condAccessInSync)
			g.Expect(c.Status).To(Equal(metav1.ConditionTrue), "%s: %s", c.Reason, c.Message)
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())

		By("a further reconcile and the binding list showing the bindings were left alone")
		pokeAndWait(ctx, &migratedApp)
		after, err := ak.BindingPKs(ctx, appPK)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before), "the application's bindings changed")
		Expect(writes(Default)).To(Equal(writes0), "the operator wrote policy bindings")
	})
})
