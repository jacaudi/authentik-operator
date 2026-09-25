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

// Package authentik wraps the official generated Authentik API client
// (goauthentik.io/api/v3) with the transport behaviour every controller needs:
// bearer auth, client-side rate limiting, retry with backoff, optional custom
// CA / TLS settings, typed errors, pagination and stable-identifier lookups.
//
// The pinned goauthentik.io/api/v3 version tracks the Authentik release the
// operator is tested against (v3.2026080.x = Authentik 2026.8.x).
package authentik

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	api "goauthentik.io/api/v3"
	"golang.org/x/time/rate"
)

const (
	// DefaultRPS matches the client-side rate limit of the operator this one replaces.
	DefaultRPS = 10
	// DefaultBurst is the token-bucket burst size.
	DefaultBurst = 20
	// DefaultMaxRetries is the number of retries after the first attempt.
	DefaultMaxRetries = 3
	// DefaultTimeout bounds a single HTTP attempt.
	DefaultTimeout = 30 * time.Second

	apiPath = "/api/v3"
)

// Config describes how to reach one Authentik instance.
type Config struct {
	// URL is the base URL of Authentik, e.g. https://authentik.example.com.
	// The /api/v3 suffix is added automatically and may be omitted.
	URL string
	// Token is an Authentik API token, sent as a Bearer token. It must never
	// be logged or written to a resource's spec or status.
	Token string

	// CABundle is an optional PEM bundle trusted in addition to the system roots.
	CABundle []byte
	// InsecureSkipVerify disables TLS certificate verification.
	InsecureSkipVerify bool

	// RPS and Burst configure the client-side rate limiter. Zero means default;
	// a negative RPS disables limiting.
	RPS   float64
	Burst int
	// MaxRetries is the number of retries on 429/5xx/transport errors.
	// Zero means default; a negative value disables retries.
	MaxRetries int
	// Timeout bounds a single HTTP attempt. Zero means default.
	Timeout time.Duration
	// UserAgent overrides the default User-Agent.
	UserAgent string

	// baseBackoff overrides the retry backoff base; tests only.
	baseBackoff time.Duration
}

// Client is a configured Authentik API client.
type Client struct {
	api     *api.APIClient
	baseURL string
}

// New validates cfg and builds a Client.
func New(cfg Config) (*Client, error) {
	base, err := normalizeURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errors.New("authentik: token is required")
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit opt-in only
	}
	if len(cfg.CABundle) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(cfg.CABundle) {
			return nil, errors.New("authentik: CA bundle contains no valid PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}

	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.TLSClientConfig = tlsCfg

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	var rt http.RoundTripper = baseTransport
	if limiter := newLimiter(cfg.RPS, cfg.Burst); limiter != nil {
		rt = &rateLimitTransport{next: rt, limiter: limiter}
	}
	rt = &retryTransport{
		next:        rt,
		maxRetries:  retriesOrDefault(cfg.MaxRetries),
		baseBackoff: cfg.baseBackoff,
		timeout:     timeout,
	}
	rt = &authTransport{next: rt, token: cfg.Token}

	apiCfg := api.NewConfiguration()
	apiCfg.Servers = api.ServerConfigurations{{URL: base + apiPath}}
	apiCfg.HTTPClient = &http.Client{Transport: rt}
	apiCfg.UserAgent = "authentik-operator"
	if cfg.UserAgent != "" {
		apiCfg.UserAgent = cfg.UserAgent
	}

	return &Client{api: api.NewAPIClient(apiCfg), baseURL: base}, nil
}

// API exposes the underlying generated client. Callers must pass returned
// errors through Check so that 404s become ErrNotFound.
func (c *Client) API() *api.APIClient { return c.api }

// BaseURL returns the normalized Authentik base URL (without /api/v3).
func (c *Client) BaseURL() string { return c.baseURL }

// Ping verifies that Authentik is reachable and the token is accepted by
// fetching the token owner. It returns the owner's username.
func (c *Client) Ping(ctx context.Context) (string, error) {
	me, resp, err := c.api.CoreAPI.CoreUsersMeRetrieve(ctx).Execute()
	if err := Check(resp, err); err != nil {
		return "", fmt.Errorf("authentik: ping: %w", err)
	}
	return me.User.Username, nil
}

func normalizeURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("authentik: URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("authentik: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("authentik: URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("authentik: URL has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("authentik: URL must not contain userinfo, query or fragment")
	}
	p := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), apiPath)
	return u.Scheme + "://" + u.Host + strings.TrimRight(p, "/"), nil
}

func newLimiter(rps float64, burst int) *rate.Limiter {
	if rps < 0 {
		return nil
	}
	if rps == 0 {
		rps = DefaultRPS
	}
	if burst <= 0 {
		burst = DefaultBurst
	}
	return rate.NewLimiter(rate.Limit(rps), burst)
}

func retriesOrDefault(n int) int {
	switch {
	case n < 0:
		return 0
	case n == 0:
		return DefaultMaxRetries
	default:
		return n
	}
}
