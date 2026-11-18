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
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// patchStatus writes obj's status as a merge patch against base, which is obj
// as it was when its status was last persisted. It writes nothing when the two
// agree, and treats an obj deleted meanwhile as written: there is no status
// left to record.
func patchStatus(ctx context.Context, w client.StatusWriter, obj, base client.Object) error {
	if equality.Semantic.DeepEqual(base, obj) {
		return nil
	}
	if err := w.Patch(ctx, obj, client.MergeFrom(base)); !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// recheckClaim is the last step of taking a claim on an Authentik object, and
// the routine check of a claim already held: persist records the claim in
// status (nil when it is already recorded), then holders lists the other
// resources holding the same object, and when one outranks self (see
// claim.outranks) release forgets the claim and the result is a Conflict
// naming it. subject names the object in that message. A resource that
// persists, re-checks through the API reader and yields before it writes to
// Authentik leaves two racing claimants exactly one winner.
func recheckClaim(persist func() error, self claim, holders func() ([]claim, error), subject string,
	release func()) error {
	if persist != nil {
		if err := persist(); err != nil {
			return err
		}
	}
	others, err := holders()
	if err != nil {
		return err
	}
	winner, lost := outrankedBy(self, others)
	if !lost {
		return nil
	}
	release()
	return condErrf(reasonConflict, "%s is also claimed by %s, which takes precedence; this resource released it",
		subject, winner)
}
