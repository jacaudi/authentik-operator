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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

var _ = Describe("AuthentikCredentialsTemplate Controller", func() {
	ctx := context.Background()

	// validate creates a template, reconciles it and returns its Ready
	// condition.
	validate := func(name string, spec authentikiov1alpha1.AuthentikCredentialsTemplateSpec) *metav1.Condition {
		t := &authentikiov1alpha1.AuthentikCredentialsTemplate{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
		Expect(k8sClient.Create(ctx, t)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, t) })
		r := &AuthentikCredentialsTemplateReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(t)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(t), t)).To(Succeed())
		return meta.FindStatusCondition(t.Status.Conditions, ConditionReady)
	}

	It("marks a valid template Ready", func() {
		c := validate("tv-ok", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			Template:          "ID: {{ .ClientID }}\nSECRET: {{ .ClientSecret }}",
			NamespaceSelector: metav1.LabelSelector{},
		})
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Reason).To(Equal(reasonTemplateValid))
	})

	It("treats an empty template (default keys) as valid", func() {
		Expect(validate("tv-empty", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{}).Status).
			To(Equal(metav1.ConditionTrue))
	})

	It("reports labels and annotations the API server would reject", func() {
		c := validate("tv-meta", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			Labels: map[string]string{"invalid-value": "not a valid label value"}})
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(reasonInvalidMetadata))
		Expect(c.Message).To(ContainSubstring("spec.labels"))
	})

	It("reports an unquoted application name, with a hint", func() {
		c := validate("tv-name", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Template: "NAME: {{ .Name }}"})
		Expect(c.Reason).To(Equal(reasonTemplateError))
		Expect(c.Message).To(ContainSubstring("printf"))
	})

	It("reports unknown variables, disallowed functions and malformed output", func() {
		for name, tmpl := range map[string]string{
			"tv-unknown": "A: {{ .NoSuchVar }}",
			"tv-func":    "A: {{ len .ClientSecret }}",
			"tv-output":  "- not a map",
		} {
			c := validate(name, authentikiov1alpha1.AuthentikCredentialsTemplateSpec{Template: tmpl})
			Expect(c.Status).To(Equal(metav1.ConditionFalse), name)
			Expect(c.Reason).To(Equal(reasonTemplateError), name)
			Expect(c.Message).NotTo(ContainSubstring("example-client-secret"), name)
		}
	})

	It("reports an invalid namespaceSelector", func() {
		c := validate("tv-selector", authentikiov1alpha1.AuthentikCredentialsTemplateSpec{
			NamespaceSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "team", Operator: metav1.LabelSelectorOpIn}}},
		})
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(reasonInvalidNamespaceSelector))
	})
})
