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

package controller

import (
	"slices"
	"time"

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// desiredInvitation builds the create request of u's invitation: single use,
// bound to the enrolment flow, and pinning the username with fixed_data the
// user-write stage merges into the new account, together with the ownership
// proof that lets the operator recognise the account as the one it invited.
func desiredInvitation(u *authentikiov1alpha1.AuthentikUser, name, flowPK string,
	expires time.Time) *api.InvitationRequest {
	fixed := map[string]any{
		"username":   u.Spec.Username,
		"attributes": authentik.OwnerProof(string(u.UID)),
	}
	if u.Spec.Name != "" {
		fixed["name"] = u.Spec.Name
	}
	req := api.NewInvitationRequest(name)
	req.SetSingleUse(true)
	req.SetFlow(flowPK)
	req.SetExpires(expires)
	req.SetFixedData(fixed)
	return req
}

// userChanges is what reconciling a user would write.
type userChanges struct {
	// patch holds only the fields that differ. It never carries groups or
	// roles: neither is managed here.
	patch api.PatchedUserRequest
	// changed names them: Authentik's field names and "attributes.<key>".
	changed []string
	// owned is the new status.managedAttributes: every key spec.attributes
	// lists, none when the spec leaves attributes out.
	owned []string
	// set and removed are the attribute keys the patch adds or changes, and
	// deletes, sorted.
	set, removed []string
}

// planUser compares the spec with live. name, email, path, type and isActive
// are compared only when the spec sets them (unset keeps live, whatever the
// provenance). Attributes have two independent writers, each key having one:
// spec.attributes, by key, when the spec lists it (owned holds the keys the
// operator set before), and firstName and lastName, which set given_name and
// family_name whenever they are set, whether or not spec.attributes is listed,
// and leave the live value alone when unset. Authentik replaces attributes
// whole, so a change to either part sends the whole resulting dict.
func planUser(u *authentikiov1alpha1.AuthentikUser, want map[string]any, live *api.User,
	owned []string) userChanges {
	var plan userChanges
	spec := &u.Spec
	var fields []managedField
	if spec.Name != "" {
		fields = append(fields, diff("name", spec.Name, live.Name, plan.patch.SetName))
	}
	if spec.Email != "" {
		fields = append(fields, diff("email", spec.Email, live.GetEmail(), plan.patch.SetEmail))
	}
	if spec.Path != "" {
		fields = append(fields, diff("path", spec.Path, live.GetPath(), plan.patch.SetPath))
	}
	if spec.Type != "" {
		fields = append(fields, diff("type", api.UserTypeEnum(spec.Type), live.GetType(), plan.patch.SetType))
	}
	if spec.IsActive != nil {
		fields = append(fields, diff("is_active", *spec.IsActive, live.GetIsActive(), plan.patch.SetIsActive))
	}
	plan.changed = changedFields(fields)

	if want == nil {
		owned = nil // releasing the field releases the keys: none is set or removed by this part
	}
	merge := mergeAttributes(live.Attributes, want, owned)
	if want != nil {
		plan.owned = merge.Owned
	}
	set := merge.Set
	for _, key := range []struct{ name, value string }{
		{attrGivenName, spec.FirstName}, {attrFamilyName, spec.LastName},
	} {
		if key.value != "" && merge.Merged[key.name] != key.value {
			merge.Merged[key.name] = key.value
			set = append(set, key.name)
		}
	}
	slices.Sort(set)
	plan.set, plan.removed = set, merge.Removed
	if keys := slices.Sorted(slices.Values(slices.Concat(set, merge.Removed))); len(keys) > 0 {
		plan.patch.SetAttributes(merge.Merged)
		for _, key := range keys {
			plan.changed = append(plan.changed, "attributes."+key)
		}
	}
	return plan
}

// reportView is the patch and the live user as a dry-run or drift message
// renders them. The attributes entry holds only the keys the patch sets or
// removes (a removed key as null): the merged dict is the whole live dict, so
// rendering it would cut the changed keys off behind the keys that sort first,
// and would print the values of keys the operator does not own.
func (p userChanges) reportView(live *api.User) (patch, current map[string]any) {
	patch, current = jsonObject(p.patch), jsonObject(live)
	if len(p.set)+len(p.removed) == 0 {
		return patch, current
	}
	want, have := map[string]any{}, map[string]any{}
	for _, key := range p.set {
		want[key] = p.patch.Attributes[key]
		if v, ok := live.Attributes[key]; ok {
			have[key] = v
		}
	}
	for _, key := range p.removed {
		want[key], have[key] = nil, live.Attributes[key]
	}
	patch["attributes"], current["attributes"] = want, have
	return patch, current
}
