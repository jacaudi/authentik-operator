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

package authentik

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated client tracks Authentik 2026.8 and rejects a response that
// lacks a property 2026.8 added. testdata/2026.2.3 holds responses recorded
// from a real Authentik 2026.2.3 (found by the end-to-end suite: the operator
// failed every Application and credentials-Secret reconcile with "no value
// given for required property pbm_uuid" / "dcr_registration").
func recorded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "2026.2.3", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serveRecorded(t *testing.T) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var file string
		switch {
		case strings.HasSuffix(r.URL.Path, "/setup_urls/"):
			file = "setup_urls.json"
		case strings.HasSuffix(r.URL.Path, "/providers/oauth2/1/"):
			file = "provider.json"
		case strings.HasSuffix(r.URL.Path, "/core/applications/e2e-oidc/"):
			file = "application.json"
		case strings.HasSuffix(r.URL.Path, "/core/applications/"):
			file = "applications_list.json"
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(recorded(t, file))
	})
}

func TestDecodesAuthentik20262Application(t *testing.T) {
	c := serveRecorded(t)
	app, err := c.GetApplication(t.Context(), "e2e-oidc")
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if app.Slug != "e2e-oidc" || !app.HasProvider() {
		t.Fatalf("unexpected application: %+v", app)
	}
}

func TestDecodesAuthentik20262ApplicationList(t *testing.T) {
	c := serveRecorded(t)
	res, resp, err := c.API().CoreAPI.CoreApplicationsList(t.Context()).Slug("e2e-oidc").Execute()
	if err := Check(resp, err); err != nil {
		t.Fatalf("list applications: %v", err)
	}
	if len(res.Results) != 1 || res.Results[0].Slug != "e2e-oidc" {
		t.Fatalf("unexpected results: %+v", res.Results)
	}
}

func TestDecodesAuthentik20262SetupURLs(t *testing.T) {
	c := serveRecorded(t)
	u, err := c.OAuth2ProviderSetupURLs(t.Context(), 1)
	if err != nil {
		t.Fatalf("OAuth2ProviderSetupURLs: %v", err)
	}
	if !strings.HasSuffix(u.Issuer, "/application/o/e2e-oidc/") {
		t.Fatalf("unexpected issuer %q", u.Issuer)
	}
}

// A provider from 2026.2 has no grant_types at all; the decoded value must
// say "not reported" (nil), not an empty list, so callers can tell the two apart.
func TestAuthentik20262ProviderReportsNoGrantTypes(t *testing.T) {
	c := serveRecorded(t)
	p, err := c.GetOAuth2Provider(t.Context(), 1)
	if err != nil {
		t.Fatalf("GetOAuth2Provider: %v", err)
	}
	if p.GrantTypes != nil {
		t.Fatalf("GrantTypes = %v, want nil (field absent on 2026.2)", p.GrantTypes)
	}
}

// Responses from a release that already sends the properties, and responses
// that are not what a rule expects, pass through unchanged.
func TestCompatLeavesPresentAndForeignContentAlone(t *testing.T) {
	fill := compatRule("/api/v3/core/applications/x/")
	got, ok := patchJSON([]byte(`{"slug":"x","pbm_uuid":"abc","pk":12345678901234567890}`), fill)
	if !ok || !strings.Contains(string(got), `"pbm_uuid":"abc"`) || !strings.Contains(string(got), "12345678901234567890") {
		t.Fatalf("present value or number changed: %s", got)
	}
	got, ok = patchJSON([]byte(`{"detail":"nope"}`), fill)
	if !ok || strings.Contains(string(got), "pbm_uuid") {
		t.Fatalf("a non-application object must not gain properties: %s", got)
	}
	if _, ok := patchJSON([]byte(`<html>`), fill); ok {
		t.Fatal("non-JSON must be reported as unpatched")
	}
	if compatRule("/api/v3/providers/oauth2/") != nil {
		t.Fatal("no rule expected for the provider endpoint itself")
	}
}
