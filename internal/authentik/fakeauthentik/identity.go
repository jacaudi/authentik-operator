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

package fakeauthentik

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Groups, users and the MFA devices of users. Membership lives in each
// group's "users" entry (a sorted []int32); a user's groups and groups_obj
// are derived from it when the user is served, so the two never disagree.

const (
	fieldUsers      = "users"
	fieldUsername   = "username"
	fieldAttributes = "attributes"
	fieldGroup      = "group"
	fieldPK         = "pk"
	fixedTime       = "2026-01-01T00:00:00Z"
	attributeSep    = "__"

	fieldSuperuser     = "is_superuser"
	fieldRoles         = "roles"
	fieldVerbose       = "verbose_name"
	fieldVerbosePlural = "verbose_name_plural"
	fieldMetaModel     = "meta_model_name"

	msgRequired = "This field is required."
	msgTimeout  = "upstream timed out"
)

// userDevice is an MFA device that belongs to a user.
type userDevice struct {
	user int32
	data map[string]any
}

// ---- helpers shared by the identity fakes ----

func writeNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{fieldDetail: notFound})
}

// splitPath splits the part of path after prefix into the object id and the
// action that follows it: "/core/groups/<pk>/add_user/" gives "<pk>", "add_user".
func splitPath(path, prefix string) (id, action string) {
	id, action, _ = strings.Cut(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
	return id, action
}

// missingFields returns a DRF "required" error for every field absent from
// body, or nil.
func missingFields(body map[string]any, fields ...string) map[string]any {
	var errs map[string]any
	for _, f := range fields {
		if _, ok := body[f]; !ok {
			if errs == nil {
				errs = map[string]any{}
			}
			errs[f] = []string{msgRequired}
		}
	}
	return errs
}

func invalidPK(pk any) []string {
	return []string{fmt.Sprintf("Invalid pk %q - object does not exist.", fmt.Sprint(pk))}
}

// str returns m[key] if it is a string, else "".
func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// normalize round-trips v through JSON, so stored values have the types a
// client decodes (numbers as float64, nested maps as map[string]any).
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic("fakeauthentik: value is not JSON: " + err.Error())
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		panic("fakeauthentik: value is not JSON: " + err.Error())
	}
	return out
}

// copyFields deep-copies fields into a new, non-nil map.
func copyFields(fields map[string]any) map[string]any {
	out := clone(fields)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// pksOf converts a JSON list of integers (or nil) into a sorted, unique list
// of user pks.
func pksOf(v any) ([]int32, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal members: %w", err)
	}
	var pks []int32
	if err := json.Unmarshal(b, &pks); err != nil {
		return nil, fmt.Errorf("decode members: %w", err)
	}
	if pks == nil {
		pks = []int32{} // an empty list serves as [], not null
	}
	slices.Sort(pks)
	return slices.Compact(pks), nil
}

func mustPKs(v any) []int32 {
	pks, err := pksOf(v)
	if err != nil {
		panic("fakeauthentik: " + err.Error())
	}
	return pks
}

func memberPKs(g map[string]any) []int32 {
	pks, _ := g[fieldUsers].([]int32)
	return pks
}

// ---- groups ----

// SeedGroup inserts a group not created through the API and returns its pk.
// fields override the defaults (a "users" entry sets the members); the name is
// not checked for uniqueness.
func (s *Server) SeedGroup(name string, fields map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := copyFields(fields)
	in[fieldName] = name
	g := s.completeGroup(in)
	s.groups[str(g, fieldPK)] = g
	return str(g, fieldPK)
}

// Group returns a copy of a stored group (members included), or nil.
func (s *Server) Group(pk string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[pk]
	if !ok {
		return nil
	}
	return clone(s.groupJSON(g, true))
}

// GroupByName returns a copy of the stored group with this name, or nil.
func (s *Server) GroupByName(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := s.sortedGroups(func(g map[string]any) bool { return g[fieldName] == name })
	if len(groups) == 0 {
		return nil
	}
	return clone(s.groupJSON(groups[0], true))
}

