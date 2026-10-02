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
	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// applicationSlug is the application's slug in Authentik.
func applicationSlug(a *authentikiov1alpha1.AuthentikApplication) string {
	if a.Spec.Slug != "" {
		return a.Spec.Slug
	}
	return a.Name
}

// desiredApplication builds the full request the application should match.
// providerPK is 0 for an application without a provider.
func desiredApplication(a *authentikiov1alpha1.AuthentikApplication, providerPK int32) *api.ApplicationRequest {
	s := a.Spec
	req := api.NewApplicationRequest(s.Name, applicationSlug(a))
	if providerPK != 0 {
		req.SetProvider(providerPK)
	} else {
		req.SetProviderNil()
	}
	req.SetGroup(s.Group)
	req.SetPolicyEngineMode(api.PolicyEngineMode(orDefault(string(s.PolicyEngineMode), "any")))
	req.SetMetaLaunchUrl(s.MetaLaunchURL)
	req.SetMetaDescription(s.MetaDescription)
	req.SetMetaPublisher(s.MetaPublisher)
	if s.MetaIcon != "" {
		req.SetMetaIcon(s.MetaIcon)
	}
	openInNewTab := true
	if s.OpenInNewTab != nil {
		openInNewTab = *s.OpenInNewTab
	}
	req.SetOpenInNewTab(openInNewTab)
	hide := false
	if s.MetaHide != nil {
		hide = *s.MetaHide
	}
	req.SetMetaHide(hide)
	return req
}

// applicationPatch returns a patch with only the fields that differ, plus
// their names. The slug is immutable and never patched. metaIcon is managed
// only when set, since Authentik may store icons as uploaded files whose
// read-back form differs from what was sent.
func applicationPatch(want *api.ApplicationRequest, got *api.Application) (api.PatchedApplicationRequest, []string) {
	var (
		patch   api.PatchedApplicationRequest
		changed []string
	)
	mark := func(f string) { changed = append(changed, f) }

	if want.Name != got.Name {
		patch.SetName(want.Name)
		mark("name")
	}
	if int32OrZero(want.Provider.Get()) != int32OrZero(got.Provider.Get()) {
		patch.Provider = want.Provider
		mark("provider")
	}
	if want.GetGroup() != got.GetGroup() {
		patch.SetGroup(want.GetGroup())
		mark("group")
	}
	if want.GetPolicyEngineMode() != got.GetPolicyEngineMode() {
		patch.SetPolicyEngineMode(want.GetPolicyEngineMode())
		mark("policyEngineMode")
	}
	if want.GetMetaLaunchUrl() != got.GetMetaLaunchUrl() {
		patch.SetMetaLaunchUrl(want.GetMetaLaunchUrl())
		mark("metaLaunchUrl")
	}
	if want.GetMetaDescription() != got.GetMetaDescription() {
		patch.SetMetaDescription(want.GetMetaDescription())
		mark("metaDescription")
	}
	if want.GetMetaPublisher() != got.GetMetaPublisher() {
		patch.SetMetaPublisher(want.GetMetaPublisher())
		mark("metaPublisher")
	}
	if want.MetaIcon != nil && want.GetMetaIcon() != got.GetMetaIcon() {
		patch.SetMetaIcon(want.GetMetaIcon())
		mark("metaIcon")
	}
	if want.GetOpenInNewTab() != got.GetOpenInNewTab() {
		patch.SetOpenInNewTab(want.GetOpenInNewTab())
		mark("openInNewTab")
	}
	if want.GetMetaHide() != got.GetMetaHide() {
		patch.SetMetaHide(want.GetMetaHide())
		mark("metaHide")
	}
	return patch, changed
}

func int32OrZero(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}
