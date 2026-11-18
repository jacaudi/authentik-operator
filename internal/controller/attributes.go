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
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// specAttributes decodes spec.attributes. nil stays nil (the field is absent:
// Authentik's attributes are neither read nor written), and an empty map stays
// a non-nil empty map (every attribute the operator set is removed). A value
// is a string, number, boolean or list: an object or null has no single owner
// per key, so it is refused, naming the key.
func specAttributes(spec map[string]apiextensionsv1.JSON) (map[string]any, error) {
	if spec == nil {
		return nil, nil
	}
	out := make(map[string]any, len(spec))
	for _, key := range slices.Sorted(maps.Keys(spec)) {
		var v any
		if err := json.Unmarshal(spec[key].Raw, &v); err != nil {
			return nil, fmt.Errorf("attribute %q is not valid JSON: %w", key, err)
		}
		switch v.(type) {
		case nil:
			return nil, fmt.Errorf("attribute %q is null; use a string, number, boolean or list, "+
				"or remove the key to stop managing it", key)
		case map[string]any:
			return nil, fmt.Errorf("attribute %q is an object; the operator owns whole top-level values, "+
				"so use a string, number, boolean or list", key)
		}
		out[key] = v
	}
	return out, nil
}

// attributeMerge is the outcome of mergeAttributes.
type attributeMerge struct {
	// Merged is the live attributes with the listed keys set and the owned,
	// no longer listed keys removed. It is never nil.
	Merged map[string]any
	// Owned is every listed key, sorted: the new status.managedAttributes.
	Owned []string
	// Set lists the keys whose value is added or changed; Removed the owned
	// keys deleted from the live attributes. Both are sorted.
	Set, Removed []string
}

// mergeAttributes applies want to live under the per-key ownership model:
// Authentik replaces the whole attributes dict on every write, so the
// operator sets the keys it lists, deletes the keys it owned (owned) and no
// longer lists, and keeps every other key. live is not modified.
func mergeAttributes(live, want map[string]any, owned []string) attributeMerge {
	m := attributeMerge{Merged: maps.Clone(live), Owned: slices.Sorted(maps.Keys(want))}
	if m.Merged == nil {
		m.Merged = map[string]any{}
	}
	for _, key := range m.Owned {
		if cur, ok := m.Merged[key]; !ok || !reflect.DeepEqual(cur, want[key]) {
			m.Merged[key] = want[key]
			m.Set = append(m.Set, key)
		}
	}
	for _, key := range slices.Sorted(slices.Values(owned)) {
		if _, listed := want[key]; listed {
			continue
		}
		if _, present := m.Merged[key]; present {
			delete(m.Merged, key)
			m.Removed = append(m.Removed, key)
		}
	}
	return m
}

// Attribute keys the operator or Authentik owns. A CEL rule cannot refuse them
// at admission (the apiserver cannot type a map of untyped values), so
// reservedAttributes does it in the reconciler.
const (
	reservedAttributePrefix = "goauthentik.io/"
	reservedOwnerKey        = authentik.OwnerAttribute
	reservedSettingsKey     = "settings"
)

// reservedAttributes refuses a spec that lists a key the operator must not
// own: one starting with goauthentik.io/ (Authentik's own), the ownership
// proof key authentik_operator, and settings (the user's own settings), plus
// any extra keys the kind reserves (a user also reserves given_name and
// family_name). The error names every offending key, sorted. It reads nothing
// and runs before any call to Authentik.
func reservedAttributes(spec map[string]apiextensionsv1.JSON, extra ...string) error {
	var bad []string
	for _, key := range slices.Sorted(maps.Keys(spec)) {
		if strings.HasPrefix(key, reservedAttributePrefix) || key == reservedOwnerKey ||
			key == reservedSettingsKey || slices.Contains(extra, key) {
			bad = append(bad, strconv.Quote(key))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("spec.attributes lists reserved key(s) %s: keys starting with %s, and %s and %s, belong to "+
		"Authentik or the operator; remove them", strings.Join(bad, ", "), reservedAttributePrefix,
		reservedOwnerKey, reservedSettingsKey)
}
