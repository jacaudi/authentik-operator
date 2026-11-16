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

package aktest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The count is taken server side from pagination.count, filtered by model,
// so it cannot be capped by a page size.
func TestModelWriteCountFiltersAndSumsServerSide(t *testing.T) {
	counts := map[string]string{"model_created": "120", "model_updated": "300", "model_deleted": "0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("context_model_app") != "authentik_core" || q.Get("context_model_name") != "application" {
			http.Error(w, "unfiltered", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"pagination":{"count":` + counts[q.Get("action")] + `},"results":[]}`))
	}))
	defer srv.Close()
	n, err := NewAPI(srv.URL, "t", nil).ModelWriteCount(t.Context(), "authentik_core", "application",
		"model_created", "model_updated", "model_deleted")
	if err != nil || n != 420 {
		t.Fatalf("got %d, %v; want 420", n, err)
	}
}
