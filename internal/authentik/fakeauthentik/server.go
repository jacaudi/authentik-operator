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

// Package fakeauthentik is an in-memory stand-in for the parts of the
// Authentik API the operator uses, for unit and envtest suites. It returns
// JSON shaped like the real API (all fields the generated client requires)
// and enforces the behaviour tests rely on: bearer auth, exact-match
// filters, unique provider names, and 400s for invalid requests.
package fakeauthentik

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Fixed primary keys of the seeded objects.
const (
	FlowAuthorizationPK  = "00000000-0000-0000-0000-00000000f001"
	FlowInvalidationPK   = "00000000-0000-0000-0000-00000000f002"
	FlowAuthenticationPK = "00000000-0000-0000-0000-00000000f003"
	SigningKeyPK         = "00000000-0000-0000-0000-00000000c001"
	EncryptionKeyPK      = "00000000-0000-0000-0000-00000000c002"
	ScopeOpenIDPK        = "00000000-0000-0000-0000-00000000a001"
	ScopeProfilePK       = "00000000-0000-0000-0000-00000000a002"
	ScopeEmailPK         = "00000000-0000-0000-0000-00000000a003"
	MappingCustomPK      = "00000000-0000-0000-0000-00000000b001"

	FlowAuthorizationSlug  = "default-provider-authorization-implicit-consent"
	FlowInvalidationSlug   = "default-provider-invalidation-flow"
	FlowAuthenticationSlug = "default-authentication-flow"

	SigningKeyName    = "authentik Self-signed Certificate"
	EncryptionKeyName = "jwe-key"
	MappingCustomName = "custom-groups"
)

const (
	fieldName   = "name"
	fieldSlug   = "slug"
	fieldAuthz  = "authorization_flow"
	fieldInval  = "invalidation_flow"
	fieldDetail = "detail"
	notFound    = "Not found."
)

// Request is a recorded API call.
type Request struct {
	Method string
	Path   string
	Body   map[string]any
}

// Server is a fake Authentik.
type Server struct {
	*httptest.Server
	Token string

	mu             sync.Mutex
	flows          []map[string]any
	scopes         []map[string]any
	mappings       []map[string]any
	certs          []map[string]any
	providers      map[int32]map[string]any
	nextPK         int32
	requests       []Request
	failNext       map[string]failure
	loseNextCreate bool

	apps              map[string]map[string]any // by slug
	nextApp           int
	loseNextAppCreate bool
}

type failure struct {
	status int
	body   map[string]any
}

// New starts a fake Authentik seeded with the default flows, scopes and
// signing key. Close it when done.
func New(token string) *Server {
	s := &Server{
		Token:     token,
		providers: map[int32]map[string]any{},
		apps:      map[string]map[string]any{},
		nextPK:    1,
		failNext:  map[string]failure{},
	}
	s.flows = []map[string]any{
		flow(FlowAuthorizationPK, FlowAuthorizationSlug),
		flow(FlowInvalidationPK, FlowInvalidationSlug),
		flow(FlowAuthenticationPK, FlowAuthenticationSlug),
	}
	s.scopes = []map[string]any{
		scope(ScopeOpenIDPK, "openid"),
		scope(ScopeProfilePK, "profile"),
		scope(ScopeEmailPK, "email"),
	}
	s.mappings = []map[string]any{mapping(MappingCustomPK, MappingCustomName)}
	s.certs = []map[string]any{cert(SigningKeyPK, SigningKeyName), cert(EncryptionKeyPK, EncryptionKeyName)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Requests returns the recorded API calls (excluding reads if writesOnly).
func (s *Server) Requests(writesOnly bool) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests {
		if writesOnly && r.Method == http.MethodGet {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Provider returns a copy of a stored OAuth2 provider, or nil.
func (s *Server) Provider(pk int32) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.providers[pk]
	if !ok {
		return nil
	}
	return clone(p)
}

// SetProviderField changes a stored provider out of band (simulates drift).
func (s *Server) SetProviderField(pk int32, field string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[pk][field] = v
}

// SeedProvider inserts a provider not created through the API (e.g. by
// another operator) and returns its pk.
func (s *Server) SeedProvider(name string) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	pk := s.nextPK
	s.nextPK++
	s.providers[pk] = s.completeProvider(pk, map[string]any{
		fieldName: name, fieldAuthz: FlowAuthorizationPK, fieldInval: FlowInvalidationPK,
		"redirect_uris": []any{},
	})
	return pk
}

// Application returns a copy of a stored application, or nil.
func (s *Server) Application(slug string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[slug]
	if !ok {
		return nil
	}
	return clone(a)
}

// SetApplicationField changes a stored application out of band.
func (s *Server) SetApplicationField(slug, field string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[slug][field] = v
}

// SeedApplication inserts an application not created through the API.
// provider is a provider pk or nil.
func (s *Server) SeedApplication(slug string, provider any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pk, ok := provider.(int32); ok {
		provider = float64(pk)
	}
	s.apps[slug] = s.completeApplication(map[string]any{fieldName: slug, fieldSlug: slug, "provider": provider})
}

// DeleteApplication removes an application out of band.
func (s *Server) DeleteApplication(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apps, slug)
}

// ApplicationCount returns the number of stored applications.
func (s *Server) ApplicationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.apps)
}

