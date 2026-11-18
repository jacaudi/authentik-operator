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

// Paths the invitation specs make the fake fail on.
const (
	pathInvitations    = "/stages/invitation/invitations/"
	typeServiceAccount = "service_account"
	pathAuthenticator  = "/authenticators/admin/all/"
	usernameDavid      = "david"
)

// The lost-response recovery specs of Task 10 behaviour 2 ("recovers an invitation whose create response was lost
// without issuing a second one", "sends the create again when the lost request never reached Authentik") are in
// authentikuser_test.go, where Task 9 implemented the behaviour.
var _ = Describe("AuthentikUser invitation lifecycle", func() {
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
	)

	// newUser makes a resource named like its person; it carries an invitation exactly when the policy may create.
	newUser := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		mutate func(*authentikiov1alpha1.AuthentikUserSpec)) *authentikiov1alpha1.AuthentikUser {
		spec := authentikiov1alpha1.AuthentikUserSpec{
			APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy}, Username: name,
		}
		if policy.MayCreate() {
			spec.Invitation = &authentikiov1alpha1.InvitationSpec{Flow: fakeauthentik.FlowEnrollmentSlug}
		}
		if mutate != nil {
			mutate(&spec)
		}
		u := &authentikiov1alpha1.AuthentikUser{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(u), u); err == nil {
				u.SetFinalizers(nil)
				_ = k8sClient.Update(ctx, u)
				_ = k8sClient.Delete(ctx, u)
			}
			// There is no garbage collector in envtest: the Secrets outlive their owner.
			for _, secret := range []string{name + "-invitation", name + "-link"} {
				_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secret, Namespace: namespace}})
			}
		})
		return u
	}
	reconcileUser := func(u *authentikiov1alpha1.AuthentikUser) (reconcile.Result, error) {
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
	condition := func(u *authentikiov1alpha1.AuthentikUser, condType string) *metav1.Condition {
		return meta.FindStatusCondition(get(u).Status.Conditions, condType)
	}
	ready := func(u *authentikiov1alpha1.AuthentikUser) *metav1.Condition { return condition(u, ConditionReady) }
	gone := func(u *authentikiov1alpha1.AuthentikUser) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(u), &authentikiov1alpha1.AuthentikUser{}))
	}
	setSpec := func(u *authentikiov1alpha1.AuthentikUser, mutate func(*authentikiov1alpha1.AuthentikUserSpec)) {
		cur := get(u)
		mutate(&cur.Spec)
		Expect(k8sClient.Update(ctx, cur)).To(Succeed())
	}
	setStatus := func(u *authentikiov1alpha1.AuthentikUser, mutate func(*authentikiov1alpha1.AuthentikUserStatus)) {
		cur := get(u)
		mutate(&cur.Status)
		Expect(k8sClient.Status().Update(ctx, cur)).To(Succeed())
	}
	secretOf := func(name string) (*corev1.Secret, error) {
		var s corev1.Secret
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s)
		return &s, err
	}
	secretGone := func(name string) bool {
		_, err := secretOf(name)
		return apierrors.IsNotFound(err)
	}
	attemptOf := func(u *authentikiov1alpha1.AuthentikUser, attempt int32) string {
		return invitationName(get(u).UID, attempt)
	}
	proof := func(u *authentikiov1alpha1.AuthentikUser) map[string]any {
		return authentik.OwnerProof(string(get(u).UID))
	}
	pkOfInvitation := func(name string) string {
		inv := fake.InvitationByName(name)
		Expect(inv).NotTo(BeNil(), name)
		pk, ok := inv["pk"].(string)
		Expect(ok).To(BeTrue())
		return pk
	}
	withAttempt := func(n int32) func(*authentikiov1alpha1.AuthentikUserSpec) {
		return func(s *authentikiov1alpha1.AuthentikUserSpec) { s.Invitation.Attempt = n }
	}
	eventsNamed := func(reason string) int {
		n := 0
		for _, e := range drainEvents(recorder) {
			if strings.Contains(e, reason) {
				n++
			}
		}
		return n
	}
	// enrol issues u's invitation and enrols the person through it, then reconciles: u tracks the new account as Created.
	enrol := func(u *authentikiov1alpha1.AuthentikUser) int32 {
		mustReconcile(u)
		pk := fake.EnrolFromInvitation(attemptOf(u, 1), "")
		Expect(pk).NotTo(BeZero())
		mustReconcile(u)
		Expect(get(u).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
		return pk
	}

	It("recovers a lost create by the current attempt's invitation name only", func() {
		u := newUser(usernameDave, policyCreate, withAttempt(2))
		fake.SeedInvitation(attemptOf(u, 1), nil) // an earlier attempt's, still live
		setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) {
			s.Invitation = &authentikiov1alpha1.InvitationStatus{Name: attemptOf(u, 2), Attempt: 2, Pending: true,
				Expires: metav1.NewTime(time.Now().Add(time.Hour)), SecretName: "dave-invitation"}
		})

		mustReconcile(u)
		Expect(fake.InvitationByName(attemptOf(u, 2))).NotTo(BeNil(), "the earlier attempt's invitation is not the lost one")
		Expect(fake.Invitations()).To(HaveLen(2))
		Expect(get(u).Status.Invitation.Pending).To(BeFalse())
	})

	Context("reissue", func() {
		It("issues a new link only when attempt is increased", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			first, second := attemptOf(u, 1), attemptOf(u, 2)
			secret, err := secretOf("dave-invitation")
			Expect(err).NotTo(HaveOccurred())
			firstURL := string(secret.Data[invitationSecretURL])
			mustReconcile(u)
			mustReconcile(u)
			Expect(fake.Invitations()).To(HaveLen(1))
			Expect(fake.InvitationByName(first)).NotTo(BeNil())

			setSpec(u, withAttempt(2))
			mustReconcile(u)
			Expect(fake.InvitationByName(first)).To(BeNil(), "the previous attempt's live invitation is deleted")
			Expect(fake.Invitations()).To(HaveLen(1))
			secret, err = secretOf("dave-invitation")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(secret.Data[invitationSecretURL])).NotTo(Equal(firstURL))
			Expect(string(secret.Data[invitationSecretURL])).To(HaveSuffix("?itoken=" + pkOfInvitation(second)))
			cur := get(u).Status.Invitation
			Expect(cur.Name).To(Equal(second))
			Expect(cur.Attempt).To(Equal(int32(2)))
			Expect(cur.Pending).To(BeFalse())
			Expect(ready(u).Reason).To(Equal(reasonInvitationPending))

			setSpec(u, withAttempt(1))
			mustReconcile(u)
			Expect(fake.Invitations()).To(HaveLen(1), "a lower attempt reissues nothing")
			Expect(fake.InvitationByName(second)).NotTo(BeNil())
			Expect(get(u).Status.Invitation.Attempt).To(Equal(int32(2)))
		})

		It("gives a new link after the invitation expired", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			setStatus(u, func(s *authentikiov1alpha1.AuthentikUserStatus) {
				s.Invitation.Expires = metav1.NewTime(time.Now().Add(-time.Hour))
			})
			mustReconcile(u)
			Expect(ready(u).Reason).To(Equal(reasonInvitationExpired))
			Expect(fake.Invitations()).To(BeEmpty())

			setSpec(u, withAttempt(2))
			mustReconcile(u)
			Expect(ready(u).Reason).To(Equal(reasonInvitationPending))
			Expect(fake.InvitationByName(attemptOf(u, 2))).NotTo(BeNil())
			_, err := secretOf("dave-invitation")
			Expect(err).NotTo(HaveOccurred())
		})

		It("moves the link to the new Secret when secretName changes with the attempt", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			Expect(secretGone("dave-invitation")).To(BeFalse())

			setSpec(u, func(s *authentikiov1alpha1.AuthentikUserSpec) {
				s.Invitation.Attempt, s.Invitation.SecretName = 2, "dave-link"
			})
			mustReconcile(u)
			Expect(secretGone("dave-invitation")).To(BeTrue(), "the previous attempt's Secret is deleted")
			secret, err := secretOf("dave-link")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(secret.Data[invitationSecretURL])).To(HaveSuffix("?itoken=" + pkOfInvitation(attemptOf(u, 2))))
			Expect(get(u).Status.Invitation.SecretName).To(Equal("dave-link"))
		})

		It("ignores attempt once the person has an account", func() {
			u := newUser(usernameDave, policyCreate, nil)
			enrol(u)
			before := len(fake.IssuedInvitationPKs())

			setSpec(u, withAttempt(3))
			mustReconcile(u)
			Expect(fake.IssuedInvitationPKs()).To(HaveLen(before))
			Expect(fake.Invitations()).To(BeEmpty())
			Expect(get(u).Status.Invitation).To(BeNil())
		})
	})

	DescribeTable("reports a Conflict and withdraws the invitation when someone else takes the username",
		func(taken map[string]any, carriesProof bool) {
			u := newUser(usernameDave, policyAdoptOrCreate, nil)
			mustReconcile(u)
			Expect(fake.Invitations()).To(HaveLen(1))
			if carriesProof { // a service account is never a person, proof or not
				taken = map[string]any{fieldType: typeServiceAccount, fieldAttributes: proof(u)}
			}
			fake.SeedUser(usernameDave, taken)

			for range 2 { // the second reconcile finds nothing left to delete and still never adopts
				mustReconcile(u)
				Expect(ready(u).Reason).To(Equal(reasonConflict))
				Expect(fake.Invitations()).To(BeEmpty())
				Expect(secretGone("dave-invitation")).To(BeTrue())
				cur := get(u)
				Expect(cur.Status.Provenance).To(BeEmpty())
				Expect(cur.Status.UserPK).To(BeZero())
				Expect(cur.Status.Invitation).NotTo(BeNil(), "kept, so AdoptOrCreate never adopts the squatter")
			}
			for _, w := range fake.Requests(true) {
				Expect(w.Method).NotTo(Equal(http.MethodPatch), "nothing is written to the squatter: %s", w.Path)
			}
		},
		Entry("an ordinary account", map[string]any(nil), false),
		Entry("a service account", map[string]any{fieldType: typeServiceAccount}, false),
		Entry("a service account carrying the ownership proof", nil, true),
	)

	It("reports EnrolledUnderOtherName and adopts nothing", func() {
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		Expect(fake.EnrolFromInvitation(attemptOf(u, 1), usernameDavid)).NotTo(BeZero())

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonEnrolledUnderOtherName))
		Expect(ready(u).Message).To(ContainSubstring(`the invitation was used to enrol "david", not "dave"`))
		cur := get(u)
		Expect(cur.Status.UserPK).To(BeZero())
		Expect(cur.Status.Provenance).To(BeEmpty())
	})

	It("reports MultipleEnrolments naming every account and adopts nothing", func() {
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		fake.SeedUser(usernameDave, map[string]any{fieldAttributes: proof(u)})
		fake.SeedUser(usernameDavid, map[string]any{fieldAttributes: proof(u)})

		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonMultipleEnrolments))
		Expect(ready(u).Message).To(And(ContainSubstring(`"dave"`), ContainSubstring(`"david"`)))
		cur := get(u)
		Expect(cur.Status.UserPK).To(BeZero())
		Expect(cur.Status.Provenance).To(BeEmpty())
	})

	Context("policy change", func() {
		toAdopt := func(s *authentikiov1alpha1.AuthentikUserSpec) {
			s.ManagementPolicy, s.Invitation = policyAdopt, nil
		}

		It("withdraws the invitation when the policy changes to Adopt", func() {
			u := newUser(usernameDave, policyAdoptOrCreate, nil)
			mustReconcile(u)
			drainEvents(recorder)

			setSpec(u, toAdopt)
			mustReconcile(u)
			Expect(fake.Invitations()).To(BeEmpty())
			Expect(secretGone("dave-invitation")).To(BeTrue())
			Expect(get(u).Status.Invitation).To(BeNil())
			Expect(drainEvents(recorder)).To(ContainElement(ContainSubstring("Normal InvitationWithdrawn")))
			Expect(ready(u).Reason).To(Equal(reasonAdoptionTargetNotFound), "then it decides as an Adopt resource")
		})

		It("adopts the account of that name once the invitation is withdrawn", func() {
			u := newUser(usernameDave, policyAdoptOrCreate, nil)
			mustReconcile(u)
			pk := fake.SeedUser(usernameDave, nil)

			mustReconcile(u) // a Conflict: the invitation is withdrawn, the status kept
			Expect(ready(u).Reason).To(Equal(reasonConflict))
			setSpec(u, toAdopt)
			mustReconcile(u)
			cur := get(u)
			Expect(cur.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(cur.Status.UserPK).To(Equal(pk))
			Expect(cur.Status.Invitation).To(BeNil())
		})
	})

	Context("deletion while inviting", func() {
		It("deletes the live invitation first and keeps the finalizer while that fails", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			name := attemptOf(u, 1)

			fake.FailNextOn(http.MethodDelete, pathInvitations, http.StatusInternalServerError)
			Expect(k8sClient.Delete(ctx, u)).To(Succeed())
			_, err := reconcileUser(u)
			Expect(err).To(HaveOccurred())
			Expect(fake.InvitationByName(name)).NotTo(BeNil(), "the invitation remains")
			Expect(get(u).Finalizers).To(ContainElement(FinalizerName))

			mustReconcile(u)
			Expect(fake.InvitationByName(name)).To(BeNil())
			Expect(gone(u)).To(BeTrue(), "the finalizer is released")
		})

		It("deletes nothing when several invitations carry the name, and stays terminating", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			fake.SeedInvitation(attemptOf(u, 1), nil)

			Expect(k8sClient.Delete(ctx, u)).To(Succeed())
			_, err := reconcileUser(u)
			Expect(err).To(HaveOccurred())
			Expect(ready(u).Reason).To(Equal(reasonInvitationAmbiguous))
			Expect(fake.Invitations()).To(HaveLen(2))
			Expect(get(u).Finalizers).To(ContainElement(FinalizerName))
		})
	})

	It("reports InvitationAmbiguous and issues nothing when two invitations share its name", func() {
		u := newUser(usernameDave, policyCreate, nil)
		mustReconcile(u)
		name := attemptOf(u, 1)
		drainEvents(recorder)
		fake.SeedInvitation(name, nil) // a second one with the same name

		res, err := reconcileUser(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
		Expect(ready(u).Status).To(Equal(metav1.ConditionFalse))
		Expect(ready(u).Reason).To(Equal(reasonInvitationAmbiguous))
		Expect(ready(u).Message).To(ContainSubstring(`several invitations in Authentik are named "` + name + `"`))
		Expect(eventsNamed("Warning InvitationAmbiguous")).To(Equal(1))
		Expect(fake.Invitations()).To(HaveLen(2), "none deleted, none added")
		Expect(secretGone("dave-invitation")).To(BeFalse(), "the Secret is left as it is")

		By("the same Warning is not repeated while nothing changes")
		mustReconcile(u)
		Expect(eventsNamed("InvitationAmbiguous")).To(BeZero())

		By("a reissue is refused the same way")
		setSpec(u, withAttempt(2))
		mustReconcile(u)
		Expect(ready(u).Reason).To(Equal(reasonInvitationAmbiguous))
		Expect(fake.Invitations()).To(HaveLen(2))
		Expect(get(u).Status.Invitation.Name).To(Equal(name))
	})

	Context("AuthenticatorMissing", func() {
		It("is set on a recognised person with no authenticator and cleared when one appears", func() {
			u := newUser(usernameDave, policyCreate, nil)
			pk := enrol(u)

			missing := condition(u, ConditionAuthenticatorMissing)
			Expect(missing).NotTo(BeNil())
			Expect(missing.Status).To(Equal(metav1.ConditionTrue))
			Expect(missing.Reason).To(Equal(reasonNoAuthenticator))
			Expect(missing.Message).To(ContainSubstring("recovery link from the Authentik admin interface"))
			Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
			Expect(ready(u).Reason).To(Equal(reasonSynced))
			Expect(eventsNamed("Warning AuthenticatorMissing")).To(Equal(1))

			mustReconcile(u)
			Expect(eventsNamed("AuthenticatorMissing")).To(BeZero(), "one Warning per change")

			fake.AddAuthenticator(pk)
			mustReconcile(u)
			Expect(condition(u, ConditionAuthenticatorMissing)).To(BeNil())
			Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
		})

		It("is not set on an account that was adopted", func() {
			fake.SeedUser("adopted", nil)
			u := newUser("adopted", policyAdopt, nil)
			mustReconcile(u)
			Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(u, ConditionAuthenticatorMissing)).To(BeNil())
			for _, r := range fake.Requests(false) {
				Expect(r.Path).NotTo(ContainSubstring(pathAuthenticator))
			}
		})

		It("keeps the condition as it is, and Ready, when the authenticators cannot be listed", func() {
			u := newUser(usernameDave, policyCreate, nil)
			mustReconcile(u)
			pk := fake.EnrolFromInvitation(attemptOf(u, 1), "")
			fake.FailNextOn(http.MethodGet, pathAuthenticator, http.StatusInternalServerError)

			res, err := reconcileUser(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
			Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(u, ConditionAuthenticatorMissing)).To(BeNil(), "unknown is not missing")

			mustReconcile(u)
			Expect(condition(u, ConditionAuthenticatorMissing)).NotTo(BeNil())

			fake.AddAuthenticator(pk)
			fake.FailNextOn(http.MethodGet, pathAuthenticator, http.StatusInternalServerError)
			res, err = reconcileUser(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(userErrorRecheck))
			Expect(ready(u).Status).To(Equal(metav1.ConditionTrue))
			Expect(condition(u, ConditionAuthenticatorMissing)).NotTo(BeNil(), "kept while the list is unknown")

			res, err = reconcileUser(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(syncedRecheck))
			Expect(condition(u, ConditionAuthenticatorMissing)).To(BeNil())
		})
	})

	It("never writes the invitation pk to a condition, event, log line or returned error", func() {
		var seen []string
		step := func(u *authentikiov1alpha1.AuthentikUser) error {
			_, err := reconcileUser(u)
			if err != nil {
				seen = append(seen, err.Error())
			}
			var cur authentikiov1alpha1.AuthentikUser
			if k8sClient.Get(ctx, client.ObjectKeyFromObject(u), &cur) == nil {
				raw, merr := json.Marshal(cur.Status)
				Expect(merr).NotTo(HaveOccurred())
				seen = append(seen, string(raw))
			}
			seen = append(seen, drainEvents(recorder)...)
			return err
		}
		failDelete := func() {
			fake.FailNextOn(http.MethodDelete, pathInvitations, http.StatusServiceUnavailable)
		}

		By("a lost create and its recovery")
		fake.LoseNextInvitationCreate()
		dave := newUser(usernameDave, policyAdoptOrCreate, nil)
		Expect(step(dave)).To(HaveOccurred())
		Expect(step(dave)).To(Succeed())

		By("a Secret that is not its own")
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "erin-invitation", Namespace: namespace}})).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "erin-invitation", Namespace: namespace}})
		})
		erin := newUser("erin", policyCreate, nil)
		Expect(step(erin)).To(Succeed())
		Expect(ready(erin).Reason).To(Equal(reasonSecretConflict))

		By("a reissue whose delete fails, then works")
		setSpec(dave, withAttempt(2))
		failDelete()
		Expect(step(dave)).To(HaveOccurred())
		Expect(step(dave)).To(Succeed())

		By("a withdrawal whose delete fails, then works")
		setSpec(dave, func(s *authentikiov1alpha1.AuthentikUserSpec) { s.ManagementPolicy, s.Invitation = policyAdopt, nil })
		failDelete()
		Expect(step(dave)).To(HaveOccurred())
		Expect(step(dave)).To(Succeed())

		By("a conflict that withdraws, with a failing delete first")
		frank := newUser("frank", policyAdoptOrCreate, nil)
		Expect(step(frank)).To(Succeed())
		fake.SeedUser("frank", nil)
		failDelete()
		Expect(step(frank)).To(HaveOccurred())
		Expect(step(frank)).To(Succeed())

		By("a deletion whose delete fails, then works")
		Expect(step(erin)).To(Succeed())
		failDelete()
		Expect(k8sClient.Delete(ctx, erin)).To(Succeed())
		Expect(step(erin)).To(HaveOccurred())
		Expect(step(erin)).To(Succeed())

		pks := fake.IssuedInvitationPKs()
		Expect(len(pks)).To(BeNumerically(">=", 4), "the drive issued several invitations")
		seen = append(seen, logs.String())
		all := strings.Join(seen, "\n")
		for _, pk := range pks {
			Expect(all).NotTo(ContainSubstring(pk))
		}
	})
})
