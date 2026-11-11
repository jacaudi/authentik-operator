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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/test/aktest"
)

// typicalTemplate is the kind of template an administrator writes for an
// application's environment: every variable the operator offers, quoted.
const typicalTemplateName = "e2e-typical"

const typicalTemplate = `CLIENT_ID: {{ printf "%q" .ClientID }}
CLIENT_SECRET: {{ printf "%q" .ClientSecret }}
ISSUER_URL: {{ printf "%q" .IssuerURL }}
AUTH_URL: {{ printf "%q" .AuthURL }}
TOKEN_URL: {{ printf "%q" .TokenURL }}
USERINFO_URL: {{ printf "%q" .UserInfoURL }}
`

func templateCreds(d map[string]string) creds {
	return creds{ClientID: d["CLIENT_ID"], ClientSecret: d["CLIENT_SECRET"], Issuer: d["ISSUER_URL"],
		AuthURL: d["AUTH_URL"], TokenURL: d["TOKEN_URL"], UserInfoURL: d["USERINFO_URL"]}
}

func defaultCreds(d map[string]string) creds {
	return creds{ClientID: d["client-id"], ClientSecret: d["client-secret"], Issuer: d["issuer-url"],
		AuthURL: d["authorization-url"], TokenURL: d["token-url"], UserInfoURL: d["userinfo-url"]}
}

