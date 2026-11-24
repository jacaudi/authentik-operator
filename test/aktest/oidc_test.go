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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	fakeClient   = "cid"
	fakeSecret   = "csecret"
	fakeRedirect = "https://app.invalid/callback"
	stageIdent   = "ak-stage-identification"
	stagePass    = "ak-stage-password"
	fieldComp    = "component"
	fakeUser     = "akadmin"
	fState       = "state"
	fAud         = "aud"
	fNonce       = "nonce"
)

// fakeAuthentik reproduces just the shape of Authentik's login: the authorize
// endpoint bounces to a flow page until a session exists, the flow executor
// walks identification then password and answers with a redirect challenge,
// and the token endpoint checks PKCE and the client secret.
type fakeAuthentik struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	authed bool
	state  string
	nonce  string
	chall  string
	issuer string
	// Misbehaviours for the negative tests.
	badState, badNonce, badAud, denied bool
	pwField                            bool // identification stage carries the password field
	// next is the challenge the executor serves on the next GET: real
	// Authentik answers a POST with a 302 to the same URL (post/redirect/get).
	next map[string]any
}

func newFake(t *testing.T) *fakeAuthentik {
	t.Helper()
	f := &fakeAuthentik{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/application/o/authorize/", f.authorize)
	mux.HandleFunc("/api/v3/flows/executor/", f.executor)
	mux.HandleFunc("/application/o/token/", f.token)
	mux.HandleFunc("/application/o/userinfo/", f.userinfo)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL + "/application/o/app/"
	return f
}

func (f *fakeAuthentik) authorize(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.state, f.nonce, f.chall = q.Get(fState), q.Get(fNonce), q.Get("code_challenge")
	if !f.authed {
		next := url.Values{"next": {r.URL.RequestURI()}}.Encode()
		http.Redirect(w, r, "/if/flow/default-authentication-flow/?"+next, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/if/flow/default-provider-authorization-implicit-consent/?"+r.URL.RawQuery, http.StatusFound)
}

func (f *fakeAuthentik) executor(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v3/flows/executor/"), "/")
	reply := func(v map[string]any) { _ = json.NewEncoder(w).Encode(v) }
	if slug == "default-provider-authorization-implicit-consent" {
		if !f.authed {
			http.Error(w, "unauthenticated", http.StatusForbidden)
			return
		}
		st := f.state
		if f.badState {
			st = "forged"
		}
		cb := url.Values{"code": {"the-code"}, fState: {st}}
		if f.denied {
			cb = url.Values{"error": {"access_denied"}, "error_description": {"nope"}, fState: {"forged"}}
		}
		reply(map[string]any{fieldComp: "xak-flow-redirect", "to": fakeRedirect + "?" + cb.Encode()})
		return
	}
	reply = f.prg(w, r, reply)
	if r.Method == http.MethodGet {
		if c := f.next; c != nil {
			f.next = nil // served once, as a flow advances
			reply(c)
			return
		}
		reply(map[string]any{fieldComp: stageIdent, "password_fields": f.pwField})
		return
	}
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	switch in[fieldComp] {
	case stageIdent:
		if in["uid_field"] != fakeUser {
			reply(map[string]any{fieldComp: stageIdent,
				"response_errors": map[string]any{"non_field_errors": []any{"nope"}}})
			return
		}
		if f.pwField {
			f.authed = in["password"] == "pw"
			f.finishAuthn(reply, r)
			return
		}
		reply(map[string]any{fieldComp: stagePass})
	case stagePass:
		f.authed = in["password"] == "pw"
		f.finishAuthn(reply, r)
	}
}

// prg turns replies to a POST into "remember the challenge, redirect to GET".
func (f *fakeAuthentik) prg(w http.ResponseWriter, r *http.Request, direct func(map[string]any)) func(map[string]any) {
	if r.Method != http.MethodPost {
		return direct
	}
	return func(v map[string]any) {
		f.next = v
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
	}
}

func (f *fakeAuthentik) finishAuthn(reply func(map[string]any), r *http.Request) {
	if !f.authed {
		reply(map[string]any{fieldComp: stagePass,
			"response_errors": map[string]any{"password": []any{"invalid"}}})
		return
	}
	next := r.URL.Query().Get("query")
	v, _ := url.ParseQuery(next)
	reply(map[string]any{fieldComp: "xak-flow-redirect", "to": v.Get("next")})
}

func (f *fakeAuthentik) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if r.Form.Get("code") != "the-code" || r.Form.Get("client_secret") != fakeSecret ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != f.chall {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	aud, nonce := fakeClient, f.nonce
	if f.badAud {
		aud = "someone-else"
	}
	if f.badNonce {
		nonce = "replayed"
	}
	claims, _ := json.Marshal(map[string]any{"iss": f.issuer, fAud: aud, "sub": "u1", fNonce: nonce})
	idt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "id_token": idt})
}

func (f *fakeAuthentik) userinfo(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer at" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"sub": "u1", "preferred_username": fakeUser})
}

func (f *fakeAuthentik) params() OIDCParams {
	return OIDCParams{
		Issuer: f.issuer, AuthURL: f.srv.URL + "/application/o/authorize/",
		TokenURL: f.srv.URL + "/application/o/token/", UserInfoURL: f.srv.URL + "/application/o/userinfo/",
		ClientID: fakeClient, ClientSecret: fakeSecret, RedirectURI: fakeRedirect,
		Username: fakeUser, Password: "pw",
	}
}

func TestLoginSeparatePasswordStage(t *testing.T) {
	f := newFake(t)
	res, err := AuthorizationCodeLogin(t.Context(), f.params())
	if err != nil {
		t.Fatal(err)
	}
	if res.UserInfo["preferred_username"] != fakeUser || res.Claims["sub"] != "u1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestLoginPasswordInIdentificationStage(t *testing.T) {
	f := newFake(t)
	f.pwField = true
	if _, err := AuthorizationCodeLogin(t.Context(), f.params()); err != nil {
		t.Fatal(err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	f := newFake(t)
	p := f.params()
	p.Password = "bad"
	_, err := AuthorizationCodeLogin(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "rejected the answer") {
		t.Fatalf("want a rejected-answer error, got %v", err)
	}
}

func TestLoginIssuerMismatch(t *testing.T) {
	f := newFake(t)
	p := f.params()
	p.Issuer = "https://elsewhere.invalid/application/o/app/"
	_, err := AuthorizationCodeLogin(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "does not match the rendered issuer") {
		t.Fatalf("want an issuer mismatch, got %v", err)
	}
}

func TestLoginBadClientSecret(t *testing.T) {
	f := newFake(t)
	p := f.params()
	p.ClientSecret = "wrong"
	_, err := AuthorizationCodeLogin(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "token endpoint: HTTP 400") {
		t.Fatalf("want a token endpoint failure, got %v", err)
	}
}

func TestJWTClaims(t *testing.T) {
	if _, err := JWTClaims("only.two"); err == nil {
		t.Fatal("want an error for a malformed token")
	}
	c, err := JWTClaims("e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"x"}`)) + ".s")
	if err != nil || c["iss"] != "x" {
		t.Fatalf("got %v, %v", c, err)
	}
}

func TestLoginRejectsForgedStateAudienceAndNonce(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*fakeAuthentik)
		want string
	}{
		fState: {func(f *fakeAuthentik) { f.badState = true }, "redirect state"},
		fAud:   {func(f *fakeAuthentik) { f.badAud = true }, fAud},
		fNonce: {func(f *fakeAuthentik) { f.badNonce = true }, fNonce},
		// An error redirect must be reported as the error, not as a state mismatch.
		"denied": {func(f *fakeAuthentik) { f.denied = true }, "access_denied"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			tc.set(f)
			_, err := AuthorizationCodeLogin(t.Context(), f.params())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// Authentik answers a signed-in user a policy refuses with an HTML page and
// status 200, which the walker must tell apart from any other unexpected page.
func TestLoginReportsPermissionDenied(t *testing.T) {
	for name, tc := range map[string]struct {
		body       string
		wantDenied bool
		want       string
	}{
		"permission denied page": {"<html><h1>Permission denied</h1></html>", true, "permission denied"},
		"another 200 page":       {"<html>welcome</html>", false, "expected a redirect"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := OIDCParams{AuthURL: srv.URL + "/application/o/authorize/", RedirectURI: fakeRedirect,
				ClientID: fakeClient, Username: fakeUser, Password: "pw"}
			_, err := AuthorizationCodeLogin(t.Context(), p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
			if got := errors.Is(err, ErrPermissionDenied); got != tc.wantDenied {
				t.Fatalf("errors.Is(err, ErrPermissionDenied) = %t, want %t (%v)", got, tc.wantDenied, err)
			}
		})
	}
}
