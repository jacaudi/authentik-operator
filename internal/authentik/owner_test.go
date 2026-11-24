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

import "testing"

func TestReservedAttribute(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"role", false},
		{"team", false},
		{OwnerAttribute, true},
		{ReservedSettingsKey, true},
		{ReservedAttributePrefix + "user/x", true},
		{ReservedAttributePrefix, true},
		{"x-" + ReservedAttributePrefix + "y", false}, // only a prefix is reserved
		{"settings2", false},                          // exact keys, not prefixes
		{OwnerAttribute + "_x", false},
	}
	for _, c := range cases {
		if got := ReservedAttribute(c.key); got != c.want {
			t.Errorf("ReservedAttribute(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}
