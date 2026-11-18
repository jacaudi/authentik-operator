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
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

const (
	testSource     = "in.yaml"
	policyModeAll  = "all"
	noNamespace    = "no namespace"
	namespaceSSO   = "sso"
	refMain        = "main"
	mediaUsersName = "media-users"
)

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
		{noNamespace, strings.Replace(app("x", "ns"), ", namespace: ns", "", 1), noNamespace, Options{}},
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

// convertApp converts one JeffResc application and returns its provider and
// application, decoded strictly, with the raw provider YAML.
func convertApp(t *testing.T, in string, opts Options) (
	prov *authentikiov1alpha1.AuthentikOAuthProvider, application *authentikiov1alpha1.AuthentikApplication, raw string,
) {
	t.Helper()
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	prov = &authentikiov1alpha1.AuthentikOAuthProvider{}
	application = &authentikiov1alpha1.AuthentikApplication{}
	for _, f := range res.Files {
		var err error
		switch {
		case strings.HasPrefix(f.Name, "authentikoauthprovider-"):
			raw = string(f.Data)
			err = sigsyaml.UnmarshalStrict(f.Data, prov)
		case strings.HasPrefix(f.Name, "authentikapplication-"):
			err = sigsyaml.UnmarshalStrict(f.Data, application)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return prov, application, raw
}

// withProvider adds provider fields to minimalApp.
func withProvider(extra string) string {
	return strings.Replace(app("x", "ns"), "    redirectUris: [\"https://x.example.com/cb\"]\n",
		"    redirectUris: [\"https://x.example.com/cb\"]\n"+extra, 1)
}

func TestEmitsAdoptPolicy(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		name := map[bool]string{true: "dryRun", false: "live"}[dryRun]
		t.Run(name, func(t *testing.T) {
			prov, application, raw := convertApp(t, app("x", "ns"), Options{DryRunAdoption: dryRun})
			if prov.Spec.ManagementPolicy != authentikiov1alpha1.ManagementPolicyAdopt ||
				application.Spec.ManagementPolicy != authentikiov1alpha1.ManagementPolicyAdopt {
				t.Errorf("managementPolicy: provider %q, application %q, want Adopt",
					prov.Spec.ManagementPolicy, application.Spec.ManagementPolicy)
			}
			if prov.Spec.Match == nil || prov.Spec.Match.Name != "x-provider" {
				t.Errorf("match = %+v, want name x-provider", prov.Spec.Match)
			}
			if prov.Spec.DeletionPolicy != authentikiov1alpha1.DeletionPolicyRetain ||
				application.Spec.DeletionPolicy != authentikiov1alpha1.DeletionPolicyRetain {
				t.Error("deletionPolicy is not Retain")
			}
			if prov.Spec.DryRun != dryRun || application.Spec.DryRun != dryRun {
				t.Errorf("dryRun: provider %v, application %v, want %v", prov.Spec.DryRun, application.Spec.DryRun, dryRun)
			}
			// dryRun is written out even when false, so it is easy to flip.
			if want := fmt.Sprintf("dryRun: %t", dryRun); !strings.Contains(raw, want) {
				t.Errorf("provider YAML lacks %q:\n%s", want, raw)
			}
		})
	}
}

// Adoption keeps the live value of every field the spec leaves unset, so a
// field JeffResc left unset (and defaulted itself) is not written out.
func TestUnsetFieldsStayUnset(t *testing.T) {
	prov, application, _ := convertApp(t, app("x", "ns"), Options{})
	p := prov.Spec
	if p.Scopes != nil || p.ClientType != "" || p.AccessCodeValidity != "" || p.AccessTokenValidity != "" ||
		p.RefreshTokenValidity != "" || p.SubMode != "" || p.IssuerMode != "" || p.SigningKey != "" ||
		p.IncludeClaimsInIDToken != nil {
		t.Errorf("provider spec sets fields JeffResc left unset: %+v", p)
	}
	if a := application.Spec; a.PolicyEngineMode != "" || a.OpenInNewTab != nil {
		t.Errorf("application spec sets fields JeffResc left unset: %+v", a)
	}
}

// grantTypes is always written (adoption keeps the live value of an unset
// field, and JeffResc v0.2.6 and older left providers with none, which
// Authentik 2026.5+ rejects every login for). JeffResc v0.2.7+ copies
// spec.provider.grantTypes, and treats an empty list as the default pair.
func TestGrantTypes(t *testing.T) {
	const (
		code, refresh, creds = "authorization_code", "refresh_token", "client_credentials"
		copied, defaulted    = "copied", "defaulted"
	)
	grants := func(g ...authentikiov1alpha1.OAuthGrantType) []authentikiov1alpha1.OAuthGrantType { return g }
	pair := grants(code, refresh)
	cases := []struct {
		name, extra string
		want        []authentikiov1alpha1.OAuthGrantType
		note        string
	}{
		{"absent: the pair", "", pair, defaulted},
		{"empty: the pair", "    grantTypes: []\n", pair, defaulted},
		{"copied", "    grantTypes: [" + code + ", " + refresh + ", " + creds + "]\n", grants(code, refresh, creds), copied},
		{"copied without duplicates, in order", "    grantTypes: [" + creds + ", " + code + ", " + creds + "]\n",
			grants(creds, code), copied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := app("x", "ns")
			if tc.extra != "" {
				in = withProvider(tc.extra)
			}
			prov, _, _ := convertApp(t, in, Options{})
			if !slices.Equal(prov.Spec.GrantTypes, tc.want) {
				t.Errorf("grantTypes = %v, want %v", prov.Spec.GrantTypes, tc.want)
			}
			res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if want := "provider.grantTypes " + tc.note; !strings.Contains(res.Report, want) {
				t.Errorf("report lacks %q:\n%s", want, res.Report)
			}
		})
	}
}