// LoseNextApplicationCreate makes the next application create succeed
// server-side but answer 504.
func (s *Server) LoseNextApplicationCreate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNextAppCreate = true
}

// DeleteProvider removes a provider out of band.
func (s *Server) DeleteProvider(pk int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteProviderLocked(pk)
}

// deleteProviderLocked deletes a provider and, like Authentik's SET_NULL,
// detaches it from any application.
func (s *Server) deleteProviderLocked(pk int32) {
	delete(s.providers, pk)
	for _, a := range s.apps {
		if a["provider"] == float64(pk) {
			a["provider"] = nil
		}
	}
}

// FailNext makes the next request with this method fail with status and a
// generic body.
func (s *Server) FailNext(method string, status int) {
	s.FailNextWith(method, status, map[string]any{fieldDetail: "injected failure"})
}

// FailNextWith makes the next request with this method fail with status and
// body, e.g. a realistic DRF validation error.
func (s *Server) FailNextWith(method string, status int, body map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = failure{status: status, body: body}
}

// LoseNextCreate makes the next provider create succeed server-side but
// answer 504, as if the response were lost (proxy timeout).
func (s *Server) LoseNextCreate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNextCreate = true
}

// ProviderCount returns the number of stored providers.
func (s *Server) ProviderCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.providers)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeJSON(w, http.StatusForbidden, map[string]any{fieldDetail: "Token invalid/expired"})
		return
	}
	var body map[string]any
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut) {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Body: body})
	if f, ok := s.failNext[r.Method]; ok {
		delete(s.failNext, r.Method)
		writeJSON(w, f.status, f.body)
		return
	}

	q := r.URL.Query()
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	switch {
	case path == "/core/users/me/":
		u := map[string]any{"pk": 1, "username": "akadmin", fieldName: "admin", "is_active": true, "is_superuser": true,
			"is_current": true, "groups": []any{}, "roles": []any{}, "avatar": "", "uid": "u",
			"settings": map[string]any{}, "system_permissions": []string{}}
		writeJSON(w, 200, map[string]any{"user": u, "users": []any{u}})
	case path == "/flows/instances/":
		writeJSON(w, 200, page(filter(s.flows, fieldSlug, q.Get(fieldSlug))))
	case path == "/propertymappings/provider/scope/":
		writeJSON(w, 200, page(filter(s.scopes, "scope_name", q.Get("scope_name"))))
	case path == "/propertymappings/all/":
		writeJSON(w, 200, page(filter(s.mappings, fieldName, q.Get(fieldName))))
	case strings.HasPrefix(path, "/propertymappings/all/"):
		id := strings.Trim(strings.TrimPrefix(path, "/propertymappings/all/"), "/")
		for _, m := range s.mappings {
			if m["pk"] == id {
				writeJSON(w, 200, m)
				return
			}
		}
		writeJSON(w, 404, map[string]any{fieldDetail: notFound})
	case path == "/crypto/certificatekeypairs/":
		writeJSON(w, 200, page(filter(s.certs, fieldName, q.Get(fieldName))))
	case strings.HasPrefix(path, "/core/applications/"):
		s.handleApplications(w, r, path, body)
	case strings.HasPrefix(path, "/providers/oauth2/"):
		s.handleProviders(w, r, path, body)
	default:
		writeJSON(w, 404, map[string]any{fieldDetail: notFound})
	}
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	q := r.URL.Query()
	switch {
	case path == "/providers/oauth2/" && r.Method == http.MethodGet:
		all := make([]map[string]any, 0, len(s.providers))
		for _, p := range s.providers {
			all = append(all, p)
		}
		writeJSON(w, 200, page(filter(all, fieldName, q.Get(fieldName))))
	case path == "/providers/oauth2/" && r.Method == http.MethodPost:
		s.createProvider(w, body)
	case strings.HasPrefix(path, "/providers/oauth2/"):
		pk, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(path, "/providers/oauth2/"), "/"))
		p, ok := s.providers[int32(pk)]
		if err != nil || !ok {
			writeJSON(w, 404, map[string]any{fieldDetail: notFound})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, p)
		case http.MethodPatch:
			if errs := s.validate(body, int32(pk)); len(errs) > 0 {
				writeJSON(w, 400, errs)
				return
			}
			maps.Copy(p, body)
			writeJSON(w, 200, p)
		case http.MethodDelete:
			s.deleteProviderLocked(int32(pk))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		writeJSON(w, 404, map[string]any{fieldDetail: notFound})
	}
}

