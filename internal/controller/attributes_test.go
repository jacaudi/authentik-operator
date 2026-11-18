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
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func rawAttributes(values map[string]string) map[string]apiextensionsv1.JSON {
	out := make(map[string]apiextensionsv1.JSON, len(values))
	for k, v := range values {
		out[k] = apiextensionsv1.JSON{Raw: []byte(v)}
	}
	return out
}

func TestSpecAttributes(t *testing.T) {
	t.Run("decodes strings, numbers, booleans and lists", func(t *testing.T) {
		got, err := specAttributes(rawAttributes(map[string]string{"s": `"x"`, "n": "1", "b": "true", "l": `["a"]`}))
		if err != nil {
			t.Fatalf("specAttributes: %v", err)
		}
		want := map[string]any{"s": "x", "n": float64(1), "b": true, "l": []any{"a"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("specAttributes = %#v, want %#v", got, want)
		}
	})
	t.Run("keeps an empty map distinct from an absent one", func(t *testing.T) {
		if got, err := specAttributes(nil); err != nil || got != nil {
			t.Errorf("specAttributes(nil) = %#v, %v; want nil, nil", got, err)
		}
		got, err := specAttributes(map[string]apiextensionsv1.JSON{})
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("specAttributes({}) = %#v, %v; want a non-nil empty map", got, err)
		}
	})
	rejected := []struct{ name, key, raw string }{
		{"an object value", "nested", `{"a": 1}`},
		{"a null value", "n", "null"},
	}
	for _, c := range rejected {
		t.Run("rejects "+c.name, func(t *testing.T) {
			_, err := specAttributes(rawAttributes(map[string]string{c.key: c.raw}))
			if err == nil || !strings.Contains(err.Error(), `"`+c.key+`"`) {
				t.Errorf("specAttributes error = %v, want one naming %q", err, c.key)
			}
		})
	}
}

func TestMergeAttributes(t *testing.T) {
	cases := []struct {
		name        string
		live, want  map[string]any
		owned       []string
		wantMerged  map[string]any
		wantSet     []string
		wantRemoved []string
		wantOwned   []string
	}{
		{"a listed key is set and foreign keys stay",
			map[string]any{"foreign": "f", attrRole: "old"}, map[string]any{attrRole: attrAdmin}, nil,
			map[string]any{"foreign": "f", attrRole: attrAdmin}, []string{attrRole}, nil, []string{attrRole}},
		{"an owned key no longer listed is removed",
			map[string]any{"a": "1", "b": "2"}, map[string]any{"a": "1"}, []string{"a", "b"},
			map[string]any{"a": "1"}, nil, []string{"b"}, []string{"a"}},
		{"an equal value is neither set nor removed",
			map[string]any{"a": float64(1)}, map[string]any{"a": float64(1)}, nil,
			map[string]any{"a": float64(1)}, nil, nil, []string{"a"}},
		{"an empty want removes every owned key and keeps a non-nil map",
			map[string]any{"a": "1"}, map[string]any{}, []string{"a"},
			map[string]any{}, nil, []string{"a"}, nil},
		{"a nil live dict is filled",
			nil, map[string]any{"a": "1"}, nil,
			map[string]any{"a": "1"}, []string{"a"}, nil, []string{"a"}},
		{"owned is sorted",
			nil, map[string]any{"z": "1", "a": "2", "m": "3"}, nil,
			map[string]any{"z": "1", "a": "2", "m": "3"}, []string{"a", "m", "z"}, nil, []string{"a", "m", "z"}},
		{"an owned key absent from live is not reported removed",
			map[string]any{"x": "1"}, map[string]any{}, []string{"gone"},
			map[string]any{"x": "1"}, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := maps.Clone(c.live)
			got := mergeAttributes(c.live, c.want, c.owned)
			if got.Merged == nil || !maps.EqualFunc(got.Merged, c.wantMerged, reflect.DeepEqual) {
				t.Errorf("Merged = %#v, want %#v (non-nil)", got.Merged, c.wantMerged)
			}
			if !slices.Equal(got.Set, c.wantSet) {
				t.Errorf("Set = %v, want %v", got.Set, c.wantSet)
			}
			if !slices.Equal(got.Removed, c.wantRemoved) {
				t.Errorf("Removed = %v, want %v", got.Removed, c.wantRemoved)
			}
			if !slices.Equal(got.Owned, c.wantOwned) {
				t.Errorf("Owned = %v, want %v", got.Owned, c.wantOwned)
			}
			if !maps.EqualFunc(c.live, before, reflect.DeepEqual) {
				t.Errorf("mergeAttributes modified live: %#v, was %#v", c.live, before)
			}
		})
	}
}

// givenNameKey is a key the user kind reserves on top of the shared ones; rawA is a JSON string value.
const (
	givenNameKey = "given_name"
	rawA         = `"a"`
)

func TestReservedAttributes(t *testing.T) {
	cases := []struct {
		name  string
		keys  map[string]string
		extra []string
		want  []string // keys the error must name; nil means no error
	}{
		{"ordinary keys pass", map[string]string{"role": rawA, "team": `"b"`}, nil, nil},
		{"settings is reserved", map[string]string{reservedSettingsKey: rawA}, nil, []string{reservedSettingsKey}},
		{"authentik_operator is reserved", map[string]string{reservedOwnerKey: rawA}, nil,
			[]string{reservedOwnerKey}},
		{"the goauthentik.io/ prefix is reserved", map[string]string{"goauthentik.io/user/x": rawA}, nil,
			[]string{"goauthentik.io/user/x"}},
		{"a key that only contains the prefix passes", map[string]string{"x-goauthentik.io/y": rawA}, nil, nil},
		{"every offending key is named, sorted",
			map[string]string{reservedSettingsKey: `1`, "ok": `1`, reservedOwnerKey: `1`}, nil,
			[]string{reservedOwnerKey, reservedSettingsKey}},
		{"extra keys are reserved too", map[string]string{givenNameKey: rawA, "role": `"b"`}, []string{givenNameKey},
			[]string{givenNameKey}},
		{"an absent map passes", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var spec map[string]apiextensionsv1.JSON
			if c.keys != nil {
				spec = rawAttributes(c.keys)
			}
			err := reservedAttributes(spec, c.extra...)
			if c.want == nil {
				if err != nil {
					t.Errorf("reservedAttributes = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("reservedAttributes = %v, want an error saying reserved", err)
			}
			for _, k := range c.want {
				if !strings.Contains(err.Error(), `"`+k+`"`) {
					t.Errorf("error %q does not name %q", err, k)
				}
			}
			if c.name == "every offending key is named, sorted" &&
				strings.Index(err.Error(), reservedOwnerKey) > strings.Index(err.Error(), reservedSettingsKey) {
				t.Errorf("error %q does not list the keys sorted", err)
			}
		})
	}
}