// groupBindings become spec.access: the operator owns the bindings JeffResc
// created, so nothing is refused and no notice is needed.
func TestGroupBindings(t *testing.T) {
	withBindings := func(list string) string {
		return strings.Replace(app("x", "ns"), "  name: X\n", "  name: X\n  groupBindings: "+list+"\n", 1)
	}
	t.Run("a group name cannot break out of the YAML", func(t *testing.T) {
		in := withBindings(`["a\nspec: injected"]`)
		res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range res.Files {
			if !strings.HasPrefix(f.Name, "authentikapplication-") {
				continue
			}
			var obj struct {
				Spec struct {
					Access authentikiov1alpha1.ApplicationAccess `json:"access"`
				} `json:"spec"`
			}
			if err := sigsyaml.Unmarshal(f.Data, &obj); err != nil {
				t.Fatal(err)
			}
			if want := []string{"a\nspec: injected"}; !slices.Equal(obj.Spec.Access.Groups, want) {
				t.Errorf("spec.access.groups = %q, want %q", obj.Spec.Access.Groups, want)
			}
			var raw struct {
				Spec map[string]any `json:"spec"`
			}
			if err := sigsyaml.Unmarshal(f.Data, &raw); err != nil {
				t.Fatal(err)
			}
			if _, bad := raw.Spec["injected"]; bad || strings.Contains(string(f.Data), "\nspec: injected") {
				t.Errorf("group name reached the YAML body as a key:\n%s", f.Data)
			}
		}
	})
	t.Run("an empty list or none triggers nothing", func(t *testing.T) {
		for _, in := range []string{withBindings(`[]`), app("x", "ns")} {
			res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(res.Report, "groupBindings") {
				t.Errorf("report mentions groupBindings:\n%s", res.Report)
			}
			for _, f := range res.Files {
				if strings.Contains(string(f.Data), "groupBindings") {
					t.Errorf("%s mentions groupBindings:\n%s", f.Name, f.Data)
				}
			}
		}
	})
}

// applicationOf converts in and returns the application's file and decoded
// object, plus the report.
func applicationOf(t *testing.T, in string, opts Options) (file string, obj authentikiov1alpha1.AuthentikApplication,
	report string) {
	t.Helper()
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if strings.HasPrefix(f.Name, "authentikapplication-") {
			if err := sigsyaml.UnmarshalStrict(f.Data, &obj); err != nil {
				t.Fatal(err)
			}
			return string(f.Data), obj, res.Report
		}
	}
	t.Fatal("no AuthentikApplication emitted")
	return "", obj, ""
}

