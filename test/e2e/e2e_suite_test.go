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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jacaudi/authentik-operator/test/aktest"
)

var (
	// ak is the test's own view of Authentik, independent of the operator.
	ak *aktest.API
	// akForward is the port-forward behind ak.
	akForward *portForward
)

// TestE2E runs the end-to-end suite against a real Authentik in a dedicated
// kind cluster. See docs/roadmap/TASK-08-e2e-against-real-authentik.md.
//
// Environment: KIND_CLUSTER (cluster name; kubectl's current context must be
// kind-<name>), AUTHENTIK_CHART_VERSION (the Authentik chart/app version
// under test), E2E_IMAGE, CONTAINER_TOOL (docker|podman), E2E_SKIP_BUILD.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	GinkgoWriter.Printf("Starting authentik-operator e2e suite against Authentik %s\n", chartVersion)
	RunSpecs(t, "e2e suite", Label("authentik-"+chartVersion))
}

var _ = BeforeSuite(func(ctx SpecContext) {
	// kuberc would make runs differ between developer machines.
	GinkgoT().Setenv("KUBECTL_KUBERC", "false")

	By("checking the kubectl context")
	Expect(guardContext(ctx)).To(Succeed())
	var err error
	k8s, err = newClient()
	Expect(err).NotTo(HaveOccurred())

	By("building and loading the operator image")
	Expect(buildAndLoadImage(ctx)).To(Succeed(), "build/load of "+managerImage)

	installAuthentik(ctx)
	akForward, ak = startAuthentikAccess(ctx)

	By("installing the operator with the in-repo Helm chart")
	Expect(ensureNamespace(ctx, operatorNS, map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted",
	})).To(Succeed())
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "authentik-api", Namespace: operatorNS},
		StringData: map[string]string{"token": bootstrapToken},
	}
	if err := k8s.Create(ctx, tokenSecret); err != nil {
		Expect(isAlreadyExists(err)).To(BeTrue(), err) // left by an earlier run
	}
	Expect(installOperator(ctx, authentikURL)).To(Succeed(), "helm install of the operator chart")

	Expect(ensureNamespace(ctx, appsNS, nil)).To(Succeed())
	resetTestResources(ctx)
	// Metrics scraping: a ServiceAccount allowed to read /metrics.
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "e2e-metrics", Namespace: operatorNS}}
	if err := k8s.Create(ctx, sa); err != nil {
		Expect(isAlreadyExists(err)).To(BeTrue(), err)
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-metrics-reader"},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole",
			Name: operatorRelease + "-metrics-reader"},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "e2e-metrics", Namespace: operatorNS}},
	}
	// roleRef is immutable: replace a binding left by an earlier run.
	Expect(client.IgnoreNotFound(k8s.Delete(ctx, crb.DeepCopy()))).To(Succeed())
	Expect(k8s.Create(ctx, crb)).To(Succeed())
})

var _ = AfterSuite(func() {
	akForward.Stop()
})

// dumpOnFailure prints what is needed to debug a failed spec from CI logs.
var _ = AfterEach(func(ctx SpecContext) {
	if !CurrentSpecReport().Failed() {
		return
	}
	GinkgoWriter.Printf("--- operator logs ---\n%s\n", controllerLogs(ctx))
	for _, kind := range []string{"authentikoauthproviders", "authentikapplications"} {
		out, _ := kubectl(ctx, "get", kind, "-A", "-o", "yaml")
		GinkgoWriter.Printf("--- %s ---\n%s\n", kind, out)
	}
})