var _ = Describe("operator against a real Authentik", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	const (
		createdName     = "e2e-oidc"
		createdRedirect = "https://e2e-oidc.invalid/callback"
		adoptName       = "e2e-adopt"
		adoptExisting   = "e2e-adopt-provider" // what JeffResc would have called it
		adoptRedirect   = "https://e2e-adopt.invalid/callback"
	)

	BeforeAll(func(ctx SpecContext) {
		tmpl := &authentikiov1alpha1.AuthentikCredentialsTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: typicalTemplateName},
			Spec: authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
				Template: typicalTemplate,
				NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{
					"kubernetes.io/metadata.name": appsNS}},
			},
		}
		Expect(client.IgnoreAlreadyExists(k8s.Create(ctx, tmpl))).To(Succeed())
	})

	It("runs the chart-installed operator and serves reconcile metrics", func(ctx SpecContext) {
		Eventually(func() bool { return deploymentReady(ctx, operatorNS, operatorRelease+"-controller-manager") }).
			WithContext(ctx).WithTimeout(waitTimeout).Should(BeTrue())
		Eventually(func(g Gomega) {
			m, err := scrapeMetrics(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			_, err = reconcileTotal(m, "authentikoauthprovider")
			g.Expect(err).To(Succeed())
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
	})

	// Scenarios 1, 2: OAuthProvider + Application + CredentialsTemplate.
	Context("a provider the operator creates", func() {
		It("renders a Secret with the template's keys and Authentik's own values", func(ctx SpecContext) {
			p := newProvider(createdName, createdRedirect)
			p.Spec.Secret = &authentikiov1alpha1.CredentialsSecretSpec{
				Name: "e2e-oidc-creds", TemplateRef: &authentikiov1alpha1.CredentialsTemplateRef{Name: typicalTemplateName}}
			Expect(k8s.Create(ctx, p)).To(Succeed())
			a := newApplication(createdName, "E2E OIDC", "e2e-oidc", createdName)
			a.Spec.MetaPublisher = "e2e-publisher"
			Expect(k8s.Create(ctx, a)).To(Succeed())

			providerCondition(ctx, createdName, metav1.ConditionTrue, "Synced")
			applicationCondition(ctx, createdName, metav1.ConditionTrue)

			got := getProvider(ctx, Default, createdName)
			Expect(got.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			Expect(got.Status.ProviderPK).NotTo(BeZero())

			data := eventuallySecret(ctx, "e2e-oidc-creds")
			Expect(data).To(HaveLen(6))
			for _, k := range []string{"CLIENT_ID", "CLIENT_SECRET", "ISSUER_URL", "AUTH_URL", "TOKEN_URL", "USERINFO_URL"} {
				Expect(data).To(HaveKey(k))
				Expect(data[k]).NotTo(BeEmpty(), k)
			}

			By("comparing the rendered values with what Authentik itself reports")
			var prov aktest.Obj
			provPath := fmt.Sprintf("/api/v3/providers/oauth2/%d/", got.Status.ProviderPK)
			Expect(ak.Do(ctx, "GET", provPath, nil, &prov)).To(Succeed())
			Expect(data["CLIENT_ID"]).To(Equal(aktest.Str(prov, "client_id")))
			Expect(data["CLIENT_SECRET"]).To(Equal(aktest.Str(prov, "client_secret")))
			var urls aktest.Obj
			Expect(ak.Do(ctx, "GET", provPath+"setup_urls/", nil, &urls)).To(Succeed())
			Expect(data["ISSUER_URL"]).To(Equal(aktest.Str(urls, "issuer")))
			Expect(data["AUTH_URL"]).To(Equal(aktest.Str(urls, "authorize")))
			Expect(data["TOKEN_URL"]).To(Equal(aktest.Str(urls, "token")))
			Expect(data["USERINFO_URL"]).To(Equal(aktest.Str(urls, "user_info")))
			Expect(data["ISSUER_URL"]).To(HavePrefix(authentikURL + "/application/o/e2e-oidc/"))
		})

		// Guards JeffResc #279: on Authentik 2026.8+ a provider created with
		// no grant types rejects every login.
		It("lets a user complete an OIDC authorization-code login", func(ctx SpecContext) {
			c := templateCreds(secretData(ctx, "e2e-oidc-creds"))
			res, err := login(ctx, c, createdRedirect)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Claims["iss"]).To(Equal(c.Issuer))
			Expect(res.UserInfo["sub"]).To(Equal(res.Claims["sub"]))
			Expect(res.UserInfo["preferred_username"]).To(Equal(bootstrapUser))
		})
	})

	// Scenarios 3, 4: adoption of an object another tool made.
	Context("adopting objects that already exist in Authentik", func() {
		var (
			pk           int
			before       aktest.Obj
			beforeApp    aktest.Obj
			writesBefore int
		)

		It("pre-creates a provider and application the way JeffResc would", func(ctx SpecContext) {
			By("removing leftovers of an earlier run on this cluster")
			Expect(ak.DeleteApplicationIfExists(ctx, adoptName)).To(Succeed())
			Expect(ak.DeleteOAuthProviderByNameIfExists(ctx, adoptExisting)).To(Succeed())
			created, err := ak.CreateOAuthProvider(ctx, adoptExisting, []string{adoptRedirect})
			Expect(err).NotTo(HaveOccurred())
			Expect(fmt.Sscan(aktest.Str(created, "pk"), &pk)).To(Equal(1))
			_, err = ak.CreateApplication(ctx, "E2E Adopt", adoptName, pk)
			Expect(err).NotTo(HaveOccurred())
			before, err = ak.OAuthProvider(ctx, pk)
			Expect(err).NotTo(HaveOccurred())
			beforeApp, err = ak.ApplicationBySlug(ctx, adoptName)
			Expect(err).NotTo(HaveOccurred())
			Expect(aktest.Str(before, "client_secret")).NotTo(BeEmpty())
			writesBefore = oauthWrites(ctx, Default)
		})

		It("reports a dry run and changes nothing in Authentik", func(ctx SpecContext) {
			p := newProvider(adoptName, adoptRedirect)
			p.Spec.Adoption.Enabled = true
			p.Spec.Adoption.DryRun = true
			p.Spec.Adoption.Match = &authentikiov1alpha1.ProviderMatch{Name: adoptExisting}
			p.Spec.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: "e2e-adopt-creds"}
			Expect(k8s.Create(ctx, p)).To(Succeed())
			a := newApplication(adoptName, "E2E Adopt", adoptName, adoptName)
			a.Spec.Adoption.Enabled = true
			a.Spec.Adoption.DryRun = true
			Expect(k8s.Create(ctx, a)).To(Succeed())

			providerCondition(ctx, adoptName, metav1.ConditionFalse, "AdoptionDryRun")
			applicationCondition(ctx, adoptName, metav1.ConditionFalse, "AdoptionDryRun", "ProviderNotReady")

			// Let a further reconcile finish so "unchanged" is not just "not yet".
			pokeAndWait(ctx, getProvider(ctx, Default, adoptName))

			Expect(getProvider(ctx, Default, adoptName).Status.Provenance).To(BeEmpty(), "a dry run decides nothing")
			after, err := ak.OAuthProvider(ctx, pk)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before), "provider changed during a dry run")
			afterApp, err := ak.ApplicationBySlug(ctx, adoptName)
			Expect(err).NotTo(HaveOccurred())
			Expect(afterApp).To(Equal(beforeApp), "application changed during a dry run")
			Expect(oauthWrites(ctx, Default)).To(Equal(writesBefore), "Authentik recorded a write during a dry run")
			var s corev1.Secret
			Expect(k8s.Get(ctx, key("e2e-adopt-creds"), &s)).NotTo(Succeed(), "a dry run renders no Secret")
		})

		It("adopts for real, keeping client_id and client_secret byte for byte", func(ctx SpecContext) {
			p := getProvider(ctx, Default, adoptName)
			p.Spec.Adoption.DryRun = false
			Expect(k8s.Update(ctx, p)).To(Succeed())
			providerCondition(ctx, adoptName, metav1.ConditionTrue, "Synced")
			Expect(getProvider(ctx, Default, adoptName).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			Expect(getProvider(ctx, Default, adoptName).Status.ProviderPK).To(BeEquivalentTo(pk))

			a := getApplication(ctx, Default, adoptName)
			a.Spec.Adoption.DryRun = false
			Expect(k8s.Update(ctx, a)).To(Succeed())
			applicationCondition(ctx, adoptName, metav1.ConditionTrue)
			Expect(getApplication(ctx, Default, adoptName).Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))

			data := eventuallySecret(ctx, "e2e-adopt-creds")
			Expect(data["client-id"]).To(Equal(aktest.Str(before, "client_id")))
			Expect(data["client-secret"]).To(Equal(aktest.Str(before, "client_secret")))
			now, err := ak.OAuthProvider(ctx, pk)
			Expect(err).NotTo(HaveOccurred())
			Expect(aktest.Str(now, "client_id")).To(Equal(aktest.Str(before, "client_id")))
			Expect(aktest.Str(now, "client_secret")).To(Equal(aktest.Str(before, "client_secret")))
		})

		// The pre-created provider has whatever grant types Authentik gave it;
		// adoption writes the spec's. On the 2026.8 leg this login is the
		// guard for JeffResc #279 (no grant types: "Invalid grant_type").
		It("lets a user log in with the adopted credentials", func(ctx SpecContext) {
			res, err := login(ctx, defaultCreds(secretData(ctx, "e2e-adopt-creds")), adoptRedirect)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.UserInfo["preferred_username"]).To(Equal(bootstrapUser))
		})

		// Scenario 4.
		It("reports AdoptionTargetNotFound and creates nothing when the target is absent", func(ctx SpecContext) {
			providersBefore, err := ak.Count(ctx, "/api/v3/providers/oauth2/")
			Expect(err).NotTo(HaveOccurred())
			appsBefore, err := ak.Count(ctx, "/api/v3/core/applications/")
			Expect(err).NotTo(HaveOccurred())

			p := newProvider("e2e-miss", "https://e2e-miss.invalid/callback")
			p.Spec.Adoption.Enabled = true // createIfMissing left unset
			p.Spec.Adoption.Match = &authentikiov1alpha1.ProviderMatch{Name: "e2e-miss-provider"}
			Expect(k8s.Create(ctx, p)).To(Succeed())
			a := newApplication("e2e-miss", "E2E Miss", "e2e-miss", "e2e-miss")
			a.Spec.Adoption.Enabled = true
			Expect(k8s.Create(ctx, a)).To(Succeed())

			providerCondition(ctx, "e2e-miss", metav1.ConditionFalse, "AdoptionTargetNotFound")
			applicationCondition(ctx, "e2e-miss", metav1.ConditionFalse, "AdoptionTargetNotFound", "ProviderNotReady")
			pokeAndWait(ctx, getProvider(ctx, Default, "e2e-miss"))

			for _, n := range []string{"e2e-miss", "e2e-miss-provider"} {
				pr, err := ak.OAuthProviderByName(ctx, n)
				Expect(err).NotTo(HaveOccurred())
				Expect(pr).To(BeNil(), "a provider named %s was created", n)
			}
			app, err := ak.ApplicationBySlug(ctx, "e2e-miss")
			Expect(err).NotTo(HaveOccurred())
			Expect(app).To(BeNil(), "an application was created")
			providersAfter, err := ak.Count(ctx, "/api/v3/providers/oauth2/")
			Expect(err).NotTo(HaveOccurred())
			appsAfter, err := ak.Count(ctx, "/api/v3/core/applications/")
			Expect(err).NotTo(HaveOccurred())
			Expect(providersAfter).To(Equal(providersBefore))
			Expect(appsAfter).To(Equal(appsBefore))
			Expect(getProvider(ctx, Default, "e2e-miss").Status.Provenance).To(BeEmpty())

			// Remove them again: their one-minute requeues would otherwise
			// add reconciles to the counts the idempotency spec relies on.
			deleteAndWait(ctx, getApplication(ctx, Default, "e2e-miss"))
			deleteAndWait(ctx, getProvider(ctx, Default, "e2e-miss"))
		})
	})

	// Scenario 6.
	Context("drift", func() {
		It("restores fields changed in Authentik behind the operator's back", func(ctx SpecContext) {
			pk := int(getProvider(ctx, Default, createdName).Status.ProviderPK)
			Expect(ak.PatchOAuthProvider(ctx, pk, aktest.Obj{
				"access_token_validity": "minutes=10", // spec default: minutes=5
				"redirect_uris":         []aktest.Obj{{"matching_mode": "strict", "url": "https://drift.invalid/cb"}},
			})).To(Succeed())
			Expect(ak.Do(ctx, "PATCH", "/api/v3/core/applications/e2e-oidc/",
				aktest.Obj{"meta_publisher": "someone-else"}, nil)).To(Succeed())

			// The sync interval when in sync is five minutes; poking the CRs
			// is the same reconcile, without the wait.
			pokeAndWait(ctx, getProvider(ctx, Default, createdName), getApplication(ctx, Default, createdName))
			Eventually(func(g Gomega) {
				prov, err := ak.OAuthProvider(ctx, pk)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(aktest.Str(prov, "access_token_validity")).To(Equal("minutes=5"))
				uris, _ := prov["redirect_uris"].([]any)
				g.Expect(uris).To(HaveLen(1))
				first, _ := uris[0].(map[string]any)
				g.Expect(aktest.Str(first, "url")).To(Equal(createdRedirect))
				app, err := ak.ApplicationBySlug(ctx, "e2e-oidc")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(aktest.Str(app, "meta_publisher")).To(Equal("e2e-publisher"))
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())

			By("a login still works with the restored provider")
			_, err := login(ctx, templateCreds(secretData(ctx, "e2e-oidc-creds")), createdRedirect)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// Scenario 7 (TASK-02 review note): version-dependent normalization
	// differences would make Ready providers PATCH forever.
	Context("idempotency", func() {
		It("sends no writes to Authentik when Ready objects are reconciled again", func(ctx SpecContext) {
			pk := int(getProvider(ctx, Default, createdName).Status.ProviderPK)

			By("proving the audit measurement: the model filter is selective and a no-op PATCH is counted")
			none, err := ak.ModelWriteCount(ctx, "authentik_core", "nosuchmodel", "model_updated")
			Expect(err).NotTo(HaveOccurred())
			Expect(none).To(BeZero(), "the audit event filter is ignored")
			base := oauthWrites(ctx, Default)
			Expect(ak.PatchOAuthProvider(ctx, pk, aktest.Obj{"name": createdName})).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(oauthWrites(ctx, g)).To(BeNumerically(">", base))
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())

			By("proving the operator-side measurement: a real change is counted as an Updated log line")
			logBase := operatorUpdates(ctx, Default)
			Expect(ak.PatchOAuthProvider(ctx, pk, aktest.Obj{"access_token_validity": "minutes=7"})).To(Succeed())
			poke(ctx, Default, getProvider(ctx, Default, createdName))
			Eventually(func(g Gomega) {
				g.Expect(operatorUpdates(ctx, g)).To(BeNumerically(">", logBase))
				prov, err := ak.OAuthProvider(ctx, pk)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(aktest.Str(prov, "access_token_validity")).To(Equal("minutes=5"))
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())

			By("forcing reconciles of every kind, each waited for through the controller metrics")
			settled := oauthWrites(ctx, Default)
			updatesSettled := operatorUpdates(ctx, Default)
			const rounds = 3
			for range rounds {
				var objs []client.Object
				for _, n := range []string{createdName, adoptName} {
					objs = append(objs, getProvider(ctx, Default, n), getApplication(ctx, Default, n))
				}
				pokeAndWait(ctx, objs...)
			}

			Consistently(func(g Gomega) {
				g.Expect(oauthWrites(ctx, g)).To(Equal(settled), "a reconcile of a Ready object wrote to Authentik")
			}).WithContext(ctx).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
			Expect(operatorUpdates(ctx, Default)).To(Equal(updatesSettled),
				"the operator sent a PATCH while reconciling Ready objects")

			for _, n := range []string{createdName, adoptName} {
				providerCondition(ctx, n, metav1.ConditionTrue, "Synced")
				applicationCondition(ctx, n, metav1.ConditionTrue)
			}
		})
	})

	// Scenario 5.
	Context("deleting resources", func() {
		It("retains what an adopted resource adopted (default deletionPolicy)", func(ctx SpecContext) {
			Expect(getProvider(ctx, Default, adoptName).Spec.DeletionPolicy).To(BeEmpty())
			deleteAndWait(ctx, getApplication(ctx, Default, adoptName))
			deleteAndWait(ctx, getProvider(ctx, Default, adoptName))

			prov, err := ak.OAuthProviderByName(ctx, adoptExisting)
			Expect(err).NotTo(HaveOccurred())
			Expect(prov).NotTo(BeNil(), "the adopted provider was deleted from Authentik")
			app, err := ak.ApplicationBySlug(ctx, adoptName)
			Expect(err).NotTo(HaveOccurred())
			Expect(app).NotTo(BeNil(), "the adopted application was deleted from Authentik")
		})

		It("removes what a resource created (default deletionPolicy)", func(ctx SpecContext) {
			pk := int(getProvider(ctx, Default, createdName).Status.ProviderPK)
			deleteAndWait(ctx, getApplication(ctx, Default, createdName))
			deleteAndWait(ctx, getProvider(ctx, Default, createdName))

			Eventually(func(g Gomega) {
				prov, err := ak.OAuthProvider(ctx, pk)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(prov).To(BeNil())
				app, err := ak.ApplicationBySlug(ctx, createdName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(app).To(BeNil())
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
			// Garbage collection of the owned Secret is asynchronous.
			Eventually(func(g Gomega) {
				var s corev1.Secret
				g.Expect(apierrors.IsNotFound(k8s.Get(ctx, key("e2e-oidc-creds"), &s))).To(BeTrue(),
					"the rendered Secret is owned and goes with its provider")
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
		})

		// An explicit deletionPolicy overrides what provenance would decide.
		It("keeps what a resource created when deletionPolicy is Retain", func(ctx SpecContext) {
			p := newProvider("e2e-keep", "https://e2e-keep.invalid/callback")
			p.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyRetain
			Expect(k8s.Create(ctx, p)).To(Succeed())
			providerCondition(ctx, "e2e-keep", metav1.ConditionTrue, "Synced")
			Expect(getProvider(ctx, Default, "e2e-keep").Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
			deleteAndWait(ctx, getProvider(ctx, Default, "e2e-keep"))
			kept, err := ak.OAuthProviderByName(ctx, "e2e-keep")
			Expect(err).NotTo(HaveOccurred())
			Expect(kept).NotTo(BeNil(), "a Created provider with deletionPolicy Retain was deleted")
			Expect(ak.DeleteOAuthProviderByNameIfExists(ctx, "e2e-keep")).To(Succeed()) // tidy up
		})

		It("deletes what a resource adopted when deletionPolicy is Delete", func(ctx SpecContext) {
			_, err := ak.CreateOAuthProvider(ctx, "e2e-adel-provider", []string{"https://e2e-adel.invalid/callback"})
			Expect(err).NotTo(HaveOccurred())
			p := newProvider("e2e-adel", "https://e2e-adel.invalid/callback")
			p.Spec.Adoption.Enabled = true
			p.Spec.Adoption.Match = &authentikiov1alpha1.ProviderMatch{Name: "e2e-adel-provider"}
			p.Spec.DeletionPolicy = authentikiov1alpha1.DeletionPolicyDelete
			Expect(k8s.Create(ctx, p)).To(Succeed())
			providerCondition(ctx, "e2e-adel", metav1.ConditionTrue, "Synced")
			Expect(getProvider(ctx, Default, "e2e-adel").Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
			deleteAndWait(ctx, getProvider(ctx, Default, "e2e-adel"))
			Eventually(func(g Gomega) {
				gone, err := ak.OAuthProviderByName(ctx, "e2e-adel-provider")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gone).To(BeNil(), "an Adopted provider with deletionPolicy Delete was kept")
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
		})
	})

	// Scenario 8. Last: it re-points the operator's default connection.
	Context("custom CA", func() {
		It("reaches an Authentik behind a private CA only with authentik.caBundle", func(ctx SpecContext) {
			const tlsHost = "authentik-tls." + authentikNS + ".svc.cluster.local"
			tlsURL := "https://" + tlsHost

			By("terminating TLS in front of Authentik with a certificate from a private CA")
			pki, err := aktest.NewPKI(tlsHost)
			Expect(err).NotTo(HaveOccurred())
			// A CA is generated per run, so a TLS terminator and CA bundle left by an
			// earlier run on this cluster would not match it: start from nothing.
			_, err = kubectl(ctx, "-n", authentikNS, "delete", "--ignore-not-found", "--wait=true",
				"deploy/authentik-tls", "svc/authentik-tls", "secret/authentik-tls-cert", "configmap/authentik-tls-nginx")
			Expect(err).NotTo(HaveOccurred())
			_, err = kubectl(ctx, "-n", operatorNS, "delete", "--ignore-not-found", "configmap/authentik-ca")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8s.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "authentik-tls-cert", Namespace: authentikNS},
				Type:       corev1.SecretTypeTLS,
				Data:       map[string][]byte{"tls.crt": pki.ServerCrt, "tls.key": pki.ServerKey},
			})).To(Succeed())
			Expect(kubectlApply(ctx, nginxTLSManifest)).To(Succeed())
			Expect(waitDeployment(ctx, authentikNS, "authentik-tls", 8*time.Minute)).To(Succeed())
			Expect(k8s.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "authentik-ca", Namespace: operatorNS},
				Data:       map[string]string{"ca.crt": string(pki.CACert)},
			})).To(Succeed())

			By("pointing the operator at the https URL WITHOUT the CA: it must refuse")
			Expect(installOperator(ctx, tlsURL)).To(Succeed())
			p := newProvider("e2e-ca", "https://e2e-ca.invalid/callback")
			p.Spec.Secret = &authentikiov1alpha1.CredentialsSecretSpec{Name: "e2e-ca-creds"}
			Expect(k8s.Create(ctx, p)).To(Succeed())
			Expect(k8s.Create(ctx, newApplication("e2e-ca", "E2E CA", "e2e-ca", "e2e-ca"))).To(Succeed())
			Eventually(func(g Gomega) {
				got := getProvider(ctx, g, "e2e-ca")
				c := condition(got.Status.Conditions, condReady)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(strings.ToLower(c.Message)).To(ContainSubstring("x509"))
			}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
			none, err := ak.OAuthProviderByName(ctx, "e2e-ca")
			Expect(err).NotTo(HaveOccurred())
			Expect(none).To(BeNil(), "nothing may be created through an untrusted connection")

			By("adding authentik.caBundle: the same resource becomes Ready")
			Expect(installOperator(ctx, tlsURL,
				setString, "authentik.caBundle.configMap.name=authentik-ca")).To(Succeed())
			Eventually(func(g Gomega) {
				poke(ctx, g, getProvider(ctx, g, "e2e-ca"))
				got := getProvider(ctx, g, "e2e-ca")
				c := condition(got.Status.Conditions, condReady)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Status).To(Equal(metav1.ConditionTrue), c.Message)
			}).WithContext(ctx).WithTimeout(3 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
			created, err := ak.OAuthProviderByName(ctx, "e2e-ca")
			Expect(err).NotTo(HaveOccurred())
			Expect(created).NotTo(BeNil())
			data := eventuallySecret(ctx, "e2e-ca-creds")
			Expect(data["issuer-url"]).To(HavePrefix(tlsURL + "/application/o/"))
		})
	})

	// Scenario 9.
	PIt("migrates a JeffResc-managed install end to end", func() {
		// TODO(TASK-09): the converter and runbook arrive on their own branch.
		// Apply JeffResc CRs, convert them with the converter, adopt with
		// dryRun then for real, and prove client_id/secret and a login survive.
		Skip("pending TASK-09 converter and runbook")
	})

})
