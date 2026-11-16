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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	flagOut  = "--out"
	fixtures = "../../internal/migrate/jeffresc/testdata"
)

func TestRunWritesFilesAndReport(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{flagOut, out, filepath.Join(fixtures, "four-shared-template", "input.yaml")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
		t.Fatalf("want 9 files (4 providers, 4 applications, 1 template), got %d", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(out, "authentikoauthprovider-gitlab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "dryRun: true") {
		t.Errorf("dry run should default on:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "AuthentikApplication authentik-operator-system/gitlab") ||
		!strings.Contains(stderr.String(), "adoption.defaultConnectionNamespaceSelector") {
		t.Errorf("report missing from stderr:\n%s", stderr.String())
	}
}

func TestRunDryRunFlagOff(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{flagOut, out, "--dry-run-adoption=false", "--namespace", "sso", "--authentik-ref", "main",
		filepath.Join(fixtures, "v1-gitlab", "input.yaml")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(out, "authentikapplication-gitlab.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dryRun: false", "namespace: sso", "authentikRef:"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestRunFailsWithoutWritingOnError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	code := run([]string{flagOut, out, filepath.Join(fixtures, "err-saml", "input.yaml")}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "unsupported: SAML") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output directory was created despite the error")
	}
}

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("want exit 2 without arguments, got %d", code)
	}
}

func TestRunRefusesNonEmptyOutWithoutForce(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(fixtures, "v1-gitlab", "input.yaml")
	var stdout, stderr bytes.Buffer
	code := run([]string{flagOut, out, in}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "--force") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if code = run([]string{flagOut, out, "--force", in}, &stdout, &stderr); code != 0 {
		t.Fatalf("with --force: exit %d: %s", code, stderr.String())
	}
}
