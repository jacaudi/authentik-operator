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
	"encoding/json"
	"fmt"
	"net/http"

	api "goauthentik.io/api/v3"
)

// User calls. Users are addressed by their integer pk. The operator never
// creates a user through the API: people enrol through an invitation.

// GetUser fetches a user by pk, or returns ErrNotFound.
func (c *Client) GetUser(ctx context.Context, pk int32) (*api.User, error) {
	u, resp, err := c.api.CoreAPI.CoreUsersRetrieve(ctx, pk).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return u, nil
}

// FindUserByUsername returns the user whose username is exactly username.
func (c *Client) FindUserByUsername(ctx context.Context, username string) (*api.User, error) {
	users, err := c.listUsers(ctx, func(r api.ApiCoreUsersListRequest) api.ApiCoreUsersListRequest {
		return r.Username(username)
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("user", "username", username, users, func(u api.User) bool { return u.Username == username })
}

// FindUsersByOwnerUID returns every user that carries the ownership proof for
// uid. The server-side attributes filter narrows the list, but Authentik
// answers every user when it cannot apply a filter, so each result is
// re-checked here and a user without the proof is never returned.
func (c *Client) FindUsersByOwnerUID(ctx context.Context, uid string) ([]api.User, error) {
	filter, err := json.Marshal(map[string]string{OwnerAttribute + "__" + OwnerUIDKey: uid})
	if err != nil {
		return nil, fmt.Errorf("authentik: encode owner filter: %w", err)
	}
	users, err := c.listUsers(ctx, func(r api.ApiCoreUsersListRequest) api.ApiCoreUsersListRequest {
		return r.Attributes(string(filter))
	})
	if err != nil {
		return nil, err
	}
	var owned []api.User
	for i := range users {
		if HasOwnerProof(users[i].Attributes, uid) {
			owned = append(owned, users[i])
		}
	}
	return owned, nil
}

// PatchUser updates only the fields set in req.
func (c *Client) PatchUser(ctx context.Context, pk int32, req api.PatchedUserRequest) (*api.User, error) {
	u, resp, err := c.api.CoreAPI.CoreUsersPartialUpdate(ctx, pk).PatchedUserRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return u, nil
}

// DeleteUser deletes a user. One that is already gone is not an error.
func (c *Client) DeleteUser(ctx context.Context, pk int32) error {
	resp, err := c.api.CoreAPI.CoreUsersDestroy(ctx, pk).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// Me returns the owner of the API token.
func (c *Client) Me(ctx context.Context) (*api.UserSelf, error) {
	me, resp, err := c.api.CoreAPI.CoreUsersMeRetrieve(ctx).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return &me.User, nil
}

// UserAuthenticators lists the MFA devices a user has enrolled.
func (c *Client) UserAuthenticators(ctx context.Context, userPK int32) ([]api.Device, error) {
	devices, resp, err := c.api.AuthenticatorsAPI.AuthenticatorsAdminAllList(ctx).User(userPK).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return devices, nil
}

// listUsers pages through the user list with one server-side filter applied.
func (c *Client) listUsers(ctx context.Context,
	filter func(api.ApiCoreUsersListRequest) api.ApiCoreUsersListRequest) ([]api.User, error) {
	return ListAll(func(page int32) ([]api.User, api.Pagination, *http.Response, error) {
		res, resp, err := filter(c.api.CoreAPI.CoreUsersList(ctx)).
			Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
}