func withSpecLines(in, lines string) string {
	return strings.Replace(in, "  name: X\n", "  name: X\n"+lines, 1)
}

func TestConvertGroupBindingsToAccess(t *testing.T) {
	in := withSpecLines(app("x", "ns"), "  groupBindings: [media-users, media-admins, media-users]\n")
	file, obj, report := applicationOf(t, in, Options{})
	want := authentikiov1alpha1.ApplicationAccess{
		Type: authentikiov1alpha1.AccessTypeGroups, Groups: []string{mediaUsersName, "media-admins"},
	}
	if obj.Spec.Access == nil || obj.Spec.Access.Type != want.Type || !slices.Equal(obj.Spec.Access.Groups, want.Groups) {
		t.Errorf("spec.access = %+v, want %+v", obj.Spec.Access, want)
	}
	if strings.Contains(file, "NOT managed") || strings.Contains(report, "NOT managed") {
		t.Errorf("the unmanaged notice is gone:\n%s\n%s", file, report)
	}
	if !strings.Contains(report, `groupBindings lists "media-users" more than once; kept once`) {
		t.Errorf("report lacks the duplicate note:\n%s", report)
	}
}

// spec.access.groups holds 1 to 64 names of 1 to 255 characters, so an entry the
// CRD would refuse stops the conversion instead of producing a file that fails
// admission.
func TestConvertRefusesGroupBindingsThatSpecAccessCannotHold(t *testing.T) {
	many := make([]string, 0, 65)
	for i := range 65 {
		many = append(many, fmt.Sprintf("g%d", i))
	}
	for name, list := range map[string]string{
		"an empty name":      `[a, ""]`,
		"a name too long":    `[` + strings.Repeat("a", 256) + `]`,
		"more than 64 names": "[" + strings.Join(many, ", ") + "]",
	} {
		t.Run(name, func(t *testing.T) {
			in := withSpecLines(app("x", "ns"), "  groupBindings: "+list+"\n")
			res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
			if err == nil || res != nil || !strings.Contains(err.Error(), "groupBindings") {
				t.Fatalf("want a groupBindings error and no result, got %v, %v", res, err)
			}
		})
	}
	t.Run("64 distinct names are fine, a repeat does not count", func(t *testing.T) {
		in := withSpecLines(app("x", "ns"), "  groupBindings: ["+strings.Join(many[:64], ", ")+", g0]\n")
		if _, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConvertWithoutGroupBindingsIsUnmanaged(t *testing.T) {
	const (
		prefix     = "# spec.access is Unmanaged: this application's live bindings are unknown offline; "
		dryHeader  = prefix + "the adoption dry run lists them.\n"
		liveHeader = prefix + "status.access.bindings lists them once applied.\n"
	)
	for _, tc := range []struct {
		name       string
		dry        bool
		want, deny string
	}{
		{"dry run on", true, dryHeader, liveHeader},
		{"dry run off", false, liveHeader, dryHeader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, obj, _ := applicationOf(t, app("x", "ns"), Options{DryRunAdoption: tc.dry})
			if obj.Spec.Access == nil || obj.Spec.Access.Type != authentikiov1alpha1.AccessTypeUnmanaged ||
				len(obj.Spec.Access.Groups) != 0 {
				t.Errorf("spec.access = %+v, want Unmanaged", obj.Spec.Access)
			}
			if !strings.Contains(file, tc.want) || strings.Contains(file, tc.deny) {
				t.Errorf("header wrong, want %q and not %q:\n%s", tc.want, tc.deny, file)
			}
		})
	}
	t.Run("a Groups application has no Unmanaged header", func(t *testing.T) {
		file, _, _ := applicationOf(t, withSpecLines(app("x", "ns"), "  groupBindings: [g]\n"), Options{})
		if strings.Contains(file, "Unmanaged") {
			t.Errorf("unexpected Unmanaged header:\n%s", file)
		}
	})
}

func TestConvertDropsPolicyEngineModeAllWithSeveralGroups(t *testing.T) {
	const note = "policyEngineMode all with several groupBindings left unset"
	t.Run("several groups", func(t *testing.T) {
		in := withSpecLines(app("x", "ns"), "  policyEngineMode: all\n  groupBindings: [a, b]\n")
		file, obj, report := applicationOf(t, in, Options{})
		if obj.Spec.PolicyEngineMode != "" || strings.Contains(file, "policyEngineMode") {
			t.Errorf("policyEngineMode must be left out:\n%s", file)
		}
		if obj.Spec.Access == nil || !slices.Equal(obj.Spec.Access.Groups, []string{"a", "b"}) {
			t.Errorf("spec.access = %+v, want Groups [a b]", obj.Spec.Access)
		}
		if !strings.Contains(report, note) || !strings.Contains(report, "AuthentikApplication ns/x") {
			t.Errorf("report lacks the note for the application:\n%s", report)
		}
	})
	t.Run("one group keeps all", func(t *testing.T) {
		in := withSpecLines(app("x", "ns"), "  policyEngineMode: all\n  groupBindings: [a]\n")
		_, obj, report := applicationOf(t, in, Options{})
		if obj.Spec.PolicyEngineMode != policyModeAll {
			t.Errorf("policyEngineMode = %q, want all", obj.Spec.PolicyEngineMode)
		}
		if strings.Contains(report, note) {
			t.Errorf("unexpected note:\n%s", report)
		}
	})
	t.Run("duplicates count once", func(t *testing.T) {
		in := withSpecLines(app("x", "ns"), "  policyEngineMode: all\n  groupBindings: [a, a]\n")
		_, obj, report := applicationOf(t, in, Options{})
		if obj.Spec.PolicyEngineMode != policyModeAll || strings.Contains(report, note) {
			t.Errorf("one distinct group keeps all, got %q:\n%s", obj.Spec.PolicyEngineMode, report)
		}
	})
	t.Run("any is copied", func(t *testing.T) {
		in := withSpecLines(app("x", "ns"), "  policyEngineMode: any\n  groupBindings: [a, b]\n")
		_, obj, _ := applicationOf(t, in, Options{})
		if obj.Spec.PolicyEngineMode != "any" {
			t.Errorf("policyEngineMode = %q, want any", obj.Spec.PolicyEngineMode)
		}
	})
}

// mediaUsers is a JeffResc group that exercises every field the converter reads.
const mediaUsers = `apiVersion: goauthentik.io/v1alpha1
kind: AuthentikGroup
metadata: {name: media-users, namespace: media}
spec:
  name: media-users
  isSuperuser: true
  deletionPolicy: Delete
  parents: [p]
  roles: [r]
  attributes:
    role: admin
    settings: {theme: dark}
    nested: {a: 1}
    goauthentik.io/x: y
`

// groupOf converts in, which must hold groups only, and returns the decoded
// group of the named file plus the result.
func groupOf(t *testing.T, in, file string, opts Options) (obj authentikiov1alpha1.AuthentikGroup, res *Result) {
	t.Helper()
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if f.Name == file {
			if err := sigsyaml.UnmarshalStrict(f.Data, &obj); err != nil {
				t.Fatal(err)
			}
			return obj, res
		}
	}
	t.Fatalf("no %s emitted; files: %v", file, res.Files)
	return obj, res
}

func TestConvertGroupAdoptsAndRetains(t *testing.T) {
	obj, res := groupOf(t, mediaUsers, "authentikgroup-media-users.yaml", Options{})
	if len(res.Files) != 1 {
		t.Fatalf("want exactly the group file, got %d files", len(res.Files))
	}
	s := obj.Spec
	if s.ManagementPolicy != authentikiov1alpha1.ManagementPolicyAdopt ||
		s.DeletionPolicy != authentikiov1alpha1.DeletionPolicyRetain {
		t.Errorf("managementPolicy %q, deletionPolicy %q, want Adopt and Retain (JeffResc's was Delete)",
			s.ManagementPolicy, s.DeletionPolicy)
	}
	if obj.Namespace != "media" || obj.Name != mediaUsersName || s.Name != mediaUsersName {
		t.Errorf("identity = %s/%s, spec.name %q", obj.Namespace, obj.Name, s.Name)
	}
	if s.IsSuperuser == nil || !*s.IsSuperuser {
		t.Errorf("isSuperuser = %v, want true", s.IsSuperuser)
	}
	if keys := slices.Sorted(maps.Keys(s.Attributes)); !slices.Equal(keys, []string{"role"}) ||
		string(s.Attributes["role"].Raw) != `"admin"` {
		t.Errorf("attributes = %v, want only role: admin", s.Attributes)
	}
	if s.Members != nil {
		t.Errorf("members = %v, want absent (JeffResc never managed membership)", *s.Members)
	}
	for _, want := range []string{
		`AuthentikGroup media/media-users`,
		`attribute "settings" is reserved for Authentik or this operator; left in Authentik, unmanaged`,
		`attribute "goauthentik.io/x" is reserved for Authentik or this operator; left in Authentik, unmanaged`,
		`attribute "nested" has an object or null value, which this operator cannot own per key; ` +
			`left in Authentik, unmanaged`,
		`parents ["p"] and roles ["r"] are not converted; they stay as they are in Authentik, unmanaged`,
		`members are not managed: JeffResc never managed membership, so spec.members is absent`,
	} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("report lacks %q:\n%s", want, res.Report)
		}
	}
	if strings.Contains(res.Report, `attribute "role"`) || strings.Contains(res.Report, `attribute "nested" is reserved`) {
		t.Errorf("report names a kept key or gives the wrong reason:\n%s", res.Report)
	}
}

