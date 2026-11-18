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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	enrolPromptChallenge = `{"component":"ak-stage-prompt","fields":[{"field_key":"username","initial_value":"dave"}]}`
	enrolDoneChallenge   = `{"component":"xak-flow-redirect","to":"/"}`
	enrolExecutorPath    = "/api/v3/flows/executor/enrol/"
	bindingCall          = "POST /api/v3/flows/bindings/"
)

// fakeEnrolment serves the flow executor the way Authentik does: a GET answers
// the current challenge, a POST answers 302 back to the executor (post/redirect/get)
// and advances to the next one.
type fakeEnrolment struct {
	mu         sync.Mutex
	challenges []string // served in order, one per GET; the last repeats
	posted     []map[string]any
	query      string
}

func (f *fakeEnrolment) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != enrolExecutorPath {
		http.NotFound(w, r)
		return
	}
	f.query = r.URL.Query().Get("query")
	if r.Method == http.MethodPost {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.posted = append(f.posted, in)
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
		return
	}
	c := f.challenges[0]
	if len(f.challenges) > 1 {
		f.challenges = f.challenges[1:]
	}
	_, _ = w.Write([]byte(c))
}

func TestEnrolAnswersThePromptWithItsInitialValues(t *testing.T) {
	f := &fakeEnrolment{challenges: []string{enrolPromptChallenge, enrolDoneChallenge}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	if err := Enrol(t.Context(), nil, srv.URL+"/if/flow/enrol/?itoken=p1"); err != nil {
		t.Fatal(err)
	}
	if f.query != "itoken=p1" {
		t.Errorf("executor query = %q, want itoken=p1", f.query)
	}
	if len(f.posted) != 1 || f.posted[0]["username"] != "dave" || f.posted[0]["component"] != "ak-stage-prompt" {
		t.Fatalf("posted %v, want one ak-stage-prompt answer with username dave", f.posted)
	}
}

func TestEnrolRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		link, challenge, want string
	}{
		"access denied": {"/if/flow/enrol/?itoken=p1", `{"component":"ak-stage-access-denied","error_message":"nope"}`,
			"access denied"},
		"another stage": {"/if/flow/enrol/?itoken=p1", `{"component":"ak-stage-authenticator-webauthn"}`,
			"ak-stage-authenticator-webauthn"},
		"no token":   {"/if/flow/enrol/", enrolDoneChallenge, "itoken"},
		"not a flow": {"/somewhere/else/?itoken=p1", enrolDoneChallenge, "/if/flow/"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(&fakeEnrolment{challenges: []string{tc.challenge}})
			defer srv.Close()
			err := Enrol(t.Context(), nil, srv.URL+tc.link)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

// recorder answers every POST with a pk named after the endpoint and records
// the calls, so the creation order and the bindings' wiring can be asserted.
type recorder struct {
	mu    sync.Mutex
	exist bool
	calls []string
	posts map[string]Obj
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.calls = append(rc.calls, r.Method+" "+r.URL.Path)
	if r.Method == http.MethodGet {
		if rc.exist {
			_, _ = w.Write([]byte(`{"results":[{"pk":"flow-pk"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
		return
	}
	var in Obj
	_ = json.NewDecoder(r.Body).Decode(&in)
	if rc.posts == nil {
		rc.posts = map[string]Obj{}
	}
	rc.posts[r.URL.Path] = in
	pk := strings.ReplaceAll(strings.Trim(r.URL.Path, "/"), "/", "-")
	_, _ = w.Write([]byte(`{"pk":"` + pk + `"}`))
}

func TestEnsureEnrolmentFlowCreatesTheStagesInOrderAndBindsFour(t *testing.T) {
	rc := &recorder{}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	if err := NewAPI(srv.URL, "t", nil).EnsureEnrolmentFlow(t.Context(), "enrol"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v3/flows/instances/",
		"POST /api/v3/flows/instances/",
		"POST /api/v3/stages/invitation/stages/",
		"POST /api/v3/stages/prompt/prompts/",
		"POST /api/v3/stages/prompt/stages/",
		"POST /api/v3/stages/user_write/",
		"POST /api/v3/stages/user_login/",
		bindingCall, bindingCall,
		bindingCall, bindingCall,
	}
	if !slices.Equal(rc.calls, want) {
		t.Fatalf("calls\n got %v\nwant %v", rc.calls, want)
	}
	if got := rc.posts["/api/v3/stages/prompt/stages/"]["fields"]; !slices.Equal(
		toStrings(got), []string{"api-v3-stages-prompt-prompts"}) {
		t.Errorf("prompt stage fields = %v, want the prompt's pk", got)
	}
}

func TestEnsureEnrolmentFlowIsANoOpWhenTheFlowExists(t *testing.T) {
	rc := &recorder{exist: true}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	if err := NewAPI(srv.URL, "t", nil).EnsureEnrolmentFlow(t.Context(), "enrol"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"GET /api/v3/flows/instances/"}; !slices.Equal(rc.calls, want) {
		t.Fatalf("calls %v, want only the lookup", rc.calls)
	}
}

func TestEnsureEnrolmentFlowNamesTheFailingPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		http.Error(w, `{"name":["bad"]}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	err := NewAPI(srv.URL, "t", nil).EnsureEnrolmentFlow(t.Context(), "enrol")
	if err == nil || !strings.Contains(err.Error(), "/api/v3/flows/instances/") {
		t.Fatalf("want an error naming the path, got %v", err)
	}
}

func toStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, e := range l {
		out = append(out, str(e))
	}
	return out
}
