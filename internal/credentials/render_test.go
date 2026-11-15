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
	"errors"
	"strings"
	"testing"
	"time"
)

const clientSecret = "s3cr3t"

var vars = Vars{
	ClientID: "id", ClientSecret: clientSecret, IssuerURL: "https://ak.example.com/application/o/app/",
	AuthURL: "https://ak.example.com/application/o/authorize/", TokenURL: "https://ak.example.com/application/o/token/",
	UserInfoURL: "https://ak.example.com/application/o/userinfo/", Slug: "app", Name: "App",
}

func TestDefaultKeys(t *testing.T) {
	t.Parallel()
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

// allowedTemplates are templates within the safe subset, with one key each
// to check; they also seed FuzzRender.
var allowedTemplates = map[string]struct {
	tmpl string
	key  string
	want string
}{
	"env var style": {"OIDC_CLIENT_ID: {{ .ClientID }}\nOIDC_CLIENT_SECRET: {{ .ClientSecret }}\n",
		"OIDC_CLIENT_SECRET", clientSecret},
	"scalars":        {"PORT: 8080\n", "PORT", "8080"},
	"if/else":        {"{{ if eq .Slug \"app\" }}A: yes{{ else }}A: no{{ end }}\n", "A", "yes"},
	"printf quote":   {"NAME: {{ printf \"%q\" .Name }}\n", "NAME", "App"},
	"pipeline":       {"N: {{ .Name | printf \"%s-x\" }}\n", "N", "App-x"},
	"comment":        {"{{/* c */}}A: b\n", "A", "b"},
	"null is empty":  {"A:\n", "A", ""},
	"secret printed": {"S: {{ print .ClientSecret }}\n", "S", clientSecret},
}

func TestAllowedTemplates(t *testing.T) {
	t.Parallel()
	for name, tc := range allowedTemplates {
		got, err := Render(tc.tmpl, vars)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got[tc.key]) != tc.want {
			t.Errorf("%s: %s = %q, want %q", name, tc.key, got[tc.key], tc.want)
		}
	}
}

// unsafeTemplates could hang a worker or exhaust memory; they must be
// rejected at parse time, before execution. They also seed FuzzRender.
var unsafeTemplates = map[string]string{
	"range int":     "{{range 100000000000}}{{end}}",
	"range output":  "{{range 100000000000}}{{$.ClientSecret}}{{end}}",
	"define":        manyDefines() + "A: b",
	"template call": "{{template \"x\"}}",
	"with":          "{{with .Name}}A: b{{end}}",
	"variable":      "{{$x := .Name}}A: b",
	"printf width":  "A: {{printf \"%0999999999d\" 1}}",
	"printf dyn":    "A: {{printf .Name 1}}",
	"bad func":      "A: {{len .Name}}",
	"call":          "A: {{call .Name}}",
	"nested field":  "A: {{.Name.X}}",
	"dot":           "A: {{.}}",
}

// manyDefines returns 60 define blocks (at most one template is allowed).
func manyDefines() string {
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		b.WriteString("{{define \"t" + string(rune('a'+i%26)) + "\"}}{{end}}")
	}
	return b.String()
}

func TestUnsafeTemplatesRejected(t *testing.T) {
	t.Parallel()
	for name, tmpl := range unsafeTemplates {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { _, err := Render(tmpl, vars); done <- err }()
			select {
			case err := <-done:
				if !IsTemplateError(err) {
					t.Errorf("err = %v, want TemplateError", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("render did not return")
			}
		})
	}
}

// The render must not become an oracle on the client secret.
func TestClientSecretCannotBeTested(t *testing.T) {
	t.Parallel()
	for _, tmpl := range []string{
		"{{if lt .ClientSecret \"m\"}}{{.Missing}}{{end}}A: b",
		"A: {{eq .ClientSecret \"x\"}}",
		"{{if .ClientSecret}}A: b{{end}}",
		"A: {{not (eq .ClientSecret \"x\")}}",
	} {
		if _, err := Render(tmpl, vars); !IsTemplateError(err) || !strings.Contains(err.Error(), "ClientSecret") {
			t.Errorf("%q: err = %v", tmpl, err)
		}
	}
}

// outputErrorTemplates render (or fail to parse) into something that is
// not a valid Secret. They also seed FuzzRender.
var outputErrorTemplates = map[string]string{
	"parse":          "A: {{ .ClientID ",
	"missing key":    "A: {{ .ClientIDX }}",
	"not a map":      "- a\n- b\n",
	"nested value":   "A:\n  B: c\n",
	"no keys":        "# nothing\n",
	"invalid key":    "bad key: v\n",
	"duplicate key":  "A: x\nA: y\n",
	"multi document": "A: b\n---\nC: d\n",
	"alias":          "A: &x v\nB: *x\n",
	"too large":      "A: {{ printf \"%s%s%s%s\" .Name .Name .Name .Name }}" + strings.Repeat("x", MaxOutput),
}

