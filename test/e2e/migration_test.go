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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/jacaudi/authentik-operator/test/utils"
)

// JeffResc/authentik-k8s-operator is GPL-3.0, so its chart is not vendored:
// it is fetched at test time from the immutable commit the 0.1.8 tag points
// at (the 0.1.x tags have no "v" prefix). The 0.1.8 chart ships no CRD in
// crds/; it lives in config/crd/bases.
const (
	jrCommit    = "744d218d53fbab726229bcaeb11aef05d8c6a674"
	jrRawBase   = "https://raw.githubusercontent.com/JeffResc/authentik-k8s-operator/" + jrCommit + "/"
	jrCRDFile   = "config/crd/bases/goauthentik.io_authentikapplications.yaml"
	jrChartPath = "charts/authentik-operator/"
	jrNS        = "authentik-operator-system" // JeffResc's chart default, as in the runbook
	jrRelease   = "authentik-operator"
	jrCRD       = "authentikapplications.goauthentik.io"
	// jrImageTag pins 0.1.8 by the digest of its multi-arch index, so the
	// image cannot change and arm64 kind nodes work as well as amd64.
	jrImageRepo = "ghcr.io/jeffresc/authentik-operator"
	jrImageTag  = "0.1.8@sha256:3bb21de76e254d99fa5af52cb0533e9adccb6ea42094d8ce18f9e0695764d64e"

	jrApp       = "jr-app"
	jrProvider  = "jr-app-provider" // what JeffResc names the provider in Authentik
	jrSecret    = "jr-app-oidc"
	jrRedirect  = "https://jr-app.invalid/callback"
	migratedNS  = "e2e-migrated" // --namespace of the converter: where the new objects live
	newSecret   = jrSecret + "-v2"
	jrTokenName = "jeffresc-token"
)

// jrFiles are the fetched files (chart-relative path, or "@crd" for the CRD) with
// the sha256 of their content at jrCommit; a mismatch
// fails the fetch, as defence in depth beside the commit pin.
var jrFiles = map[string]string{
	"Chart.yaml":                          "e70909c9035f6fbe966094dd2c4eab65a93e144965bb23603d1ee6f4f59f95c4",
	"values.yaml":                         "65d3468c9981e30f105a7c2d9bb5b0a2bc00adf3701549bdd9e304f8b5842b56",
	"templates/_helpers.tpl":              "b00072d4c303437b0bf82276c72a60c2eea197bf5387795cccad5d72f0f72132",
	"templates/clusterrole.yaml":          "22e6038027400c1e9c45255c2404eb0db8d980332a15a83bdc5ec959673d045a",
	"templates/clusterrolebinding.yaml":   "e3deb920cb92cc8dfb75db0318df7f63a429deef231b947cedf59af914d9e781",
	"templates/deployment.yaml":           "7d071449423f394962edfd5627da423f80ce9351bbdeed237ea6e6d525b55a13",
	"templates/leader-election-role.yaml": "60b8d1cb00ba532afccbd66474c4a9c9dbedd02392c5a00dfe5ab074b6a618ec",
	"templates/service.yaml":              "a7c209e32e1be876369871a28bfeff4edf08b3027747a274fe7299b641042f89",
	"templates/serviceaccount.yaml":       "96949eccac5c5b2f5cd339bad4aff9860bdd6f0f8c5d1bb70ad8dc01d56b10b3",
	"templates/servicemonitor.yaml":       "eaefd226f9251f9a2ff6487c53ebad38d5c0f0569d603d01ca2f42ba15f89d08",
	jrCRDFile:                             "449b9841828b33f27472864d4af76c1f597046d4a01931e6a39b8a256bacd332",
}

// jeffrescTemplate is a typical JeffResc 0.1.x secret template.
const jeffrescTemplate = `CLIENT_ID: {{ .ClientID }}
CLIENT_SECRET: {{ .ClientSecret }}
ISSUER_URL: {{ .IssuerURL }}
AUTH_URL: {{ .AuthURL }}
TOKEN_URL: {{ .TokenURL }}
USERINFO_URL: {{ .UserInfoURL }}
`

