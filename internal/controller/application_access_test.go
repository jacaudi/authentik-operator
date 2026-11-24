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
	"testing"

	api "goauthentik.io/api/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// bindingTarget is the application pk the test bindings belong to.
const bindingTarget = "app"

// bindingOpt adjusts a binding built by the test helpers.
type bindingOpt func(*api.PolicyBinding)

func negated(b *api.PolicyBinding)  { b.SetNegate(true) }
func disabled(b *api.PolicyBinding) { b.SetEnabled(false) }
func expiring(b *api.PolicyBinding) { b.Expiring = true }

// groupBinding is a binding of the group named name (pk "pk-<name>").
func groupBinding(pk, name string, order int32, opts ...bindingOpt) api.PolicyBinding {
	groupPK := "pk-" + name
	b := api.PolicyBinding{Pk: pk, Target: bindingTarget, Order: order}
	b.Group = *api.NewNullableString(&groupPK)
	b.GroupObj = *api.NewNullablePartialGroup(&api.PartialGroup{Pk: groupPK, Name: name})
	for _, o := range opts {
		o(&b)
	}
	return b
}

func userBinding(pk, username string, order int32) api.PolicyBinding {
	n := int32(7)
	b := api.PolicyBinding{Pk: pk, Target: bindingTarget, Order: order}
	b.User = *api.NewNullableInt32(&n)
	b.UserObj = *api.NewNullablePartialUser(&api.PartialUser{Pk: n, Username: username})
	return b
}

func policyBinding(pk, name string, order int32) api.PolicyBinding {
	policyPK := "policy-" + name
	b := api.PolicyBinding{Pk: pk, Target: bindingTarget, Order: order}
	b.Policy = *api.NewNullableString(&policyPK)
	b.PolicyObj = *api.NewNullablePolicy(&api.Policy{Pk: policyPK, Name: name})
	return b
}

// resolved is accessGroups with every name resolved to pk-<name>.
func resolved(names ...string) accessGroups {
	g := accessGroups{pks: map[string]string{}}
	for _, n := range names {
		g.pks[n] = "pk-" + n
	}
	return g
}

func bindingPKs(bs []api.PolicyBinding) []string {
	var pks []string
	for i := range bs {
		pks = append(pks, bs[i].Pk)
	}
	return pks
}

func TestPlanBindings(t *testing.T) {
	const anyMode, allMode = api.POLICYENGINEMODE_ANY, api.POLICYENGINEMODE_ALL
	missing := resolved("a")
	missing.missing = []string{"unresolved"}
	tests := []struct {
		name       string
		live       []api.PolicyBinding
		groups     accessGroups
		mode       api.PolicyEngineMode
		wantAdd    []string
		wantEnable []string
		wantRemove []string
		wantNext   int32
	}{
		{
			name:     "adds a missing group one past the highest live order",
			live:     []api.PolicyBinding{userBinding("u", "bob", 3)},
			groups:   resolved("a"),
			mode:     anyMode,
			wantAdd:  []string{"a"},
			wantNext: 4,
		},
		{
			name:     "two adds are ordered by name and take consecutive orders from NextOrder",
			live:     []api.PolicyBinding{userBinding("u", "bob", 3)},
			groups:   resolved("b", "a"),
			mode:     anyMode,
			wantAdd:  []string{"a", "b"},
			wantNext: 4,
		},
		{
			name:     "no live binding starts at order 0",
			groups:   resolved("a"),
			mode:     anyMode,
			wantAdd:  []string{"a"},
			wantNext: 0,
		},
		{
			name:       "removes a managed binding of an unlisted group",
			live:       []api.PolicyBinding{groupBinding("old1", "old", 1), groupBinding("a1", "a", 2)},
			groups:     resolved("a"),
			mode:       anyMode,
			wantRemove: []string{"old1"},
			wantNext:   3,
		},
		{
			name:       "keeps the lowest order of duplicates and removes the rest",
			live:       []api.PolicyBinding{groupBinding("a2", "a", 2), groupBinding("a1", "a", 1)},
			groups:     resolved("a"),
			mode:       anyMode,
			wantRemove: []string{"a2"},
			wantNext:   3,
		},
		{
			name:       "ties on order keep the lowest pk",
			live:       []api.PolicyBinding{groupBinding("a-z", "a", 1), groupBinding("a-b", "a", 1)},
			groups:     resolved("a"),
			mode:       anyMode,
			wantRemove: []string{"a-z"},
			wantNext:   2,
		},
		{
			name:       "enables a disabled managed binding",
			live:       []api.PolicyBinding{groupBinding("a1", "a", 1, disabled)},
			groups:     resolved("a"),
			mode:       anyMode,
			wantEnable: []string{"a1"},
			wantNext:   2,
		},
		{
			name:       "mode any removes negated group bindings, listed or not",
			live:       []api.PolicyBinding{groupBinding("n1", "a", 1, negated), groupBinding("n2", "x", 2, negated)},
			groups:     resolved("a"),
			mode:       anyMode,
			wantAdd:    []string{"a"},
			wantRemove: []string{"n1", "n2"},
			wantNext:   3,
		},
		{
			name:     "mode all leaves negated group bindings alone",
			live:     []api.PolicyBinding{groupBinding("n1", "a", 1, negated), groupBinding("n2", "x", 2, negated)},
			groups:   resolved("a"),
			mode:     allMode,
			wantAdd:  []string{"a"},
			wantNext: 3,
		},
		{
			name:     "an unset mode is any",
			live:     []api.PolicyBinding{groupBinding("n1", "x", 1, negated)},
			groups:   resolved("a"),
			mode:     "",
			wantAdd:  []string{"a"},
			wantNext: 2, wantRemove: []string{"n1"},
		},
		{
			name: "expiring, user and policy bindings are in no list, under any",
			live: []api.PolicyBinding{
				groupBinding("e1", "x", 1, expiring), groupBinding("e2", "a", 2, expiring, disabled),
				userBinding("u", "bob", 3), policyBinding("p", "p", 4),
			},
			groups:   resolved("a"),
			mode:     anyMode,
			wantAdd:  []string{"a"},
			wantNext: 5,
		},
		{
			name: "expiring, user and policy bindings are in no list, under all",
			live: []api.PolicyBinding{
				groupBinding("e1", "x", 1, expiring), groupBinding("e2", "a", 2, expiring, negated),
				userBinding("u", "bob", 3), policyBinding("p", "p", 4),
			},
			groups:   resolved("a"),
			mode:     allMode,
			wantAdd:  []string{"a"},
			wantNext: 5,
		},
		{
			name:     "an unresolved group removes nothing",
			live:     []api.PolicyBinding{groupBinding("old1", "old", 1), groupBinding("n1", "x", 2, negated)},
			groups:   missing,
			mode:     anyMode,
			wantAdd:  []string{"a"},
			wantNext: 3,
		},
		{
			name:     "a bound group with no change plans nothing",
			live:     []api.PolicyBinding{groupBinding("a1", "a", 1)},
			groups:   resolved("a"),
			mode:     anyMode,
			wantNext: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planBindings(tt.live, tt.groups, tt.mode)
			if !slices.Equal(got.Add, tt.wantAdd) {
				t.Errorf("Add = %v, want %v", got.Add, tt.wantAdd)
			}
			if pks := bindingPKs(got.Enable); !slices.Equal(pks, tt.wantEnable) {
				t.Errorf("Enable = %v, want %v", pks, tt.wantEnable)
			}
			if pks := bindingPKs(got.Remove); !slices.Equal(pks, tt.wantRemove) {
				t.Errorf("Remove = %v, want %v", pks, tt.wantRemove)
			}
			if got.NextOrder != tt.wantNext {
				t.Errorf("NextOrder = %d, want %d", got.NextOrder, tt.wantNext)
			}
		})
	}
}

