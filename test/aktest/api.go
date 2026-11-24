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

package aktest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// fName is the JSON field Authentik uses for an object's name.
const fName = "name"

// JSON fields several calls share.
const (
	fSlug   = "slug"
	fTarget = "target"
	fOrder  = "order"
)

// Obj is a decoded JSON object. The suite deliberately does not use the
// operator's generated client: a second, schema-free view of the API is what
// notices when the operator's view of it drifts.
type Obj = map[string]any

// API is a minimal Authentik REST client authenticated with a bearer token.
type API struct {
	Base  string // e.g. http://authentik-server.authentik.svc.cluster.local
	Token string
	HTTP  *http.Client
}

// NewAPI returns a client for base using rt (nil for the default transport).
func NewAPI(base, token string, rt http.RoundTripper) *API {
	return &API{Base: strings.TrimRight(base, "/"), Token: token,
		HTTP: &http.Client{Transport: rt, Timeout: 30 * time.Second}}
}

// Do sends a request and decodes a JSON response into out (if non-nil).
// A status of 400 or above is an error carrying the body.
func (a *API) Do(ctx context.Context, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	raw, err := readBody(resp, 8<<20)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, snippet(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

// list GETs a paginated endpoint and returns the first page's results.
func (a *API) list(ctx context.Context, path string, q url.Values) ([]Obj, error) {
	var page struct {
		Results []Obj `json:"results"`
	}
	if err := a.Do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
		return nil, err
	}
	return page.Results, nil
}

// Count returns the total number of objects behind a list endpoint.
func (a *API) Count(ctx context.Context, path string) (int, error) {
	var page struct {
		Pagination struct {
			Count int `json:"count"`
		} `json:"pagination"`
	}
	if err := a.Do(ctx, http.MethodGet, path+"?page_size=1", nil, &page); err != nil {
		return 0, err
	}
	return page.Pagination.Count, nil
}

// Whoami returns the username the token authenticates as. It is the cheapest
// call that proves the bootstrap token exists and the server is serving.
func (a *API) Whoami(ctx context.Context) (string, error) {
	var out struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := a.Do(ctx, http.MethodGet, "/api/v3/core/users/me/", nil, &out); err != nil {
		return "", err
	}
	return out.User.Username, nil
}

// Version returns Authentik's reported version string.
func (a *API) Version(ctx context.Context) (string, error) {
	var out struct {
		VersionCurrent string `json:"version_current"`
	}
	if err := a.Do(ctx, http.MethodGet, "/api/v3/admin/version/", nil, &out); err != nil {
		return "", err
	}
	return out.VersionCurrent, nil
}

// FlowPK returns the pk of the flow with the given slug.
func (a *API) FlowPK(ctx context.Context, slug string) (string, error) {
	rs, err := a.list(ctx, "/api/v3/flows/instances/", url.Values{fSlug: {slug}})
	if err != nil {
		return "", err
	}
	if len(rs) != 1 {
		return "", fmt.Errorf("flow %q: %d matches", slug, len(rs))
	}
	return Str(rs[0], "pk"), nil
}

// ScopeMappingPKs returns the pks of the scope mappings for the given scope
// names, preferring the managed (built-in) one when several share a name.
func (a *API) ScopeMappingPKs(ctx context.Context, scopes ...string) ([]string, error) {
	var out []string
	for _, s := range scopes {
		rs, err := a.list(ctx, "/api/v3/propertymappings/provider/scope/", url.Values{"scope_name": {s}})
		if err != nil {
			return nil, err
		}
		pk := ""
		for _, r := range rs {
			if Str(r, "managed") != "" || pk == "" {
				pk = Str(r, "pk")
			}
		}
		if pk == "" {
			return nil, fmt.Errorf("no scope mapping for scope %q", s)
		}
		out = append(out, pk)
	}
	return out, nil
}

// CreateOAuthProvider creates a confidential OAuth2 provider the way another
// operator (or an administrator) would: Authentik generates the client ID and
// secret. The returned object includes them.
func (a *API) CreateOAuthProvider(ctx context.Context, name string, redirects []string) (Obj, error) {
	authz, err := a.FlowPK(ctx, "default-provider-authorization-implicit-consent")
	if err != nil {
		return nil, err
	}
	inval, err := a.FlowPK(ctx, "default-provider-invalidation-flow")
	if err != nil {
		return nil, err
	}
	mappings, err := a.ScopeMappingPKs(ctx, "openid", "profile", "email")
	if err != nil {
		return nil, err
	}
	var uris []Obj
	for _, r := range redirects {
		uris = append(uris, Obj{"matching_mode": "strict", "url": r})
	}
	body := Obj{
		fName:                        name,
		"authorization_flow":         authz,
		"invalidation_flow":          inval,
		"client_type":                "confidential",
		"redirect_uris":              uris,
		"property_mappings":          mappings,
		"include_claims_in_id_token": true,
		"sub_mode":                   "hashed_user_id",
		"issuer_mode":                "per_provider",
	}
	var out Obj
	if err := a.Do(ctx, http.MethodPost, "/api/v3/providers/oauth2/", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// OAuthProviderByName returns the provider with exactly this name, or nil.
func (a *API) OAuthProviderByName(ctx context.Context, name string) (Obj, error) {
	rs, err := a.list(ctx, "/api/v3/providers/oauth2/", url.Values{fName: {name}})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if Str(r, fName) == name {
			return r, nil
		}
	}
	return nil, nil
}

// OAuthProvider returns a provider by pk, or nil when it does not exist.
func (a *API) OAuthProvider(ctx context.Context, pk int) (Obj, error) {
	var out Obj
	err := a.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v3/providers/oauth2/%d/", pk), nil, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	return out, err
}

// PatchOAuthProvider changes fields of a provider.
func (a *API) PatchOAuthProvider(ctx context.Context, pk int, fields Obj) error {
	return a.Do(ctx, http.MethodPatch, fmt.Sprintf("/api/v3/providers/oauth2/%d/", pk), fields, nil)
}

// CreateApplication creates an application bound to a provider pk.
func (a *API) CreateApplication(ctx context.Context, name, slug string, providerPK int) (Obj, error) {
	var out Obj
	err := a.Do(ctx, http.MethodPost, "/api/v3/core/applications/",
		Obj{fName: name, fSlug: slug, "provider": providerPK}, &out)
	return out, err
}

// ApplicationBySlug returns the application, or nil when it does not exist.
func (a *API) ApplicationBySlug(ctx context.Context, slug string) (Obj, error) {
	var out Obj
	err := a.Do(ctx, http.MethodGet, "/api/v3/core/applications/"+url.PathEscape(slug)+"/", nil, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	return out, err
}

// DeleteApplicationIfExists deletes the application, if there is one.
func (a *API) DeleteApplicationIfExists(ctx context.Context, slug string) error {
	err := a.Do(ctx, http.MethodDelete, "/api/v3/core/applications/"+url.PathEscape(slug)+"/", nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteOAuthProviderByNameIfExists deletes the provider with exactly this
// name, if there is one.
func (a *API) DeleteOAuthProviderByNameIfExists(ctx context.Context, name string) error {
	p, err := a.OAuthProviderByName(ctx, name)
	if err != nil || p == nil {
		return err
	}
	return a.Do(ctx, http.MethodDelete, "/api/v3/providers/oauth2/"+Str(p, "pk")+"/", nil, nil)
}

// ModelWriteCount returns how many audit events of the given actions
// (model_created, model_updated, model_deleted) Authentik holds for objects
// of one model, e.g. app "authentik_providers_oauth2", model "oauth2provider".
// Authentik writes one for every API save, even when no field changed, which
// is what lets the suite measure idempotency on the server instead of
// trusting the operator's own bookkeeping. Filtering and counting happen
// server side, so the result is not limited by a page size.
func (a *API) ModelWriteCount(ctx context.Context, app, model string, actions ...string) (int, error) {
	return a.writeCount(ctx, url.Values{"context_model_app": {app}, "context_model_name": {model}}, actions)
}

// ObjectWriteCount is ModelWriteCount for one object, by its pk (an integer
// for providers, the UUID for applications). Writes to other objects of the
// model, by anything else in the instance, are not counted. An empty pk would
// count every object of the model, so it is an error.
func (a *API) ObjectWriteCount(ctx context.Context, app, model, pk string, actions ...string) (int, error) {
	if pk == "" {
		return 0, fmt.Errorf("ObjectWriteCount %s.%s: empty pk", app, model)
	}
	return a.writeCount(ctx, url.Values{"context_model_app": {app}, "context_model_name": {model},
		"context_model_pk": {pk}}, actions)
}

// writeCount sums pagination.count of the audit events matching filter, per action.
func (a *API) writeCount(ctx context.Context, filter url.Values, actions []string) (int, error) {
	total := 0
	for _, act := range actions {
		var page struct {
			Pagination struct {
				Count int `json:"count"`
			} `json:"pagination"`
		}
		q := url.Values{"action": {act}, "page_size": {"1"}}
		maps.Copy(q, filter)
		if err := a.Do(ctx, http.MethodGet, "/api/v3/events/events/?"+q.Encode(), nil, &page); err != nil {
			return 0, err
		}
		total += page.Pagination.Count
	}
	return total, nil
}

// post creates an object at path and returns its pk.
func (a *API) post(ctx context.Context, path string, in Obj) (string, error) {
	var out Obj
	if err := a.Do(ctx, http.MethodPost, path, in, &out); err != nil {
		return "", err
	}
	return Str(out, "pk"), nil
}

// CreateUserWithPassword creates an internal user that can sign in with
// password, and returns its pk.
func (a *API) CreateUserWithPassword(ctx context.Context, username, password string) (int, error) {
	raw, err := a.post(ctx, "/api/v3/core/users/", Obj{"username": username, fName: username, "path": "users"})
	if err != nil {
		return 0, err
	}
	pk, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("user %q: pk %q is not a number", username, raw)
	}
	err = a.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v3/core/users/%d/set_password/", pk),
		Obj{"password": password}, nil)
	return pk, err
}

// CreateGroup creates a group and returns its pk.
func (a *API) CreateGroup(ctx context.Context, name string) (string, error) {
	return a.post(ctx, "/api/v3/core/groups/", Obj{fName: name})
}

// CreateGroupBinding binds a group to an application, the way an
// administrator would in the UI, and returns the binding's pk.
func (a *API) CreateGroupBinding(ctx context.Context, groupPK, appPK string, order int) (string, error) {
	return a.post(ctx, "/api/v3/policies/bindings/", Obj{"group": groupPK, fTarget: appPK, fOrder: order})
}

// BindingPKs returns the sorted pks of the policy bindings of an application.
// An empty appPK is an error: the query would list every binding.
func (a *API) BindingPKs(ctx context.Context, appPK string) ([]string, error) {
	if appPK == "" {
		return nil, errors.New("aktest: listing bindings needs an application pk")
	}
	rs, err := a.list(ctx, "/api/v3/policies/bindings/", url.Values{fTarget: {appPK}, "page_size": {"100"}})
	if err != nil {
		return nil, err
	}
	pks := make([]string, 0, len(rs))
	for _, r := range rs {
		pks = append(pks, Str(r, "pk"))
	}
	slices.Sort(pks)
	return pks, nil
}

// EnsureEnrolmentFlow creates, unless a flow with this slug exists, a
// test-only copy of the owner's enrolment flow without the passkey stage:
// invitation, a read-only username prompt, user write, user login. The
// operator's contract ends at "invitation -> account carrying the ownership
// proof"; enrolling a passkey is Authentik's and is not re-tested here.
func (a *API) EnsureEnrolmentFlow(ctx context.Context, slug string) error {
	have, err := a.list(ctx, "/api/v3/flows/instances/", url.Values{fSlug: {slug}})
	if err != nil || len(have) > 0 {
		return err
	}
	flow, err := a.post(ctx, "/api/v3/flows/instances/", Obj{fName: slug, fSlug: slug,
		"title": "Create your account", "designation": "enrollment", "authentication": "require_unauthenticated"})
	if err != nil {
		return err
	}
	invitation, err := a.post(ctx, "/api/v3/stages/invitation/stages/",
		Obj{fName: slug + "-invitation", "continue_flow_without_invitation": false})
	if err != nil {
		return err
	}
	username, err := a.post(ctx, "/api/v3/stages/prompt/prompts/", Obj{fName: slug + "-username",
		"field_key": "username", "label": "Username", "type": "text_read_only", "required": true,
		"placeholder": "Username", fOrder: 0})
	if err != nil {
		return err
	}
	prompt, err := a.post(ctx, "/api/v3/stages/prompt/stages/",
		Obj{fName: slug + "-prompt", "fields": []string{username}})
	if err != nil {
		return err
	}
	write, err := a.post(ctx, "/api/v3/stages/user_write/",
		Obj{fName: slug + "-write", "user_creation_mode": "always_create", "user_type": "internal"})
	if err != nil {
		return err
	}
	login, err := a.post(ctx, "/api/v3/stages/user_login/", Obj{fName: slug + "-login"})
	if err != nil {
		return err
	}
	for _, b := range []struct {
		stage string
		order int
	}{{invitation, 5}, {prompt, 10}, {write, 20}, {login, 100}} {
		if _, err := a.post(ctx, "/api/v3/flows/bindings/",
			Obj{fTarget: flow, "stage": b.stage, fOrder: b.order}); err != nil {
			return err
		}
	}
	return nil
}

// Str returns o[k] as a string, or "".
func Str(o Obj, k string) string {
	switch v := o[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// IsNotFound reports whether err is an HTTP 404 from Do.
func IsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), ": HTTP 404:")
}
