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

// Package aktest holds helpers for the end-to-end suite that talk to a real
// Authentik over HTTP: a headless OIDC authorization-code login and a small
// REST helper for creating, mutating and auditing Authentik objects behind
// the operator's back. It has no build tag so its parsing logic is unit
// tested against httptest servers.
package aktest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// OIDCParams configures one headless login.
type OIDCParams struct {
	// Transport carries every request; the suite uses it to dial the
	// in-cluster Authentik URL through a port-forward. Nil means the default.
	Transport http.RoundTripper
	// Issuer, AuthURL, TokenURL and UserInfoURL are the values rendered into
	// the credentials Secret, deliberately not derived from BaseURL.
	Issuer, AuthURL, TokenURL, UserInfoURL string
	ClientID, ClientSecret                 string
	// RedirectURI must be one of the provider's redirect URIs. It is never
	// contacted: the login stops at the 302 to it.
	RedirectURI string
	Scope       string
	Username    string
	Password    string
}

// OIDCResult is what a successful login produced.
type OIDCResult struct {
	AccessToken string
	IDToken     string
	Claims      map[string]any
	UserInfo    map[string]any
}

var flowPath = regexp.MustCompile(`^/if/flow/([^/]+)/?$`)

const (
	maxHops    = 12
	kComponent = "component"
)

// AuthorizationCodeLogin drives Authentik's flow executor with a cookie jar:
// authorize, authenticate as Username, pass the authorization flow, capture
// the code from the redirect to RedirectURI (PKCE S256), exchange it at the
// token endpoint with the client credentials, validate the id_token claims
// (iss, aud, nonce) and call userinfo with the access token.
func AuthorizationCodeLogin(ctx context.Context, p OIDCParams) (*OIDCResult, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{
		Transport: p.Transport,
		Jar:       jar,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	scope := p.Scope
	if scope == "" {
		scope = "openid profile email"
	}
	verifier, state, nonce := randToken(32), randToken(16), randToken(16)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURI},
		"scope":                 {scope},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	start, err := url.Parse(p.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("authorization URL: %w", err)
	}
	start.RawQuery = q.Encode()

	l := &login{hc: hc, p: p}
	cb, err := l.follow(ctx, start)
	if err != nil {
		return nil, err
	}
	if e := cb.Query().Get("error"); e != "" {
		return nil, fmt.Errorf("authorization failed: %s: %s", e, cb.Query().Get("error_description"))
	}
	if got := cb.Query().Get("state"); got != state {
		return nil, fmt.Errorf("redirect state %q, want %q", got, state)
	}
	code := cb.Query().Get("code")
	if code == "" {
		return nil, fmt.Errorf("redirect to %s carries no code", cb.Redacted())
	}

	tok, err := l.exchange(ctx, code, verifier)
	if err != nil {
		return nil, err
	}
	res := &OIDCResult{AccessToken: str(tok["access_token"]), IDToken: str(tok["id_token"])}
	if res.AccessToken == "" || res.IDToken == "" {
		return nil, errors.New("token response lacks access_token or id_token")
	}
	if res.Claims, err = JWTClaims(res.IDToken); err != nil {
		return nil, err
	}
	if err := checkClaims(res.Claims, p, nonce); err != nil {
		return nil, err
	}
	if res.UserInfo, err = l.userinfo(ctx, res.AccessToken); err != nil {
		return nil, err
	}
	return res, nil
}

// checkClaims validates the id_token claims the suite cares about.
func checkClaims(claims map[string]any, p OIDCParams, nonce string) error {
	if got := str(claims["iss"]); got != p.Issuer {
		return fmt.Errorf("id_token iss %q does not match the rendered issuer %q", got, p.Issuer)
	}
	if !audienceHas(claims["aud"], p.ClientID) {
		return fmt.Errorf("id_token aud %v does not contain client %q", claims["aud"], p.ClientID)
	}
	if got := str(claims["nonce"]); got != nonce {
		return fmt.Errorf("id_token nonce %q, want %q", got, nonce)
	}
	return nil
}

type login struct {
	hc *http.Client
	p  OIDCParams
}

// follow GETs u, following redirects by hand: an Authentik flow page is
// answered through the executor API, the redirect_uri ends the walk.
func (l *login) follow(ctx context.Context, u *url.URL) (*url.URL, error) {
	for range maxHops {
		if l.isCallback(u) {
			return u, nil
		}
		if m := flowPath.FindStringSubmatch(u.Path); m != nil {
			to, err := l.runFlow(ctx, u, m[1])
			if err != nil {
				return nil, err
			}
			u = to
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
		if err != nil {
			return nil, err
		}
		resp, err := l.hc.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := readBody(resp, 4096)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			return nil, fmt.Errorf("GET %s: HTTP %d, expected a redirect: %s", u.Redacted(), resp.StatusCode, snippet(body))
		}
		loc, err := resp.Location()
		if err != nil {
			return nil, fmt.Errorf("GET %s: redirect without Location: %w", u.Redacted(), err)
		}
		u = loc
	}
	return nil, fmt.Errorf("no redirect to %s after %d hops (last %s)", l.p.RedirectURI, maxHops, u.Redacted())
}