func (s *Server) createProvider(w http.ResponseWriter, body map[string]any) {
	missing := map[string]any{}
	for _, f := range []string{fieldName, fieldAuthz, fieldInval, "redirect_uris"} {
		if _, ok := body[f]; !ok {
			missing[f] = []string{"This field is required."}
		}
	}
	if len(missing) > 0 {
		writeJSON(w, 400, missing)
		return
	}
	if errs := s.validate(body, 0); len(errs) > 0 {
		writeJSON(w, 400, errs)
		return
	}
	pk := s.nextPK
	s.nextPK++
	p := s.completeProvider(pk, body)
	s.providers[pk] = p
	if s.loseNextCreate {
		s.loseNextCreate = false
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{fieldDetail: "upstream timed out"})
		return
	}
	writeJSON(w, 201, p)
}

// validate mimics the server-side checks the operator depends on: unique
// name and client_id, and references to objects that exist.
func (s *Server) validate(body map[string]any, self int32) map[string]any {
	errs := map[string]any{}
	if name, ok := body[fieldName].(string); ok && s.nameTaken(name, self) {
		errs[fieldName] = []string{"provider with this name already exists."}
	}
	if id, ok := body["client_id"].(string); ok {
		for pk, p := range s.providers {
			if pk != self && p["client_id"] == id {
				errs["client_id"] = []string{"provider with this client id already exists."}
			}
		}
	}
	exists := func(items []map[string]any, pk any) bool {
		for _, it := range items {
			if it["pk"] == pk {
				return true
			}
		}
		return false
	}
	for _, f := range []string{fieldAuthz, fieldInval, "authentication_flow"} {
		if v, ok := body[f]; ok && v != nil && !exists(s.flows, v) {
			errs[f] = []string{fmt.Sprintf("Invalid pk %q - object does not exist.", v)}
		}
	}
	for _, f := range []string{"signing_key", "encryption_key"} {
		if v, ok := body[f]; ok && v != nil && !exists(s.certs, v) {
			errs[f] = []string{fmt.Sprintf("Invalid pk %q - object does not exist.", v)}
		}
	}
	if ms, ok := body["property_mappings"].([]any); ok {
		for _, m := range ms {
			if !exists(s.scopes, m) && !exists(s.mappings, m) {
				errs["property_mappings"] = []string{fmt.Sprintf("Invalid pk %q - object does not exist.", m)}
			}
		}
	}
	return errs
}

