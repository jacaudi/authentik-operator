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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSource = "in.yaml"

const minimalApp = `apiVersion: goauthentik.io/v1alpha1
kind: AuthentikApplication
metadata: {name: %s, namespace: %s}
spec:
  name: X
  provider:
    authorizationFlow: a
    invalidationFlow: b
    redirectUris: ["https://x.example.com/cb"]
  secret:
    template: |
      id: {{ .ClientID }}
`

func app(name, ns string) string {
	return strings.Replace(strings.Replace(minimalApp, "%s", name, 1), "%s", ns, 1)
}

func TestConvertErrors(t *testing.T) {
	long := strings.Repeat("a", 51)
	cases := []struct {
		name, in, want string
		opts           Options
	}{
		{"slug too long", strings.Replace(app("x", "ns"), "name: X", "name: X\n  slug: "+long, 1), "slug", Options{}},
		{"no namespace", strings.Replace(app("x", "ns"), ", namespace: ns", "", 1), "no namespace", Options{}},
		{"same output name twice", app("x", "ns1") + "---\n" + app("x", "ns2"), "same file", Options{}},
		{"nothing to convert", "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\n",
			"no AuthentikApplication", Options{}},
		{"bad template name", app("x", "ns"), "template name", Options{TemplateName: "Not_Valid"}},
		{"unquoted .Name is not safe here",
			strings.Replace(app("x", "ns"), "{{ .ClientID }}", "{{ .Name }}", 1), "Name", Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Convert([]Input{{Source: testSource, Data: []byte(tc.in)}}, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestConvertTemplateAccepts(t *testing.T) {
	for _, body := range []string{
		"a: {{ .ClientID }}\n",
		"a: {{ printf \"%q\" .Name }}\nb: {{ if eq .Slug \"x\" }}1{{ else }}2{{ end }}\n",
	} {
		got, err := convertTemplate(body, false, "Example", "example")
		if err != nil || got != body {
			t.Errorf("convertTemplate(%q) = %q, %v", body, got, err)
		}
	}
}

func TestConvertTemplateRejects(t *testing.T) {
	for body, want := range map[string]string{
		"a: {{ .Nope }}\n":               ".Nope",
		"a: {{ .ClientID \n":             "does not parse",
		"a: {{ .ClientSecret | upper }}": "", // upper is not a function in either operator.
	} {
		if _, err := convertTemplate(body, false, "Example", "example"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("convertTemplate(%q): want error containing %q, got %v", body, want, err)
		}
	}
}

func TestTemplatesShareByTupleAndUnionNamespaces(t *testing.T) {
	in := app("a", "ns1") + "---\n" + app("b", "ns2") + "---\n" + app("c", "ns1")
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var templates []File
	for _, f := range res.Files {
		if strings.HasPrefix(f.Name, "authentikcredentialstemplate-") {
			templates = append(templates, f)
		}
	}
	if len(templates) != 1 {
		t.Fatalf("want 1 template, got %d", len(templates))
	}
	if !strings.Contains(string(templates[0].Data), "- ns1\n      - ns2\n") {
		t.Fatalf("namespaces not unioned and sorted:\n%s", templates[0].Data)
	}
}

func TestNamespaceFlagRetargetsSelector(t *testing.T) {
	res, err := Convert([]Input{{Source: testSource, Data: []byte(app("a", "old"))}}, Options{Namespace: "new"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if strings.Contains(string(f.Data), "old") && !strings.Contains(f.Name, "-a.") {
			t.Errorf("%s still mentions the old namespace:\n%s", f.Name, f.Data)
		}
	}
	if !strings.Contains(res.Report, "moves from namespace old to new") {
		t.Errorf("report does not mention the namespace move:\n%s", res.Report)
	}
}

func TestListAndSkippedKinds(t *testing.T) {
	in := "apiVersion: v1\nkind: List\nitems:\n" +
		indent(app("a", "ns")) + "- apiVersion: v1\n  kind: ConfigMap\n  metadata: {name: cm, namespace: ns}\n"
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 || !strings.Contains(res.Report, "ConfigMap ns/cm") {
		t.Fatalf("files=%d report:\n%s", len(res.Files), res.Report)
	}
}

func indent(doc string) string {
	lines := strings.Split(strings.TrimRight(doc, "\n"), "\n")
	lines[0] = "- " + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestReadInputsDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.yaml": app("b", "ns"), "a.yml": app("a", "ns"), "notes.txt": "ignored"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in, err := ReadInputs([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 2 || filepath.Base(in[0].Source) != "a.yml" {
		t.Fatalf("unexpected inputs: %v", in)
	}
}

func TestV2YAMLCommentIsAccepted(t *testing.T) {
	in := strings.Replace(strings.Replace(app("x", "ns"), "{{ .ClientID }}", "{{ .ClientID }} # note", 1),
		"AuthentikApplication", "AuthentikOAuth2Application", 1)
	if _, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyLabelsEqualAbsent(t *testing.T) {
	withEmpty := strings.Replace(app("b", "ns"), "  secret:\n", "  secret:\n    labels: {}\n    annotations: {}\n", 1)
	res, err := Convert([]Input{{Source: testSource, Data: []byte(app("a", "ns") + "---\n" + withEmpty)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range res.Files {
		if strings.HasPrefix(f.Name, "authentikcredentialstemplate-") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one shared template, got %d", n)
	}
}
