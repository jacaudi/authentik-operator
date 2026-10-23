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
)

// FuzzRender checks the properties a template author must not be able to
// break: Render never panics, the output never exceeds MaxOutput, and an
// error never echoes the client secret (unless the template text itself
// contains it, which the author then already knows).
func FuzzRender(f *testing.F) {
	for _, tc := range allowedTemplates {
		f.Add(tc.tmpl)
	}
	for _, tmpl := range unsafeTemplates {
		f.Add(tmpl)
	}
	for name, tmpl := range outputErrorTemplates {
		if name == "too large" {
			// A 1 MiB seed would slow every mutation; the output cap is
			// asserted on whatever the fuzzer produces instead.
			continue
		}
		f.Add(tmpl)
	}
	f.Fuzz(func(t *testing.T, tmpl string) {
		got, err := Render(tmpl, vars)
		if err != nil {
			if !IsTemplateError(err) {
				t.Fatalf("err = %v, want a TemplateError", err)
			}
			if !strings.Contains(tmpl, clientSecret) && strings.Contains(err.Error(), clientSecret) {
				t.Fatalf("error echoes the client secret: %v", err)
			}
			return
		}
		size := 0
		for k, v := range got {
			size += len(k) + len(v)
		}
		if size > MaxOutput {
			t.Fatalf("output is %d bytes, over the %d budget", size, MaxOutput)
		}
	})
}

// FuzzRewrite checks that Rewrite never panics, that every error is an
// ErrURLMismatch, and that a rewritten URL starts with the external base
// (or is unchanged when there is nothing to rewrite to).
func FuzzRewrite(f *testing.F) {
	const (
		in  = "http://authentik-server.authentik.svc.cluster.local"
		ext = "https://sso.example.com"
	)
	for _, u := range []string{
		in + "/application/o/app/",
		in,
		"HTTP://Authentik-Server.authentik.svc.cluster.local:80/x/",
		in + "/application/o/token/?a=b",
		"https://other.example.com/application/o/token/",
		in + "evil/",
		"https://authentik-server.authentik.svc.cluster.local/x",
	} {
		f.Add(u, in, ext)
	}
	f.Add(in+"/authentik/application/o/app/", in+"/authentik", ext+"/sso")
	f.Add(in+"/x", in, "")
	f.Fuzz(func(t *testing.T, u, internalBase, externalBase string) {
		got, err := Rewrite(u, internalBase, externalBase)
		if err != nil {
			if !errors.Is(err, ErrURLMismatch) {
				t.Fatalf("err = %v, want ErrURLMismatch", err)
			}
			return
		}
		internalBase = strings.TrimRight(internalBase, "/")
		externalBase = strings.TrimRight(externalBase, "/")
		if externalBase == "" || strings.EqualFold(internalBase, externalBase) {
			if got != u {
				t.Fatalf("Rewrite(%q) = %q, want it unchanged", u, got)
			}
			return
		}
		if !strings.HasPrefix(got, externalBase) {
			t.Fatalf("Rewrite(%q, %q, %q) = %q, want the external prefix", u, internalBase, externalBase, got)
		}
	})
}
