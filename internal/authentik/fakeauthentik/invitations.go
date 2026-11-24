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
	"regexp"
	"slices"
	"strings"
)

// Invitations. Names are not unique, as in Authentik: two invitations may
// share one, and a lookup by name can match several.

const fixedDataKey = "fixed_data"

var invitationName = regexp.MustCompile(`^[-a-zA-Z0-9_]+$`)

// SeedInvitation adds an invitation not created through the API, even if one
// with this name exists, and returns its pk.
func (s *Server) SeedInvitation(name string, fields map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := copyFields(fields)
	in[fieldName] = name
	return str(s.addInvitation(in), fieldPK)
}

// Invitations returns copies of every invitation, ordered by name then pk.
func (s *Server) Invitations() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	invitations := s.sortedInvitations(func(map[string]any) bool { return true })
	out := make([]map[string]any, 0, len(invitations))
	for _, inv := range invitations {
		out = append(out, clone(s.invitationJSON(inv)))
	}
	return out
}

// InvitationByName returns a copy of the first invitation with this name, or nil.
func (s *Server) InvitationByName(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := s.firstInvitation(name)
	if inv == nil {
		return nil
	}
	return clone(s.invitationJSON(inv))
}

// IssuedInvitationPKs returns the pk of every invitation ever created, the
// deleted and consumed ones included, in creation order.
func (s *Server) IssuedInvitationPKs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.issuedInvitations)
}

// ConsumeInvitation deletes the first invitation with this name, as Authentik
// does when the first stage of an enrolment flow runs.
func (s *Server) ConsumeInvitation(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumeLocked(name)
}

// EnrolFromInvitation consumes the first invitation with this name and creates
// the user its fixed_data describes (username, name, attributes), using
// username instead when it is not empty. It returns the new user's pk, or 0
// if there is no such invitation.
func (s *Server) EnrolFromInvitation(name, username string) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := s.consumeLocked(name)
	if inv == nil {
		return 0
	}
	fixed, _ := inv[fixedDataKey].(map[string]any)
	in := map[string]any{}
	for _, k := range []string{fieldUsername, fieldName, fieldAttributes} {
		if v, ok := fixed[k]; ok {
			in[k] = v
		}
	}
	in = copyFields(in)
	if username != "" {
		in[fieldUsername] = username
	}
	return s.addUser(in)
}

// LoseNextInvitationCreate makes the next invitation create succeed
// server-side but answer 504.
func (s *Server) LoseNextInvitationCreate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseNextInvitationCreate = true
}

// consumeLocked deletes the first invitation named name and returns it.
func (s *Server) consumeLocked(name string) map[string]any {
	inv := s.firstInvitation(name)
	if inv != nil {
		delete(s.invitations, str(inv, fieldPK))
	}
	return inv
}

func (s *Server) firstInvitation(name string) map[string]any {
	invitations := s.sortedInvitations(func(inv map[string]any) bool { return inv[fieldName] == name })
	if len(invitations) == 0 {
		return nil
	}
	return invitations[0]
}

// sortedInvitations returns the invitations that match, ordered by name then pk.
func (s *Server) sortedInvitations(match func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, inv := range s.invitations {
		if match(inv) {
			out = append(out, inv)
		}
	}
	slices.SortFunc(out, func(a, b map[string]any) int {
		return cmp.Or(
			strings.Compare(str(a, fieldName), str(b, fieldName)),
			strings.Compare(str(a, fieldPK), str(b, fieldPK)),
		)
	})
	return out
}

// addInvitation stores a new invitation with the defaults under in and returns it.
func (s *Server) addInvitation(in map[string]any) map[string]any {
	s.nextInvitation++
	inv := map[string]any{"expires": nil, fixedDataKey: map[string]any{}, "single_use": false, "flow": nil}
	maps.Copy(inv, in)
	pk := fmt.Sprintf("00000000-0000-0000-0002-%012d", s.nextInvitation)
	inv[fieldPK] = pk
	s.invitations[pk] = inv
	s.issuedInvitations = append(s.issuedInvitations, pk)
	return inv
}

// invitationJSON is the invitation as served: created_by is the token user and
// flow_obj the flow the invitation is bound to. The generated client requires
// a flow_obj object, so an invitation without a flow serves the enrollment
// flow there (its flow stays null).
func (s *Server) invitationJSON(inv map[string]any) map[string]any {
	out := maps.Clone(inv)
	out["created_by"] = partialUser(TokenUserPK, s.users[TokenUserPK])
	out["flow_obj"] = nil
	for _, f := range s.flows {
		if f[fieldPK] == inv["flow"] || (inv["flow"] == nil && f[fieldPK] == FlowEnrollmentPK) {
			out["flow_obj"] = f
		}
	}
	return out
}

func (s *Server) handleInvitations(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	id, _ := splitPath(path, "/stages/invitation/invitations/")
	switch {
	case id == "" && r.Method == http.MethodGet:
		name := r.URL.Query().Get(fieldName)
		invitations := s.sortedInvitations(func(inv map[string]any) bool { return name == "" || inv[fieldName] == name })
		results := make([]map[string]any, 0, len(invitations))
		for _, inv := range invitations {
			results = append(results, s.invitationJSON(inv))
		}
		writeJSON(w, http.StatusOK, page(results))
	case id == "" && r.Method == http.MethodPost:
		s.createInvitation(w, body)
	case id != "" && r.Method == http.MethodDelete:
		if _, ok := s.invitations[id]; !ok {
			writeNotFound(w)
			return
		}
		delete(s.invitations, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) createInvitation(w http.ResponseWriter, body map[string]any) {
	errs := missingFields(body, fieldName)
	if errs == nil {
		errs = s.validateInvitation(body)
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, errs)
		return
	}
	inv := s.addInvitation(maps.Clone(body))
	if s.loseNextInvitationCreate {
		s.loseNextInvitationCreate = false
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{fieldDetail: msgTimeout})
		return
	}
	writeJSON(w, http.StatusCreated, s.invitationJSON(inv))
}

// validateInvitation mimics the server-side checks: a slug name, and a flow
// that exists.
func (s *Server) validateInvitation(body map[string]any) map[string]any {
	errs := map[string]any{}
	if !invitationName.MatchString(str(body, fieldName)) {
		errs[fieldName] = []string{"Enter a valid slug consisting of letters, numbers, underscores or hyphens."}
	}
	if flow, ok := body["flow"]; ok && flow != nil && !exists(s.flows, flow) {
		errs["flow"] = invalidPK(flow)
	}
	return errs
}
