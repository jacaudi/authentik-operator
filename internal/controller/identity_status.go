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

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// classifyFailure sorts why a sync of an AuthentikGroup or AuthentikUser
// stopped into the Ready reason to report, the result to return, and the error
// to return with it. Problems retrying cannot fix -- a conditionError, an
// Authentik conflict or validation rejection -- are reported and retried on
// the slow interval (retErr nil); anything else backs off (retErr is err).
// warn is true when the problem is the user's spec as Authentik sees it, which
// deserves a Warning event once.
//
//nolint:revive // error-return: the shape (reason, result, error, warn) is shared with the user reconciler.
func classifyFailure(err error) (reason string, result ctrl.Result, retErr error, warn bool) {
	retryLater := ctrl.Result{RequeueAfter: userErrorRecheck}
	if ce, ok := errors.AsType[*conditionError](err); ok {
		return ce.reason, retryLater, nil, false
	}
	switch {
	case authentik.IsConflict(err):
		return reasonConflict, retryLater, nil, false
	case authentik.IsValidation(err):
		return reasonInvalidSpec, retryLater, nil, true
	}
	return reasonAuthentikError, ctrl.Result{}, err, false
}
