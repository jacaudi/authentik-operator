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
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "goauthentik.io/api/v3"
)

const testToken = "s3cr3t-token"

func newTestClient(t *testing.T, h http.HandlerFunc, mutate ...func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL, Token: testToken, RPS: -1, baseBackoff: time.Millisecond}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func page(results []map[string]any, current, next, total int) map[string]any {
	return map[string]any{
		"pagination": map[string]any{
			"next": next, "previous": max(current-1, 0), "count": total, "current": current,
			"total_pages": 0, "start_index": 0, "end_index": 0,
		},
		"results":      results,
		"autocomplete": map[string]any{},
	}
}

func flow(slug string) map[string]any {
	return map[string]any{
		"pk": "00000000-0000-0000-0000-000000000001", "policybindingmodel_ptr_id": "x", "name": slug,
		"slug": slug, "title": slug, "designation": "authorization", "background_url": "",
		"background_themed_urls": nil,
		"stages":                 []string{}, "policies": []string{}, "cache_count": 0, "export_url": "",
		"meta_model_name": "authentik_flows.flow",
	}
}

func TestNormalizeURL(t *testing.T) {
	const (
		base    = "https://ak.example.com"
		subpath = "http://ak.svc:9000/authentik"
	)
	cases := map[string]string{
		base:                base,
		base + "/":          base,
		base + "/api/v3":    base,
		base + "/api/v3/":   base,
		subpath:             subpath,
		subpath + "/api/v3": subpath,
	}
	for in, want := range cases {
		got, err := normalizeURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ak.example.com", "ftp://ak", "https://", "https://u:p@ak", "https://ak?x=1"} {
		if _, err := normalizeURL(bad); err == nil {
			t.Errorf("normalizeURL(%q) succeeded, want error", bad)
		}
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Config{URL: "https://authentik.example.com"}); err == nil {
		t.Fatal("expected error without token")
	}
}

func TestPingSendsBearerToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/core/users/me/" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "authentik-operator" {
			t.Errorf("User-Agent = %q", got)
		}
		user := map[string]any{
			"pk": 1, "username": "akadmin", "name": "admin", "is_active": true, "is_superuser": true,
			"is_current": true, "groups": []any{}, "roles": []any{}, "avatar": "", "uid": "u",
			"settings": map[string]any{}, "system_permissions": []string{},
		}
		writeJSON(t, w, 200, map[string]any{"user": user, "users": []any{user}})
	})
	user, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if user != "akadmin" {
		t.Errorf("user = %q", user)
	}
}

func TestListAllFollowsPages(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if got := r.URL.Query().Get("page_size"); got != strconv.Itoa(PageSize) {
			t.Errorf("page_size = %q", got)
		}
		next := p + 1
		if p == 3 {
			next = 0
		}
		writeJSON(t, w, 200, page([]map[string]any{flow(fmt.Sprintf("f%d", p))}, p, next, 3))
	})
	flows, err := ListAll(func(p int32) ([]api.Flow, api.Pagination, *http.Response, error) {
		res, resp, err := c.API().FlowsAPI.FlowsInstancesList(context.Background()).Page(p).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(flows) != 3 || flows[2].Slug != "f3" || calls.Load() != 3 {
		t.Fatalf("got %d flows over %d calls", len(flows), calls.Load())
	}
}

func TestErrorClassification(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/propertymappings/all/missing/":
			writeJSON(t, w, 404, map[string]any{"detail": "No PropertyMapping matches the given query."})
		default:
			writeJSON(t, w, 400, map[string]any{"slug": []string{"flow with this slug already exists."}})
		}
	})
	ctx := context.Background()

	_, err := c.PropertyMappingByUUID(ctx, "missing")
	if !IsNotFound(err) || StatusCode(err) != 404 {
		t.Fatalf("404: IsNotFound=%v status=%d err=%v", IsNotFound(err), StatusCode(err), err)
	}

	_, err = c.PropertyMappingByUUID(ctx, "bad")
	if IsNotFound(err) {
		t.Fatalf("400 must not be NotFound: %v", err)
	}
	if !IsConflict(err) || StatusCode(err) != 400 || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("400: conflict=%v err=%v", IsConflict(err), err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("error leaks token")
	}
}

func TestRetry(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		status    int
		failures  int32
		wantCalls int32
		wantOK    bool
	}{
		{"GET retried on 503", http.MethodGet, 503, 2, 3, true},
		{"GET gives up after max retries", http.MethodGet, 503, 10, 4, false},
		{"POST not retried on 503", http.MethodPost, 503, 1, 1, false},
		{"POST retried on 429", http.MethodPost, 429, 2, 3, true},
		{"GET not retried on 400", http.MethodGet, 400, 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method == http.MethodPost && !strings.Contains(string(body), "replayed") {
					t.Errorf("attempt %d: body not replayed: %q", n, body)
				}
				if n <= tc.failures {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(200)
			})
			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(`{"name":"replayed"}`)
			}
			req, _ := http.NewRequestWithContext(context.Background(), tc.method, c.BaseURL()+"/api/v3/x/", body)
			resp, err := c.API().GetConfig().HTTPClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()
			if got := resp.StatusCode == 200; got != tc.wantOK {
				t.Errorf("status %d, wantOK %v", resp.StatusCode, tc.wantOK)
			}
			if calls.Load() != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestRetryHonoursContext(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(503)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.PropertyMappingByUUID(ctx, "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("retry ignored context cancellation")
	}
}

