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
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Policy bindings. A stored binding holds only the fields it was given plus
// the defaults; policy_obj, group_obj and user_obj are filled in when it is
// served, from the live group and user, unless the stored binding carries its
// own.

const (
	fieldTarget = "target"
	fieldOrder  = "order"
	fieldPolicy = "policy"
	fieldUser   = "user"
)

// SeedBinding inserts a binding of target not created through the API and
// returns its pk. Every key in fields overrides the default (so a seeded
// policy binding supplies its own policy and policy_obj); group_obj is derived
// from group only when fields has no group_obj.
func (s *Server) SeedBinding(target string, fields map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return str(s.addBinding(target, copyFields(fields)), fieldPK)
}

// Bindings returns copies of the bindings of target, ordered by order then pk.
func (s *Server) Bindings(target string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	bindings := s.sortedBindings(func(b map[string]any) bool { return b[fieldTarget] == target })
	out := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, clone(s.bindingJSON(b)))
	}
	return out
}

// DeleteBinding removes a binding out of band.
func (s *Server) DeleteBinding(pk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, pk)
}

// dropBindings deletes every binding that matches.
func (s *Server) dropBindings(match func(map[string]any) bool) {
	maps.DeleteFunc(s.bindings, func(_ string, b map[string]any) bool { return match(b) })
}

// addBinding stores a new binding with the defaults under in and returns it.
func (s *Server) addBinding(target string, in map[string]any) map[string]any {
	s.nextBinding++
	b := map[string]any{
		fieldPolicy: nil, fieldGroup: nil, fieldUser: nil, "negate": false, "enabled": true,
		fieldOrder: float64(0), "timeout": float64(30), "failure_result": false, "expires": nil, "expiring": false,
	}
	maps.Copy(b, in)
	b[fieldPK] = fmt.Sprintf("00000000-0000-0000-0004-%012d", s.nextBinding)
	b[fieldTarget] = target
	s.bindings[str(b, fieldPK)] = b
	return b
}

// bindingJSON is the binding as served.
func (s *Server) bindingJSON(b map[string]any) map[string]any {
	out := maps.Clone(b)
	if _, ok := out["policy_obj"]; !ok {
		out["policy_obj"] = nil
	}
	if _, ok := out["group_obj"]; !ok {
		out["group_obj"] = nil
		if g, ok := s.groups[str(b, fieldGroup)]; ok {
			out["group_obj"] = partialGroup(g)
		}
	}
	if _, ok := out["user_obj"]; !ok {
		out["user_obj"] = nil
		if pk, ok := b[fieldUser].(float64); ok {
			if u, ok := s.users[int32(pk)]; ok {
				out["user_obj"] = partialUser(int32(pk), u)
			}
		}
	}
	return out
}

// sortedBindings returns the bindings that match, ordered by order then pk.
func (s *Server) sortedBindings(match func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, b := range s.bindings {
		if match(b) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b map[string]any) int {
		x, _ := a[fieldOrder].(float64)
		y, _ := b[fieldOrder].(float64)
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
		return strings.Compare(str(a, fieldPK), str(b, fieldPK))
	})
	return out
}

func (s *Server) handleBindings(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	id, _ := splitPath(path, "/policies/bindings/")
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			s.listBindings(w, r.URL.Query().Get(fieldTarget))
		case http.MethodPost:
			s.createBinding(w, body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	b, ok := s.bindings[id]
	if !ok {
		writeNotFound(w)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.patchBinding(w, b, body)
	case http.MethodDelete:
		delete(s.bindings, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// listBindings filters by target. Like django-filter, an empty or absent
// target is no filter: every binding is returned.
func (s *Server) listBindings(w http.ResponseWriter, target string) {
	bindings := s.sortedBindings(func(b map[string]any) bool { return target == "" || b[fieldTarget] == target })
	results := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		results = append(results, s.bindingJSON(b))
	}
	writeJSON(w, http.StatusOK, page(results))
}

func (s *Server) createBinding(w http.ResponseWriter, body map[string]any) {
	errs := missingFields(body, fieldTarget, fieldOrder)
	if errs == nil {
		errs = s.validateBinding(body, body)
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, errs)
		return
	}
	in := maps.Clone(body)
	delete(in, fieldTarget)
	writeJSON(w, http.StatusCreated, s.bindingJSON(s.addBinding(str(body, fieldTarget), in)))
}

func (s *Server) patchBinding(w http.ResponseWriter, b, body map[string]any) {
	merged := maps.Clone(b)
	maps.Copy(merged, body)
	if errs := s.validateBinding(merged, body); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, errs)
		return
	}
	maps.Copy(b, body)
	writeJSON(w, http.StatusOK, s.bindingJSON(b))
}

// validateBinding mimics the server-side checks on the binding that would
// result (merged): exactly one of policy, group and user, and that what the
// request (body) names exists: the target must be an application pk.
func (s *Server) validateBinding(merged, body map[string]any) map[string]any {
	errs := map[string]any{}
	if target, ok := body[fieldTarget]; ok && !s.applicationExists(target) {
		errs[fieldTarget] = invalidPK(target)
	}
	if g, ok := body[fieldGroup]; ok && g != nil {
		if _, exists := s.groups[str(body, fieldGroup)]; !exists {
			errs[fieldGroup] = invalidPK(g)
		}
	}
	if u, ok := body[fieldUser].(float64); ok {
		if _, exists := s.users[int32(u)]; !exists {
			errs[fieldUser] = invalidPK(u)
		}
	}
	set := 0
	for _, f := range []string{fieldPolicy, fieldGroup, fieldUser} {
		if merged[f] != nil {
			set++
		}
	}
	if set != 1 {
		errs["non_field_errors"] = []string{"One of 'policy', 'group' or 'user' must be set."}
	}
	return errs
}

func (s *Server) applicationExists(pk any) bool {
	for _, a := range s.apps {
		if a[fieldPK] == pk {
			return true
		}
	}
	return false
}
