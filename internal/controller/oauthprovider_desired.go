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

// defaultSigningKeyName is the certificate-key pair a new provider signs
// with when spec.signingKey is empty. It is applied here rather than as a
// schema default so that adopting a provider with another (or no) signing
// key does not rewrite it.
const defaultSigningKeyName = "authentik Self-signed Certificate"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// referenceError reports a spec reference (flow, scope, key, mapping) that
// does not resolve to exactly one Authentik object under the matching
// condition reason; any other error is returned as is.
func referenceError(err error) error {
	switch {
	case errors.Is(err, authentik.ErrNotFound):
		return condErr(reasonReferenceNotFound, err)
	case errors.Is(err, authentik.ErrAmbiguous):
		return condErr(reasonReferenceAmbiguous, err)
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

// expectedClientID is the client ID the provider should have: the pinned
// spec.clientId, else for an adopted provider the one it already has
// (recorded in status when it was adopted; adoption never rewrites it), else
// the derived one.
func expectedClientID(p *authentikiov1alpha1.AuthentikOAuthProvider) string {
	switch {
	case p.Spec.ClientID != "":
		return p.Spec.ClientID
	case p.Status.Provenance == authentikiov1alpha1.ProvenanceAdopted && p.Status.ClientID != "":
		return p.Status.ClientID
	default:
		return derivedClientID(p)
	}
}

// adoptionView narrows want, the full desired state, to what adoption may
// write: the fields the spec sets. Where the spec says nothing, the adopted
// provider's own value stands -- the client ID (unless spec.clientId is
// set), the name (unless spec.name is set) and the optional authenticationFlow,
// encryptionKey, logoutUri and signingKey. Fields with a CRD default are always "set"
// (the API server fills them), so they stay in want; see APIAdoptionSpec.
func adoptionView(p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) {
	s := p.Spec
	if s.ClientID == "" {
		want.SetClientId(got.GetClientId())
	}
	if s.Name == "" {
		want.Name = got.Name
	}
	if s.AuthenticationFlow == "" {
		want.AuthenticationFlow = got.AuthenticationFlow
	}
	if s.EncryptionKey == "" {
		want.EncryptionKey = got.EncryptionKey
	}
	if s.LogoutURI == "" {
		want.SetLogoutUri(got.GetLogoutUri())
	}
	if s.SigningKey == "" {
		want.SigningKey = got.SigningKey
	}
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
			return "", referenceError(err)
		}
		return f.Pk, nil
	}
	certPK := func(name string) (string, error) {
		k, err := c.CertificateByName(ctx, name)
		if err != nil {
			return "", referenceError(err)
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

	req.PropertyMappings, err = propertyMappingPKs(ctx, c, &p.Spec)
	if err != nil {
		return nil, err
	}

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

	// An empty signingKey is resolved to the default certificate only where
	// it will be used (see resolveDefaultSigningKey): not for an adopted
	// provider, which keeps its own.
	if s.SigningKey != "" {
		signing, err := certPK(s.SigningKey)
		if err != nil {
			return nil, err
		}
		req.SetSigningKey(signing)
	}
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

// resolveDefaultSigningKey fills in the default certificate when
// spec.signingKey is empty, for a provider about to be created or managed as
// created.
func resolveDefaultSigningKey(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest) error {
	if p.Spec.SigningKey != "" {
		return nil
	}
	k, err := c.CertificateByName(ctx, defaultSigningKeyName)
	if err != nil {
		return referenceError(err)
	}
	want.SetSigningKey(k.Pk)
	return nil
}

// propertyMappingPKs resolves the scope mappings and property mappings the
// spec names to a sorted, de-duplicated list of pks.
func propertyMappingPKs(ctx context.Context, c *authentik.Client,
	s *authentikiov1alpha1.AuthentikOAuthProviderSpec) ([]string, error) {
	mappings := make([]string, 0, len(s.Scopes)+len(s.PropertyMappings))
	for _, name := range s.Scopes {
		m, err := c.ScopeMappingByScopeName(ctx, name)
		if err != nil {
			return nil, referenceError(err)
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
			return nil, referenceError(fmt.Errorf("property mapping %q: %w", ref, err))
		}
		mappings = append(mappings, m.Pk)
	}
	slices.Sort(mappings)
	return slices.Compact(mappings), nil
}

// oauth2ProviderPatch compares desired with actual field by field and
// returns a patch containing only the differing fields, plus their names.
// Fields the spec does not manage (client secret, JWT federation) are never
// touched; every managed field has a default, so removing it from the spec
// resets it.
func oauth2ProviderPatch(want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) (api.PatchedOAuth2ProviderRequest, []string) {
	var patch api.PatchedOAuth2ProviderRequest
	changed := changedFields([]managedField{
		diff("name", want.Name, got.Name, patch.SetName),
		diff("authorizationFlow", want.AuthorizationFlow, got.AuthorizationFlow, patch.SetAuthorizationFlow),
		diff("invalidationFlow", want.InvalidationFlow, got.InvalidationFlow, patch.SetInvalidationFlow),
		{"authenticationFlow", nullable(want.AuthenticationFlow.Get()) == nullable(got.AuthenticationFlow.Get()),
			func() { patch.AuthenticationFlow = want.AuthenticationFlow }},
		{"propertyMappings", sameSet(want.PropertyMappings, got.PropertyMappings),
			func() { patch.PropertyMappings = want.PropertyMappings }},
		diff("clientType", want.GetClientType(), got.GetClientType(), patch.SetClientType),
		// nil means the server does not report grant types at all (Authentik
		// 2026.2 predates them), so there is nothing to compare or to patch;
		// an empty list is a real, reported value.
		{"grantTypes", got.GrantTypes == nil || sameSet(want.GrantTypes, got.GrantTypes),
			func() { patch.GrantTypes = want.GrantTypes }},
		diff("clientId", want.GetClientId(), got.GetClientId(), patch.SetClientId),
		diff("accessCodeValidity", want.GetAccessCodeValidity(), got.GetAccessCodeValidity(), patch.SetAccessCodeValidity),
		diff("accessTokenValidity", want.GetAccessTokenValidity(), got.GetAccessTokenValidity(),
			patch.SetAccessTokenValidity),
		diff("refreshTokenValidity", want.GetRefreshTokenValidity(), got.GetRefreshTokenValidity(),
			patch.SetRefreshTokenValidity),
		diff("refreshTokenThreshold", want.GetRefreshTokenThreshold(), got.GetRefreshTokenThreshold(),
			patch.SetRefreshTokenThreshold),
		diff("includeClaimsInIdToken", want.GetIncludeClaimsInIdToken(), got.GetIncludeClaimsInIdToken(),
			patch.SetIncludeClaimsInIdToken),
		{"signingKey", nullable(want.SigningKey.Get()) == nullable(got.SigningKey.Get()),
			func() { patch.SigningKey = want.SigningKey }},
		{"encryptionKey", nullable(want.EncryptionKey.Get()) == nullable(got.EncryptionKey.Get()),
			func() { patch.EncryptionKey = want.EncryptionKey }},
		{"redirectUris", sameRedirects(want.RedirectUris, got.RedirectUris),
			func() { patch.RedirectUris = want.RedirectUris }},
		diff("logoutUri", want.GetLogoutUri(), got.GetLogoutUri(), patch.SetLogoutUri),
		diff("logoutMethod", want.GetLogoutMethod(), got.GetLogoutMethod(), patch.SetLogoutMethod),
		diff("subMode", want.GetSubMode(), got.GetSubMode(), patch.SetSubMode),
		diff("issuerMode", want.GetIssuerMode(), got.GetIssuerMode(), patch.SetIssuerMode),
	})
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
