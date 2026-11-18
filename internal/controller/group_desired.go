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
	"cmp"
	"maps"
	"slices"

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// groupName is the group's name in Authentik: spec.name, else metadata.name.
func groupName(g *authentikiov1alpha1.AuthentikGroup) string {
	return cmp.Or(g.Spec.Name, g.Name)
}

// desiredGroup builds the create request: the name, isSuperuser (unset is
// false), and the spec attributes with this resource's ownership proof. It
// never carries users: membership changes only through add_user and
// remove_user.
func desiredGroup(g *authentikiov1alpha1.AuthentikGroup, want map[string]any) *api.GroupRequest {
	req := api.NewGroupRequest(groupName(g))
	req.SetIsSuperuser(g.Spec.IsSuperuser != nil && *g.Spec.IsSuperuser)
	attrs := maps.Clone(want)
	if attrs == nil {
		attrs = map[string]any{}
	}
	maps.Copy(attrs, authentik.OwnerProof(string(g.UID)))
	req.SetAttributes(attrs)
	return req
}

// groupChanges is what reconciling a group would write.
type groupChanges struct {
	// patch holds only the fields that differ.
	patch api.PatchedGroupRequest
	// changed names them: "name", "is_superuser" and "attributes.<key>".
	changed []string
	// owned is the new status.managedAttributes, meaningful when the spec
	// lists attributes.
	owned []string
}

// planGroup compares the spec with live. name is always compared;
// isSuperuser is the spec value when set, else false on a group this resource
// created and untouched on any other; attributes only when the spec lists
// them (even an empty map), by owned key.
func planGroup(g *authentikiov1alpha1.AuthentikGroup, want map[string]any, live *api.Group) groupChanges {
	var plan groupChanges
	fields := []managedField{diff("name", groupName(g), live.Name, plan.patch.SetName)}
	switch {
	case g.Spec.IsSuperuser != nil:
		fields = append(fields, diff("is_superuser", *g.Spec.IsSuperuser, live.GetIsSuperuser(), plan.patch.SetIsSuperuser))
	case g.Status.Provenance == authentikiov1alpha1.ProvenanceCreated:
		fields = append(fields, diff("is_superuser", false, live.GetIsSuperuser(), plan.patch.SetIsSuperuser))
	}
	plan.changed = changedFields(fields)
	if want == nil {
		return plan
	}
	merge := mergeAttributes(live.Attributes, want, g.Status.ManagedAttributes)
	plan.owned = merge.Owned
	if keys := slices.Sorted(slices.Values(slices.Concat(merge.Set, merge.Removed))); len(keys) > 0 {
		plan.patch.SetAttributes(merge.Merged)
		for _, key := range keys {
			plan.changed = append(plan.changed, "attributes."+key)
		}
	}
	return plan
}
