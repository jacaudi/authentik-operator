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
)

func grantsWanted() *api.OAuth2ProviderRequest {
	req := api.NewOAuth2ProviderRequest("p", "authz", "inval", nil)
	req.GrantTypes = []api.GrantTypeEnum{api.GRANTTYPEENUM_AUTHORIZATION_CODE, api.GRANTTYPEENUM_REFRESH_TOKEN}
	return req
}

// Authentik 2026.2 has no grant_types property: its providers decode with a
// nil GrantTypes, forever. The end-to-end suite caught the operator sending a
// PATCH for grantTypes on every reconcile (an Updated event each resync, Ready
// still True). A property the server does not report cannot differ.
func TestOAuth2ProviderPatchIgnoresGrantTypesOlderServersDoNotReport(t *testing.T) {
	got := &api.OAuth2Provider{Name: "p", GrantTypes: nil}
	_, changed := oauth2ProviderPatch(grantsWanted(), got)
	if slices.Contains(changed, "grantTypes") {
		t.Fatalf("grantTypes reported as changed against a server that reports none: %v", changed)
	}
}

// 2026.8 always reports the list; an EMPTY list there is a real difference
// (the #279 case: logins fail until grant types are set) and must be patched.
func TestOAuth2ProviderPatchStillFixesAnEmptyGrantTypeList(t *testing.T) {
	got := &api.OAuth2Provider{Name: "p", GrantTypes: []api.GrantTypeEnum{}}
	_, changed := oauth2ProviderPatch(grantsWanted(), got)
	if !slices.Contains(changed, "grantTypes") {
		t.Fatalf("an empty reported list must be patched, changed=%v", changed)
	}
}

func TestOAuth2ProviderPatchGrantTypesInSyncIsNoChange(t *testing.T) {
	got := &api.OAuth2Provider{Name: "p", GrantTypes: []api.GrantTypeEnum{
		api.GRANTTYPEENUM_REFRESH_TOKEN, api.GRANTTYPEENUM_AUTHORIZATION_CODE}}
	_, changed := oauth2ProviderPatch(grantsWanted(), got)
	if slices.Contains(changed, "grantTypes") {
		t.Fatalf("in-sync grant types reported as changed: %v", changed)
	}
}
