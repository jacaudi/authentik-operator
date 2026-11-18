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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// maxCompatBody bounds how much of a response compatTransport will rewrite.
const maxCompatBody = 16 << 20

// compatTransport lets the generated client (which tracks Authentik 2026.8
// and treats every property the schema marks required as mandatory) decode
// responses from an older supported release. A property that release does
// not send yet is added as null (a no-op for the non-pointer string PbmUuid, a null for the
// NullableString DcrRegistration); nothing
// present is ever changed. The first two were found by the end-to-end suite
// against 2026.2.3, the last two by decoding recorded 2026.2.3 responses
// (record_test.go):
//
//   - Application.pbm_uuid (new after 2026.2; the operator does not use it)
//   - OAuth2ProviderSetupURLs.dcr_registration (new after 2026.2; unused)
//   - PolicyBinding.expires and expiring (added as null, which decodes to the zero value; the operator reads neither)
//   - /core/users/me/ top-level users (an array holding the same user) and
//     user.is_current (true: the token's own user is the current one); the
//     operator needs both to decode the token owner (Ping, Me)
//
// Add a rule here only for a property the pinned client requires and an older
// supported release lacks, with a recorded response in testdata/ proving it.
type compatTransport struct {
	next http.RoundTripper
}

func (t *compatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 || resp.Body == nil {
		return resp, err
	}
	rule := compatRule(req.URL.Path)
	if rule == nil || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return resp, nil
	}
	orig := resp.Body
	raw, err := io.ReadAll(io.LimitReader(orig, maxCompatBody+1))
	if err != nil {
		return nil, errors.Join(err, orig.Close())
	}
	if len(raw) > maxCompatBody {
		// Too large to rewrite: hand the decoder everything, untouched.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(raw), orig), orig}
		return resp, nil
	}
	if err := orig.Close(); err != nil {
		return nil, err
	}
	out := raw
	if !rule.satisfied(raw) {
		if patched, ok := patchJSON(raw, rule.fill); ok {
			out = patched
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return resp, nil
}

// compat describes one rule: the properties it may add and how.
type compat struct {
	keys []string
	fill func(map[string]any)
}

// satisfied reports whether every property the rule adds already appears in
// raw (a cheap byte check; a false positive only skips a needless rewrite of a
// body that then fails to decode exactly as it would have without this shim).
func (c *compat) satisfied(raw []byte) bool {
	for _, k := range c.keys {
		if !bytes.Contains(raw, []byte(`"`+k+`"`)) {
			return false
		}
	}
	return true
}

// compatRule returns the rule for the request path, or nil for paths with none.
func compatRule(path string) *compat {
	switch {
	case strings.Contains(path, "/core/applications/"):
		return &compat{keys: []string{"pbm_uuid"}, fill: func(o map[string]any) { addMissing(o, "slug", "pbm_uuid") }}
	case strings.HasSuffix(path, "/setup_urls/"):
		return &compat{keys: []string{"dcr_registration"},
			fill: func(o map[string]any) { addMissing(o, "issuer", "dcr_registration") }}
	case strings.Contains(path, "/policies/bindings/"):
		return &compat{keys: []string{"expires", "expiring"},
			fill: func(o map[string]any) { addMissing(o, "target", "expires"); addMissing(o, "target", "expiring") }}
	case strings.HasSuffix(path, "/core/users/me/"):
		return &compat{keys: []string{"users", "is_current"}, fill: fillMe}
	}
	return nil
}

// fillMe adds what 2026.8 sends and 2026.2.3 does not to a /core/users/me/ body: user.is_current (true) and the
// top-level users array, which holds the very same user map so users[0] carries is_current too.
func fillMe(doc map[string]any) {
	user, ok := doc["user"].(map[string]any)
	if !ok {
		return
	}
	if _, ok := user["is_current"]; !ok {
		user["is_current"] = true
	}
	if _, ok := doc["users"]; !ok {
		doc["users"] = []any{user}
	}
}

// addMissing sets o[prop] to null when o looks like the object type (it has
// the sentinel key) and lacks prop.
func addMissing(o map[string]any, sentinel, prop string) {
	if _, ok := o[sentinel]; !ok {
		return
	}
	if _, ok := o[prop]; !ok {
		o[prop] = nil
	}
}

// patchJSON applies fill to a JSON object, or to every object in its
// "results" array (a paginated list). ok is false when raw is not JSON of
// that shape; the caller then passes the body through untouched.
func patchJSON(raw []byte, fill func(map[string]any)) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers byte-exact through the round trip
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	if results, ok := doc["results"].([]any); ok {
		for _, r := range results {
			if o, ok := r.(map[string]any); ok {
				fill(o)
			}
		}
	} else {
		fill(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return out, true
}
