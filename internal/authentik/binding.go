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
	"fmt"
	"net/http"

	api "goauthentik.io/api/v3"
)

// ErrNoTarget is returned when a policy binding call has no application to
// scope it to. Authentik ignores an empty target filter and answers every
// binding of every object, so the operator never lists or deletes without one.
var ErrNoTarget = errors.New("authentik: policy binding target is empty")

// ListBindings returns the bindings of one target (an application pk). An
// empty target is ErrNoTarget and sends no request. Results for any other
// target are dropped, so a filter Authentik fails to apply cannot widen the
// list.
func (c *Client) ListBindings(ctx context.Context, target string) ([]api.PolicyBinding, error) {
	if target == "" {
		return nil, ErrNoTarget
	}
	all, err := ListAll(func(page int32) ([]api.PolicyBinding, api.Pagination, *http.Response, error) {
		res, resp, err := c.api.PoliciesAPI.PoliciesBindingsList(ctx).
			Target(target).Page(page).PageSize(PageSize).Execute()
		if res == nil {
			return nil, api.Pagination{}, resp, err
		}
		return res.Results, res.Pagination, resp, err
	})
	if err != nil {
		return nil, err
	}
	var bindings []api.PolicyBinding
	for i := range all {
		if all[i].Target == target {
			bindings = append(bindings, all[i])
		}
	}
	return bindings, nil
}

// CreateBinding creates a policy binding.
func (c *Client) CreateBinding(ctx context.Context, req api.PolicyBindingRequest) (*api.PolicyBinding, error) {
	b, resp, err := c.api.PoliciesAPI.PoliciesBindingsCreate(ctx).PolicyBindingRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return b, nil
}

// PatchBinding updates only the fields set in req.
func (c *Client) PatchBinding(ctx context.Context, pk string,
	req api.PatchedPolicyBindingRequest) (*api.PolicyBinding, error) {
	b, resp, err := c.api.PoliciesAPI.PoliciesBindingsPartialUpdate(ctx, pk).PatchedPolicyBindingRequest(req).Execute()
	if err := Check(resp, err); err != nil {
		return nil, err
	}
	return b, nil
}

// DeleteBinding deletes b, but only after re-checking that it belongs to
// target: an empty target is ErrNoTarget and a binding of another target is
// an error, in both cases without a request. A binding that is already gone is
// not an error.
func (c *Client) DeleteBinding(ctx context.Context, b *api.PolicyBinding, target string) error {
	if target == "" {
		return ErrNoTarget
	}
	if b.Target != target {
		return fmt.Errorf("authentik: policy binding %s belongs to target %q, not %q", b.Pk, b.Target, target)
	}
	resp, err := c.api.PoliciesAPI.PoliciesBindingsDestroy(ctx, b.Pk).Execute()
	if err := Check(resp, err); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}