func TestConvertGroupNotesOnlyTheListsItHas(t *testing.T) {
	in := strings.Replace(mediaUsers, "  roles: [r]\n", "", 1)
	_, res := groupOf(t, in, "authentikgroup-media-users.yaml", Options{})
	if !strings.Contains(res.Report, `parents ["p"] are not converted`) || strings.Contains(res.Report, "roles") {
		t.Errorf("want a note for parents only:\n%s", res.Report)
	}
	bare := "apiVersion: goauthentik.io/v1alpha1\nkind: AuthentikGroup\nmetadata: {name: g, namespace: ns}\nspec: {}\n"
	_, res = groupOf(t, bare, "authentikgroup-g.yaml", Options{})
	if strings.Contains(res.Report, "are not converted") || strings.Contains(res.Report, "attribute ") {
		t.Errorf("a bare group has nothing to report but members:\n%s", res.Report)
	}
}

func TestConvertGroupDefaultsNameToMetadata(t *testing.T) {
	in := "apiVersion: goauthentik.io/v1alpha1\nkind: AuthentikGroup\nmetadata: {name: media-admins, namespace: media}\n" +
		"spec: {isSuperuser: false}\n"
	obj, _ := groupOf(t, in, "authentikgroup-media-admins.yaml", Options{})
	if obj.Spec.Name != "media-admins" {
		t.Errorf("spec.name = %q, want the metadata name", obj.Spec.Name)
	}
	if obj.Spec.IsSuperuser == nil || *obj.Spec.IsSuperuser {
		t.Errorf("isSuperuser = %v, want an explicit false", obj.Spec.IsSuperuser)
	}
	if obj.Spec.Attributes != nil {
		t.Errorf("attributes = %v, want none", obj.Spec.Attributes)
	}
}