func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	q := r.URL.Query()
	switch {
	case path == "/core/applications/" && r.Method == http.MethodGet:
		if q.Get("superuser_full_list") != "true" {
			// Real Authentik filters by the token user's policy access
			// without this flag; model that as "sees nothing".
			writeJSON(w, 200, page(nil))
			return
		}
		all := make([]map[string]any, 0, len(s.apps))
		for _, a := range s.apps {
			all = append(all, a)
		}
		writeJSON(w, 200, page(filter(all, fieldSlug, q.Get(fieldSlug))))
	case path == "/core/applications/" && r.Method == http.MethodPost:
		s.createApplication(w, body)
	case strings.HasPrefix(path, "/core/applications/"):
		slug := strings.Trim(strings.TrimPrefix(path, "/core/applications/"), "/")
		a, ok := s.apps[slug]
		if !ok {
			writeJSON(w, 404, map[string]any{fieldDetail: notFound})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, a)
		case http.MethodPatch:
			if errs := s.validateApplication(body, slug); len(errs) > 0 {
				writeJSON(w, 400, errs)
				return
			}
			maps.Copy(a, body)
			if newSlug, _ := a[fieldSlug].(string); newSlug != slug {
				delete(s.apps, slug)
				s.apps[newSlug] = a
			}
			writeJSON(w, 200, a)
		case http.MethodDelete:
			delete(s.apps, slug)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		writeJSON(w, 404, map[string]any{fieldDetail: notFound})
	}
}

func (s *Server) createApplication(w http.ResponseWriter, body map[string]any) {
	missing := map[string]any{}
	for _, f := range []string{fieldName, "slug"} {
		if _, ok := body[f]; !ok {
			missing[f] = []string{"This field is required."}
		}
	}
	if len(missing) > 0 {
		writeJSON(w, 400, missing)
		return
	}
	if errs := s.validateApplication(body, ""); len(errs) > 0 {
		writeJSON(w, 400, errs)
		return
	}
	a := s.completeApplication(body)
	s.apps[a[fieldSlug].(string)] = a
	if s.loseNextAppCreate {
		s.loseNextAppCreate = false
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{fieldDetail: "upstream timed out"})
		return
	}
	writeJSON(w, 201, a)
}

// validateApplication mimics Authentik: unique slug, provider must exist
// and may belong to at most one application (OneToOne).
func (s *Server) validateApplication(body map[string]any, self string) map[string]any {
	errs := map[string]any{}
	if name, ok := body[fieldName].(string); ok && name == "" {
		errs[fieldName] = []string{"This field may not be blank."}
	}
	if slug, ok := body[fieldSlug].(string); ok && slug != self {
		if _, taken := s.apps[slug]; taken {
			errs[fieldSlug] = []string{"application with this slug already exists."}
		}
	}
	if v, ok := body["provider"]; ok && v != nil {
		pk, _ := v.(float64)
		if _, exists := s.providers[int32(pk)]; !exists {
			errs["provider"] = []string{fmt.Sprintf("Invalid pk %q - object does not exist.", fmt.Sprint(v))}
		}
		for slug, a := range s.apps {
			if slug != self && a["provider"] == v {
				errs["provider"] = []string{"application with this provider already exists."}
			}
		}
	}
	return errs
}

func (s *Server) completeApplication(in map[string]any) map[string]any {
	s.nextApp++
	a := map[string]any{
		"group": "", "policy_engine_mode": "any", "meta_launch_url": "", "meta_description": "",
		"meta_publisher": "", "meta_icon": "", "open_in_new_tab": false, "meta_hide": false,
		"provider": nil, "backchannel_providers": []any{},
	}
	maps.Copy(a, in)
	a["pk"] = fmt.Sprintf("00000000-0000-0000-0000-%012d", s.nextApp)
	a["pbm_uuid"] = a["pk"]
	a["provider_obj"] = nil
	a["backchannel_providers_obj"] = []any{}
	a["launch_url"] = nil
	a["meta_icon_url"] = nil
	a["meta_icon_themed_urls"] = nil
	return a
}

