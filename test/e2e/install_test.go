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
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/jacaudi/authentik-operator/test/aktest"
	"github.com/jacaudi/authentik-operator/test/utils"
)

const setString = "--set-string"

//go:embed testdata/postgres.yaml
var postgresManifest string

//go:embed testdata/nginx-tls.yaml
var nginxTLSManifest string

// buildAndLoadImage builds the operator image and loads it into the kind
// cluster. docker uses `kind load docker-image`; podman has no docker daemon
// for kind to read from, so it goes through an image archive.
func buildAndLoadImage(ctx context.Context) error {
	if skipBuild {
		GinkgoWriter.Printf("E2E_SKIP_BUILD=true: assuming %s is already loaded\n", managerImage)
		return nil
	}
	if _, err := utils.Run(exec.CommandContext(ctx, "make", "docker-build",
		"IMG="+managerImage, "CONTAINER_TOOL="+containerTool)); err != nil {
		return err
	}
	if containerTool != "podman" {
		return utils.LoadImageToKindClusterWithName(ctx, managerImage)
	}
	dir, err := os.MkdirTemp("", "ao-e2e-image-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	archive := filepath.Join(dir, "manager.tar")
	if _, err := utils.Run(exec.CommandContext(ctx, "podman", "save", "-o", archive, managerImage)); err != nil {
		return err
	}
	_, err = utils.Run(exec.CommandContext(ctx, "kind", "load", "image-archive", archive, "--name", clusterName))
	return err
}

// installAuthentik installs PostgreSQL and the official Authentik chart at
// chartVersion, bootstrapped non-interactively (AUTHENTIK_BOOTSTRAP_PASSWORD
// and AUTHENTIK_BOOTSTRAP_TOKEN, which the chart passes through from
// authentik.bootstrap_*), and returns once the API accepts the bootstrap
// token. Safe to re-run against an existing cluster.
func installAuthentik(ctx context.Context) {
	By("creating the authentik namespace and PostgreSQL")
	gomega.Expect(ensureNamespace(ctx, authentikNS, nil)).To(gomega.Succeed())
	gomega.Expect(kubectlApply(ctx, fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: authentik-postgresql
  namespace: %s
stringData:
  password: %s
`, authentikNS, postgresPassword))).To(gomega.Succeed())
	gomega.Expect(kubectlApply(ctx, postgresManifest)).To(gomega.Succeed())
	gomega.Expect(waitDeployment(ctx, authentikNS, "authentik-postgresql", 5*time.Minute)).To(gomega.Succeed())

	By("installing the Authentik chart " + chartVersion)
	dir, err := os.MkdirTemp("", "ao-e2e-authentik-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = os.RemoveAll(dir) }()
	values := filepath.Join(dir, "values.yaml")
	gomega.Expect(os.WriteFile(values, []byte(fmt.Sprintf(`authentik:
  secret_key: %s
  bootstrap_password: %s
  bootstrap_token: %s
  postgresql:
    host: authentik-postgresql
    password: %s
`, secretKey, bootstrapPassword, bootstrapToken, postgresPassword)), 0o600)).To(gomega.Succeed())

	_, err = helm(ctx, "repo", "add", "authentik", "https://charts.goauthentik.io", "--force-update")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = helm(ctx, "repo", "update", "authentik")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = helm(ctx, "upgrade", "--install", authentikRelease, "authentik/authentik",
		"--version", chartVersion, "-n", authentikNS, "-f", values, "--timeout", "5m")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	By("waiting for the Authentik server and worker (first start migrates the database)")
	for _, d := range []string{"authentik-server", "authentik-worker"} {
		gomega.Expect(waitDeployment(ctx, authentikNS, d, 15*time.Minute)).To(gomega.Succeed())
	}
}

// startAuthentikAccess forwards the Authentik Service, registers the dialer
// mapping, and waits until the bootstrap token authenticates: the worker
// creates it asynchronously after the server is Ready, so pod readiness
// alone is not enough.
func startAuthentikAccess(ctx context.Context) (*portForward, *aktest.API) {
	By("port-forwarding Authentik")
	pf, err := startPortForward(ctx, authentikNS, "svc/authentik-server", 80)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	dialMu.Lock()
	dialMap[authentikHost+":80"] = fmt.Sprintf("127.0.0.1:%d", pf.Port)
	dialMu.Unlock()

	api := aktest.NewAPI(authentikURL, bootstrapToken, clusterTransport())
	By("waiting for the bootstrap token to authenticate")
	gomega.Eventually(func(g gomega.Gomega) {
		who, err := api.Whoami(ctx)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(who).To(gomega.Equal(bootstrapUser))
	}).WithTimeout(10 * time.Minute).WithPolling(5 * time.Second).Should(gomega.Succeed())

	ver, err := api.Version(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	GinkgoWriter.Printf("Authentik reports version %s (chart %s)\n", ver, chartVersion)
	gomega.Expect(ver).To(gomega.Equal(chartVersion),
		"the running Authentik is not the chart version under test")
	return pf, api
}

// installOperator installs (or upgrades) the operator from the in-repo chart
// with a default connection to url (also the external URL, so rendered
// Secrets carry the address the test dials) and adoption allowed in the test
// namespaces (adoptionSelector). extra are further helm args.
func installOperator(ctx context.Context, url string, extra ...string) error {
	repo, tag := imageParts(managerImage)
	args := []string{"upgrade", "--install", operatorRelease, "./dist/chart",
		"-n", operatorNS, "--wait", "--timeout", "3m",
		setString, "image.repository=" + repo,
		setString, "image.tag=" + tag,
		setString, "image.pullPolicy=IfNotPresent",
		setString, "authentik.url=" + url,
		setString, "authentik.externalUrl=" + url,
		setString, "authentik.tokenSecret.name=authentik-api",
		// --set splits on commas: the selector's own must be escaped.
		setString, "adoption.defaultConnectionNamespaceSelector=" + strings.ReplaceAll(adoptionSelector, ",", `\,`),
	}
	args = append(args, extra...)
	if _, err := helm(ctx, args...); err != nil {
		return err
	}
	return waitOperatorLeader(ctx)
}

// waitOperatorLeader returns once the rollout has finished (old pod gone) and
// the one remaining pod holds the leader lease, so nothing a spec does next is
// handled by an operator still configured with the previous connection.
func waitOperatorLeader(ctx context.Context) error {
	dep := "deploy/" + operatorRelease + "-controller-manager"
	if _, err := kubectl(ctx, "-n", operatorNS, "rollout", "status", dep, "--timeout=3m"); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		pods, err := kubectl(ctx, "-n", operatorNS, "get", "pods", "-l", "control-plane=controller-manager",
			"-o", "name")
		if err != nil {
			return err
		}
		if names := utils.GetNonEmptyLines(pods); len(names) == 1 {
			logs, err := kubectl(ctx, "-n", operatorNS, "logs", names[0], "--tail=-1")
			if err == nil && strings.Contains(logs, "Successfully acquired lease") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("operator pod did not become the single leader")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