// SetGroupField changes a stored group out of band (simulates drift).
func (s *Server) SetGroupField(pk, field string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if field == fieldUsers {
		s.groups[pk][field] = mustPKs(v)
		return
	}
	s.groups[pk][field] = normalize(v)
}

// DeleteGroup removes a group, and its bindings, out of band.
func (s *Server) DeleteGroup(pk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteGroupLocked(pk)
}

// GroupCount returns the number of stored groups.
func (s *Server) GroupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.groups)
}

// GroupMemberPKs returns the member user pks of a group, sorted.
func (s *Server) GroupMemberPKs(pk string) []int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(memberPKs(s.groups[pk]))
}

// AddGroupMember adds a user to a group out of band.
func (s *Server) AddGroupMember(groupPK string, userPK int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setMember(s.groups[groupPK], userPK, true)
}

// LoseNextGroupCreate makes the next group create succeed server-side but
// answer 504.
func (s *Server) LoseNextGroupCreate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNextGroupCreate = true
}

func (s *Server) deleteGroupLocked(pk string) {
	delete(s.groups, pk)
	s.dropBindings(func(b map[string]any) bool { return b[fieldGroup] == pk })
}

// setMember adds or removes one user in g's membership.
func (s *Server) setMember(g map[string]any, user int32, add bool) {
	members := slices.DeleteFunc(slices.Clone(memberPKs(g)), func(m int32) bool { return m == user })
	if add {
		members = append(members, user)
		slices.Sort(members)
	}
	g[fieldUsers] = members
}

// completeGroup fills the server-side defaults and read-only fields.
func (s *Server) completeGroup(in map[string]any) map[string]any {
	s.nextGroup++
	g := map[string]any{
		fieldSuperuser: false, "parents": []any{}, "parents_obj": []any{}, fieldAttributes: map[string]any{},
		fieldRoles: []any{}, "roles_obj": []any{}, "inherited_roles_obj": []any{},
		"children": []any{}, "children_obj": []any{},
	}
	maps.Copy(g, in)
	g[fieldPK] = fmt.Sprintf("00000000-0000-0000-0001-%012d", s.nextGroup)
	g["num_pk"] = s.nextGroup
	g[fieldUsers] = mustPKs(in[fieldUsers])
	return g
}

// groupJSON is the group as served: members as pks, and as partial users
// unless withUsers is false (include_users=false), which gives null.
func (s *Server) groupJSON(g map[string]any, withUsers bool) map[string]any {
	out := maps.Clone(g)
	out["users_obj"] = nil
	if withUsers {
		objs := []map[string]any{}
		for _, pk := range memberPKs(g) {
			if u, ok := s.users[pk]; ok {
				objs = append(objs, partialUser(pk, u))
			}
		}
		out["users_obj"] = objs
	}
	return out
}

func partialGroup(g map[string]any) map[string]any {
	return map[string]any{fieldPK: g[fieldPK], "num_pk": g["num_pk"], fieldName: g[fieldName]}
}

func partialUser(pk int32, u map[string]any) map[string]any {
	return map[string]any{fieldPK: pk, fieldUsername: u[fieldUsername], fieldName: u[fieldName], "uid": u["uid"]}
}