func (s *Server) nameTaken(name string, except int32) bool {
	for pk, p := range s.providers {
		if pk != except && p[fieldName] == name {
			return true
		}
	}
	return false
}

// completeProvider fills server-side defaults and read-only fields.
func (s *Server) completeProvider(pk int32, in map[string]any) map[string]any {
	p := map[string]any{
		"client_type": "confidential", "grant_types": []any{}, "access_code_validity": "minutes=1",
		"access_token_validity": "minutes=5", "refresh_token_validity": "days=30",
		"refresh_token_threshold": "seconds=0", "include_claims_in_id_token": true, "sub_mode": "hashed_user_id",
		"issuer_mode": "per_provider", "logout_uri": "", "logout_method": "backchannel",
		"property_mappings": []any{}, "signing_key": nil, "encryption_key": nil, "authentication_flow": nil,
		"jwt_federation_sources": []any{}, "jwt_federation_providers": []any{},
		"client_id":     fmt.Sprintf("generated-client-%d", pk),
		"client_secret": fmt.Sprintf("generated-secret-%d", pk),
	}
	maps.Copy(p, in)
	p["pk"] = pk
	p["component"] = "ak-provider-oauth2-form"
	p["assigned_application_slug"] = nil
	p["assigned_application_name"] = nil
	p["assigned_backchannel_application_slug"] = nil
	p["assigned_backchannel_application_name"] = nil
	p["verbose_name"] = "OAuth2/OpenID Provider"
	p["verbose_name_plural"] = "OAuth2/OpenID Providers"
	p["meta_model_name"] = "authentik_providers_oauth2.oauth2provider"
	return p
}

func flow(pk, slug string) map[string]any {
	return map[string]any{
		"pk": pk, "policybindingmodel_ptr_id": pk, fieldName: slug, fieldSlug: slug, "title": slug,
		"designation": "authorization", "background_url": "", "background_themed_urls": nil,
		"stages": []string{}, "policies": []string{}, "cache_count": 0, "export_url": "",
	}
}

func scope(pk, scopeName string) map[string]any {
	return map[string]any{
		"pk": pk, fieldName: "authentik default OAuth Mapping: OpenID '" + scopeName + "'", "expression": "return {}",
		"component": "ak-property-mapping-provider-scope-form", "verbose_name": "Scope Mapping",
		"verbose_name_plural": "Scope Mappings", "meta_model_name": "authentik_providers_oauth2.scopemapping",
		"scope_name": scopeName, "managed": "goauthentik.io/providers/oauth2/scope-" + scopeName,
	}
}

func mapping(pk, name string) map[string]any {
	return map[string]any{
		"pk": pk, fieldName: name, "expression": "return {}", "component": "ak-property-mapping-provider-scope-form",
		"verbose_name": "Scope Mapping", "verbose_name_plural": "Scope Mappings",
		"meta_model_name": "authentik_providers_oauth2.scopemapping",
	}
}

func cert(pk, name string) map[string]any {
	return map[string]any{
		"pk": pk, fieldName: name, "fingerprint_sha256": "x", "fingerprint_sha1": "x", "cert_expiry": nil,
		"cert_subject": "CN=" + name, "private_key_available": true, "key_type": "rsa",
		"certificate_download_url": "", "private_key_download_url": "", "managed": nil,
	}
}

func filter(items []map[string]any, field, value string) []map[string]any {
	if value == "" {
		return items
	}
	var out []map[string]any
	for _, it := range items {
		if it[field] == value {
			out = append(out, it)
		}
	}
	return out
}

func page(results []map[string]any) map[string]any {
	if results == nil {
		results = []map[string]any{}
	}
	return map[string]any{
		"pagination": map[string]any{"next": 0, "previous": 0, "count": len(results), "current": 1,
			"total_pages": 1, "start_index": 1, "end_index": len(results)},
		"results":      results,
		"autocomplete": map[string]any{},
	}
}

func clone(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