func jeffrescApp() string {
	return fmt.Sprintf(`apiVersion: goauthentik.io/v1alpha1
kind: AuthentikApplication
metadata:
  name: %s
  namespace: %s
spec:
  name: JR App
  slug: %s
  provider:
    authorizationFlow: %s
    invalidationFlow: %s
    redirectUris:
      - %s
    clientType: confidential
  secret:
    name: %s
    template: |
%s`, jrApp, appsNS, jrApp, flowAuthorize, flowInvalidation, jrRedirect, jrSecret, indent(jeffrescTemplate, "      "))
}

func indent(s, p string) string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, p+l)
	}
	return strings.Join(out, "\n") + "\n"
}

// atLeast reports whether the Authentik under test is at least major.minor.
func atLeast(major, minor int) bool {
	p := strings.Split(chartVersion, ".")
	a, _ := strconv.Atoi(p[0])
	b, _ := strconv.Atoi(p[1])
	return a > major || (a == major && b >= minor)
}

func fetchJeffResc(ctx context.Context, dir string) error {
	hc := &http.Client{Timeout: 60 * time.Second}
	for rel, want := range jrFiles {
		path := jrChartPath + rel
		if rel == "@crd" {
			path = jrCRDFile
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, jrRawBase+path, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("%s at %s has sha256 %x, want %s", path, jrCommit, sum, want)
		}
		dest := filepath.Join(dir, "chart", rel)
		if rel == "@crd" {
			dest = filepath.Join(dir, "crd.yaml")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dest, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// bash runs a script from the runbook (or a step that stands for one) in dir.
func bash(ctx context.Context, dir, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	GinkgoWriter.Printf("--- bash ---\n%s\n%s\n", script, out)
	return string(out), err
}

// runbookBlock returns the marked, fenced script of docs/migration-from-jeffresc.md.
func runbookBlock(name string) string {
	root, err := utils.GetProjectDir()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	md, err := os.ReadFile(filepath.Join(root, "docs", "migration-from-jeffresc.md"))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	blk, err := aktest.DocBlock(string(md), name)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return blk
}

func podsIn(ctx context.Context, g Gomega, ns string) int {
	var l corev1.PodList
	g.Expect(k8s.List(ctx, &l, client.InNamespace(ns))).To(Succeed())
	return len(l.Items)
}

var _ = Describe("migration from JeffResc, following docs/migration-from-jeffresc.md", Ordered, func() {
	var (
		work                string
		pk                  int
		clientID, clientSec string
		oldData             map[string]string
		beforeProv          aktest.Obj
		beforeApp           aktest.Obj
	)
	newKey := func(name string) client.ObjectKey { return client.ObjectKey{Namespace: migratedNS, Name: name} }
	wantReady := func(ctx context.Context, status metav1.ConditionStatus, reason string) {
		EventuallyWithOffset(1, func(g Gomega) {
			var p authentikiov1alpha1.AuthentikOAuthProvider
			g.Expect(k8s.Get(ctx, newKey(jrApp), &p)).To(Succeed())
			c := condition(p.Status.Conditions, condReady)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(status), "%s: %s", c.Reason, c.Message)
			if reason != "" {
				g.Expect(c.Reason).To(Equal(reason), c.Message)
			}
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
	}
	wantAppReady := func(ctx context.Context) {
		EventuallyWithOffset(1, func(g Gomega) {
			var a authentikiov1alpha1.AuthentikApplication
			g.Expect(k8s.Get(ctx, newKey(jrApp), &a)).To(Succeed())
			c := condition(a.Status.Conditions, condReady)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Status).To(Equal(metav1.ConditionTrue), "%s: %s", c.Reason, c.Message)
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
	}
	provenance := func(ctx context.Context, g Gomega) (authentikiov1alpha1.Provenance, authentikiov1alpha1.Provenance) {
		var p authentikiov1alpha1.AuthentikOAuthProvider
		g.Expect(k8s.Get(ctx, newKey(jrApp), &p)).To(Succeed())
		var a authentikiov1alpha1.AuthentikApplication
		g.Expect(k8s.Get(ctx, newKey(jrApp), &a)).To(Succeed())
		return p.Status.Provenance, a.Status.Provenance
	}

	// cleanup removes what an earlier run left; also used afterwards.
	cleanup := func(ctx context.Context) {
		_, _ = kubectl(ctx, "-n", appsNS, "patch", jrCRD, jrApp, "--type", "merge",
			"-p", `{"metadata":{"finalizers":[]}}`)
		_, _ = kubectl(ctx, "-n", appsNS, "delete", jrCRD, jrApp, "--ignore-not-found", "--wait=false")
		_, _ = helm(ctx, "-n", jrNS, "uninstall", jrRelease)
		_, _ = kubectl(ctx, "delete", "crd", jrCRD, "--ignore-not-found")
		_, _ = kubectl(ctx, "-n", appsNS, "delete", "secret", jrSecret, "--ignore-not-found")
		_, _ = kubectl(ctx, "delete", "namespace", migratedNS, "--ignore-not-found", "--timeout=120s")
		_, _ = kubectl(ctx, "delete", "authentikcredentialstemplates.authentik.io", "-l",
			"app.kubernetes.io/managed-by=convert-jeffresc", "--ignore-not-found")
		_ = ak.DeleteApplicationIfExists(ctx, jrApp)
		_ = ak.DeleteOAuthProviderByNameIfExists(ctx, jrProvider)
	}

	BeforeAll(func(ctx SpecContext) {
		var err error
		work, err = os.MkdirTemp("", "ao-e2e-migrate-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(work) })
		cleanup(ctx)
		// Templates from an earlier run, whatever their name.
		out, _ := kubectl(ctx, "get", "authentikcredentialstemplates.authentik.io", "-o", "name")
		for _, n := range utils.GetNonEmptyLines(out) {
			if strings.Contains(n, "jeffresc-") {
				_, _ = kubectl(ctx, "delete", n, "--ignore-not-found")
			}
		}
	})

	AfterAll(func(ctx SpecContext) {
		cleanup(ctx)
		out, _ := kubectl(ctx, "get", "authentikcredentialstemplates.authentik.io", "-o", "name")
		for _, n := range utils.GetNonEmptyLines(out) {
			if strings.Contains(n, "jeffresc-") {
				_, _ = kubectl(ctx, "delete", n, "--ignore-not-found")
			}
		}
	})

	It("starts from JeffResc 0.1.8 managing an application", func(ctx SpecContext) {
		By("installing JeffResc's chart and CRD, image pinned by digest, into " + jrNS)
		Expect(fetchJeffResc(ctx, work)).To(Succeed())
		_, err := kubectl(ctx, "apply", "-f", filepath.Join(work, "crd.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ensureNamespace(ctx, jrNS, nil)).To(Succeed())
		Expect(kubectlApply(ctx, fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
stringData:
  token: %s
`, jrTokenName, jrNS, bootstrapToken))).To(Succeed())
		_, err = helm(ctx, "upgrade", "--install", jrRelease, filepath.Join(work, "chart"), "-n", jrNS,
			"--wait", "--timeout", "5m",
			setString, "image.repository="+jrImageRepo, setString, "image.tag="+jrImageTag,
			setString, "authentik.url="+authentikURL, setString, "authentik.existingSecret.name="+jrTokenName)
		Expect(err).NotTo(HaveOccurred())

		By("creating a JeffResc AuthentikApplication and waiting for its Secret")
		Expect(kubectlApply(ctx, jeffrescApp())).To(Succeed())
		oldData = eventuallySecret(ctx, jrSecret)
		Expect(oldData).To(HaveKey("CLIENT_ID"))

		By("recording what Authentik holds")
		prov, err := ak.OAuthProviderByName(ctx, jrProvider)
		Expect(err).NotTo(HaveOccurred())
		Expect(prov).NotTo(BeNil(), "JeffResc did not create %s", jrProvider)
		Expect(fmt.Sscan(aktest.Str(prov, "pk"), &pk)).To(Equal(1))
		clientID, clientSec = aktest.Str(prov, "client_id"), aktest.Str(prov, "client_secret")
		Expect(oldData["CLIENT_ID"]).To(Equal(clientID))
		Expect(oldData["CLIENT_SECRET"]).To(Equal(clientSec))
		beforeProv, err = ak.OAuthProvider(ctx, pk)
		Expect(err).NotTo(HaveOccurred())
		beforeApp, err = ak.ApplicationBySlug(ctx, jrApp)
		Expect(err).NotTo(HaveOccurred())
		Expect(beforeApp).NotTo(BeNil())
	})

	// JeffResc #279: it creates providers with no grant types.
	It("logs in with the old Secret on 2026.2 and fails to on 2026.8+ (JeffResc #279)", func(ctx SpecContext) {
		_, err := login(ctx, templateCreds(oldData), jrRedirect)
		if atLeast(2026, 8) {
			// The cause: JeffResc sends no grant types, so the provider has none.
			grants, _ := beforeProv["grant_types"].([]any)
			Expect(grants).To(BeEmpty(), "the JeffResc-created provider already has grant types")
			Expect(err).To(HaveOccurred(), "JeffResc's provider is expected to reject logins on 2026.8+")
			// What 2026.8 actually answers (recorded: invalid_request, "The request is otherwise malformed").
			Expect(err.Error()).To(ContainSubstring("invalid_request"))
			GinkgoWriter.Printf("pre-migration login failed as expected: %v\n", err)
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("runbook 1: installs this operator alongside, in another namespace", func(ctx SpecContext) {
		Expect(operatorNS).NotTo(Equal(jrNS))
		Expect(installOperator(ctx, authentikURL)).To(Succeed())
		Expect(deploymentReady(ctx, operatorNS, operatorRelease+"-controller-manager")).To(BeTrue())
		Expect(deploymentReady(ctx, jrNS, jrRelease)).To(BeTrue())
	})

	It("runbook 2: backs up, converts with --secret-suffix -v2 and applies the template", func(ctx SpecContext) {
		_, err := bash(ctx, work, runbookBlock("backup"))
		Expect(err).NotTo(HaveOccurred())
		backups, err := filepath.Glob(filepath.Join(work, "jeffresc-backup-*.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(backups).NotTo(BeEmpty())

		Expect(ensureNamespace(ctx, migratedNS, nil)).To(Succeed())
		out := filepath.Join(work, "out")
		args := append([]string{"run", "./hack/convert-jeffresc", "--out", out, "--secret-suffix", "-v2",
			"--namespace", migratedNS}, backups...)
		_, err = utils.Run(exec.CommandContext(ctx, "go", args...))
		Expect(err).NotTo(HaveOccurred())

		tmpls, err := filepath.Glob(filepath.Join(out, "authentikcredentialstemplate-*.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(tmpls).To(HaveLen(1))
		for _, f := range tmpls {
			_, err = kubectl(ctx, "apply", "-f", f)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("runbook 3: adopts as a dry run first and changes nothing in Authentik", func(ctx SpecContext) {
		out := filepath.Join(work, "out")
		for _, pat := range []string{"authentikoauthprovider-*.yaml", "authentikapplication-*.yaml"} {
			fs, err := filepath.Glob(filepath.Join(out, pat))
			Expect(err).NotTo(HaveOccurred())
			Expect(fs).To(HaveLen(1))
			_, err = kubectl(ctx, "apply", "-f", fs[0])
			Expect(err).NotTo(HaveOccurred())
		}
		wantReady(ctx, metav1.ConditionFalse, "AdoptionDryRun")
		Eventually(func(g Gomega) {
			var a authentikiov1alpha1.AuthentikApplication
			g.Expect(k8s.Get(ctx, newKey(jrApp), &a)).To(Succeed())
			c := condition(a.Status.Conditions, condReady)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Reason).To(BeElementOf("AdoptionDryRun", "ProviderNotReady"))
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())

		prov, err := ak.OAuthProvider(ctx, pk)
		Expect(err).NotTo(HaveOccurred())
		Expect(prov).To(Equal(beforeProv), "the provider changed during the dry run")
		app, err := ak.ApplicationBySlug(ctx, jrApp)
		Expect(err).NotTo(HaveOccurred())
		Expect(app).To(Equal(beforeApp), "the application changed during the dry run")
		p, a := provenance(ctx, Default)
		Expect(p).To(BeEmpty())
		Expect(a).To(BeEmpty())
	})

	It("runbook 3: takes the provider, then the application, out of dry run", func(ctx SpecContext) {
		patch := `{"spec":{"adoption":{"dryRun":false}}}`
		_, err := kubectl(ctx, "-n", migratedNS, "patch", "authentikoauthproviders.authentik.io", jrApp,
			"--type", "merge", "-p", patch)
		Expect(err).NotTo(HaveOccurred())
		wantReady(ctx, metav1.ConditionTrue, "Synced")
		_, err = kubectl(ctx, "-n", migratedNS, "patch", "authentikapplications.authentik.io", jrApp,
			"--type", "merge", "-p", patch)
		Expect(err).NotTo(HaveOccurred())
		wantAppReady(ctx)
		p, a := provenance(ctx, Default)
		Expect(p).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(a).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		if atLeast(2026, 8) {
			// Adoption wrote the spec's grant types: the fix for #279.
			after, err := ak.OAuthProvider(ctx, pk)
			Expect(err).NotTo(HaveOccurred())
			grants, _ := after["grant_types"].([]any)
			Expect(grants).NotTo(BeEmpty(), "adoption did not give the provider grant types")
		}
	})

	It("runbook 4: the new Secret is byte-identical to the old one", func(ctx SpecContext) {
		Eventually(func(g Gomega) {
			var s corev1.Secret
			g.Expect(k8s.Get(ctx, newKey(newSecret), &s)).To(Succeed())
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
		// Connection URL and external URL match JeffResc's, so the runbook's
		// diff must be exactly empty, with no host difference to excuse.
		script, err := aktest.ReplaceAll(runbookBlock("compare"), "gitlab-oidc-credentials-v2", newSecret,
			"gitlab-oidc-credentials", jrSecret, "OLD_NS", appsNS, "NEW_NS", migratedNS)
		Expect(err).NotTo(HaveOccurred())
		out, err := bash(ctx, work, script)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("identical"))

		// Both operators are running and own the same Authentik objects: forcing
		// a reconcile in each must not start a write fight.
		By("no write fight while both operators run")
		var prov authentikiov1alpha1.AuthentikOAuthProvider
		Expect(k8s.Get(ctx, newKey(jrApp), &prov)).To(Succeed())
		var app authentikiov1alpha1.AuthentikApplication
		Expect(k8s.Get(ctx, newKey(jrApp), &app)).To(Succeed())
		pokeAndWait(ctx, &prov, &app)
		_, err = kubectl(ctx, "-n", appsNS, "annotate", jrCRD, jrApp, "--overwrite",
			pokeAnnotation+"="+strconv.FormatInt(time.Now().UnixNano(), 10))
		Expect(err).NotTo(HaveOccurred())
		pokeAndWait(ctx, &prov, &app)
		writes := oauthWrites(ctx, Default)
		updates := operatorUpdates(ctx, Default)
		pokeAndWait(ctx, &prov, &app)
		Consistently(func(g Gomega) {
			g.Expect(oauthWrites(ctx, g)).To(Equal(writes), "the operators are writing to each other's objects")
		}).WithContext(ctx).WithTimeout(20 * time.Second).WithPolling(4 * time.Second).Should(Succeed())
		Expect(operatorUpdates(ctx, Default)).To(Equal(updates), "this operator sent a PATCH during the overlap")
	})

	It("runbook 5: retires the old CR with the runbook's own gated script", func(ctx SpecContext) {
		// Gate 1 (suspend GitOps reconciliation of the old operator) is by
		// hand and does not apply in kind: nothing here would bring it back.
		script, err := aktest.SetVars(runbookBlock("retire-one"), map[string]string{
			"OLD_OP_NS": jrNS, "OLD_NS": appsNS, "OLD_DEPLOY": jrRelease,
			"OLD_WEBHOOK": "", "R": jrCRD, "NAME": jrApp, "NEW_NS": migratedNS,
		})
		Expect(err).NotTo(HaveOccurred())
		out, err := bash(ctx, work, script)
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("GATES-OK"))

		By("the Authentik objects and credentials survive the old CR's deletion")
		prov, err := ak.OAuthProvider(ctx, pk)
		Expect(err).NotTo(HaveOccurred())
		Expect(prov).NotTo(BeNil())
		Expect(aktest.Str(prov, "client_id")).To(Equal(clientID))
		Expect(aktest.Str(prov, "client_secret")).To(Equal(clientSec))
		app, err := ak.ApplicationBySlug(ctx, jrApp)
		Expect(err).NotTo(HaveOccurred())
		Expect(app).NotTo(BeNil())
		Expect(aktest.Str(app, "pk")).To(Equal(aktest.Str(beforeApp, "pk")), "the application was recreated")
		_, err = kubectl(ctx, "-n", appsNS, "get", jrCRD, jrApp)
		Expect(err).To(HaveOccurred(), "the old CR is still there")
		Eventually(func(g Gomega) {
			var s corev1.Secret
			g.Expect(apierrors.IsNotFound(k8s.Get(ctx, key(jrSecret), &s))).To(BeTrue())
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
		wantReady(ctx, metav1.ConditionTrue, "Synced")
	})

	It("runbook 6: uninstalls the old chart and its CRD", func(ctx SpecContext) {
		script, err := aktest.ReplaceAll(runbookBlock("uninstall"), "<old operator namespace>", jrNS,
			"<old release>", jrRelease)
		Expect(err).NotTo(HaveOccurred())
		_, err = bash(ctx, work, script)
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectl(ctx, "get", "crd", jrCRD)
		Expect(err).To(HaveOccurred(), "the old CRD is still installed")
		Eventually(func(g Gomega) {
			g.Expect(podsIn(ctx, g, jrNS)).To(BeZero())
		}).WithContext(ctx).WithTimeout(waitTimeout).Should(Succeed())
	})

	It("logs in with the new Secret, whose credentials are the pre-migration ones", func(ctx SpecContext) {
		data := secretDataIn(ctx, Default, migratedNS, newSecret)
		c := templateCreds(data)
		Expect(c.ClientID).To(Equal(clientID))
		Expect(c.ClientSecret).To(Equal(clientSec))
		Expect(data).To(Equal(oldData))
		res, err := login(ctx, c, jrRedirect)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.UserInfo["preferred_username"]).To(Equal(bootstrapUser))
	})

	It("runbook 7: leaves adopt-only mode and a reconcile then writes nothing", func(ctx SpecContext) {
		patch := `{"spec":{"adoption":{"enabled":false}}}`
		for _, kind := range []string{"authentikoauthproviders.authentik.io", "authentikapplications.authentik.io"} {
			_, err := kubectl(ctx, "-n", migratedNS, "patch", kind, jrApp, "--type", "merge", "-p", patch)
			Expect(err).NotTo(HaveOccurred())
		}
		wantReady(ctx, metav1.ConditionTrue, "Synced")
		wantAppReady(ctx)
		p, a := provenance(ctx, Default)
		Expect(p).To(Equal(authentikiov1alpha1.ProvenanceAdopted))
		Expect(a).To(Equal(authentikiov1alpha1.ProvenanceAdopted))

		writes := oauthWrites(ctx, Default)
		updates := operatorUpdates(ctx, Default)
		var prov authentikiov1alpha1.AuthentikOAuthProvider
		Expect(k8s.Get(ctx, newKey(jrApp), &prov)).To(Succeed())
		var app authentikiov1alpha1.AuthentikApplication
		Expect(k8s.Get(ctx, newKey(jrApp), &app)).To(Succeed())
		pokeAndWait(ctx, &prov, &app)
		Consistently(func(g Gomega) {
			g.Expect(oauthWrites(ctx, g)).To(Equal(writes), "a reconcile wrote to Authentik")
		}).WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		Expect(operatorUpdates(ctx, Default)).To(Equal(updates), "the operator sent a PATCH")
		wantReady(ctx, metav1.ConditionTrue, "Synced")
	})
})

func secretDataIn(ctx context.Context, g Gomega, ns, name string) map[string]string {
	var s corev1.Secret
	g.Expect(k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &s)).To(Succeed())
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}
