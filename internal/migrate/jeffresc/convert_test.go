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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

const (
	testSource    = "in.yaml"
	policyModeAll = "all"
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
		Type: authentikiov1alpha1.AccessTypeGroups, Groups: []string{"media-users", "media-admins"},
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

func TestGroupKindIsRefusedWithGuidance(t *testing.T) {
	in := "apiVersion: goauthentik.io/v1alpha1\nkind: AuthentikGroup\n" +
		"metadata: {name: g, namespace: ns}\nspec: {name: g}\n"
	_, err := Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"AuthentikGroup", "not supported", "keep it on JeffResc", "deletionPolicy Retain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
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
	res, err = Convert([]Input{{Source: testSource, Data: []byte(in)}}, Options{AuthentikRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Report, "adoption.defaultConnectionNamespaceSelector") {
		t.Errorf("report names the selector although authentikRef is set:\n%s", res.Report)
	}
}
