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
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Enrol completes the enrolment flow behind a sign-up link
// (<base>/if/flow/<slug>/?itoken=<pk>) the way a browser would, through the
// flow executor API, and returns once the flow redirects. Each prompt is
// answered with its fields' initial values: the invitation pins the username
// and the prompt is read-only, so that is the whole answer. Any other stage,
// and an access-denied challenge, is an error naming it.
func Enrol(ctx context.Context, rt http.RoundTripper, inviteURL string) error {
	link, err := url.Parse(inviteURL)
	if err != nil {
		return fmt.Errorf("invitation link: %w", err)
	}
	m := flowPath.FindStringSubmatch(link.Path)
	if m == nil {
		return fmt.Errorf("invitation link path %q is not /if/flow/<slug>/", link.Path)
	}
	slug, token := m[1], link.Query().Get("itoken")
	if token == "" {
		return errors.New("invitation link has no itoken query")
	}
	hc, err := newWalkerClient(rt)
	if err != nil {
		return err
	}
	l := &login{hc: hc}

	exec := *link
	exec.Path = "/api/v3/flows/executor/" + slug + "/"
	exec.RawQuery = url.Values{"query": {url.Values{"itoken": {token}}.Encode()}}.Encode()
	exec.Fragment = ""

	ch, err := l.executor(ctx, http.MethodGet, exec.String(), nil)
	if err != nil {
		return err
	}
	for range maxHops {
		comp := str(ch[kComponent])
		switch comp {
		case compRedirect:
			return nil
		case compPrompt:
			ch, err = l.executor(ctx, http.MethodPost, exec.String(), promptAnswer(ch))
		case compDenied:
			return fmt.Errorf("flow %s: access denied: %s", slug, str(ch["error_message"]))
		default:
			return fmt.Errorf("flow %s: unsupported stage %q: %s", slug, comp, snippet(mustJSON(ch)))
		}
		if err != nil {
			return err
		}
		if err := answerErrors(slug, comp, ch); err != nil {
			return err
		}
	}
	return fmt.Errorf("flow %s did not finish within %d challenges", slug, maxHops)
}

// promptAnswer answers a prompt challenge with each field's initial value,
// keyed by field_key.
func promptAnswer(ch map[string]any) map[string]any {
	body := map[string]any{kComponent: compPrompt}
	fields, _ := ch["fields"].([]any)
	for _, f := range fields {
		fm, _ := f.(map[string]any)
		if k := str(fm["field_key"]); k != "" {
			body[k] = fm["initial_value"]
		}
	}
	return body
}