func TestConvertGroupHonoursOptions(t *testing.T) {
	obj, _ := groupOf(t, mediaUsers, "authentikgroup-media-users.yaml",
		Options{DryRunAdoption: true, Namespace: namespaceSSO, AuthentikRef: refMain})
	if obj.Namespace != namespaceSSO || !obj.Spec.DryRun ||
		obj.Spec.AuthentikRef == nil || obj.Spec.AuthentikRef.Name != refMain {
		t.Errorf("namespace %q, dryRun %v, authentikRef %+v, want sso, true, main",
			obj.Namespace, obj.Spec.DryRun, obj.Spec.AuthentikRef)
	}
}

func TestConvertRejectsUnknownGroupField(t *testing.T) {
	in := "apiVersion: goauthentik.io/v1alpha1\nkind: AuthentikGroup\nmetadata: {name: g, namespace: ns}\n" +
		"spec: {users: [a]}\n"
	_, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want an unknown field error, got %v", err)
	}
}

func TestConvertRefusesGroupThatTheCRDCannotHold(t *testing.T) {
	const head = "apiVersion: goauthentik.io/v1alpha1\nkind: AuthentikGroup\n"
	long := strings.Repeat("a", 256)
	for name, tc := range map[string]struct{ in, want string }{
		noNamespace:     {head + "metadata: {name: g}\nspec: {}\n", noNamespace},
		"no name":       {head + "metadata: {namespace: ns}\nspec: {}\n", "no metadata.name"},
		"name too long": {head + "metadata: {name: g, namespace: ns}\nspec: {name: " + long + "}\n", "255"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := Convert([]Input{{Source: testSource, Data: []byte(tc.in)}}, Options{})
			if err == nil || res != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q and no result, got %v, %v", tc.want, res, err)
			}
		})
	}
}

