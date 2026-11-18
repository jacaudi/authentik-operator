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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const tokenSecretKey = "token"

func TestParseNamespaceSelector(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		labels    labels.Set
		wantMatch bool
		wantErr   bool
	}{
		{"empty denies every namespace", "", labels.Set{corev1.LabelMetadataName: "team-a"}, false, false},
		{"blank denies every namespace", "  ", labels.Set{}, false, false},
		{"metadata name matches", corev1.LabelMetadataName + "=team-a",
			labels.Set{corev1.LabelMetadataName: "team-a"}, true, false},
		{"metadata name mismatch", corev1.LabelMetadataName + "=team-a",
			labels.Set{corev1.LabelMetadataName: "team-b"}, false, false},
		{"set based selector", "kubernetes.io/metadata.name in (team-a,team-b)",
			labels.Set{corev1.LabelMetadataName: "team-b"}, true, false},
		{"invalid selector", "a==b==c(", nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel, err := ParseNamespaceSelector(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, c.wantErr)
			}
			if err != nil {
				return
			}
			if got := sel.Matches(c.labels); got != c.wantMatch {
				t.Fatalf("Matches(%v) = %t, want %t", c.labels, got, c.wantMatch)
			}
		})
	}
}

var _ = Describe("Adoption gate on the default connection", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake        *fakeauthentik.Server
		connections *connection.Resolver
		providers   *AuthentikOAuthProviderReconciler
		apps        *AuthentikApplicationReconciler
	)

	BeforeEach(func() {
		fake = fakeauthentik.New(testAPIToken)
		connections = &connection.Resolver{
			Reader:  k8sClient,
			Default: &authentik.Config{URL: fake.URL, Token: testAPIToken, RPS: -1, MaxRetries: -1},
		}
		providers = &AuthentikOAuthProviderReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
			Connections: connections, APIReader: k8sClient}
		apps = &AuthentikApplicationReconciler{Client: indexedClient{k8sClient}, Scheme: k8sClient.Scheme(),
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

	selector := func(s string) labels.Selector {
		sel, err := ParseNamespaceSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return sel
	}

	newProvider := func(name string, policy authentikiov1alpha1.ManagementPolicy, dryRun bool,
		ref *authentikiov1alpha1.AuthentikRef) *authentikiov1alpha1.AuthentikOAuthProvider {
		p := &authentikiov1alpha1.AuthentikOAuthProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{
				AuthorizationFlow: fakeauthentik.FlowAuthorizationSlug,
				InvalidationFlow:  fakeauthentik.FlowInvalidationSlug,
				RedirectURIs:      []authentikiov1alpha1.RedirectURI{{URL: "https://" + name + ".example.com/cb"}},
				AuthentikRef:      ref,
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy, DryRun: dryRun},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		cleanup(p)
		return p
	}
	newApp := func(name string, policy authentikiov1alpha1.ManagementPolicy,
		dryRun bool) *authentikiov1alpha1.AuthentikApplication {
		a := &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikApplicationSpec{Name: "App " + name,
				APIManagementSpec: authentikiov1alpha1.APIManagementSpec{ManagementPolicy: policy, DryRun: dryRun}},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		cleanup(a)
		return a
	}

	reconcileProvider := func(p *authentikiov1alpha1.AuthentikOAuthProvider) {
		_, err := providers.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(p)})
		Expect(err).NotTo(HaveOccurred())
	}
	reconcileApp := func(a *authentikiov1alpha1.AuthentikApplication) {
		_, err := apps.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(a)})
		Expect(err).NotTo(HaveOccurred())
	}
	providerReady := func(p *authentikiov1alpha1.AuthentikOAuthProvider) *metav1.Condition {
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		return meta.FindStatusCondition(p.Status.Conditions, ConditionReady)
	}
	appReady := func(a *authentikiov1alpha1.AuthentikApplication) *metav1.Condition {
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(a), a)).To(Succeed())
		return meta.FindStatusCondition(a.Status.Conditions, ConditionReady)
	}

	policies := []authentikiov1alpha1.ManagementPolicy{authentikiov1alpha1.ManagementPolicyAdopt,
		authentikiov1alpha1.ManagementPolicyAdoptOrCreate, authentikiov1alpha1.ManagementPolicyReadOnly}

	It("refuses to adopt, observe or create-or-adopt a provider when no selector is set", func() {
		for i, policy := range policies {
			name := fmt.Sprintf("gated-p-%d", i)
			fake.SeedProviderWith(map[string]any{jsonName: name})
			p := newProvider(name, policy, false, nil)
			providers.AdoptionNamespaceSelector = selector("")
			reconcileProvider(p)
			Expect(providerReady(p).Reason).To(Equal(reasonAdoptionNotAllowed), string(policy))
			Expect(p.Status.ProviderPK).To(BeZero(), string(policy))
			Expect(p.Status.Provenance).To(BeEmpty(), string(policy))
		}
		Expect(fake.Requests(false)).To(BeEmpty(), "a refused resource must not even read Authentik")
	})

	It("refuses an application the same way", func() {
		for i, policy := range policies {
			name := fmt.Sprintf("gated-a-%d", i)
			fake.SeedApplication(name, nil)
			a := newApp(name, policy, false)
			apps.AdoptionNamespaceSelector = selector("")
			reconcileApp(a)
			Expect(appReady(a).Reason).To(Equal(reasonAdoptionNotAllowed), string(policy))
			Expect(a.Status.ApplicationUID).To(BeEmpty(), string(policy))
		}
		Expect(fake.Requests(false)).To(BeEmpty())
	})

	It("covers dryRun: a refused namespace cannot enumerate through a dry run", func() {
		fake.SeedProviderWith(map[string]any{jsonName: "dry-gated"})
		providers.AdoptionNamespaceSelector = selector("")
		p := newProvider("dry-gated", authentikiov1alpha1.ManagementPolicyAdopt, true, nil)
		reconcileProvider(p)
		Expect(providerReady(p).Reason).To(Equal(reasonAdoptionNotAllowed))
		Expect(fake.Requests(false)).To(BeEmpty())
	})

	It("refuses a namespace the selector does not match", func() {
		fake.SeedProviderWith(map[string]any{jsonName: "other-ns"})
		providers.AdoptionNamespaceSelector = selector(corev1.LabelMetadataName + "=somewhere-else")
		p := newProvider("other-ns", authentikiov1alpha1.ManagementPolicyAdopt, false, nil)
		reconcileProvider(p)
		Expect(providerReady(p).Reason).To(Equal(reasonAdoptionNotAllowed))
	})

	It("allows a namespace matched on kubernetes.io/metadata.name", func() {
		fake.SeedProviderWith(map[string]any{jsonName: "allowed"})
		providers.AdoptionNamespaceSelector = selector(corev1.LabelMetadataName + "=" + namespace)
		p := newProvider("allowed", authentikiov1alpha1.ManagementPolicyAdopt, false, nil)
		reconcileProvider(p)
		Expect(providerReady(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(p.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))

		fake.SeedApplication("allowed-app", nil)
		apps.AdoptionNamespaceSelector = selector(corev1.LabelMetadataName + "=" + namespace)
		a := newApp("allowed-app", authentikiov1alpha1.ManagementPolicyAdopt, false)
		reconcileApp(a)
		Expect(appReady(a).Status).To(Equal(metav1.ConditionTrue))
	})

	It("never gates the Create policy", func() {
		providers.AdoptionNamespaceSelector = selector("")
		p := newProvider("create-ok", authentikiov1alpha1.ManagementPolicyCreate, false, nil)
		reconcileProvider(p)
		Expect(providerReady(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(p.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceCreated))
	})

	It("allows adoption through a same-namespace authentikRef whatever the selector says", func() {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-token", Namespace: namespace, Labels: map[string]string{
				authentikiov1alpha1.LabelConnection: authentikiov1alpha1.LabelConnectionValue}},
			StringData: map[string]string{tokenSecretKey: testAPIToken},
		}
		Expect(k8sClient.Create(ctx, sec)).To(Succeed())
		DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, sec)) })
		d := &authentikiov1alpha1.AuthentikDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-deploy", Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikDeploymentSpec{
				Mode: authentikiov1alpha1.DeploymentModeConnectOnly,
				Connection: &authentikiov1alpha1.ConnectionSpec{
					URL:            fake.URL,
					TokenSecretRef: authentikiov1alpha1.SecretKeyRef{Name: sec.Name},
				},
			},
		}
		Expect(k8sClient.Create(ctx, d)).To(Succeed())
		DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, d)) })

		fake.SeedProviderWith(map[string]any{jsonName: "via-ref"})
		providers.AdoptionNamespaceSelector = selector("")
		p := newProvider("via-ref", authentikiov1alpha1.ManagementPolicyAdopt, false,
			&authentikiov1alpha1.AuthentikRef{Name: d.Name})
		reconcileProvider(p)
		Expect(providerReady(p).Status).To(Equal(metav1.ConditionTrue))
		Expect(p.Status.Provenance).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
	})
})
