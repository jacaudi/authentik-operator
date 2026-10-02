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

// Application CRUD. Applications are addressed by slug in the API.

// GetApplication fetches an application by slug.
func (c *Client) GetApplication(ctx context.Context, slug string) (*api.Application, error) {
	a, resp, err := c.api.CoreAPI.CoreApplicationsRetrieve(ctx, slug).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return a, nil
}

// FindApplicationBySlug returns the application with exactly this slug, or
// ErrNotFound. It lists with superuser_full_list: without it Authentik only
// returns applications the token's user passes policies for, which would
// hide existing applications and turn a conflict into a failed create.
func (c *Client) FindApplicationBySlug(ctx context.Context, slug string) (*api.Application, error) {
	all, err := ListAll(func(page int32) ([]api.Application, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.CoreAPI.CoreApplicationsList(ctx).Slug(slug).SuperuserFullList(true).
			Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("application", "slug", slug, all, func(a api.Application) bool { return a.Slug == slug })
}

// CreateApplication creates an application.
func (c *Client) CreateApplication(ctx context.Context, req api.ApplicationRequest) (*api.Application, error) {
	a, resp, err := c.api.CoreAPI.CoreApplicationsCreate(ctx).ApplicationRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return a, nil
}

// PatchApplication updates only the fields set in req.
func (c *Client) PatchApplication(ctx context.Context, slug string, req api.PatchedApplicationRequest) (*api.Application, error) {
	a, resp, err := c.api.CoreAPI.CoreApplicationsPartialUpdate(ctx, slug).PatchedApplicationRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return a, nil
}

// DeleteApplication deletes an application. One that is already gone is
// not an error.
func (c *Client) DeleteApplication(ctx context.Context, slug string) error {
	resp, err := c.api.CoreAPI.CoreApplicationsDestroy(ctx, slug).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}
