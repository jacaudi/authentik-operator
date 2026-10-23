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
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseBackoff = 250 * time.Millisecond
	maxBackoff         = 10 * time.Second
)

// authTransport adds the bearer token to every request.
type authTransport struct {
	next  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(r)
}

// rateLimitTransport blocks each attempt on a shared token bucket.
type rateLimitTransport struct {
	next    http.RoundTripper
	limiter *rate.Limiter
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// retryTransport retries 429 for any method (the server rejected the request
// before acting on it) and 5xx / transport errors for idempotent methods only,
// so a POST that may have been applied is never replayed.
type retryTransport struct {
	next        http.RoundTripper
	maxRetries  int
	baseBackoff time.Duration
	timeout     time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 {
			var err error
			if r, err = rewind(req); err != nil {
				return nil, err
			}
		}

		resp, err := t.attempt(r)
		if attempt >= t.maxRetries || !t.shouldRetry(req.Method, resp, err) {
			return resp, err
		}

		wait := t.backoff(attempt, resp)
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

// attempt runs one request under the per-attempt timeout. The timeout is
// cancelled only once the response body is closed.
func (t *retryTransport) attempt(req *http.Request) (*http.Response, error) {
	if t.timeout <= 0 {
		return t.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), t.timeout)
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func (t *retryTransport) shouldRetry(method string, resp *http.Response, err error) bool {
	if err != nil {
		return isIdempotent(method)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true
	case resp.StatusCode >= 500:
		return isIdempotent(method)
	default:
		return false
	}
}

func (t *retryTransport) backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
			return min(time.Duration(s)*time.Second, maxBackoff)
		}
	}
	base := t.baseBackoff
	if base <= 0 {
		base = defaultBaseBackoff
	}
	d := maxBackoff
	if attempt < 16 {
		d = min(base<<attempt, maxBackoff)
	}
	// Full jitter in [d/2, d).
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func rewind(req *http.Request) (*http.Request, error) {
	r := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return r, nil
	}
	if req.GetBody == nil {
		return nil, errNoRewind
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	r.Body = body
	return r, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