// sortedGroups returns the groups that match, ordered by pk.
func (s *Server) sortedGroups(match func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, g := range s.groups {
		if match(g) {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b map[string]any) int { return strings.Compare(str(a, fieldPK), str(b, fieldPK)) })
	return out
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	id, action := splitPath(path, "/core/groups/")
	if id == "" {
		s.handleGroupCollection(w, r, body)
		return
	}
	g, ok := s.groups[id]
	if !ok {
		writeNotFound(w)
		return
	}
	switch {
	case action == "":
		s.handleGroup(w, r, g, body)
	case action == "used_by" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.groupUsedBy(g))
	case (action == "add_user" || action == "remove_user") && r.Method == http.MethodPost:
		s.changeMembership(w, g, body, action == "add_user")
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGroup serves /core/groups/{pk}/ for one stored group.
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request, g, body map[string]any) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.groupJSON(g, r.URL.Query().Get("include_users") != "false"))
	case http.MethodPatch:
		s.patchGroup(w, g, body)
	case http.MethodDelete:
		s.deleteGroupLocked(str(g, fieldPK))
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGroupCollection(w http.ResponseWriter, r *http.Request, body map[string]any) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		name := q.Get(fieldName)
		groups := s.sortedGroups(func(g map[string]any) bool { return name == "" || g[fieldName] == name })
		results := make([]map[string]any, 0, len(groups))
		for _, g := range groups {
			results = append(results, s.groupJSON(g, q.Get("include_users") != "false"))
		}
		writeJSON(w, http.StatusOK, page(results))
	case http.MethodPost:
		s.createGroup(w, body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) createGroup(w http.ResponseWriter, body map[string]any) {
	errs := missingFields(body, fieldName)
	if errs == nil {
		errs = s.validateGroup(body, "")
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, errs)
		return
	}
	g := s.completeGroup(body)
	s.groups[str(g, fieldPK)] = g
	if s.loseNextGroupCreate {
		s.loseNextGroupCreate = false
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{fieldDetail: msgTimeout})
		return
	}
	writeJSON(w, http.StatusCreated, s.groupJSON(g, true))
}

// patchGroup copies body over the stored group: attributes are replaced whole,
// as Authentik does, and a users key replaces the membership.
func (s *Server) patchGroup(w http.ResponseWriter, g, body map[string]any) {
	if errs := s.validateGroup(body, str(g, fieldPK)); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, errs)
		return
	}
	patch := maps.Clone(body)
	if users, ok := patch[fieldUsers]; ok {
		g[fieldUsers] = mustPKs(users)
		delete(patch, fieldUsers)
	}
	maps.Copy(g, patch)
	writeJSON(w, http.StatusOK, s.groupJSON(g, true))
}

// validateGroup mimics the server-side checks: a non-blank unique name and
// members that exist. self is the pk of the group being changed.
func (s *Server) validateGroup(body map[string]any, self string) map[string]any {
	errs := map[string]any{}
	if name, ok := body[fieldName].(string); ok {
		taken := s.sortedGroups(func(g map[string]any) bool { return g[fieldName] == name && str(g, fieldPK) != self })
		switch {
		case name == "":
			errs[fieldName] = []string{"This field may not be blank."}
		case len(taken) > 0:
			errs[fieldName] = []string{"group with this name already exists."}
		}
	}
	if users, ok := body[fieldUsers]; ok {
		pks, err := pksOf(users)
		if err != nil {
			errs[fieldUsers] = []string{"Expected a list of items."}
		}
		for _, pk := range pks {
			if _, exists := s.users[pk]; !exists {
				errs[fieldUsers] = invalidPK(pk)
			}
		}
	}
	return errs
}

// changeMembership serves add_user and remove_user: 204, or 404 for an unknown
// user (the group is known by now).
func (s *Server) changeMembership(w http.ResponseWriter, g, body map[string]any, add bool) {
	pk, ok := body[fieldPK].(float64)
	if !ok {
		writeJSON(w, http.StatusBadRequest, missingFields(body, fieldPK))
		return
	}
	if _, exists := s.users[int32(pk)]; !exists {
		writeNotFound(w)
		return
	}
	s.setMember(g, int32(pk), add)
	w.WriteHeader(http.StatusNoContent)
}

// groupUsedBy lists what references a group: its policy bindings (deleted with
// it) and its members (detached from it).
func (s *Server) groupUsedBy(g map[string]any) []map[string]any {
	used := []map[string]any{}
	for _, b := range s.sortedBindings(func(b map[string]any) bool { return b[fieldGroup] == g[fieldPK] }) {
		name := fmt.Sprintf("Binding from %s #%v to Group %s", s.applicationName(b[fieldTarget]), b["order"], g[fieldName])
		used = append(used, usedBy("authentik_policies", "policybinding", b[fieldPK], name, "cascade"))
	}
	for _, pk := range memberPKs(g) {
		if u, ok := s.users[pk]; ok {
			used = append(used, usedBy("authentik_core", "user", strconv.Itoa(int(pk)), u[fieldUsername], "cascade_many"))
		}
	}
	return used
}

