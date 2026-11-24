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
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	api "goauthentik.io/api/v3"
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
		case strings.HasSuffix(r.URL.Path, "/policies/bindings/") && r.Method == http.MethodPost:
			file = "binding.json"
		case strings.HasSuffix(r.URL.Path, "/policies/bindings/"):
			file = "bindings_list.json"
		case strings.Contains(r.URL.Path, "/core/groups/") && strings.HasSuffix(r.URL.Path, "/used_by/"):
			file = "group_used_by.json"
		case strings.HasSuffix(r.URL.Path, "/core/groups/"):
			file = "groups_list.json"
		case strings.Contains(r.URL.Path, "/core/groups/"):
			file = "group.json"
		case strings.HasSuffix(r.URL.Path, "/core/users/me/"):
			file = "users_me.json"
		case strings.HasSuffix(r.URL.Path, "/core/users/") && r.URL.Query().Has("attributes"):
			file = "users_owner_list.json"
		case strings.HasSuffix(r.URL.Path, "/core/users/"):
			file = "users_list.json"
		case strings.Contains(r.URL.Path, "/core/users/"):
			file = "user.json"
		case strings.HasSuffix(r.URL.Path, "/stages/invitation/invitations/") && r.Method == http.MethodPost:
			file = "invitation.json"
		case strings.HasSuffix(r.URL.Path, "/stages/invitation/invitations/"):
			file = "invitations_list.json"
		case strings.HasSuffix(r.URL.Path, "/authenticators/admin/all/"):
			file = "authenticators.json"
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
	res, resp, err := c.api.CoreAPI.CoreApplicationsList(t.Context()).Slug("e2e-oidc").Execute()
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
	fill := compatRule("/api/v3/core/applications/x/").fill
	got, ok := patchJSON([]byte(`{"slug":"x","pbm_uuid":"abc","pk":12345678901234567890}`), fill)
	if !ok || !strings.Contains(string(got), `"pbm_uuid":"abc"`) ||
		!strings.Contains(string(got), "12345678901234567890") {
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

// roundTrip runs compatTransport over a canned response.
func roundTrip(t *testing.T, path string, status int, ctype string, body io.ReadCloser) (*http.Response, []byte) {
	t.Helper()
	next := roundTripFunc(func(*http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("Content-Type", ctype)
		return &http.Response{StatusCode: status, Header: h, Body: body}, nil
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://x"+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&compatTransport{next: next}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp, got
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeSpy struct {
	io.Reader

	closed bool
}

func (c *closeSpy) Close() error { c.closed = true; return nil }

const appPath = "/api/v3/core/applications/x/"

func TestCompatAddsMissingProperty(t *testing.T) {
	_, got := roundTrip(t, appPath, 200, "application/json", io.NopCloser(strings.NewReader(`{"slug":"x"}`)))
	if !strings.Contains(string(got), `"pbm_uuid":null`) {
		t.Fatalf("not patched: %s", got)
	}
}

// A body over the limit must reach the decoder complete and unmodified.
func TestCompatOverLimitBodyPassesThroughUntruncated(t *testing.T) {
	big := `{"slug":"x","pad":"` + strings.Repeat("a", maxCompatBody) + `"}`
	spy := &closeSpy{Reader: strings.NewReader(big)}
	resp, got := roundTrip(t, appPath, 200, "application/json", spy)
	if string(got) != big {
		t.Fatalf("body changed: got %d bytes, want %d", len(got), len(big))
	}
	if !spy.closed {
		t.Fatal("the original body was not closed")
	}
	_ = resp
}

func TestCompatPassesThroughNon2xxAndNonJSON(t *testing.T) {
	_, got := roundTrip(t, appPath, 404, "application/json", io.NopCloser(strings.NewReader(`{"slug":"x"}`)))
	if string(got) != `{"slug":"x"}` {
		t.Fatalf("non-2xx rewritten: %s", got)
	}
	_, got = roundTrip(t, appPath, 200, "text/html", io.NopCloser(strings.NewReader(`{"slug":"x"}`)))
	if string(got) != `{"slug":"x"}` {
		t.Fatalf("non-JSON rewritten: %s", got)
	}
}

// When the key is already there the body is returned byte for byte (no
// decode and re-encode, so no reordering or reformatting).
func TestCompatSkipsRewriteWhenKeyPresent(t *testing.T) {
	in := `{ "slug" : "x",   "pbm_uuid": "abc" }`
	_, got := roundTrip(t, appPath, 200, "application/json", io.NopCloser(strings.NewReader(in)))
	if string(got) != in {
		t.Fatalf("rewritten: %s", got)
	}
}

// recordedFirst returns results[0] of a recorded page. Keys that the tests
// need from a recording (a target, a pk) are read from it, never hard-coded.
func recordedFirst(t *testing.T, name string) map[string]any {
	t.Helper()
	var page struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(recorded(t, name), &page); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(page.Results) == 0 {
		t.Fatalf("%s has no results", name)
	}
	return page.Results[0]
}

// recordedObject returns the top-level object of a recording.
func recordedObject(t *testing.T, name string) map[string]any {
	t.Helper()
	var o map[string]any
	if err := json.Unmarshal(recorded(t, name), &o); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return o
}

func TestDecodesAuthentik20262Bindings(t *testing.T) {
	target, _ := recordedFirst(t, "bindings_list.json")["target"].(string)
	got, err := serveRecorded(t).ListBindings(t.Context(), target)
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	if len(got) != 1 || got[0].Expiring || got[0].GroupObj.Get() == nil {
		t.Fatalf("unexpected bindings: %+v", got)
	}
}

func TestDecodesAuthentik20262BindingCreate(t *testing.T) {
	got, err := serveRecorded(t).CreateBinding(t.Context(), *api.NewPolicyBindingRequest("ignored", 0))
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if got.Order != 0 {
		t.Fatalf("Order = %d, want 0", got.Order)
	}
}

func TestDecodesAuthentik20262Group(t *testing.T) {
	pk, _ := recordedObject(t, "group.json")["pk"].(string)
	got, err := serveRecorded(t).GetGroup(t.Context(), pk)
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if got.Name != "rec-group" || !HasOwnerProof(got.Attributes, "rec-uid") || len(got.UsersObj) != 1 {
		t.Fatalf("unexpected group: %+v", got)
	}
}

func TestDecodesAuthentik20262GroupByName(t *testing.T) {
	if _, err := serveRecorded(t).FindGroupByName(t.Context(), "rec-group"); err != nil {
		t.Fatalf("FindGroupByName: %v", err)
	}
}

func TestDecodesAuthentik20262GroupUsedBy(t *testing.T) {
	pk, _ := recordedObject(t, "group.json")["pk"].(string)
	got, err := serveRecorded(t).GroupUsedBy(t.Context(), pk)
	if err != nil {
		t.Fatalf("GroupUsedBy: %v", err)
	}
	if !slices.ContainsFunc(got, func(u api.UsedBy) bool {
		return u.App == "authentik_policies" && u.ModelName == "policybinding"
	}) {
		t.Fatalf("no policy binding in %+v", got)
	}
}

// userPK reads the recorded user's integer pk.
func userPK(t *testing.T) int32 {
	t.Helper()
	pk, ok := recordedObject(t, "user.json")["pk"].(float64)
	if !ok {
		t.Fatal("user.json has no numeric pk")
	}
	return int32(pk)
}

func TestDecodesAuthentik20262User(t *testing.T) {
	got, err := serveRecorded(t).GetUser(t.Context(), userPK(t))
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "rec-user" {
		t.Fatalf("Username = %q, want rec-user", got.Username)
	}
}

func TestDecodesAuthentik20262UserByUsername(t *testing.T) {
	if _, err := serveRecorded(t).FindUserByUsername(t.Context(), "rec-user"); err != nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
}

func TestDecodesAuthentik20262UsersByOwnerUID(t *testing.T) {
	got, err := serveRecorded(t).FindUsersByOwnerUID(t.Context(), "rec-uid")
	if err != nil {
		t.Fatalf("FindUsersByOwnerUID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d users, want 1", len(got))
	}
}

func TestDecodesAuthentik20262Me(t *testing.T) {
	got, err := serveRecorded(t).Me(t.Context())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if !got.IsSuperuser || got.Username != tokenUsername {
		t.Fatalf("unexpected token owner: %+v", got)
	}
}

// Ping shares the endpoint: the AuthentikDeployment reconciler reports
// Connected from it.
func TestPingDecodesAuthentik20262(t *testing.T) {
	got, err := serveRecorded(t).Ping(t.Context())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got != tokenUsername {
		t.Fatalf("Ping = %q, want %q", got, tokenUsername)
	}
}

func TestDecodesAuthentik20262InvitationCreate(t *testing.T) {
	got, err := serveRecorded(t).CreateInvitation(t.Context(), api.InvitationRequest{Name: "rec-invitation"})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if _, ok := got.FixedData["attributes"]; got.Name != "rec-invitation" || !ok {
		t.Fatalf("unexpected invitation: %+v", got)
	}
}

func TestDecodesAuthentik20262InvitationByName(t *testing.T) {
	if _, err := serveRecorded(t).FindInvitationByName(t.Context(), "rec-invitation"); err != nil {
		t.Fatalf("FindInvitationByName: %v", err)
	}
}

func TestDecodesAuthentik20262Authenticators(t *testing.T) {
	got, err := serveRecorded(t).UserAuthenticators(t.Context(), userPK(t))
	if err != nil {
		t.Fatalf("UserAuthenticators: %v", err)
	}
	if len(got) != 1 || got[0].Name == "" || got[0].Type == "" {
		t.Fatalf("want exactly one named, typed device, got %+v", got)
	}
}

// A binding that already carries expires and expiring (2026.8) is not changed.
func TestCompatBindingRuleLeavesPresentValues(t *testing.T) {
	rule := compatRule("/api/v3/policies/bindings/")
	got, ok := patchJSON([]byte(`{"target":"x","expires":"2026-01-01T00:00:00Z","expiring":true}`), rule.fill)
	if !ok || !strings.Contains(string(got), `"expires":"2026-01-01T00:00:00Z"`) ||
		!strings.Contains(string(got), `"expiring":true`) {
		t.Fatalf("present values changed: %s", got)
	}
}

// /core/users/me/ on 2026.2.3 sends only "user": the rule adds the top-level
// "users" array (holding that same user) and user.is_current, which 2026.8
// sends and the pinned client requires.
func TestCompatMeRuleAddsUsersAndIsCurrent(t *testing.T) {
	rule := compatRule("/api/v3/core/users/me/")
	got, ok := patchJSON([]byte(`{"user":{"pk":4,"username":"akadmin"}}`), rule.fill)
	if !ok {
		t.Fatal("not patched")
	}
	var doc struct {
		User  map[string]any   `json:"user"`
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.User["is_current"] != true || len(doc.Users) != 1 ||
		doc.Users[0]["is_current"] != true || doc.Users[0]["username"] != tokenUsername {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestCompatMeRuleLeavesPresentValues(t *testing.T) {
	rule := compatRule("/api/v3/core/users/me/")
	got, ok := patchJSON([]byte(`{"users":[{"username":"a"}],"user":{"username":"a","is_current":false}}`), rule.fill)
	if !ok {
		t.Fatal("not patched")
	}
	var doc struct {
		User  map[string]any   `json:"user"`
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc.Users[0]["is_current"]; has || len(doc.Users) != 1 || doc.User["is_current"] != false {
		t.Fatalf("present values changed: %s", got)
	}
}
