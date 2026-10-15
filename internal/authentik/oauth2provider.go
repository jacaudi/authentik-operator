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
	"net/http"

	api "goauthentik.io/api/v3"
)

// OAuth2 provider CRUD. Callers get typed errors (ErrNotFound on 404).

// GetOAuth2Provider fetches a provider by primary key.
func (c *Client) GetOAuth2Provider(ctx context.Context, pk int32) (*api.OAuth2Provider, error) {
	p, resp, err := c.api.ProvidersAPI.ProvidersOauth2Retrieve(ctx, pk).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return p, nil
}

// FindOAuth2ProviderByName returns the provider with exactly this name,
// ErrNotFound if none, or ErrAmbiguous if several.
func (c *Client) FindOAuth2ProviderByName(ctx context.Context, name string) (*api.OAuth2Provider, error) {
	all, err := ListAll(func(page int32) ([]api.OAuth2Provider, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.ProvidersAPI.ProvidersOauth2List(ctx).Name(name).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("OAuth2 provider", "name", name, all, func(p api.OAuth2Provider) bool { return p.Name == name })
}

// CreateOAuth2Provider creates a provider.
func (c *Client) CreateOAuth2Provider(ctx context.Context, req api.OAuth2ProviderRequest) (*api.OAuth2Provider, error) {
	p, resp, err := c.api.ProvidersAPI.ProvidersOauth2Create(ctx).OAuth2ProviderRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return p, nil
}

// PatchOAuth2Provider updates only the fields set in req, so fields the
// operator does not manage (e.g. JWT federation, client secret) are kept.
func (c *Client) PatchOAuth2Provider(ctx context.Context, pk int32, req api.PatchedOAuth2ProviderRequest) (*api.OAuth2Provider, error) {
	p, resp, err := c.api.ProvidersAPI.ProvidersOauth2PartialUpdate(ctx, pk).PatchedOAuth2ProviderRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return p, nil
}

// DeleteOAuth2Provider deletes a provider. A provider that is already gone
// is not an error.
func (c *Client) DeleteOAuth2Provider(ctx context.Context, pk int32) error {
	resp, err := c.api.ProvidersAPI.ProvidersOauth2Destroy(ctx, pk).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// OAuth2ProviderSetupURLs returns the endpoint URLs Authentik computes for a
// provider (issuer, authorize, token, ...). Authentik builds them from the
// host of the request, i.e. the operator's API URL; callers rewrite them to
// the browser-facing URL (see credentials.Rewrite).
func (c *Client) OAuth2ProviderSetupURLs(ctx context.Context, pk int32) (*api.OAuth2ProviderSetupURLs, error) {
	u, resp, err := c.api.ProvidersAPI.ProvidersOauth2SetupUrlsRetrieve(ctx, pk).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return u, nil
}