func TestConvertStillRefusesEmptyInput(t *testing.T) {
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\n"
	_, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "or AuthentikGroup") {
		t.Fatalf("want the empty-input error naming AuthentikGroup, got %v", err)
	}
}

func TestExplicitFieldsAreCopied(t *testing.T) {
	in := withProvider(`    scopes: [openid]
    clientType: public
    accessCodeValidity: minutes=2
    accessTokenValidity: minutes=6
    refreshTokenValidity: days=7
    subMode: user_id
    issuerMode: global
    includeClaimsInIdToken: false
    signingKey: my-key
`)
	in = strings.Replace(in, "  name: X\n", "  name: X\n  policyEngineMode: all\n  openInNewTab: false\n", 1)
	prov, application, _ := convertApp(t, in, Options{})
	p := prov.Spec
	if !slices.Equal(p.Scopes, []string{"openid"}) || p.ClientType != "public" || p.AccessCodeValidity != "minutes=2" ||
		p.AccessTokenValidity != "minutes=6" || p.RefreshTokenValidity != "days=7" || p.SubMode != "user_id" ||
		p.IssuerMode != "global" || p.SigningKey != "my-key" ||
		p.IncludeClaimsInIDToken == nil || *p.IncludeClaimsInIDToken {
		t.Errorf("provider spec does not carry the JeffResc values: %+v", p)
	}
	if a := application.Spec; a.PolicyEngineMode != policyModeAll || a.OpenInNewTab == nil || *a.OpenInNewTab {
		t.Errorf("application spec does not carry the JeffResc values: %+v", a)
	}
}

// An explicit scopes: [] makes JeffResc leave the live property mappings
// alone when propertyMappings is empty, which is what an unset scopes means
// to adoption. With propertyMappings the provider keeps only those, which
// needs scopes: [] to stay explicit.
func TestExplicitEmptyScopes(t *testing.T) {
	t.Run("no propertyMappings: unset keeps the live mappings", func(t *testing.T) {
		prov, _, raw := convertApp(t, withProvider("    scopes: []\n"), Options{})
		if prov.Spec.Scopes != nil || strings.Contains(raw, "scopes:") {
			t.Errorf("want scopes omitted, got:\n%s", raw)
		}
	})
	t.Run("propertyMappings: scopes stays an explicit empty list", func(t *testing.T) {
		prov, _, raw := convertApp(t, withProvider("    scopes: []\n    propertyMappings: [m1]\n"), Options{})
		if !strings.Contains(raw, "scopes: []") || len(prov.Spec.PropertyMappings) != 1 {
			t.Errorf("want scopes: [] and the mapping, got:\n%s", raw)
		}
	})
}

// On the default connection the admin must allow the migrated namespaces, so
// the report says which; an authentikRef needs no allowance.
func TestReportNamesAdoptionGateSelector(t *testing.T) {
	in := app("a", "ns2") + "---\n" + app("b", "ns1")
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kubernetes.io/metadata.name in (ns1, ns2)", "adoption.defaultConnectionNamespaceSelector",
	} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("report lacks %q:\n%s", want, res.Report)
		}
	}
	res, err = Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{AuthentikRef: refMain})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Report, "adoption.defaultConnectionNamespaceSelector") {
		t.Errorf("report names the selector although authentikRef is set:\n%s", res.Report)
	}
}

