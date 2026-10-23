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
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

const (
	reasonReferenceNotFound  = "ReferenceNotFound"
	reasonReferenceAmbiguous = "ReferenceAmbiguous"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// referenceError is a spec reference (flow, scope, key, mapping) that does
// not resolve to exactly one Authentik object. It is a user error: reported
// in the Ready condition, retried on the slow interval.
type referenceError struct {
	reason string
	err    error
}

func (e *referenceError) Error() string { return e.err.Error() }
func (e *referenceError) Unwrap() error { return e.err }

func asReferenceError(err error) error {
	switch {
	case errors.Is(err, authentik.ErrNotFound):
		return &referenceError{reason: reasonReferenceNotFound, err: err}
	case errors.Is(err, authentik.ErrAmbiguous):
		return &referenceError{reason: reasonReferenceAmbiguous, err: err}
	default:
		return err
	}
}

// derivedClientID is the client ID used when spec.clientId is unset. Being
// derived from the resource UID, only this resource would ever send it, which
// lets the reconciler recognise its own provider when the outcome of a create
// was lost (timeout, undecodable response, failed status write).
func derivedClientID(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	return "k8s-" + string(p.UID)
}

// expectedClientID is the client ID the provider should have.
func expectedClientID(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	if p.Spec.ClientID != "" {
		return p.Spec.ClientID
	}
	return derivedClientID(p)
}

// ownsProvider reports whether got is the provider this resource created:
// its client ID is the one recorded in status, or the one the spec asks for.
// The second covers a spec.clientId change whose PATCH was applied but whose
// status write was lost; in steady state the two are the same ID.
func ownsProvider(p *authentikiov1alpha1.AuthentikOAuthProvider, got *api.OAuth2Provider) bool {
	id := got.GetClientId()
	return id == p.Status.ClientID || id == expectedClientID(p)
}

// providerName is the provider's name in Authentik.
func providerName(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	if p.Spec.Name != "" {
		return p.Spec.Name
	}
	return p.Name
}

// desiredOAuth2Provider resolves every reference in the spec and returns the
// full request the provider should match. Spec defaults are applied by the
// API server, so every defaulted field is read as is.
func desiredOAuth2Provider(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider) (*api.OAuth2ProviderRequest, error) {
	s := p.Spec

	flowPK := func(slug string) (string, error) {
		f, err := c.FlowBySlug(ctx, slug)
		if err != nil {
			return "", asReferenceError(err)
		}
		return f.Pk, nil
	}
	certPK := func(name string) (string, error) {
		k, err := c.CertificateByName(ctx, name)
		if err != nil {
			return "", asReferenceError(err)
		}
		return k.Pk, nil
	}

	authz, err := flowPK(s.AuthorizationFlow)
	if err != nil {
		return nil, err
	}
	invalidation, err := flowPK(s.InvalidationFlow)
	if err != nil {
		return nil, err
	}

	redirects := make([]api.RedirectURIRequest, 0, len(s.RedirectURIs))
	for _, u := range s.RedirectURIs {
		redirects = append(redirects, api.RedirectURIRequest{
			Url:             u.URL,
			MatchingMode:    api.MatchingModeEnum(u.MatchingMode),
			RedirectUriType: new(api.RedirectURITypeEnum(u.Type)),
		})
	}

	req := api.NewOAuth2ProviderRequest(providerName(p), authz, invalidation, redirects)

	if s.AuthenticationFlow != "" {
		pk, err := flowPK(s.AuthenticationFlow)
		if err != nil {
			return nil, err
		}
		req.SetAuthenticationFlow(pk)
	} else {
		req.SetAuthenticationFlowNil()
	}

	mappings := make([]string, 0, len(s.Scopes)+len(s.PropertyMappings))
	for _, name := range s.Scopes {
		m, err := c.ScopeMappingByScopeName(ctx, name)
		if err != nil {
			return nil, asReferenceError(err)
		}
		mappings = append(mappings, m.Pk)
	}
	for _, ref := range s.PropertyMappings {
		var (
			m   *api.PropertyMapping
			err error
		)
		if uuidPattern.MatchString(ref) {
			m, err = c.PropertyMappingByUUID(ctx, ref)
		} else {
			m, err = c.PropertyMappingByName(ctx, ref)
		}
		if err != nil {
			return nil, asReferenceError(fmt.Errorf("property mapping %q: %w", ref, err))
		}
		mappings = append(mappings, m.Pk)
	}
	slices.Sort(mappings)
	req.PropertyMappings = slices.Compact(mappings)

	req.SetClientType(api.ClientTypeEnum(s.ClientType))
	req.GrantTypes = make([]api.GrantTypeEnum, 0, len(s.GrantTypes))
	for _, g := range s.GrantTypes {
		req.GrantTypes = append(req.GrantTypes, api.GrantTypeEnum(g))
	}
	slices.Sort(req.GrantTypes)
	req.SetClientId(expectedClientID(p))

	req.SetAccessCodeValidity(s.AccessCodeValidity)
	req.SetAccessTokenValidity(s.AccessTokenValidity)
	req.SetRefreshTokenValidity(s.RefreshTokenValidity)
	req.SetRefreshTokenThreshold(s.RefreshTokenThreshold)
	req.SetSubMode(api.SubModeEnum(s.SubMode))
	req.SetIssuerMode(api.IssuerModeEnum(s.IssuerMode))
	req.SetIncludeClaimsInIdToken(*s.IncludeClaimsInIDToken)

	signing, err := certPK(s.SigningKey)
	if err != nil {
		return nil, err
	}
	req.SetSigningKey(signing)
	if s.EncryptionKey != "" {
		pk, err := certPK(s.EncryptionKey)
		if err != nil {
			return nil, err
		}
		req.SetEncryptionKey(pk)
	} else {
		req.SetEncryptionKeyNil()
	}

	req.SetLogoutUri(s.LogoutURI)
	req.SetLogoutMethod(api.OAuth2ProviderLogoutMethodEnum(s.LogoutMethod))
	return req, nil
}

// oauth2ProviderPatch compares desired with actual field by field and
// returns a patch containing only the differing fields, plus their names.
// Fields the spec does not manage (client secret, JWT federation) are never
// touched; every managed field has a default, so removing it from the spec
// resets it.
func oauth2ProviderPatch(want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) (api.PatchedOAuth2ProviderRequest, []string) {
	var (
		patch   api.PatchedOAuth2ProviderRequest
		changed []string
	)
	mark := func(field string) { changed = append(changed, field) }

	if want.Name != got.Name {
		patch.SetName(want.Name)
		mark("name")
	}
	if want.AuthorizationFlow != got.AuthorizationFlow {
		patch.SetAuthorizationFlow(want.AuthorizationFlow)
		mark("authorizationFlow")
	}
	if want.InvalidationFlow != got.InvalidationFlow {
		patch.SetInvalidationFlow(want.InvalidationFlow)
		mark("invalidationFlow")
	}
	if nullable(want.AuthenticationFlow.Get()) != nullable(got.AuthenticationFlow.Get()) {
		patch.AuthenticationFlow = want.AuthenticationFlow
		mark("authenticationFlow")
	}
	if !sameSet(want.PropertyMappings, got.PropertyMappings) {
		patch.PropertyMappings = want.PropertyMappings
		mark("propertyMappings")
	}
	if want.GetClientType() != got.GetClientType() {
		patch.SetClientType(want.GetClientType())
		mark("clientType")
	}
	if !sameSet(want.GrantTypes, got.GrantTypes) {
		patch.GrantTypes = want.GrantTypes
		mark("grantTypes")
	}
	if want.GetClientId() != got.GetClientId() {
		patch.SetClientId(want.GetClientId())
		mark("clientId")
	}
	if want.GetAccessCodeValidity() != got.GetAccessCodeValidity() {
		patch.SetAccessCodeValidity(want.GetAccessCodeValidity())
		mark("accessCodeValidity")
	}
	if want.GetAccessTokenValidity() != got.GetAccessTokenValidity() {
		patch.SetAccessTokenValidity(want.GetAccessTokenValidity())
		mark("accessTokenValidity")
	}
	if want.GetRefreshTokenValidity() != got.GetRefreshTokenValidity() {
		patch.SetRefreshTokenValidity(want.GetRefreshTokenValidity())
		mark("refreshTokenValidity")
	}
	if want.GetRefreshTokenThreshold() != got.GetRefreshTokenThreshold() {
		patch.SetRefreshTokenThreshold(want.GetRefreshTokenThreshold())
		mark("refreshTokenThreshold")
	}
	if want.GetIncludeClaimsInIdToken() != got.GetIncludeClaimsInIdToken() {
		patch.SetIncludeClaimsInIdToken(want.GetIncludeClaimsInIdToken())
		mark("includeClaimsInIdToken")
	}
	if nullable(want.SigningKey.Get()) != nullable(got.SigningKey.Get()) {
		patch.SigningKey = want.SigningKey
		mark("signingKey")
	}
	if nullable(want.EncryptionKey.Get()) != nullable(got.EncryptionKey.Get()) {
		patch.EncryptionKey = want.EncryptionKey
		mark("encryptionKey")
	}
	if !sameRedirects(want.RedirectUris, got.RedirectUris) {
		patch.RedirectUris = want.RedirectUris
		mark("redirectUris")
	}
	if want.GetLogoutUri() != got.GetLogoutUri() {
		patch.SetLogoutUri(want.GetLogoutUri())
		mark("logoutUri")
	}
	if want.GetLogoutMethod() != got.GetLogoutMethod() {
		patch.SetLogoutMethod(want.GetLogoutMethod())
		mark("logoutMethod")
	}
	if want.GetSubMode() != got.GetSubMode() {
		patch.SetSubMode(want.GetSubMode())
		mark("subMode")
	}
	if want.GetIssuerMode() != got.GetIssuerMode() {
		patch.SetIssuerMode(want.GetIssuerMode())
		mark("issuerMode")
	}
	return patch, changed
}

func sameSet[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[T]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		if seen[v] == 0 {
			return false
		}
		seen[v]--
	}
	return true
}

func sameRedirects(want []api.RedirectURIRequest, got []api.RedirectURI) bool {
	if len(want) != len(got) {
		return false
	}
	typeOf := func(t *api.RedirectURITypeEnum) api.RedirectURITypeEnum {
		if t == nil {
			return api.REDIRECTURITYPEENUM_AUTHORIZATION
		}
		return *t
	}
	for i := range want {
		if want[i].Url != got[i].Url || want[i].MatchingMode != got[i].MatchingMode ||
			typeOf(want[i].RedirectUriType) != typeOf(got[i].RedirectUriType) {
			return false
		}
	}
	return true
}

func nullable(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
