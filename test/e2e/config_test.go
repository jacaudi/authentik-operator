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
	"os"
	"strings"
	"time"
)

// Fixed names. The Authentik install is throwaway and its credentials are
// test-only values for a cluster that exists for the length of one run.
const (
	authentikNS      = "authentik"
	authentikRelease = "authentik"
	// authentikHost is how the operator (in the cluster) and the test (through
	// a port-forward dialer) both address Authentik, so the issuer Authentik
	// derives from the request Host is identical on both sides.
	authentikHost = "authentik-server." + authentikNS + ".svc.cluster.local"
	authentikURL  = "http://" + authentikHost

	operatorNS      = "authentik-operator-system"
	operatorRelease = "authentik-operator"
	appsNS          = "e2e-apps"

	bootstrapUser     = "akadmin"
	bootstrapPassword = "e2e-bootstrap-password-not-a-secret"
	bootstrapToken    = "e2e-bootstrap-token-not-a-secret-0123456789"
	secretKey         = "e2e-secret-key-not-a-secret-0123456789abcdef"
	postgresPassword  = "e2e-postgres-password-not-a-secret"
)

// waitTimeout bounds every Eventually. It is passed explicitly because an
// Eventually given a context (needed to abort on interrupt) has NO timeout of
// its own: Gomega's default timeout applies only without a context.
const waitTimeout = 2 * time.Minute

// Run-time configuration, all from the environment.
var (
	// clusterName is the kind cluster the suite is allowed to touch; the
	// current kubectl context must be kind-<clusterName>.
	clusterName = envOr("KIND_CLUSTER", "authentik-operator-test-e2e")
	// chartVersion is the Authentik Helm chart version (equal to the app
	// version) under test: the matrix parameter.
	chartVersion = envOr("AUTHENTIK_CHART_VERSION", "2026.8.5")
	// managerImage is the operator image the chart is installed with.
	managerImage = envOr("E2E_IMAGE", "localhost/authentik-operator:e2e")
	// containerTool builds the image: docker (default) or podman.
	containerTool = envOr("CONTAINER_TOOL", "docker")
	// skipBuild assumes managerImage is already loaded into the cluster.
	skipBuild = os.Getenv("E2E_SKIP_BUILD") == "true"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// imageParts splits "repo/name:tag" into repository and tag for the chart.
func imageParts(ref string) (repo, tag string) {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") {
		return ref, "latest"
	}
	return ref[:i], ref[i+1:]
}
