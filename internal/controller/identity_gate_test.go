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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

var _ = Describe("Identity gate", func() {
	const namespace = "default"
	ctx := context.Background()

	mustSelector := func(s string) labels.Selector {
		sel, err := ParseNamespaceSelector(s)
		Expect(err).NotTo(HaveOccurred())
		return sel
	}

	It("denies every namespace on the default connection when the selector is empty", func() {
		err := identityGate{k8sClient, mustSelector("")}.check(ctx, namespace, nil)
		Expect(conditionReason(err, "")).To(Equal(reasonIdentityNotAllowed))
		Expect(err.Error()).To(ContainSubstring("--default-connection-identity-namespace-selector"))
	})

	It("treats a nil selector as deny", func() {
		err := identityGate{k8sClient, nil}.check(ctx, namespace, nil)
		Expect(conditionReason(err, "")).To(Equal(reasonIdentityNotAllowed))
	})

	It("allows a namespace the selector matches", func() {
		sel := mustSelector("kubernetes.io/metadata.name in (default)")
		Expect(identityGate{k8sClient, sel}.check(ctx, namespace, nil)).To(Succeed())
	})

	It("matches the labels of the namespace, not only its name", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "identity-gate-labelled", Labels: map[string]string{"identity": "allowed"},
		}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })

		Expect(identityGate{k8sClient, mustSelector("identity=allowed")}.check(ctx, ns.Name, nil)).To(Succeed())
		err := identityGate{k8sClient, mustSelector("identity=denied")}.check(ctx, ns.Name, nil)
		Expect(conditionReason(err, "")).To(Equal(reasonIdentityNotAllowed))
	})

	It("refuses a namespace the selector does not match", func() {
		err := identityGate{k8sClient, mustSelector("kubernetes.io/metadata.name=other")}.check(ctx, namespace, nil)
		Expect(conditionReason(err, "")).To(Equal(reasonIdentityNotAllowed))
		Expect(err.Error()).To(ContainSubstring(`"default"`))
	})

	It("never gates a resource with spec.authentikRef", func() {
		ref := &authentikiov1alpha1.AuthentikRef{Name: "main"}
		Expect(identityGate{k8sClient, nil}.check(ctx, namespace, ref)).To(Succeed())
		ok, err := identityGate{k8sClient, nil}.allowed(ctx, namespace, ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("reports a denied namespace as not allowed without an error", func() {
		ok, err := identityGate{k8sClient, mustSelector("")}.allowed(ctx, namespace, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})