func usedBy(app, model string, pk, name any, action string) map[string]any {
	return map[string]any{"app": app, "model_name": model, fieldPK: pk, fieldName: name, "action": action}
}

// applicationName is the name of the application with this pk, or the pk
// itself when there is none.
func (s *Server) applicationName(pk any) string {
	for _, a := range s.apps {
		if a[fieldPK] == pk {
			return str(a, fieldName)
		}
	}
	return fmt.Sprint(pk)
}

// ---- users ----

// SeedUser inserts a user not created through the API and returns its pk.
// fields override the defaults; the username is not checked for uniqueness.
func (s *Server) SeedUser(username string, fields map[string]any) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := copyFields(fields)
	in[fieldUsername] = username
	return s.addUser(in)
}

// User returns a copy of a stored user (groups included), or nil.
func (s *Server) User(pk int32) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[pk]; !ok {
		return nil
	}
	return clone(s.userJSON(pk))
}

// UserByUsername returns a copy of the stored user with this username, or nil.
func (s *Server) UserByUsername(username string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	pks := s.sortedUserPKs(func(u map[string]any) bool { return u[fieldUsername] == username })
	if len(pks) == 0 {
		return nil
	}
	return clone(s.userJSON(pks[0]))
}

// SetUserField changes a stored user out of band (simulates drift).
func (s *Server) SetUserField(pk int32, field string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[pk][field] = normalize(v)
}

// DeleteUser removes a user, its group memberships and its bindings out of band.
func (s *Server) DeleteUser(pk int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteUserLocked(pk)
}

// SetTokenUserSuperuser sets whether the API token's user is a superuser.
func (s *Server) SetTokenUserSuperuser(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[TokenUserPK][fieldSuperuser] = on
}

// IgnoreUserAttributeFilter makes the user list ignore the attributes filter
// and answer every user, as Authentik does for a lookup it cannot apply.
func (s *Server) IgnoreUserAttributeFilter(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignoreUserAttrFilter = on
}

// AddAuthenticator gives a user one confirmed MFA device.
func (s *Server) AddAuthenticator(userPK int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.devices) + 1
	s.devices = append(s.devices, userDevice{user: userPK, data: map[string]any{
		fieldVerbose: "TOTP Device", fieldVerbosePlural: "TOTP Devices",
		fieldMetaModel: "authentik_stages_authenticator_totp.totpdevice", fieldPK: strconv.Itoa(n),
		fieldName: "device-" + strconv.Itoa(n), "type": "totp", "confirmed": true,
		"created": fixedTime, "last_updated": fixedTime, "last_used": nil, "extra_description": "", "external_id": nil,
	}})
}

func (s *Server) deleteUserLocked(pk int32) {
	delete(s.users, pk)
	for _, g := range s.groups {
		s.setMember(g, pk, false)
	}
	s.dropBindings(func(b map[string]any) bool { return b[fieldUser] == float64(pk) })
}

// addUser stores a new user with the server-side defaults and returns its pk.
func (s *Server) addUser(in map[string]any) int32 {
	pk := s.nextUser
	s.nextUser++
	u := map[string]any{
		fieldName: "", "is_active": true, "last_login": nil, "date_joined": fixedTime, fieldSuperuser: false,
		fieldRoles: []any{}, "roles_obj": []any{}, "email": "", "avatar": "", fieldAttributes: map[string]any{},
		"path": "users", "type": "internal", "password_change_date": fixedTime, "last_updated": fixedTime,
	}
	maps.Copy(u, in)
	u["uid"] = fmt.Sprintf("uid%d", pk)
	u["uuid"] = fmt.Sprintf("00000000-0000-0000-0003-%012d", pk)
	s.users[pk] = u
	return pk
}

// userJSON is the user as served, with the groups derived from membership.
func (s *Server) userJSON(pk int32) map[string]any {
	out := maps.Clone(s.users[pk])
	out[fieldPK] = pk
	mine := s.sortedGroups(func(g map[string]any) bool { return slices.Contains(memberPKs(g), pk) })
	groups, objs := make([]string, 0, len(mine)), make([]map[string]any, 0, len(mine))
	for _, g := range mine {
		groups = append(groups, str(g, fieldPK))
		objs = append(objs, partialGroup(g))
	}
	out["groups"], out["groups_obj"] = groups, objs
	return out
}