func TestDescribeBinding(t *testing.T) {
	tests := []struct {
		name string
		b    api.PolicyBinding
		want string
	}{
		{"group binding", groupBinding("1", "a", 0), "group a"},
		{"negated group", groupBinding("1", "a", 0, negated), "group a (negated)"},
		{"user", userBinding("1", "bob", 0), "user bob"},
		{"disabled policy", func() api.PolicyBinding {
			b := policyBinding("1", "p", 0)
			b.SetEnabled(false)
			return b
		}(), "policy p (disabled)"},
		{"every suffix, in order", groupBinding("1", "a", 0, negated, expiring, disabled),
			"group a (negated) (expiring) (disabled)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeBinding(tt.b); got != tt.want {
				t.Errorf("describeBinding = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccessGroupIndex(t *testing.T) {
	app := func(a *authentikiov1alpha1.ApplicationAccess) *authentikiov1alpha1.AuthentikApplication {
		return &authentikiov1alpha1.AuthentikApplication{
			ObjectMeta: metav1.ObjectMeta{Name: "x"},
			Spec:       authentikiov1alpha1.AuthentikApplicationSpec{Access: a},
		}
	}
	if got := accessGroupIndex(app(nil)); got != nil {
		t.Errorf("an application without access must not be indexed, got %v", got)
	}
	unmanaged := &authentikiov1alpha1.ApplicationAccess{Type: authentikiov1alpha1.AccessTypeUnmanaged}
	if got := accessGroupIndex(app(unmanaged)); got != nil {
		t.Errorf("an Unmanaged application must not be indexed, got %v", got)
	}
	groups := &authentikiov1alpha1.ApplicationAccess{
		Type: authentikiov1alpha1.AccessTypeGroups, Groups: []string{"a", "b"},
	}
	if got := accessGroupIndex(app(groups)); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("accessGroupIndex = %v, want [a b]", got)
	}
}

func TestGroupNameIndex(t *testing.T) {
	g := &authentikiov1alpha1.AuthentikGroup{ObjectMeta: metav1.ObjectMeta{Name: groupResource}}
	if got := groupNameIndex(g); !slices.Equal(got, []string{groupResource}) {
		t.Errorf("without spec.name the metadata.name is indexed, got %v", got)
	}
	g.Spec.Name = immichGroup
	if got := groupNameIndex(g); !slices.Equal(got, []string{immichGroup}) {
		t.Errorf("spec.name is indexed, never metadata.name, got %v", got)
	}
}
