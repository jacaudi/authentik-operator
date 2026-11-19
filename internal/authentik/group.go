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

// Group calls. Groups are addressed by their UUID pk. Membership changes only
// through AddUserToGroup and RemoveUserFromGroup: a group create or PATCH that
// sent users would replace the whole membership.

// GetGroup fetches a group by pk, members included (include_users defaults to
// true), or returns ErrNotFound.
func (c *Client) GetGroup(ctx context.Context, pk string) (*api.Group, error) {
	g, resp, err := c.api.CoreAPI.CoreGroupsRetrieve(ctx, pk).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return g, nil
}

// FindGroupByName returns the group whose name is exactly name. The members
// are not requested (include_users=false), so the result is a cheap handle:
// use GetGroup for the membership.
func (c *Client) FindGroupByName(ctx context.Context, name string) (*api.Group, error) {
	groups, err := ListAll(func(page int32) ([]api.Group, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.CoreAPI.CoreGroupsList(ctx).
			Name(name).IncludeUsers(false).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("group", "name", name, groups, func(g api.Group) bool { return g.Name == name })
}

// CreateGroup creates a group.
func (c *Client) CreateGroup(ctx context.Context, req api.GroupRequest) (*api.Group, error) {
	g, resp, err := c.api.CoreAPI.CoreGroupsCreate(ctx).GroupRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return g, nil
}

// PatchGroup updates only the fields set in req.
func (c *Client) PatchGroup(ctx context.Context, pk string, req api.PatchedGroupRequest) (*api.Group, error) {
	g, resp, err := c.api.CoreAPI.CoreGroupsPartialUpdate(ctx, pk).PatchedGroupRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return g, nil
}

// DeleteGroup deletes a group. One that is already gone is not an error.
func (c *Client) DeleteGroup(ctx context.Context, pk string) error {
	resp, err := c.api.CoreAPI.CoreGroupsDestroy(ctx, pk).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// AddUserToGroup adds one user to a group. An unknown group or user is
// ErrNotFound.
func (c *Client) AddUserToGroup(ctx context.Context, groupPK string, userPK int32) error {
	resp, err := c.api.CoreAPI.CoreGroupsAddUserCreate(ctx, groupPK).
		UserAccountRequest(api.UserAccountRequest{Pk: userPK}).Execute()
	return Check(resp, err)
}

// RemoveUserFromGroup removes one user from a group. An unknown group or user
// is ErrNotFound.
func (c *Client) RemoveUserFromGroup(ctx context.Context, groupPK string, userPK int32) error {
	resp, err := c.api.CoreAPI.CoreGroupsRemoveUserCreate(ctx, groupPK).
		UserAccountRequest(api.UserAccountRequest{Pk: userPK}).Execute()
	return Check(resp, err)
}

// GroupUsedBy lists the objects that reference a group, with what deleting
// the group does to each (the deletion guard reads this).
func (c *Client) GroupUsedBy(ctx context.Context, pk string) ([]api.UsedBy, error) {
	used, resp, err := c.api.CoreAPI.CoreGroupsUsedByList(ctx, pk).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return used, nil
}
