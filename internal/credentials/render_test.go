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

package credentials

import (
	"strings"
	"testing"
)

const clientSecret = "s3cr3t"

var vars = Vars{
	ClientID: "id", ClientSecret: clientSecret, IssuerURL: "https://ak.example.com/application/o/app/",
	AuthURL: "https://ak.example.com/application/o/authorize/", TokenURL: "https://ak.example.com/application/o/token/",
	UserInfoURL: "https://ak.example.com/application/o/userinfo/", Slug: "app", Name: "App",
}

func TestDefaultKeys(t *testing.T) {
	got, err := Render("", vars)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"client-id": "id", "client-secret": clientSecret, "issuer-url": vars.IssuerURL,
		"authorization-url": vars.AuthURL, "token-url": vars.TokenURL, "userinfo-url": vars.UserInfoURL,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d", len(got), len(want))
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// The template shape orac's gitops uses today.
func TestOracStyleTemplate(t *testing.T) {
	got, err := Render("OIDC_CLIENT_ID: {{ .ClientID }}\nOIDC_CLIENT_SECRET: {{ .ClientSecret }}\nISSUER: {{ .IssuerURL }}\n", vars)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["OIDC_CLIENT_ID"]) != "id" || string(got["OIDC_CLIENT_SECRET"]) != clientSecret || string(got["ISSUER"]) != vars.IssuerURL {
		t.Fatalf("unexpected data: %q", got)
	}
}

func TestScalarsBecomeStrings(t *testing.T) {
	got, err := Render("PORT: 8080\nENABLED: true\n", vars)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["PORT"]) != "8080" || string(got["ENABLED"]) != "true" {
		t.Fatalf("unexpected data: %q", got)
	}
}

func TestTemplateErrors(t *testing.T) {
	for name, tmpl := range map[string]string{
		"parse":        "A: {{ .ClientID ",
		"missing key":  "A: {{ .ClientIDX }}",
		"not a map":    "- a\n- b\n",
		"nested value": "A:\n  B: c\n",
		"no keys":      "# nothing\n",
		"invalid key":  "bad key: v\n",
	} {
		_, err := Render(tmpl, vars)
		if !IsTemplateError(err) {
			t.Errorf("%s: err = %v, want TemplateError", name, err)
		}
	}
}

func TestErrorsNeverContainTheSecret(t *testing.T) {
	// The secret lands on a line YAML cannot parse.
	_, err := Render("A: {{ .ClientSecret }}\n  : : [\n", vars)
	if err == nil || strings.Contains(err.Error(), clientSecret) {
		t.Fatalf("err = %v", err)
	}
}

func TestRewrite(t *testing.T) {
	in := "http://authentik-server.authentik.svc.cluster.local"
	ext := "https://sso.example.com"
	cases := map[string]string{
		in + "/application/o/app/": ext + "/application/o/app/",
		in:                         ext,
		"https://already.example.com/application/o/token/": "https://already.example.com/application/o/token/",
		in + "evil/": in + "evil/", // prefix match must stop at a path boundary
	}
	for u, want := range cases {
		if got := Rewrite(u, in, ext); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", u, got, want)
		}
	}
	if got := Rewrite(in+"/x", in, ""); got != in+"/x" {
		t.Errorf("empty external must not rewrite: %q", got)
	}
}
