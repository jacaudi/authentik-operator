//go:build record

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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecord20262Responses records the Authentik 2026.2.3 responses that
// testdata/2026.2.3 holds for the group, user, policy binding, invitation and
// authenticator calls (compat_test.go decodes them). It is not part of the
// normal suite: it needs the build tag record and a throwaway instance, which
// carries no real secret. Start one inside the devcontainer:
//
//	docker network create ak-rec
//	E="-e AUTHENTIK_POSTGRESQL__HOST=ak-rec-pg -e AUTHENTIK_POSTGRESQL__USER=authentik \
//	  -e AUTHENTIK_POSTGRESQL__NAME=authentik -e AUTHENTIK_POSTGRESQL__PASSWORD=rec-postgres-not-a-secret \
//	  -e AUTHENTIK_SECRET_KEY=rec-secret-key-not-a-secret-0123456789abcdef \
//	  -e AUTHENTIK_BOOTSTRAP_TOKEN=rec-token-not-a-secret-0123456789 \
//	  -e AUTHENTIK_BOOTSTRAP_PASSWORD=rec-password-not-a-secret -e AUTHENTIK_BOOTSTRAP_EMAIL=rec@example.invalid"
//	docker run -d --name ak-rec-pg --network ak-rec -e POSTGRES_USER=authentik -e POSTGRES_DB=authentik \
//	  -e POSTGRES_PASSWORD=rec-postgres-not-a-secret postgres:16-alpine
//	docker run -d --name ak-rec-server --network ak-rec -p 127.0.0.1:9000:9000 $E \
//	  ghcr.io/goauthentik/server:2026.2.3 server
//	docker run -d --name ak-rec-worker --network ak-rec $E ghcr.io/goauthentik/server:2026.2.3 worker
//
// then run, once the server answers:
//
//	AK_RECORD_URL=http://127.0.0.1:9000 AK_RECORD_TOKEN=rec-token-not-a-secret-0123456789 \
//	  go test -tags record -run TestRecord20262Responses ./internal/authentik/ -count=1 -v
//
// Authentik 2026.2.3 answers 405 to POST /authenticators/admin/static/ (and its siblings; they also cannot
// name a user), so the recorder creates the static device through "ak shell" in the ak-rec-server container;
// the response it records is still the real API one.
//
// Remove the instance with
//
//	docker rm -f ak-rec-server ak-rec-worker ak-rec-pg; docker network rm ak-rec
func TestRecord20262Responses(t *testing.T) {
	base := os.Getenv("AK_RECORD_URL")
	token := os.Getenv("AK_RECORD_TOKEN")
	if base == "" || token == "" {
		t.Fatal("AK_RECORD_URL and AK_RECORD_TOKEN must be set")
	}
	r := &recorder{t: t, base: base + "/api/v3", token: token, client: &http.Client{Timeout: time.Minute}}

	flow := r.waitForFlow("default-authentication-flow")
	app := r.create("/core/applications/", map[string]any{"name": "rec-app", "slug": "rec-app"})
	group := r.create("/core/groups/", map[string]any{"name": "rec-group", "attributes": OwnerProof("rec-uid")})
	user := r.create("/core/users/", map[string]any{
		"username": "rec-user", "name": "Rec User", "path": "users", "attributes": OwnerProof("rec-uid"),
	})
	r.send(http.MethodPost, fmt.Sprintf("/core/groups/%s/add_user/", group), map[string]any{"pk": user})
	r.save("binding.json", r.send(http.MethodPost, "/policies/bindings/",
		map[string]any{"group": group, "target": app, "order": 0}))
	r.save("invitation.json", r.send(http.MethodPost, "/stages/invitation/invitations/", map[string]any{
		"name": "rec-invitation", "single_use": true, "flow": flow,
		"expires":    time.Now().Add(168 * time.Hour).UTC().Format(time.RFC3339),
		"fixed_data": map[string]any{"username": "rec-invitee", "attributes": OwnerProof("rec-uid-2")},
	}))
	r.createStaticDevice(user)

	owner := url.QueryEscape(`{"` + OwnerAttribute + "__" + OwnerUIDKey + `":"rec-uid"}`)
	for file, path := range map[string]string{
		"bindings_list.json":    "/policies/bindings/?target=" + fmt.Sprint(app),
		"group.json":            fmt.Sprintf("/core/groups/%s/", group),
		"groups_list.json":      "/core/groups/?name=rec-group&include_users=false",
		"group_used_by.json":    fmt.Sprintf("/core/groups/%s/used_by/", group),
		"user.json":             fmt.Sprintf("/core/users/%s/", user),
		"users_list.json":       "/core/users/?username=rec-user",
		"users_owner_list.json": "/core/users/?attributes=" + owner,
		"users_me.json":         "/core/users/me/",
		"invitations_list.json": "/stages/invitation/invitations/?name=rec-invitation",
		"authenticators.json":   fmt.Sprintf("/authenticators/admin/all/?user=%s", user),
	} {
		r.save(file, r.send(http.MethodGet, path, nil))
	}
}

