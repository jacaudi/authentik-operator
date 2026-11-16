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

// The values a provider takes on create for every field the spec leaves unset.
// They are applied by the controller, not by the CRD schema, so that adopting
// a provider can tell "unset, keep what it has" from "set". A provider this
// resource created is held to them on every reconcile.
const (
	defaultClientType            = "confidential"
	defaultAccessCodeValidity    = "minutes=1"
	defaultAccessTokenValidity   = "minutes=5"
	defaultRefreshTokenValidity  = "days=30"
	defaultRefreshTokenThreshold = "seconds=0"
	defaultSubMode               = "hashed_user_id"
	defaultIssuerMode            = "per_provider"
	defaultLogoutMethod          = "backchannel"
	defaultMatchingMode          = "strict"
	defaultRedirectType          = "authorization"
)

// defaultScopes are the scope names a new provider takes when spec.scopes is unset.
func defaultScopes() []string { return []string{"openid", "profile", "email"} }

// defaultGrantTypes are the grant types a new provider takes when spec.grantTypes is unset.
func defaultGrantTypes() []api.GrantTypeEnum {
	return []api.GrantTypeEnum{api.GRANTTYPEENUM_AUTHORIZATION_CODE, api.GRANTTYPEENUM_REFRESH_TOKEN}
}

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

