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

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// defaultPolicyEngineMode is the policyEngineMode a new application takes when
// the spec leaves it unset; openInNewTab defaults to true and metaHide to false.
const defaultPolicyEngineMode = "any"

// applicationSlug is the application's slug in Authentik.
func applicationSlug(a *authentikiov1alpha1.AuthentikApplication) string {
	if a.Spec.Slug != "" {
		return a.Spec.Slug
	}
	return a.Name
}

// desiredApplication builds the full request the application should match.
// providerPK is 0 for an application without a provider. A field the spec
// leaves unset takes its create default (any, true, false); adoptionViewApplication
// swaps those for the live values when the application is adopted.
func desiredApplication(a *authentikiov1alpha1.AuthentikApplication, providerPK int32) *api.ApplicationRequest {
	s := a.Spec
	req := api.NewApplicationRequest(s.Name, applicationSlug(a))
	if providerPK != 0 {
		req.SetProvider(providerPK)
	} else {
		req.SetProviderNil()
	}
	req.SetGroup(s.Group)
	req.SetPolicyEngineMode(api.PolicyEngineMode(cmp.Or(s.PolicyEngineMode, defaultPolicyEngineMode)))
	req.SetMetaLaunchUrl(s.MetaLaunchURL)
	req.SetMetaDescription(s.MetaDescription)
	req.SetMetaPublisher(s.MetaPublisher)
	req.SetMetaIcon(s.MetaIcon)
	req.SetOpenInNewTab(s.OpenInNewTab == nil || *s.OpenInNewTab)
	req.SetMetaHide(s.MetaHide != nil && *s.MetaHide)
	return req
}

// adoptionViewApplication narrows want, the full desired state, to what
// adoption may write: the fields the spec sets. Optional fields the spec
// leaves empty (group, the meta* strings) and the provider link when
// providerRef is unset keep the adopted application's own value, and so do an
// unset policyEngineMode, openInNewTab and metaHide.
func adoptionViewApplication(a *authentikiov1alpha1.AuthentikApplication, want *api.ApplicationRequest,
	got *api.Application) {
	s := a.Spec
	if s.ProviderRef == nil {
		want.Provider = got.Provider
	}
	if s.Group == "" {
		want.SetGroup(got.GetGroup())
	}
	if s.MetaLaunchURL == "" {
		want.SetMetaLaunchUrl(got.GetMetaLaunchUrl())
	}
	if s.MetaDescription == "" {
		want.SetMetaDescription(got.GetMetaDescription())
	}
	if s.MetaPublisher == "" {
		want.SetMetaPublisher(got.GetMetaPublisher())
	}
	if s.MetaIcon == "" {
		want.SetMetaIcon(got.GetMetaIcon())
	}
	if s.PolicyEngineMode == "" {
		want.SetPolicyEngineMode(got.GetPolicyEngineMode())
	}
	if s.OpenInNewTab == nil {
		want.SetOpenInNewTab(got.GetOpenInNewTab())
	}
	if s.MetaHide == nil {
		want.SetMetaHide(got.GetMetaHide())
	}
}

// applicationPatch returns a patch with only the fields that differ, plus
// their names. The slug is immutable and never patched. Every managed field
// has a default, so removing it from the spec resets it.
func applicationPatch(want *api.ApplicationRequest, got *api.Application) (api.PatchedApplicationRequest, []string) {
	var patch api.PatchedApplicationRequest
	changed := changedFields([]managedField{
		diff("name", want.Name, got.Name, patch.SetName),
		{"provider", int32OrZero(want.Provider.Get()) == int32OrZero(got.Provider.Get()),
			func() { patch.Provider = want.Provider }},
		diff("group", want.GetGroup(), got.GetGroup(), patch.SetGroup),
		diff("policyEngineMode", want.GetPolicyEngineMode(), got.GetPolicyEngineMode(), patch.SetPolicyEngineMode),
		diff("metaLaunchUrl", want.GetMetaLaunchUrl(), got.GetMetaLaunchUrl(), patch.SetMetaLaunchUrl),
		diff("metaDescription", want.GetMetaDescription(), got.GetMetaDescription(), patch.SetMetaDescription),
		diff("metaPublisher", want.GetMetaPublisher(), got.GetMetaPublisher(), patch.SetMetaPublisher),
		diff("metaIcon", want.GetMetaIcon(), got.GetMetaIcon(), patch.SetMetaIcon),
		diff("openInNewTab", want.GetOpenInNewTab(), got.GetOpenInNewTab(), patch.SetOpenInNewTab),
		diff("metaHide", want.GetMetaHide(), got.GetMetaHide(), patch.SetMetaHide),
	})
	return patch, changed
}

func int32OrZero(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}
