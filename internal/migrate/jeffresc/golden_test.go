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

package jeffresc

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the golden files: go test -run Golden -update.
var update = flag.Bool("update", false, "rewrite golden files")

type goldenCase struct {
	name string
	opts Options
	// wantErr, when set, is a substring the error must contain; the full
	// error text is the golden.
	wantErr string
}

func goldenCases() []goldenCase {
	dry := Options{DryRunAdoption: true}
	return []goldenCase{
		{name: "v1-gitlab", opts: dry},
		{name: "v1-gitlab-live", opts: Options{DryRunAdoption: false, Namespace: "sso", AuthentikRef: "main"}},
		{name: "four-shared-template", opts: dry},
		{name: "two-templates", opts: dry},
		{name: "v2-oauth2", opts: dry},
		{name: "public-client", opts: dry},
		{name: "secret-suffix", opts: Options{DryRunAdoption: true, SecretSuffix: "-v2", ProviderSuffix: "-provider"}},
		{name: "template-name-flag", opts: Options{DryRunAdoption: true, TemplateName: "oidc-env"}},
		{name: "explicit-empty", opts: dry},
		{name: "err-empty-scopes-no-mappings", opts: dry, wantErr: "propertyMappings"},
		{name: "err-name-tab", opts: dry, wantErr: "does not render"},
		{name: "claims-false", opts: dry},
		{name: "err-v1-trailing-comment", opts: dry, wantErr: "differently"},
		{name: "err-v1-block-scalar", opts: dry, wantErr: "differently"},
		{name: "err-launch-url", opts: dry, wantErr: "keep the live launch URL"},
		{name: "err-unknown-variable", opts: dry, wantErr: ".Namespace"},
		{name: "err-saml", opts: dry, wantErr: "unsupported: SAML"},
		{name: "err-unknown-field", opts: dry, wantErr: "unknown field"},
		{name: "err-template-construct", opts: dry, wantErr: "range"},
	}
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join("testdata", tc.name)
			in, err := os.ReadFile(filepath.Join(dir, "input.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := Convert([]Input{{Source: "input.yaml", Data: in}}, tc.opts)
			got := map[string]string{}
			switch {
			case tc.wantErr != "":
				if err == nil {
					t.Fatalf("want error containing %q, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				got["error.txt"] = err.Error() + "\n"
			case err != nil:
				t.Fatal(err)
			default:
				for _, f := range res.Files {
					got[f.Name] = string(f.Data)
				}
				got["report.txt"] = res.Report
			}
			compareGolden(t, filepath.Join(dir, "golden"), got)
		})
	}
}

func compareGolden(t *testing.T, dir string, got map[string]string) {
	t.Helper()
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, data := range got {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no golden files (run with -update): %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name()] = true
		want, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		g, ok := got[e.Name()]
		if !ok {
			t.Errorf("golden file %s was not produced", e.Name())
			continue
		}
		if g != string(want) {
			t.Errorf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", e.Name(), g, want)
		}
	}
	for name := range got {
		if !seen[name] {
			t.Errorf("produced %s with no golden file", name)
		}
	}
}

func TestGoldenDeterministic(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "four-shared-template", "input.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Convert([]Input{{Source: "a", Data: in}}, Options{DryRunAdoption: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		next, err := Convert([]Input{{Source: "a", Data: in}}, Options{DryRunAdoption: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Files) != len(first.Files) || next.Report != first.Report {
			t.Fatal("output is not deterministic")
		}
		for i := range next.Files {
			if next.Files[i].Name != first.Files[i].Name || !bytes.Equal(next.Files[i].Data, first.Files[i].Data) {
				t.Fatalf("file %d differs between runs", i)
			}
		}
	}
}