func TestOutputErrors(t *testing.T) {
	t.Parallel()
	for name, tmpl := range outputErrorTemplates {
		_, err := Render(tmpl, vars)
		if !IsTemplateError(err) {
			t.Errorf("%s: err = %v, want TemplateError", name, err)
		}
	}
}

func TestErrorsNeverContainTheSecret(t *testing.T) {
	t.Parallel()
	for _, tmpl := range []string{
		"A: {{ .ClientSecret }}\n  : : [\n",                // YAML parse error on the secret's line
		"\"{{ .ClientSecret }} x\": y\n",                   // invalid key containing the secret
		"{{ .ClientSecret }}: a\n{{ .ClientSecret }}: b\n", // duplicate key
	} {
		_, err := Render(tmpl, vars)
		if err == nil || strings.Contains(err.Error(), clientSecret) {
			t.Errorf("%q: err = %v", tmpl, err)
		}
	}
}

// A value containing a newline cannot add keys when quoted with printf %q.
func TestQuotedValuesCannotInjectKeys(t *testing.T) {
	t.Parallel()
	v := vars
	v.Name = `x: y, INJECTED: "z" #c`
	for _, tmpl := range []string{"NAME: {{ printf \"%q\" .Name }}\n", "{NAME: {{ printf \"%q\" .Name }}}"} {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tmpl, v)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || string(got["NAME"]) != v.Name {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestControlCharactersRejected(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Vars){
		"newline in client ID": func(v *Vars) { v.ClientID = "x\nINJECTED: y" },
		"newline in name":      func(v *Vars) { v.Name = "x\nINJECTED: y" },
		"DEL in secret":        func(v *Vars) { v.ClientSecret = clientSecret + "\x7f" },
		// YAML also breaks lines on NEL, LINE SEPARATOR and PARAGRAPH
		// SEPARATOR, so each of these would otherwise add a key.
		"NEL in client ID":                 func(v *Vars) { v.ClientID = "x\u0085INJECTED: y" },
		"line separator in client ID":      func(v *Vars) { v.ClientID = "x INJECTED: y" },
		"paragraph separator in client ID": func(v *Vars) { v.ClientID = "x INJECTED: y" },
		"C1 control in name":               func(v *Vars) { v.Name = "x\u0080y" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := vars
			mutate(&v)
			_, err := Render("ID: {{ .ClientID }}\n", v)
			if !IsTemplateError(err) || !strings.Contains(err.Error(), "control characters") {
				t.Fatalf("got %v", err)
			}
			if strings.Contains(err.Error(), "INJECTED") || strings.Contains(err.Error(), clientSecret) {
				t.Fatalf("error echoes the value: %v", err)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	for tmpl, wantErr := range map[string]string{
		"":                                    "",
		"ID: {{ .ClientID }}":                 "",
		"NAME: {{ printf \"%q\" .Name }}":     "",
		"NAME: {{ .Name }}":                   "printf",
		"A: {{ .NoSuchVar }}":                 "NoSuchVar",
		"- not a map":                         "map",
		"S: {{ if .ClientSecret }}x{{ end }}": "ClientSecret",
	} {
		err := Check(tmpl)
		switch {
		case wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tmpl, err)
		case wantErr != "" && (!IsTemplateError(err) || !strings.Contains(err.Error(), wantErr)):
			t.Errorf("%q: got %v, want a template error mentioning %q", tmpl, err, wantErr)
		}
	}
}

func TestRewrite(t *testing.T) {
	t.Parallel()
	in := "http://authentik-server.authentik.svc.cluster.local"
	ext := "https://sso.example.com"
	for u, want := range map[string]string{
		in + "/application/o/app/": ext + "/application/o/app/",
		in:                         ext,
		"HTTP://Authentik-Server.authentik.svc.cluster.local:80/x/": ext + "/x/",
		in + "/application/o/token/?a=b":                            ext + "/application/o/token/?a=b",
	} {
		got, err := Rewrite(u, in, ext)
		if err != nil || got != want {
			t.Errorf("Rewrite(%q) = %q, %v; want %q", u, got, err, want)
		}
	}
	for _, u := range []string{
		"https://other.example.com/application/o/token/",
		in + "evil/",
		"https://authentik-server.authentik.svc.cluster.local/x",
	} {
		if _, err := Rewrite(u, in, ext); !errors.Is(err, ErrURLMismatch) {
			t.Errorf("Rewrite(%q): err = %v, want ErrURLMismatch", u, err)
		}
	}
	// Subpath deployments.
	got, err := Rewrite(in+"/authentik/application/o/app/", in+"/authentik", ext+"/sso")
	if err != nil || got != ext+"/sso/application/o/app/" {
		t.Errorf("subpath: %q, %v", got, err)
	}
	if got, err := Rewrite(in+"/x", in, ""); err != nil || got != in+"/x" {
		t.Errorf("empty external must not rewrite: %q, %v", got, err)
	}
}
