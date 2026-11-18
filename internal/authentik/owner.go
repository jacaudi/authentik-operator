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

// Ownership proof. The operator marks every group and user it created, and
// every user it adopted through an invitation, with its owner's metadata.uid
// under this attribute key, and trusts no object without the proof. The
// attribute is a nested dict because Authentik splits attribute paths on
// "__" when filtering and on dots elsewhere. This file is the single source
// of the shape.
const (
	// OwnerAttribute is the attributes key that holds the ownership proof.
	OwnerAttribute = "authentik_operator"
	// OwnerUIDKey is the key inside OwnerAttribute that holds the owner's uid.
	OwnerUIDKey = "owner_uid"
)

// OwnerProof returns the attributes that prove uid owns an object:
// {"authentik_operator": {"owner_uid": uid}}.
func OwnerProof(uid string) map[string]any {
	return map[string]any{OwnerAttribute: map[string]any{OwnerUIDKey: uid}}
}

// HasOwnerProof reports whether attrs carry the ownership proof for uid. An
// empty uid proves nothing.
func HasOwnerProof(attrs map[string]any, uid string) bool {
	proof, ok := attrs[OwnerAttribute].(map[string]any)
	if !ok || uid == "" {
		return false
	}
	got, ok := proof[OwnerUIDKey].(string)
	return ok && got == uid
}
