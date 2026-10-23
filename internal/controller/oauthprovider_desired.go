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

// providerName is the provider's name in Authentik.
func providerName(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	if p.Spec.Name != "" {
		return p.Spec.Name
	}
	return p.Name
}

// desiredOAuth2Provider resolves every reference in the spec and returns the
// full request the provider should match. Spec defaults are applied by the
// API server; the fallbacks here only cover objects created before a
// default existed.
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
		r := api.RedirectURIRequest{
			Url:          u.URL,
			MatchingMode: api.MatchingModeEnum(orDefault(string(u.MatchingMode), "strict")),
		}
		t := api.RedirectURITypeEnum(orDefault(string(u.Type), "authorization"))
		r.RedirectUriType = &t
		redirects = append(redirects, r)
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

	scopes := s.Scopes
	if scopes == nil {
		scopes = []string{"openid", "profile", "email"}
	}
	mappings := make([]string, 0, len(scopes)+len(s.PropertyMappings))
	for _, name := range scopes {
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

	req.SetClientType(api.ClientTypeEnum(orDefault(string(s.ClientType), "confidential")))
	grants := s.GrantTypes
	if len(grants) == 0 {
		grants = []authentikiov1alpha1.OAuthGrantType{"authorization_code", "refresh_token"}
	}
	req.GrantTypes = make([]api.GrantTypeEnum, 0, len(grants))
	for _, g := range grants {
		req.GrantTypes = append(req.GrantTypes, api.GrantTypeEnum(g))
	}
	slices.Sort(req.GrantTypes)
	req.SetClientId(expectedClientID(p))

	req.SetAccessCodeValidity(orDefault(s.AccessCodeValidity, "minutes=1"))
	req.SetAccessTokenValidity(orDefault(s.AccessTokenValidity, "minutes=5"))
	req.SetRefreshTokenValidity(orDefault(s.RefreshTokenValidity, "days=30"))
	req.SetRefreshTokenThreshold(orDefault(s.RefreshTokenThreshold, "seconds=0"))
	req.SetSubMode(api.SubModeEnum(orDefault(string(s.SubMode), "hashed_user_id")))
	req.SetIssuerMode(api.IssuerModeEnum(orDefault(string(s.IssuerMode), "per_provider")))
	includeClaims := true
	if s.IncludeClaimsInIDToken != nil {
		includeClaims = *s.IncludeClaimsInIDToken
	}
	req.SetIncludeClaimsInIdToken(includeClaims)

	signing, err := certPK(orDefault(s.SigningKey, "authentik Self-signed Certificate"))
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
	req.SetLogoutMethod(api.OAuth2ProviderLogoutMethodEnum(orDefault(string(s.LogoutMethod), "backchannel")))
	return req, nil
}

// oauth2ProviderPatch compares desired with actual field by field and
// returns a patch containing only the differing fields, plus their names.
// Fields the spec does not manage (client secret, JWT federation) are never
// touched; every managed field has a default, so removing it from the spec
// resets it.
func oauth2ProviderPatch(want *api.OAuth2ProviderRequest, got *api.OAuth2Provider) (api.PatchedOAuth2ProviderRequest, []string) {
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

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
