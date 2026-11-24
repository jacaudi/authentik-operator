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
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "goauthentik.io/api/v3"

	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
)

// Field names and values the tests pass to the fake in several places.
const (
	keyOrder      = "order"
	keyAttributes = "attributes"
	keyPolicy     = "policy"
	keyUsername   = "username"
	keyName       = "name"
	userDave      = "dave"
	tokenUsername = "akadmin"
	keyGroup      = "group"
	uidOne        = "u-1"
	teamName      = "team"
)

// newFakeClient starts a fake Authentik and a client for it with no rate
// limit and no retries, so an injected failure reaches the caller once.
func newFakeClient(t *testing.T) (*fakeauthentik.Server, *Client) {
	t.Helper()
	fake := fakeauthentik.New("t")
	t.Cleanup(fake.Close)
	c, err := New(Config{URL: fake.URL, Token: "t", RPS: -1, MaxRetries: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fake, c
}

// lastRequest returns the most recent recorded request with this method.
func lastRequest(t *testing.T, fake *fakeauthentik.Server, method string) fakeauthentik.Request {
	t.Helper()
	reqs := fake.Requests(false)
	for _, r := range slices.Backward(reqs) {
		if r.Method == method {
			return r
		}
	}
	t.Fatalf("no %s request recorded in %+v", method, reqs)
	return fakeauthentik.Request{}
}

func TestOwnerProof(t *testing.T) {
	t.Parallel()
	want := map[string]any{OwnerAttribute: map[string]any{"owner_uid": uidOne}}
	if got := OwnerProof(uidOne); !reflect.DeepEqual(got, want) {
		t.Errorf("OwnerProof = %#v, want %#v", got, want)
	}
	cases := []struct {
		name  string
		attrs map[string]any
		uid   string
		want  bool
	}{
		{"own proof", OwnerProof(uidOne), uidOne, true},
		{"other uid", OwnerProof(uidOne), "u-2", false},
		{"nil attributes", nil, uidOne, false},
		{"key is not a dict", map[string]any{OwnerAttribute: "x"}, uidOne, false},
		{"uid is not a string", map[string]any{OwnerAttribute: map[string]any{"owner_uid": 1.0}}, uidOne, false},
		{"empty uid never proves", OwnerProof(""), "", false},
	}
	for _, tc := range cases {
		if got := HasOwnerProof(tc.attrs, tc.uid); got != tc.want {
			t.Errorf("%s: HasOwnerProof = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestFindGroupByNameIsExact(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedGroup("Immich Users & Admins", nil)
	pk := fake.SeedGroup("Immich Users", nil)

	g, err := c.FindGroupByName(t.Context(), "Immich Users")
	if err != nil || g.Pk != pk {
		t.Fatalf("FindGroupByName = %+v, %v; want pk %s", g, err, pk)
	}
	if _, err = c.FindGroupByName(t.Context(), "immich users"); !IsNotFound(err) {
		t.Errorf("FindGroupByName(other case) error = %v, want not found", err)
	}
	if got := lastRequest(t, fake, http.MethodGet).Query; !strings.Contains(got, "include_users=false") {
		t.Errorf("query %q does not ask for include_users=false", got)
	}
}

func TestCreateGroupSendsNoUsers(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	created, err := c.CreateGroup(t.Context(), api.GroupRequest{Name: teamName, Attributes: OwnerProof(uidOne)})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, has := lastRequest(t, fake, http.MethodPost).Body["users"]; has {
		t.Error("create body carries a users key")
	}
	if !HasOwnerProof(created.Attributes, uidOne) {
		t.Errorf("created attributes %v lack the owner proof", created.Attributes)
	}
}

func TestGroupMembershipCalls(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	u := fake.SeedUser("bob", nil)
	g := fake.SeedGroup(teamName, nil)

	if err := c.AddUserToGroup(t.Context(), g, u); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}
	if got := fake.GroupMemberPKs(g); !slices.Equal(got, []int32{u}) {
		t.Errorf("members = %v, want [%d]", got, u)
	}
	req := lastRequest(t, fake, http.MethodPost)
	if !strings.HasSuffix(req.Path, "/core/groups/"+g+"/add_user/") || req.Body["pk"] != float64(u) {
		t.Errorf("add request = %s %v", req.Path, req.Body)
	}

	got, err := c.GetGroup(t.Context(), g)
	if err != nil || len(got.Users) != 1 || len(got.UsersObj) != 1 || got.UsersObj[0].Username != "bob" {
		t.Errorf("GetGroup = %+v, %v; want bob as the only member", got, err)
	}

	if err := c.RemoveUserFromGroup(t.Context(), g, u); err != nil {
		t.Fatalf("RemoveUserFromGroup: %v", err)
	}
	if got := fake.GroupMemberPKs(g); len(got) != 0 {
		t.Errorf("members after remove = %v, want none", got)
	}
	if err := c.AddUserToGroup(t.Context(), g, 999); !IsNotFound(err) {
		t.Errorf("AddUserToGroup(unknown user) error = %v, want not found", err)
	}
}

func TestGroupUsedByListsBindings(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedApplication("immich", nil)
	g := fake.SeedGroup("immich-users", nil)
	appPK, ok := fake.Application("immich")["pk"].(string)
	if !ok {
		t.Fatal("seeded application has no pk")
	}
	fake.SeedBinding(appPK, map[string]any{keyGroup: g, keyOrder: float64(0)})

	used, err := c.GroupUsedBy(t.Context(), g)
	if err != nil {
		t.Fatalf("GroupUsedBy: %v", err)
	}
	found := slices.ContainsFunc(used, func(u api.UsedBy) bool {
		return u.App == "authentik_policies" && u.ModelName == "policybinding" &&
			u.Name == "Binding from immich #0 to Group immich-users"
	})
	if !found {
		t.Errorf("used_by %+v lacks the binding entry", used)
	}
}

func TestFindUsersByOwnerUIDRechecksClientSide(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedUser("a", map[string]any{keyAttributes: OwnerProof("uid-1")})
	fake.SeedUser("b", map[string]any{keyAttributes: OwnerProof("uid-2")})
	fake.SeedUser("c", nil)
	fake.IgnoreUserAttributeFilter(true)

	users, err := c.FindUsersByOwnerUID(t.Context(), "uid-1")
	if err != nil {
		t.Fatalf("FindUsersByOwnerUID: %v", err)
	}
	if len(users) != 1 || users[0].Username != "a" {
		t.Errorf("users = %+v, want only a", users)
	}
	raw := lastRequest(t, fake, http.MethodGet).Query
	got, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatalf("unescape %q: %v", raw, err)
	}
	if want := `attributes={"authentik_operator__owner_uid":"uid-1"}`; !strings.Contains(got, want) {
		t.Errorf("query %q does not contain %q", got, want)
	}
}

func TestFindUsersByOwnerUIDUsesTheServerFilter(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedUser("a", map[string]any{keyAttributes: OwnerProof("uid-1")})
	fake.SeedUser("b", map[string]any{keyAttributes: OwnerProof("uid-2")})

	users, err := c.FindUsersByOwnerUID(t.Context(), "uid-2")
	if err != nil || len(users) != 1 || users[0].Username != "b" {
		t.Errorf("FindUsersByOwnerUID = %+v, %v; want only b", users, err)
	}
}

func TestFindUserByUsernameIsExact(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	pk := fake.SeedUser("Alice", nil)

	u, err := c.FindUserByUsername(t.Context(), "Alice")
	if err != nil || u.Pk != pk {
		t.Fatalf("FindUserByUsername(Alice) = %+v, %v; want pk %d", u, err, pk)
	}
	if _, err = c.FindUserByUsername(t.Context(), "alice"); !IsNotFound(err) {
		t.Errorf("FindUserByUsername(alice) error = %v, want not found", err)
	}
}

func TestPatchAndDeleteUser(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	pk := fake.SeedUser(userDave, nil)

	patched, err := c.PatchUser(t.Context(), pk, api.PatchedUserRequest{Name: new("Dave D")})
	if err != nil || patched.Name != "Dave D" {
		t.Fatalf("PatchUser = %+v, %v", patched, err)
	}
	got, err := c.GetUser(t.Context(), pk)
	if err != nil || got.Name != "Dave D" {
		t.Fatalf("GetUser = %+v, %v", got, err)
	}
	if err := c.DeleteUser(t.Context(), pk); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := c.DeleteUser(t.Context(), pk); err != nil {
		t.Errorf("DeleteUser(already gone) = %v, want nil", err)
	}
	if _, err := c.GetUser(t.Context(), pk); !IsNotFound(err) {
		t.Errorf("GetUser after delete error = %v, want not found", err)
	}
}

func TestPatchAndDeleteGroup(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	pk := fake.SeedGroup(teamName, nil)

	patched, err := c.PatchGroup(t.Context(), pk, api.PatchedGroupRequest{Attributes: map[string]any{"k": "v"}})
	if err != nil || patched.Attributes["k"] != "v" {
		t.Fatalf("PatchGroup = %+v, %v", patched, err)
	}
	if err := c.DeleteGroup(t.Context(), pk); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if err := c.DeleteGroup(t.Context(), pk); err != nil {
		t.Errorf("DeleteGroup(already gone) = %v, want nil", err)
	}
	if fake.GroupCount() != 0 {
		t.Errorf("GroupCount = %d, want 0", fake.GroupCount())
	}
}

func TestMeReportsSuperuser(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)

	me, err := c.Me(t.Context())
	if err != nil || me.Pk != 1 || me.Username != tokenUsername || !me.IsSuperuser {
		t.Fatalf("Me = %+v, %v; want akadmin (pk 1) as superuser", me, err)
	}
	fake.SetTokenUserSuperuser(false)
	if me, err = c.Me(t.Context()); err != nil || me.IsSuperuser {
		t.Errorf("Me after demotion = %+v, %v; want not superuser", me, err)
	}
}

func TestListBindingsRefusesEmptyTarget(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	if _, err := c.ListBindings(t.Context(), ""); !errors.Is(err, ErrNoTarget) {
		t.Errorf("ListBindings(empty) error = %v, want ErrNoTarget", err)
	}
	if got := fake.Requests(false); len(got) != 0 {
		t.Errorf("requests = %+v, want none", got)
	}
}

func TestListBindingsDropsOtherTargets(t *testing.T) {
	t.Parallel()
	binding := func(pk, target string) map[string]any {
		return map[string]any{
			"pk": pk, "policy_obj": nil, "group_obj": nil, "user_obj": nil, "target": target,
			keyOrder: 0, "expires": nil, "expiring": false,
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/policies/bindings/" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, page([]map[string]any{binding("b-a", "app-a"), binding("b-b", "app-b")}, 1, 0, 2))
	})
	got, err := c.ListBindings(t.Context(), "app-a")
	if err != nil || len(got) != 1 || got[0].Target != "app-a" {
		t.Errorf("ListBindings = %+v, %v; want only the app-a binding", got, err)
	}
}

func TestBindingCreateAndPatch(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedApplication("immich", nil)
	appPK, ok := fake.Application("immich")["pk"].(string)
	if !ok {
		t.Fatal("seeded application has no pk")
	}
	g := fake.SeedGroup(teamName, nil)

	created, err := c.CreateBinding(t.Context(), api.PolicyBindingRequest{
		Target: appPK, Order: 3, Group: *api.NewNullableString(&g),
	})
	if err != nil || created.Target != appPK || created.Order != 3 {
		t.Fatalf("CreateBinding = %+v, %v", created, err)
	}
	patched, err := c.PatchBinding(t.Context(), created.Pk, api.PatchedPolicyBindingRequest{Order: new(int32(7))})
	if err != nil || patched.Order != 7 {
		t.Fatalf("PatchBinding = %+v, %v", patched, err)
	}
	listed, err := c.ListBindings(t.Context(), appPK)
	if err != nil || len(listed) != 1 || listed[0].Order != 7 {
		t.Errorf("ListBindings = %+v, %v; want the patched binding", listed, err)
	}
}

func TestDeleteBindingChecksTarget(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	pk := fake.SeedBinding("app-a", map[string]any{keyPolicy: "p-1"})
	b := &api.PolicyBinding{Pk: pk, Target: "app-a"}
	deletes := func() int {
		n := 0
		for _, r := range fake.Requests(true) {
			if r.Method == http.MethodDelete {
				n++
			}
		}
		return n
	}

	if err := c.DeleteBinding(t.Context(), b, "app-b"); err == nil {
		t.Error("DeleteBinding(other target) succeeded, want error")
	}
	if err := c.DeleteBinding(t.Context(), b, ""); !errors.Is(err, ErrNoTarget) {
		t.Errorf("DeleteBinding(empty target) error = %v, want ErrNoTarget", err)
	}
	if n := deletes(); n != 0 {
		t.Fatalf("%d DELETE requests after refused deletes, want 0", n)
	}
	if err := c.DeleteBinding(t.Context(), b, "app-a"); err != nil {
		t.Fatalf("DeleteBinding: %v", err)
	}
	if n := deletes(); n != 1 {
		t.Errorf("%d DELETE requests, want 1", n)
	}
	if got := fake.Bindings("app-a"); len(got) != 0 {
		t.Errorf("bindings left = %v", got)
	}
	if err := c.DeleteBinding(t.Context(), b, "app-a"); err != nil {
		t.Errorf("DeleteBinding(already gone) = %v, want nil", err)
	}
}

func TestInvitationLifecycle(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)

	inv, err := c.CreateInvitation(t.Context(), api.InvitationRequest{
		Name:      "k8s-u1-1",
		SingleUse: new(true),
		Flow:      *api.NewNullableString(new(fakeauthentik.FlowEnrollmentPK)),
		FixedData: map[string]any{keyUsername: userDave},
	})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	found, err := c.FindInvitationByName(t.Context(), "k8s-u1-1")
	if err != nil || found.Pk != inv.Pk {
		t.Fatalf("FindInvitationByName = %+v, %v; want pk %s", found, err, inv.Pk)
	}
	if err := c.DeleteInvitation(t.Context(), inv.Pk); err != nil {
		t.Fatalf("DeleteInvitation: %v", err)
	}
	if _, err := c.FindInvitationByName(t.Context(), "k8s-u1-1"); !IsNotFound(err) {
		t.Errorf("FindInvitationByName after delete error = %v, want not found", err)
	}
	if err := c.DeleteInvitation(t.Context(), inv.Pk); err != nil {
		t.Errorf("DeleteInvitation(already gone) = %v, want nil", err)
	}

	fake.SeedInvitation("dup", nil)
	fake.SeedInvitation("dup", nil)
	if _, err := c.FindInvitationByName(t.Context(), "dup"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("FindInvitationByName(dup) error = %v, want ErrAmbiguous", err)
	}
}

func TestDeleteInvitationRedactsThePK(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	inv, err := c.CreateInvitation(t.Context(), api.InvitationRequest{
		Name: "k8s-u1-1", Flow: *api.NewNullableString(new(fakeauthentik.FlowEnrollmentPK)),
	})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	fake.FailNextOn(http.MethodDelete, "/stages/invitation/invitations/", http.StatusInternalServerError)
	err = c.DeleteInvitation(t.Context(), inv.Pk)
	if err == nil {
		t.Fatal("DeleteInvitation succeeded despite the injected failure")
	}
	if strings.Contains(err.Error(), inv.Pk) || !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("error %q must hide the pk and say <redacted>", err)
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || strings.Contains(apiErr.Path+apiErr.Body, inv.Pk) {
		t.Errorf("APIError = %+v, want a copy without the pk", apiErr)
	}
	if StatusCode(err) != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", StatusCode(err))
	}

	closed := fakeauthentik.New("t")
	closed.Close()
	dead, err := New(Config{URL: closed.URL, Token: "t", RPS: -1, MaxRetries: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = dead.DeleteInvitation(t.Context(), inv.Pk)
	if err == nil || strings.Contains(err.Error(), inv.Pk) {
		t.Errorf("transport error %v must exist and hide the pk", err)
	}
}

func TestRedactIgnoresAnEmptySecret(t *testing.T) {
	t.Parallel()
	err := errors.New("boom")
	if got := redact(err, ""); got.Error() != "boom" {
		t.Errorf("redact(err, empty) = %v, want the same error", got)
	}
}

func TestUserAuthenticators(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	u := fake.SeedUser(userDave, nil)
	other := fake.SeedUser("erin", nil)

	got, err := c.UserAuthenticators(t.Context(), u)
	if err != nil || len(got) != 0 {
		t.Fatalf("UserAuthenticators = %+v, %v; want none", got, err)
	}
	fake.AddAuthenticator(u)
	if got, err = c.UserAuthenticators(t.Context(), u); err != nil || len(got) != 1 {
		t.Errorf("UserAuthenticators after add = %+v, %v; want 1", got, err)
	}
	if got, err = c.UserAuthenticators(t.Context(), other); err != nil || len(got) != 0 {
		t.Errorf("UserAuthenticators(other) = %+v, %v; want none", got, err)
	}
}

// The tests below pin fakeauthentik behaviour the controller suites rely on.

func TestFakeGroupNamesAreUnique(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedGroup(teamName, nil)
	_, err := c.CreateGroup(t.Context(), api.GroupRequest{Name: teamName})
	if !IsConflict(err) {
		t.Errorf("CreateGroup(duplicate) error = %v, want a conflict", err)
	}
	other := fake.SeedGroup("other", nil)
	_, err = c.PatchGroup(t.Context(), other, api.PatchedGroupRequest{Name: new(teamName)})
	if !IsConflict(err) {
		t.Errorf("PatchGroup(rename onto a taken name) error = %v, want a conflict", err)
	}
}

func TestFakeGroupCreateLost(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.LoseNextGroupCreate()
	_, err := c.CreateGroup(t.Context(), api.GroupRequest{Name: teamName})
	if StatusCode(err) != http.StatusGatewayTimeout {
		t.Errorf("CreateGroup error = %v, want a 504", err)
	}
	if fake.GroupByName(teamName) == nil {
		t.Error("the lost create did not store the group")
	}
}

func TestFakeAttributeFilter(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedUser("a", map[string]any{keyAttributes: map[string]any{"x": map[string]any{"y": "1"}, "z": "2"}})
	fake.SeedUser("b", map[string]any{keyAttributes: map[string]any{"x": map[string]any{"y": "2"}}})

	list := func(filter string) ([]api.User, error) {
		res, resp, err := c.api.CoreAPI.CoreUsersList(t.Context()).Attributes(filter).Execute()
		if err := Check(resp, err); err != nil {
			return nil, err
		}
		return res.Results, nil
	}
	got, err := list(`{"x__y":"1","z":"2"}`)
	if err != nil || len(got) != 1 || got[0].Username != "a" {
		t.Errorf("list(both keys) = %+v, %v; want only a", got, err)
	}
	if got, err = list(`{"x__y":"3"}`); err != nil || len(got) != 0 {
		t.Errorf("list(no match) = %+v, %v; want none", got, err)
	}
	if _, err = list(`{not json`); !IsValidation(err) {
		t.Errorf("list(malformed) error = %v, want a 400", err)
	}
}

func TestFakeBindingValidation(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedApplication("immich", nil)
	appPK, ok := fake.Application("immich")["pk"].(string)
	if !ok {
		t.Fatal("seeded application has no pk")
	}
	g := fake.SeedGroup(teamName, nil)

	cases := map[string]api.PolicyBindingRequest{
		"unknown target": {Target: "nope", Order: 0, Group: *api.NewNullableString(&g)},
		"no subject":     {Target: appPK, Order: 0},
		"group and policy": {
			Target: appPK, Order: 0, Group: *api.NewNullableString(&g), Policy: *api.NewNullableString(new("p")),
		},
		"group does not exist": {Target: appPK, Order: 0, Group: *api.NewNullableString(new("nope"))},
	}
	for name, req := range cases {
		if _, err := c.CreateBinding(t.Context(), req); !IsValidation(err) {
			t.Errorf("%s: CreateBinding error = %v, want a 400", name, err)
		}
	}
	if got := fake.Bindings(appPK); len(got) != 0 {
		t.Errorf("rejected requests stored bindings: %v", got)
	}
}

func TestFakeBindingsListedByTarget(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedBinding("app-a", map[string]any{keyPolicy: "p", keyOrder: float64(2)})
	fake.SeedBinding("app-a", map[string]any{keyPolicy: "p", keyOrder: float64(1)})
	fake.SeedBinding("app-b", map[string]any{keyPolicy: "p"})

	got, err := c.ListBindings(t.Context(), "app-a")
	if err != nil || len(got) != 2 || got[0].Order != 1 || got[1].Order != 2 {
		t.Fatalf("ListBindings = %+v, %v; want the two app-a bindings by order", got, err)
	}
	if b := fake.Bindings("app-a"); len(b) != 2 || b[0][keyOrder] != float64(1) || b[1][keyOrder] != float64(2) {
		t.Errorf("Bindings(app-a) = %v, want ordered by order", b)
	}
	// An unfiltered request returns every binding, as django-filter does.
	res, resp, err := c.api.PoliciesAPI.PoliciesBindingsList(t.Context()).Execute()
	if err := Check(resp, err); err != nil || len(res.Results) != 3 {
		t.Errorf("unfiltered list = %+v, %v; want all 3 bindings", res, err)
	}
}

func TestFakeCascades(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	fake.SeedApplication("immich", nil)
	appPK, ok := fake.Application("immich")["pk"].(string)
	if !ok {
		t.Fatal("seeded application has no pk")
	}
	u := fake.SeedUser("bob", nil)
	g1, g2 := fake.SeedGroup("g1", map[string]any{"users": []int32{u}}), fake.SeedGroup("g2", nil)
	fake.SeedBinding(appPK, map[string]any{keyGroup: g1})
	fake.SeedBinding(appPK, map[string]any{keyGroup: g2, keyOrder: float64(1)})
	fake.SeedBinding(appPK, map[string]any{"user": float64(u), keyOrder: float64(2)})

	if got, err := c.GetUser(t.Context(), u); err != nil || !slices.Equal(got.Groups, []string{g1}) {
		t.Fatalf("GetUser = %+v, %v; want membership of g1", got, err)
	}
	if err := c.DeleteGroup(t.Context(), g2); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if got := fake.Bindings(appPK); len(got) != 2 {
		t.Errorf("bindings after group delete = %v, want 2", got)
	}
	if err := c.DeleteUser(t.Context(), u); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if got := fake.GroupMemberPKs(g1); len(got) != 0 {
		t.Errorf("members after user delete = %v, want none", got)
	}
	if got := fake.Bindings(appPK); len(got) != 1 {
		t.Errorf("bindings after user delete = %v, want only the g1 binding", got)
	}
	if err := c.DeleteApplication(t.Context(), "immich"); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if got := fake.Bindings(appPK); len(got) != 0 {
		t.Errorf("bindings after application delete = %v, want none", got)
	}
}

func TestFakeInvitationRules(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	if _, err := c.CreateInvitation(t.Context(), api.InvitationRequest{Name: "has space"}); !IsValidation(err) {
		t.Errorf("CreateInvitation(bad name) error = %v, want a 400", err)
	}
	fake.LoseNextInvitationCreate()
	if _, err := c.CreateInvitation(t.Context(), api.InvitationRequest{Name: "lost"}); StatusCode(err) != 504 {
		t.Errorf("CreateInvitation error = %v, want a 504", err)
	}
	if got := fake.IssuedInvitationPKs(); len(got) != 1 {
		t.Errorf("IssuedInvitationPKs = %v, want the lost create", got)
	}
	fake.ConsumeInvitation("lost")
	if got := fake.IssuedInvitationPKs(); len(got) != 1 || len(fake.Invitations()) != 0 {
		t.Errorf("after consume: issued %v, stored %v; want 1 issued, none stored", got, fake.Invitations())
	}
}

func TestFakeEnrolFromInvitation(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	attrs := map[string]any{teamName: "a"}
	fake.SeedInvitation("inv", map[string]any{"fixed_data": map[string]any{
		keyUsername: userDave, keyName: "Dave", keyAttributes: attrs,
	}})

	pk := fake.EnrolFromInvitation("inv", "")
	u, err := c.GetUser(t.Context(), pk)
	if err != nil || u.Username != userDave || u.Name != "Dave" || u.Attributes[teamName] != "a" {
		t.Fatalf("enrolled user = %+v, %v; want dave from fixed_data", u, err)
	}
	if fake.InvitationByName("inv") != nil {
		t.Error("the invitation survived the enrolment")
	}
	if got := fake.EnrolFromInvitation("inv", "x"); got != 0 {
		t.Errorf("EnrolFromInvitation(consumed) = %d, want 0", got)
	}

	fake.SeedInvitation("inv2", map[string]any{"fixed_data": map[string]any{keyUsername: "erin"}})
	if got := fake.User(fake.EnrolFromInvitation("inv2", "frank"))[keyUsername]; got != "frank" {
		t.Errorf("username = %v, want the override frank", got)
	}
}

func TestFakeFailNextOnFiresOnce(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	g := fake.SeedGroup(teamName, nil)
	fake.FailNextOn(http.MethodGet, "/core/users/", http.StatusServiceUnavailable)

	if _, err := c.GetGroup(t.Context(), g); err != nil {
		t.Errorf("GetGroup (path does not match) = %v, want success", err)
	}
	if _, err := c.GetUser(t.Context(), 1); StatusCode(err) != http.StatusServiceUnavailable {
		t.Errorf("GetUser error = %v, want the injected 503", err)
	}
	if _, err := c.GetUser(t.Context(), 1); err != nil {
		t.Errorf("GetUser again = %v, want success (the rule is consumed)", err)
	}
}

func TestFakeInvitationSingleUseDefaultsToFalse(t *testing.T) {
	t.Parallel()
	fake, c := newFakeClient(t)
	if _, err := c.CreateInvitation(t.Context(), api.InvitationRequest{Name: "reusable"}); err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if got := fake.InvitationByName("reusable")["single_use"]; got != false {
		t.Errorf("single_use = %v, want false (Authentik's default) when the request omits it", got)
	}
	fake.SeedInvitation("seeded", nil)
	if got := fake.InvitationByName("seeded")["single_use"]; got != false {
		t.Errorf("seeded single_use = %v, want false", got)
	}
}