// recorder talks to one Authentik instance and fails the test on any non-2xx.
type recorder struct {
	t      *testing.T
	base   string
	token  string
	client *http.Client
}

// send performs one API call and returns the response body.
func (r *recorder) send(method, path string, body any) []byte {
	r.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			r.t.Fatalf("encode %s %s: %v", method, path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.t.Context(), method, r.base+path, rd)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.t.Fatalf("%s %s: status %d: %s", method, path, resp.StatusCode, out)
	}
	return out
}

// pkOf returns the pk of a created object: a string (uuid) or a json.Number.
func (r *recorder) pkOf(body []byte) any {
	r.t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var o struct {
		PK any `json:"pk"`
	}
	if err := dec.Decode(&o); err != nil || o.PK == nil {
		r.t.Fatalf("no pk in %s (%v)", body, err)
	}
	return o.PK
}

// createStaticDevice gives the user a confirmed static MFA device by running the ORM inside the throwaway
// server container: no admin API call can create one on 2026.2.3 (405).
func (r *recorder) createStaticDevice(user any) {
	r.t.Helper()
	script := fmt.Sprintf(
		"from authentik.core.models import User\n"+
			"from authentik.stages.authenticator_static.models import StaticDevice\n"+
			"d = StaticDevice.objects.create(user=User.objects.get(pk=%v), name='rec-device', confirmed=True)\n"+
			"print('static-device-created', d.pk)\n", user)
	cmd := exec.CommandContext(r.t.Context(), "docker", "exec", "-i", "ak-rec-server", "ak", "shell")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "static-device-created") {
		r.t.Fatalf("create static device: %v\n%s", err, out)
	}
}

// create POSTs body to path and returns the new object's pk.
func (r *recorder) create(path string, body any) any {
	r.t.Helper()
	return r.pkOf(r.send(http.MethodPost, path, body))
}

// waitForFlow polls until the blueprint that creates the flow has been applied
// (up to five minutes) and returns the flow's pk.
func (r *recorder) waitForFlow(slug string) any {
	r.t.Helper()
	for range 60 {
		var page struct {
			Results []json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(r.send(http.MethodGet, "/flows/instances/?slug="+slug, nil), &page); err != nil {
			r.t.Fatalf("decode flow list: %v", err)
		}
		if len(page.Results) == 1 {
			return r.pkOf(page.Results[0])
		}
		time.Sleep(5 * time.Second)
	}
	r.t.Fatalf("flow %q did not appear within 5 minutes", slug)
	return nil
}

// save writes body, pretty-printed, to testdata/2026.2.3/name.
func (r *recorder) save(name string, body []byte) {
	r.t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		r.t.Fatalf("indent %s: %v", name, err)
	}
	pretty.WriteByte('\n')
	if err := os.WriteFile(filepath.Join("testdata", "2026.2.3", name), pretty.Bytes(), 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}