func TestFlowBySlug(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("slug") {
		case "login":
			// Server returned a near match too; only the exact slug counts.
			writeJSON(t, w, 200, page([]map[string]any{flow("login-extra"), flow("login")}, 1, 0, 2))
		case "dup":
			writeJSON(t, w, 200, page([]map[string]any{flow("dup"), flow("dup")}, 1, 0, 2))
		default:
			writeJSON(t, w, 200, page(nil, 1, 0, 0))
		}
	})
	ctx := context.Background()
	f, err := c.FlowBySlug(ctx, "login")
	if err != nil || f.Slug != "login" {
		t.Fatalf("FlowBySlug(login) = %v, %v", f, err)
	}
	if _, err := c.FlowBySlug(ctx, "nope"); !IsNotFound(err) {
		t.Fatalf("FlowBySlug(nope) err = %v", err)
	}
	if _, err := c.FlowBySlug(ctx, "dup"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("FlowBySlug(dup) err = %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) },
		func(cfg *Config) { cfg.RPS = 20; cfg.burst = 1 })
	start := time.Now()
	for range 5 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.BaseURL()+"/api/v3/x/", nil)
		resp, err := c.API().GetConfig().HTTPClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	// 5 requests, burst 1, 20 rps => at least ~200ms.
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("5 requests took %v; limiter not applied", el)
	}
}

func TestRedirectsNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(other.Close)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	})
	_, err := c.PropertyMappingByUUID(context.Background(), "x")
	if leaked.Load() {
		t.Fatal("token was sent to the redirect target")
	}
	if StatusCode(err) != http.StatusFound {
		t.Fatalf("err = %v, want 302 APIError", err)
	}
}

func TestErrorBodyOnlyFor400(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("internal secret page"))
	})
	_, err := c.PropertyMappingByUUID(context.Background(), "x")
	if StatusCode(err) != 403 || strings.Contains(err.Error(), "secret page") {
		t.Fatalf("403 body surfaced in error: %v", err)
	}
}

func TestConflictVsValidation(t *testing.T) {
	dup := &APIError{StatusCode: 400, Body: `{"slug":["flow with this slug already exists."]}`}
	bad := &APIError{StatusCode: 400, Body: `{"name":["This field is required."]}`}
	if !IsConflict(dup) || IsValidation(dup) {
		t.Error("duplicate slug not classified as conflict")
	}
	if IsConflict(bad) || !IsValidation(bad) {
		t.Error("plain validation error classified as conflict")
	}
	if !IsConflict(&APIError{StatusCode: 409}) {
		t.Error("409 not a conflict")
	}
}

func TestConfigRedactsToken(t *testing.T) {
	cfg := Config{URL: "https://ak", Token: testToken}
	for _, s := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), fmt.Sprint(&cfg)} {
		if strings.Contains(s, testToken) {
			t.Fatalf("token leaked: %s", s)
		}
	}
}

func TestBackoffLargeAttemptDoesNotPanic(t *testing.T) {
	rt := &retryTransport{}
	for _, a := range []int{0, 5, 35, 64, 1000} {
		if d := rt.backoff(a, nil); d <= 0 || d > maxBackoff {
			t.Fatalf("attempt %d: backoff %v", a, d)
		}
	}
}

func TestListAllStopsOnNonAdvancingNext(t *testing.T) {
	calls := 0
	items, err := ListAll(func(page int32) ([]int, api.Pagination, *http.Response, error) {
		calls++
		return []int{int(page)}, api.Pagination{Next: float32(page)}, nil, nil
	})
	if err != nil || calls != 1 || len(items) != 1 {
		t.Fatalf("calls=%d items=%v err=%v", calls, items, err)
	}
}

func TestTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	t.Cleanup(srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	ctx := context.Background()

	untrusted, err := New(Config{URL: srv.URL, Token: testToken, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.PropertyMappingByUUID(ctx, "x"); err == nil || StatusCode(err) != 0 {
		t.Fatalf("expected TLS verification failure, got %v", err)
	}

	for name, cfg := range map[string]Config{
		"ca bundle": {URL: srv.URL, Token: testToken, CABundle: caPEM},
		"insecure":  {URL: srv.URL, Token: testToken, InsecureSkipVerify: true},
	} {
		c, err := New(cfg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := c.PropertyMappingByUUID(ctx, "x"); !IsNotFound(err) {
			t.Fatalf("%s: expected 404 over verified TLS, got %v", name, err)
		}
	}

	if _, err := New(Config{URL: srv.URL, Token: testToken, CABundle: []byte("not pem")}); err == nil {
		t.Fatal("expected error for invalid CA bundle")
	}
}
