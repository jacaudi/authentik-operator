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
	"errors"
	"net/http"
	"strings"

	api "goauthentik.io/api/v3"
)

// Invitation calls. An invitation's pk is the sign-up token: anyone holding it
// can create an account. Errors from DeleteInvitation therefore have the pk
// removed; callers must keep the pk out of status, events and logs too.

// redacted replaces a secret inside an error message.
const redacted = "<redacted>"

// FindInvitationByName returns the invitation named name. Invitation names are
// not unique in Authentik, so more than one match is ErrAmbiguous.
func (c *Client) FindInvitationByName(ctx context.Context, name string) (*api.Invitation, error) {
	all, err := ListAll(func(page int32) ([]api.Invitation, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.StagesAPI.StagesInvitationInvitationsList(ctx).
			Name(name).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	return exactlyOne("invitation", "name", name, all, func(i api.Invitation) bool { return i.Name == name })
}

// CreateInvitation creates an invitation.
func (c *Client) CreateInvitation(ctx context.Context, req api.InvitationRequest) (*api.Invitation, error) {
	inv, resp, err := c.api.StagesAPI.StagesInvitationInvitationsCreate(ctx).InvitationRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return inv, nil
}

// DeleteInvitation deletes an invitation. One that is already gone (used, or
// expired and removed) is not an error. Any other error has pk replaced by
// "<redacted>" in Error(), in APIError.Path and in APIError.Body.
func (c *Client) DeleteInvitation(ctx context.Context, pk string) error {
	resp, err := c.api.StagesAPI.StagesInvitationInvitationsDestroy(ctx, pk).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return redact(err, pk)
	}
	return nil
}

// redact returns err with every occurrence of secret replaced. An *APIError
// stays an *APIError (a copy, so StatusCode still classifies it); any other
// error, such as a transport failure whose message names the request URL,
// becomes a plain error with the same text redacted.
func redact(err error, secret string) error {
	if secret == "" {
		return err
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		hidden := *apiErr
		hidden.Path = strings.ReplaceAll(hidden.Path, secret, redacted)
		hidden.Body = strings.ReplaceAll(hidden.Body, secret, redacted)
		return &hidden
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, redacted))
}
