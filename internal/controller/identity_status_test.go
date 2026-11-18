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
	"errors"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jacaudi/authentik-operator/internal/authentik"
)

func TestClassifyFailure(t *testing.T) {
	boom := errors.New("boom")
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}
	cases := []struct {
		name       string
		err        error
		wantReason string
		wantResult ctrl.Result
		wantErr    error
		wantWarn   bool
	}{
		{"a condition error is reported and retried slowly", condErr("Conflict", errors.New("x")),
			"Conflict", retryLater, nil, false},
		{"an Authentik conflict is reported as Conflict", &authentik.APIError{StatusCode: 409},
			reasonConflict, retryLater, nil, false},
		{"an Authentik validation error is InvalidSpec with a warning",
			&authentik.APIError{StatusCode: 400, Body: `{"attributes":["bad"]}`},
			reasonInvalidSpec, retryLater, nil, true},
		{"anything else backs off with the error", boom, reasonAuthentikError, ctrl.Result{}, boom, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, result, err, warn := classifyFailure(c.err)
			if reason != c.wantReason {
				t.Errorf("reason = %q, want %q", reason, c.wantReason)
			}
			if result != c.wantResult {
				t.Errorf("result = %+v, want %+v", result, c.wantResult)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("returned error = %v, want %v", err, c.wantErr)
			}
			if warn != c.wantWarn {
				t.Errorf("warn = %t, want %t", warn, c.wantWarn)
			}
		})
	}
}