// sortedUserPKs returns the pks of the users that match, ascending.
func (s *Server) sortedUserPKs(match func(map[string]any) bool) []int32 {
	var pks []int32
	for pk, u := range s.users {
		if match(u) {
			pks = append(pks, pk)
		}
	}
	slices.Sort(pks)
	return pks
}

// me is the token owner, as /core/users/me/ serves it.
func (s *Server) me() map[string]any {
	t := s.users[TokenUserPK]
	superuser, _ := t[fieldSuperuser].(bool)
	u := map[string]any{
		fieldPK: TokenUserPK, fieldUsername: t[fieldUsername], fieldName: t[fieldName], "is_active": true,
		fieldSuperuser: superuser, "is_current": true, "groups": []any{}, fieldRoles: []any{}, "avatar": "",
		"uid": t["uid"], "settings": map[string]any{}, "system_permissions": []string{},
	}
	return map[string]any{"user": u, "users": []any{u}}
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	id, action := splitPath(path, "/core/users/")
	if id == "" && r.Method == http.MethodGet {
		s.listUsers(w, r.URL.Query())
		return
	}
	n, err := strconv.ParseInt(id, 10, 32)
	pk := int32(n)
	if _, ok := s.users[pk]; err != nil || !ok || action != "" {
		writeNotFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.userJSON(pk))
	case http.MethodPatch:
		s.patchUser(w, pk, body)
	case http.MethodDelete:
		s.deleteUserLocked(pk)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// listUsers serves the user list with the username filter and the attributes
// filter: a JSON object whose keys are attribute paths joined with "__".
func (s *Server) listUsers(w http.ResponseWriter, q url.Values) {
	want := map[string]any{}
	if raw := q.Get(fieldAttributes); raw != "" && !s.ignoreUserAttrFilter {
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{fieldAttributes: []string{"filter: failed to parse JSON"}})
			return
		}
	}
	username := q.Get(fieldUsername)
	pks := s.sortedUserPKs(func(u map[string]any) bool {
		return (username == "" || u[fieldUsername] == username) && hasAttributes(u, want)
	})
	results := make([]map[string]any, 0, len(pks))
	for _, pk := range pks {
		results = append(results, s.userJSON(pk))
	}
	writeJSON(w, http.StatusOK, page(results))
}

// hasAttributes reports whether u's attributes hold every wanted value, each
// key being a path into the attributes split on "__".
func hasAttributes(u, want map[string]any) bool {
	for key, v := range want {
		cur := u[fieldAttributes]
		for seg := range strings.SplitSeq(key, attributeSep) {
			m, ok := cur.(map[string]any)
			if !ok {
				return false
			}
			if cur, ok = m[seg]; !ok {
				return false
			}
		}
		if !reflect.DeepEqual(cur, v) {
			return false
		}
	}
	return true
}

func (s *Server) patchUser(w http.ResponseWriter, pk int32, body map[string]any) {
	if name, ok := body[fieldUsername].(string); ok {
		taken := s.sortedUserPKs(func(u map[string]any) bool { return u[fieldUsername] == name })
		if slices.ContainsFunc(taken, func(other int32) bool { return other != pk }) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				fieldUsername: []string{"user with this username already exists."},
			})
			return
		}
	}
	maps.Copy(s.users[pk], body)
	writeJSON(w, http.StatusOK, s.userJSON(pk))
}

// handleAuthenticators serves /authenticators/admin/all/: the devices of the
// user named by the user query parameter, or of everyone without it.
func (s *Server) handleAuthenticators(w http.ResponseWriter, q url.Values) {
	user := q.Get("user")
	devices := []map[string]any{}
	for _, d := range s.devices {
		if user == "" || user == strconv.Itoa(int(d.user)) {
			devices = append(devices, d.data)
		}
	}
	writeJSON(w, http.StatusOK, devices)
}