func (l *login) isCallback(u *url.URL) bool {
	r, err := url.Parse(l.p.RedirectURI)
	return err == nil && u.Scheme == r.Scheme && u.Host == r.Host && u.Path == r.Path
}

// runFlow completes the flow slug (the page at u) and returns where it sends
// the browser. The executor takes the page's query string as ?query=.
func (l *login) runFlow(ctx context.Context, page *url.URL, slug string) (*url.URL, error) {
	exec := *page
	exec.Path = "/api/v3/flows/executor/" + slug + "/"
	exec.RawQuery = url.Values{"query": {page.RawQuery}}.Encode()
	exec.Fragment = ""

	ch, err := l.executor(ctx, http.MethodGet, exec.String(), nil)
	if err != nil {
		return nil, err
	}
	for range maxHops {
		comp := str(ch[kComponent])
		switch comp {
		case "xak-flow-redirect":
			to, err := url.Parse(str(ch["to"]))
			if err != nil || str(ch["to"]) == "" {
				return nil, fmt.Errorf("flow %s: redirect challenge without a usable target", slug)
			}
			return page.ResolveReference(to), nil
		case "ak-stage-identification":
			body := map[string]any{kComponent: comp, "uid_field": l.p.Username}
			if b, _ := ch["password_fields"].(bool); b {
				body["password"] = l.p.Password
			}
			ch, err = l.executor(ctx, http.MethodPost, exec.String(), body)
		case "ak-stage-password":
			ch, err = l.executor(ctx, http.MethodPost, exec.String(),
				map[string]any{kComponent: comp, "password": l.p.Password})
		case "ak-stage-consent":
			ch, err = l.executor(ctx, http.MethodPost, exec.String(),
				map[string]any{kComponent: comp, "token": ch["token"]})
		case "ak-stage-access-denied":
			return nil, fmt.Errorf("flow %s: access denied: %s", slug, str(ch["error_message"]))
		default:
			return nil, fmt.Errorf("flow %s: unsupported stage %q: %s", slug, comp, snippet(mustJSON(ch)))
		}
		if err != nil {
			return nil, err
		}
		if errs, ok := ch["response_errors"].(map[string]any); ok && len(errs) > 0 {
			return nil, fmt.Errorf("flow %s: stage %s rejected the answer: %s", slug, comp, snippet(mustJSON(errs)))
		}
	}
	return nil, fmt.Errorf("flow %s did not finish within %d challenges", slug, maxHops)
}

// executor sends one request to the flow executor and returns the challenge
// it answers with. A POST is answered with a 302 back to the executor URL
// (post/redirect/get): the next challenge is on the GET it names.
func (l *login) executor(ctx context.Context, method, u string, body map[string]any) (map[string]any, error) {
	for range 3 {
		resp, err := l.executorRequest(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			return decodeChallenge(resp, method, u)
		}
		loc, err := resp.Location()
		if cerr := resp.Body.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: redirect without a usable Location: %w", method, u, err)
		}
		method, u, body = http.MethodGet, loc.String(), nil
	}
	return nil, fmt.Errorf("flow executor redirected more than 3 times at %s", u)
}

func (l *login) executorRequest(ctx context.Context, method, u string, body map[string]any) (*http.Response, error) {
	rd := io.Reader(http.NoBody)
	if body != nil {
		rd = strings.NewReader(string(mustJSON(body)))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if pu, perr := url.Parse(u); perr == nil {
		for _, c := range l.hc.Jar.Cookies(pu) {
			if c.Name == "authentik_csrf" {
				req.Header.Set("X-authentik-CSRF", c.Value)
			}
		}
	}
	return l.hc.Do(req)
}

func decodeChallenge(resp *http.Response, method, u string) (map[string]any, error) {
	raw, err := readBody(resp, 1<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, u, resp.StatusCode, snippet(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s %s: not a JSON challenge: %s", method, u, snippet(raw))
	}
	return out, nil
}

func (l *login) exchange(ctx context.Context, code, verifier string) (map[string]any, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {l.p.RedirectURI},
		"client_id":     {l.p.ClientID},
		"client_secret": {l.p.ClientSecret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, err
	}
	raw, err := readBody(resp, 1<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("token endpoint: not JSON: %s", snippet(raw))
	}
	return out, nil
}

func (l *login) userinfo(ctx context.Context, access string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.p.UserInfoURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := l.hc.Do(req)
	if err != nil {
		return nil, err
	}
	raw, err := readBody(resp, 1<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("userinfo: not JSON: %s", snippet(raw))
	}
	return out, nil
}

// JWTClaims decodes the payload of a compact JWT WITHOUT verifying the
// signature: the token came straight from the token endpoint over the
// connection under test, and the suite checks claims, not cryptography.
func JWTClaims(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("id_token has %d segments, want 3", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("id_token payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, fmt.Errorf("id_token payload: %w", err)
	}
	return claims, nil
}

func audienceHas(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if str(a) == want {
				return true
			}
		}
	}
	return false
}

// readBody reads up to limit bytes of resp's body and closes it.
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, errors.Join(err, resp.Body.Close())
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only ever called with maps of plain values
	}
	return b
}

// snippet shortens a response body for an error message.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