// Groups on the default connection are refused unless the admin allows their
// namespaces for identities, which is a different flag from the adoption one.
func TestConvertGroupReportNamesIdentitySelector(t *testing.T) {
	_, res := groupOf(t, mediaUsers, "authentikgroup-media-users.yaml", Options{})
	for _, want := range []string{
		"--default-connection-identity-namespace-selector", "identity.defaultConnectionNamespaceSelector",
		"IdentityNotAllowed", "kubernetes.io/metadata.name in (media)",
	} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("report lacks %q:\n%s", want, res.Report)
		}
	}
	_, res = groupOf(t, mediaUsers, "authentikgroup-media-users.yaml", Options{AuthentikRef: refMain})
	if strings.Contains(res.Report, "Identity gate") {
		t.Errorf("report names the identity selector although authentikRef is set:\n%s", res.Report)
	}
	appOnly, err := Convert([]Input{{Source: testSource, Data: []byte(app("a", "ns"))}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(appOnly.Report, "Identity gate") {
		t.Errorf("an application-only report names the identity selector:\n%s", appOnly.Report)
	}
}

// The identity selector grants power over users and superuser groups, so it
// names only the namespaces of the converted groups; the adoption selector
// still names every converted namespace.
func TestConvertGroupIdentitySelectorListsOnlyGroupNamespaces(t *testing.T) {
	groups := strings.Replace(mediaUsers, "namespace: media", "namespace: authentik", 1)
	in := app("a", "team-a") + "---\n" + groups
	res, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(res.Report, "Identity gate")
	if i < 0 {
		t.Fatalf("report lacks the identity gate paragraph:\n%s", res.Report)
	}
	adoption, identity := res.Report[:i], res.Report[i:]
	if !strings.Contains(adoption, "kubernetes.io/metadata.name in (authentik, team-a)") {
		t.Errorf("the adoption line must list every namespace:\n%s", adoption)
	}
	if !strings.Contains(identity, "in (authentik)\n") || strings.Contains(identity, "team-a") {
		t.Errorf("the identity line must list only the group namespace:\n%s", identity)
	}
}

func TestConvertGroupAttributesNotOwnedAreReported(t *testing.T) {
	in := strings.Replace(mediaUsers, "    goauthentik.io/x: y\n",
		"    empty: null\n    tilde: ~\n    authentik_operator: x\n    list: [a, 1]\n    flag: false\n", 1)
	obj, res := groupOf(t, in, "authentikgroup-media-users.yaml", Options{})
	if keys := slices.Sorted(maps.Keys(obj.Spec.Attributes)); !slices.Equal(keys, []string{"flag", "list", "role"}) {
		t.Errorf("kept attributes = %v, want flag, list and role", keys)
	}
	const (
		object   = "has an object or null value, which this operator cannot own per key; left in Authentik, unmanaged"
		reserved = "is reserved for Authentik or this operator; left in Authentik, unmanaged"
	)
	for _, want := range []string{
		`attribute "empty" ` + object, `attribute "tilde" ` + object, `attribute "nested" ` + object,
		`attribute "authentik_operator" ` + reserved, `attribute "settings" ` + reserved,
	} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("report lacks %q:\n%s", want, res.Report)
		}
	}
}

// A report of groups alone must not talk about providers and applications.
func TestConvertGroupReportWording(t *testing.T) {
	_, res := groupOf(t, mediaUsers, "authentikgroup-media-users.yaml", Options{})
	for _, bad := range []string{"provider or application", "grantTypes", "every converted provider and application"} {
		if strings.Contains(res.Report, bad) {
			t.Errorf("a group-only report mentions %q:\n%s", bad, res.Report)
		}
	}
	for _, want := range []string{"adopts the group JeffResc created", "every converted group"} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("a group-only report lacks %q:\n%s", want, res.Report)
		}
	}
	mixed, err := Convert([]Input{{Source: testSource, Data: []byte(app("a", "ns") + "---\n" + mediaUsers)}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"adopts the provider or application JeffResc created", "Converted groups adopt the group",
		"Converted groups have no match",
	} {
		if !strings.Contains(mixed.Report, want) {
			t.Errorf("a mixed report lacks %q:\n%s", want, mixed.Report)
		}
	}
}
