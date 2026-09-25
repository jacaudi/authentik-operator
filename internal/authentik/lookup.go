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

import (
	"context"
	"fmt"
	"net/http"

	api "goauthentik.io/api/v3"
)

// Lookups resolve references by stable identifiers (slug, scope_name, name,
// UUID). The server-side filter narrows the result; an exact client-side
// match is still required, and more than one match is an error.

// FlowBySlug returns the flow with the given slug.
func (c *Client) FlowBySlug(ctx context.Context, slug string) (*api.Flow, error) {
	flows, err := ListAll(func(page int32) ([]api.Flow, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.FlowsAPI.FlowsInstancesList(ctx).Slug(slug).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("flow", "slug", slug, flows, func(f api.Flow) bool { return f.Slug == slug })
}

// ScopeMappingByScopeName returns the OAuth scope mapping for scopeName
// (e.g. "openid"). If several mappings share the scope name, the managed
// (built-in) one is preferred.
func (c *Client) ScopeMappingByScopeName(ctx context.Context, scopeName string) (*api.ScopeMapping, error) {
	all, err := ListAll(func(page int32) ([]api.ScopeMapping, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.PropertymappingsAPI.PropertymappingsProviderScopeList(ctx).
			ScopeName(scopeName).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	var matches, managed []api.ScopeMapping
	for _, m := range all {
		if m.ScopeName != scopeName {
			continue
		}
		matches = append(matches, m)
		if m.Managed.IsSet() && m.Managed.Get() != nil && *m.Managed.Get() != "" {
			managed = append(managed, m)
		}
	}
	if len(matches) > 1 && len(managed) == 1 {
		return &managed[0], nil
	}
	return exactlyOne("scope mapping", "scope_name", scopeName, matches, func(api.ScopeMapping) bool { return true })
}

// ScopeMappingByManaged returns the scope mapping with the given managed
// identifier (e.g. "goauthentik.io/providers/oauth2/scope-openid").
func (c *Client) ScopeMappingByManaged(ctx context.Context, managed string) (*api.ScopeMapping, error) {
	all, err := ListAll(func(page int32) ([]api.ScopeMapping, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.PropertymappingsAPI.PropertymappingsProviderScopeList(ctx).
			Managed([]string{managed}).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("scope mapping", "managed", managed, all, func(m api.ScopeMapping) bool {
		return m.Managed.Get() != nil && *m.Managed.Get() == managed
	})
}

// CertificateByName returns the certificate-key pair with the given name.
func (c *Client) CertificateByName(ctx context.Context, name string) (*api.CertificateKeyPair, error) {
	all, err := ListAll(func(page int32) ([]api.CertificateKeyPair, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.CryptoAPI.CryptoCertificatekeypairsList(ctx).Name(name).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("certificate", "name", name, all, func(k api.CertificateKeyPair) bool { return k.Name == name })
}

// PropertyMappingByName returns the property mapping (any type) with the given name.
func (c *Client) PropertyMappingByName(ctx context.Context, name string) (*api.PropertyMapping, error) {
	all, err := ListAll(func(page int32) ([]api.PropertyMapping, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.PropertymappingsAPI.PropertymappingsAllList(ctx).Name(name).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("property mapping", "name", name, all, func(m api.PropertyMapping) bool { return m.Name == name })
}

// PropertyMappingByUUID returns the property mapping (any type) with the given UUID.
func (c *Client) PropertyMappingByUUID(ctx context.Context, uuid string) (*api.PropertyMapping, error) {
	m, resp, err := c.api.PropertymappingsAPI.PropertymappingsAllRetrieve(ctx, uuid).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return m, nil
}

func exactlyOne[T any](kind, field, value string, items []T, match func(T) bool) (*T, error) {
	var found []T
	for _, it := range items {
		if match(it) {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s with %s %q: %w", kind, field, value, ErrNotFound)
	case 1:
		return &found[0], nil
	default:
		return nil, fmt.Errorf("%s with %s %q (%d matches): %w", kind, field, value, len(found), ErrAmbiguous)
	}
}
