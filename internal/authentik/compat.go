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
// not send yet is added as null where the generated type accepts it; nothing
// present is ever changed. Found by the end-to-end suite against 2026.2.3:
//
//   - Application.pbm_uuid (new after 2026.2; the operator does not use it)
//   - OAuth2ProviderSetupURLs.dcr_registration (new after 2026.2; unused)
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
	fill := compatRule(req.URL.Path)
	if fill == nil || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return resp, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCompatBody+1))
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	out := raw
	if len(raw) <= maxCompatBody {
		if patched, ok := patchJSON(raw, fill); ok {
			out = patched
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return resp, nil
}

// compatRule returns the function that fills missing properties of a
// decoded JSON object for the request path, or nil for paths with no rule.
func compatRule(path string) func(map[string]any) {
	switch {
	case strings.Contains(path, "/core/applications/"):
		return func(o map[string]any) { addMissing(o, "slug", "pbm_uuid") }
	case strings.HasSuffix(path, "/setup_urls/"):
		return func(o map[string]any) { addMissing(o, "issuer", "dcr_registration") }
	}
	return nil
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
