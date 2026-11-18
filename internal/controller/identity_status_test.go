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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
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

func TestRecheckClaim(t *testing.T) {
	self := claim{namespace: "n", name: "self", uid: "m", provenance: authentikiov1alpha1.ProvenanceAdopted}
	owner := claim{namespace: "n", name: "holder", uid: "y", provenance: authentikiov1alpha1.ProvenanceCreated}
	boom := errors.New("boom")

	t.Run("persists before it lists the holders, and keeps the claim when nobody outranks it", func(t *testing.T) {
		var order []string
		persist := func() error { order = append(order, "persist"); return nil }
		holders := func() ([]claim, error) { order = append(order, "holders"); return nil, nil }
		released := false
		err := recheckClaim(persist, self, holders, `group "x"`, func() { released = true })
		if err != nil || released {
			t.Fatalf("err = %v, released = %t; want the claim kept", err, released)
		}
		if got := strings.Join(order, ","); got != "persist,holders" {
			t.Errorf("order = %s, want persist,holders", got)
		}
	})

	t.Run("a nil persist only re-checks", func(t *testing.T) {
		err := recheckClaim(nil, self, func() ([]claim, error) { return nil, nil }, `group "x"`, func() {})
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})

	t.Run("an outranking holder releases the claim and reports a Conflict naming it", func(t *testing.T) {
		released := false
		err := recheckClaim(nil, self, func() ([]claim, error) { return []claim{owner}, nil }, `group "x"`,
			func() { released = true })
		if !released {
			t.Error("the claim was not released")
		}
		reason := conditionReason(err, "")
		if reason != reasonConflict {
			t.Fatalf("reason = %q, want %q (err %v)", reason, reasonConflict, err)
		}
		for _, want := range []string{`group "x"`, "n/holder", "takes precedence"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message %q lacks %q", err.Error(), want)
			}
		}
	})

	t.Run("a failed persist stops before the holders are listed", func(t *testing.T) {
		listed := false
		err := recheckClaim(func() error { return boom }, self,
			func() ([]claim, error) { listed = true; return nil, nil }, `group "x"`, func() {})
		if !errors.Is(err, boom) || listed {
			t.Errorf("err = %v, listed = %t; want boom and no listing", err, listed)
		}
	})

	t.Run("a failed listing is returned and keeps the claim", func(t *testing.T) {
		released := false
		err := recheckClaim(nil, self, func() ([]claim, error) { return nil, boom }, `group "x"`,
			func() { released = true })
		if !errors.Is(err, boom) || released {
			t.Errorf("err = %v, released = %t; want boom and the claim kept", err, released)
		}
	})
}

func TestSyncThenPersist(t *testing.T) {
	boom, wasted := errors.New("boom"), errors.New("status write failed")
	want := ctrl.Result{RequeueAfter: userErrorRecheck}

	t.Run("hands sync the persist function and persists the outcome once more", func(t *testing.T) {
		calls := 0
		persist := func() error { calls++; return nil }
		got, err := syncThenPersist(persist, func(p func() error) (ctrl.Result, error) {
			if err := p(); err != nil {
				t.Errorf("persist inside sync: %v", err)
			}
			return want, boom
		})
		if got != want || !errors.Is(err, boom) {
			t.Errorf("got %+v, %v; want sync's result and error", got, err)
		}
		if calls != 2 {
			t.Errorf("persist called %d times, want 2 (inside sync, then the outcome)", calls)
		}
	})

	t.Run("a failed final persist is joined to sync's error and drops its result", func(t *testing.T) {
		got, err := syncThenPersist(func() error { return wasted }, func(func() error) (ctrl.Result, error) {
			return want, boom
		})
		if !errors.Is(err, boom) || !errors.Is(err, wasted) {
			t.Errorf("err = %v, want both sync's error and the persist error", err)
		}
		if got != (ctrl.Result{}) {
			t.Errorf("result = %+v, want none", got)
		}
	})
}

func TestAddFinalizer(t *testing.T) {
	newMap := func(finalizers ...string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "n", Finalizers: finalizers}}
	}
	patches := 0
	build := func(obj client.Object) client.Client {
		return fake.NewClientBuilder().WithObjects(obj).WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch,
				opts ...client.PatchOption) error {
				patches++
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	}

	t.Run("writes the finalizer once, when it is missing", func(t *testing.T) {
		patches = 0
		cm := newMap()
		c := build(cm)
		if err := addFinalizer(t.Context(), c, cm, cm.DeepCopy()); err != nil {
			t.Fatalf("addFinalizer: %v", err)
		}
		var stored corev1.ConfigMap
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(cm), &stored); err != nil {
			t.Fatalf("get: %v", err)
		}
		if !controllerutil.ContainsFinalizer(&stored, FinalizerName) || patches != 1 {
			t.Errorf("finalizers = %v, patches = %d; want the finalizer stored by one patch", stored.Finalizers, patches)
		}
	})

	t.Run("writes nothing when it is already there", func(t *testing.T) {
		patches = 0
		cm := newMap(FinalizerName)
		c := build(cm)
		if err := addFinalizer(t.Context(), c, cm, cm.DeepCopy()); err != nil || patches != 0 {
			t.Errorf("err = %v, patches = %d; want no write", err, patches)
		}
	})
}