// adoptionView narrows want, the full desired state (spec values, with the
// create defaults standing in for unset fields), to what adoption may write:
// the fields the spec sets. Where the spec says nothing, the adopted
// provider's own value stands -- the client ID (unless spec.clientId is set),
// the name (unless spec.name is set), authenticationFlow, encryptionKey,
// logoutUri, signingKey, redirectUris, scopes (see below), and every field
// that takes a default on create (grantTypes, clientType, the four validity
// and threshold fields, subMode, includeClaimsInIdToken, issuerMode,
// logoutMethod, and the matchingMode and type of each redirect URI).
//
// Property mappings are one Authentik field built from scopes plus
// propertyMappings. With scopes unset the live list is kept and the mappings
// the spec names are added to it (never removed); with scopes set the spec
// owns the whole list.
func adoptionView(p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) {
	s := p.Spec
	if s.ClientID == "" {
		want.SetClientId(got.GetClientId())
	}
	keepLiveTuning(&s, want, got)
	if s.Scopes == nil {
		want.PropertyMappings = sortedUnique(slices.Concat(got.PropertyMappings, want.PropertyMappings))
	}
	want.RedirectUris = adoptedRedirects(s.RedirectURIs, want.RedirectUris, got.RedirectUris)
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

// keepLiveTuning is the part of adoptionView for the fields that take a
// default on create: each one the spec leaves unset keeps the live value.
func keepLiveTuning(s *authentikiov1alpha1.AuthentikOAuthProviderSpec, want *api.OAuth2ProviderRequest,
	got *api.OAuth2Provider) {
	if s.ClientType == "" {
		want.SetClientType(got.GetClientType())
	}
	if len(s.GrantTypes) == 0 {
		want.GrantTypes = slices.Clone(got.GrantTypes)
		slices.Sort(want.GrantTypes)
	}
	if s.AccessCodeValidity == "" {
		want.SetAccessCodeValidity(got.GetAccessCodeValidity())
	}
	if s.AccessTokenValidity == "" {
		want.SetAccessTokenValidity(got.GetAccessTokenValidity())
	}
	if s.RefreshTokenValidity == "" {
		want.SetRefreshTokenValidity(got.GetRefreshTokenValidity())
	}
	if s.RefreshTokenThreshold == "" {
		want.SetRefreshTokenThreshold(got.GetRefreshTokenThreshold())
	}
	if s.SubMode == "" {
		want.SetSubMode(got.GetSubMode())
	}
	if s.IncludeClaimsInIDToken == nil {
		want.SetIncludeClaimsInIdToken(got.GetIncludeClaimsInIdToken())
	}
	if s.IssuerMode == "" {
		want.SetIssuerMode(got.GetIssuerMode())
	}
	if s.LogoutMethod == "" {
		want.SetLogoutMethod(got.GetLogoutMethod())
	}
}

// adoptedRedirects is the redirect URIs an adopted provider should have. With
// none in the spec, the live ones stand. Otherwise a spec entry that leaves
// matchingMode or type unset takes the value of the live redirect URI with the
// same url, and the create default when there is none. want holds the spec
// entries with the create defaults already applied.
func adoptedRedirects(spec []authentikiov1alpha1.RedirectURI, want []api.RedirectURIRequest,
	live []api.RedirectURI) []api.RedirectURIRequest {
	if len(spec) == 0 {
		out := make([]api.RedirectURIRequest, 0, len(live))
		for _, l := range live {
			out = append(out, api.RedirectURIRequest{Url: l.Url, MatchingMode: l.MatchingMode,
				RedirectUriType: l.RedirectUriType})
		}
		return out
	}
	out := slices.Clone(want)
	for i, u := range spec {
		if u.MatchingMode != "" && u.Type != "" {
			continue
		}
		j := slices.IndexFunc(live, func(l api.RedirectURI) bool { return l.Url == u.URL })
		if j < 0 {
			continue
		}
		if u.MatchingMode == "" {
			out[i].MatchingMode = live[j].MatchingMode
		}
		if u.Type == "" && live[j].RedirectUriType != nil {
			out[i].RedirectUriType = live[j].RedirectUriType
		}
	}
	return out
}

// sortedUnique returns the sorted, de-duplicated values of in.
func sortedUnique[T cmp.Ordered](in []T) []T {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
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

// setTuningWithDefaults sets the fields that take a create default when the
// spec leaves them unset: client type, grant types, the four validity and
// threshold fields, sub mode, issuer mode, includeClaimsInIdToken and the
// logout method. Scopes and redirect URIs are not plain values and are
// handled by their own code.
func setTuningWithDefaults(req *api.OAuth2ProviderRequest, s *authentikiov1alpha1.AuthentikOAuthProviderSpec) {
	req.SetClientType(api.ClientTypeEnum(cmp.Or(s.ClientType, defaultClientType)))
	req.GrantTypes = defaultGrantTypes()
	if len(s.GrantTypes) > 0 {
		req.GrantTypes = make([]api.GrantTypeEnum, 0, len(s.GrantTypes))
		for _, g := range s.GrantTypes {
			req.GrantTypes = append(req.GrantTypes, api.GrantTypeEnum(g))
		}
	}
	slices.Sort(req.GrantTypes)
	req.SetAccessCodeValidity(cmp.Or(s.AccessCodeValidity, defaultAccessCodeValidity))
	req.SetAccessTokenValidity(cmp.Or(s.AccessTokenValidity, defaultAccessTokenValidity))
	req.SetRefreshTokenValidity(cmp.Or(s.RefreshTokenValidity, defaultRefreshTokenValidity))
	req.SetRefreshTokenThreshold(cmp.Or(s.RefreshTokenThreshold, defaultRefreshTokenThreshold))
	req.SetSubMode(api.SubModeEnum(cmp.Or(s.SubMode, defaultSubMode)))
	req.SetIssuerMode(api.IssuerModeEnum(cmp.Or(s.IssuerMode, defaultIssuerMode)))
	req.SetIncludeClaimsInIdToken(s.IncludeClaimsInIDToken == nil || *s.IncludeClaimsInIDToken)
	req.SetLogoutMethod(api.OAuth2ProviderLogoutMethodEnum(cmp.Or(s.LogoutMethod, defaultLogoutMethod)))
}

// desiredOAuth2Provider resolves every reference in the spec and returns the
// full request the provider should match. A field the spec leaves unset takes
// its create default here (see the default* constants); adoptionView swaps
// those for the live values when the provider is adopted.
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
			MatchingMode:    api.MatchingModeEnum(cmp.Or(u.MatchingMode, defaultMatchingMode)),
			RedirectUriType: new(api.RedirectURITypeEnum(cmp.Or(u.Type, defaultRedirectType))),
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

	req.SetClientId(expectedClientID(p))
	setTuningWithDefaults(req, &s)

	// An empty signingKey is resolved to the default certificate only where
	// it will be used (see applyCreateDefaults): not for an adopted
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
	return req, nil
}

// applyCreateDefaults completes want for a provider about to be created or
// managed as created: it refuses a spec without redirectUris (nothing to
// adopt them from), and resolves the references whose defaults need Authentik
// -- the default signing key when spec.signingKey is empty and the default
// scope mappings when spec.scopes is unset. The plain defaults are already in
// want (see desiredOAuth2Provider); an adopted provider is never given any of
// these, see adoptionView.
func applyCreateDefaults(ctx context.Context, c *authentik.Client,
	p *authentikiov1alpha1.AuthentikOAuthProvider, want *api.OAuth2ProviderRequest) error {
	if len(p.Spec.RedirectURIs) == 0 {
		return condErr(reasonInvalidSpec, errors.New("spec.redirectUris is required when the provider is created; "+
			"it may be omitted only when adopting a provider that already has redirect URIs"))
	}
	if p.Spec.SigningKey == "" {
		k, err := c.CertificateByName(ctx, defaultSigningKeyName)
		if err != nil {
			return referenceError(err)
		}
		want.SetSigningKey(k.Pk)
	}
	if p.Spec.Scopes == nil {
		pks, err := scopeMappingPKs(ctx, c, defaultScopes())
		if err != nil {
			return err
		}
		want.PropertyMappings = sortedUnique(slices.Concat(want.PropertyMappings, pks))
	}
	return nil
}

// scopeMappingPKs resolves scope names to their scope mappings' pks.
func scopeMappingPKs(ctx context.Context, c *authentik.Client, names []string) ([]string, error) {
	pks := make([]string, 0, len(names))
	for _, name := range names {
		m, err := c.ScopeMappingByScopeName(ctx, name)
		if err != nil {
			return nil, referenceError(err)
		}
		pks = append(pks, m.Pk)
	}
	return pks, nil
}

// propertyMappingPKs resolves the scope mappings and property mappings the
// spec names to a sorted, de-duplicated list of pks. An unset spec.scopes
// resolves none: the default scopes are added by applyCreateDefaults, only
// where the provider is created.
func propertyMappingPKs(ctx context.Context, c *authentik.Client,
	s *authentikiov1alpha1.AuthentikOAuthProviderSpec) ([]string, error) {
	mappings, err := scopeMappingPKs(ctx, c, s.Scopes)
	if err != nil {
		return nil, err
	}
	mappings = slices.Grow(mappings, len(s.PropertyMappings))
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
		{"grantTypes", sameSet(want.GrantTypes, got.GrantTypes), func() { patch.GrantTypes = want.GrantTypes }},
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
