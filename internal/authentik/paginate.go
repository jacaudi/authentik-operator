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
	"fmt"
	"net/http"

	api "goauthentik.io/api/v3"
)

// PageSize is the page size requested by list helpers.
const PageSize = 100

// maxPages guards against a server that never reports a last page.
const maxPages = 10000

// PageFunc fetches one page (1-based) of a list endpoint.
type PageFunc[T any] func(page int32) ([]T, api.Pagination, *http.Response, error)

// ListAll follows Authentik's page-number pagination until the last page.
func ListAll[T any](fetch PageFunc[T]) ([]T, error) {
	var all []T
	for page := int32(1); ; {
		items, p, resp, err := fetch(page)
		if err := Check(resp, err); err != nil {
			return nil, err
		}
		all = append(all, items...)
		next := int32(p.Next)
		if next <= page {
			return all, nil
		}
		if next > maxPages {
			return nil, fmt.Errorf("authentik: pagination exceeded %d pages", maxPages)
		}
		page = next
	}
}
